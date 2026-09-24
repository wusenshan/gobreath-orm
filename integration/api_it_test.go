package integration

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	orm "github.com/wusenshan/gobreath-orm"
)

// 本文件是「公开 API 真库走查」：与既有用例的差别在于**覆盖面**而不是深度 ——
// 凡是能在 MySQL / PG / SQLite 上执行的公开接口，这里都至少真跑一次。
//
// 动机：mock 用例只能证明「生成了什么 SQL」，证明不了「这条 SQL 服务器肯执行」。
// 已有 integration 用例集中在 CRUD 主干与聚合上，而 repo.go 的 Repo[T] 门面、
// 一整套谓词构造器（Like 系列 / Between / IsNull / Or / If）、JOIN 的六个变体、
// 写选项（OmitZero / OnlyColumns）、DB 级配置方法（WithLogger / WithHooks /
// WithExecutor …）此前一次都没在真库上跑过。门面层最典型的失效是
// 「转发时条件丢了 / 参数顺序错了」—— 这类错误照样能编译、照样生成一条看似合理的
// SQL，只有真执行（并且断言命中行数）才会暴露。

// recordingHook 记录框架发出的每个 HookEvent，用于验证 Hook 链路在真库上确实被触发。
type recordingHook struct{ events []orm.HookEvent }

func (h *recordingHook) On(e orm.HookEvent) { h.events = append(h.events, e) }

// countingExecutor 包装底层执行器并统计调用次数，用于验证 WithExecutor 真的换了执行器。
type countingExecutor struct {
	inner   orm.Executor
	queries int
	execs   int
}

func (c *countingExecutor) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	c.queries++
	return c.inner.QueryContext(ctx, q, args...)
}

func (c *countingExecutor) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	c.queries++
	return c.inner.QueryRowContext(ctx, q, args...)
}

func (c *countingExecutor) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	c.execs++
	return c.inner.ExecContext(ctx, q, args...)
}

// uDeletedAt 指向软删除列。User.DeletedAt 是 *time.Time，故 F = *time.Time。
// 它让 IsNull / IsNotNull 有一列**可空**的列可以真正落到 SQL 上（其余列都是 NOT NULL）。
var uDeletedAt = orm.Col[User](func(u *User) **time.Time { return &u.DeletedAt })

// TestRepoLayerFullWalk 把 Repo[T] 的每个方法在真库上真跑一遍。
func TestRepoLayerFullWalk(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		repo := orm.NewRepo[User](db)

		if repo.DB() != db {
			t.Errorf("[%s] Repo.DB() 未返回底层 *DB", b.name)
		}

		// ---- Insert：自增主键回填 ----
		a := User{Name: "repo-a", Age: 20, Score: 1.5, City: "bj",
			Meta: map[string]any{"k": "v"}, CreatedAt: baseTime}
		if err := repo.Insert(ctx, &a); err != nil {
			t.Fatalf("[%s] Repo.Insert：%v", b.name, err)
		}
		if a.ID == 0 {
			t.Fatalf("[%s] Repo.Insert 未回填自增主键", b.name)
		}

		// ---- SelectById ----
		got, err := repo.SelectById(ctx, a.ID)
		if err != nil || got == nil || got.Name != "repo-a" || got.Age != 20 {
			t.Fatalf("[%s] Repo.SelectById = %+v, err = %v", b.name, got, err)
		}
		if got.Meta["k"] != "v" {
			t.Errorf("[%s] Repo.SelectById 的 JSON 列往返不对：%#v", b.name, got.Meta)
		}

		// ---- BatchInsert（契约：批量版本**不**回填主键，与 Insert 不同）----
		batch := []User{
			{Name: "repo-b", Age: 30, Score: 2.5, City: "sh", Meta: map[string]any{}, CreatedAt: baseTime},
			{Name: "repo-c", Age: 40, Score: 3.5, City: "gz", Meta: map[string]any{}, CreatedAt: baseTime},
		}
		if err := repo.BatchInsert(ctx, batch); err != nil {
			t.Fatalf("[%s] Repo.BatchInsert：%v", b.name, err)
		}

		// ---- SelectList / SelectOne / Count / Exists ----
		list, err := repo.SelectList(ctx, orm.NewQuery[User]().Ge(uAge, 20).OrderBy(uID, true))
		if err != nil {
			t.Fatalf("[%s] Repo.SelectList：%v", b.name, err)
		}
		if len(list) != 3 {
			t.Errorf("[%s] Repo.SelectList 返回 %d 行；期望 3", b.name, len(list))
		}
		one, err := repo.SelectOne(ctx, orm.NewQuery[User]().Eq(uName, "repo-b"))
		if err != nil || one == nil || one.Age != 30 {
			t.Fatalf("[%s] Repo.SelectOne = %+v, err = %v；期望 age=30", b.name, one, err)
		}
		if n, err := repo.Count(ctx, orm.NewQuery[User]()); err != nil || n != 3 {
			t.Errorf("[%s] Repo.Count = %d, err = %v；期望 3", b.name, n, err)
		}
		if ok, err := repo.Exists(ctx, orm.NewQuery[User]().Eq(uName, "repo-c")); err != nil || !ok {
			t.Errorf("[%s] Repo.Exists = %v, err = %v；期望 true", b.name, ok, err)
		}

		// ---- Page：第 2 页每页 2 条 → 1 条，且元信息自洽 ----
		pg, err := repo.Page(ctx, orm.NewQuery[User]().OrderBy(uID, true), 2, 2)
		if err != nil {
			t.Fatalf("[%s] Repo.Page：%v", b.name, err)
		}
		if pg.Total != 3 || pg.Pages != 2 || len(pg.List) != 1 || !pg.HasPrev || pg.HasNext {
			t.Errorf("[%s] Repo.Page 元信息 = total%d pages%d 行%d prev%v next%v；期望 3/2/1/true/false",
				b.name, pg.Total, pg.Pages, len(pg.List), pg.HasPrev, pg.HasNext)
		}

		// ---- UpdateById：整行覆盖 ----
		a.Age = 21
		if err := repo.UpdateById(ctx, &a); err != nil {
			t.Fatalf("[%s] Repo.UpdateById：%v", b.name, err)
		}
		if got, _ := repo.SelectById(ctx, a.ID); got == nil || got.Age != 21 {
			t.Errorf("[%s] Repo.UpdateById 未生效：%+v", b.name, got)
		}

		// ---- Update（带条件）----
		// CreatedAt 必须显式给值：MySQL 严格模式拒收零值时间（见 TestZeroTimeWrite）；
		// 只想改部分列请用 UpdateSets / UpdatePartial / OnlyColumns。
		if err := repo.Update(ctx, orm.NewQuery[User]().Eq(uName, "repo-b"),
			&User{Name: "repo-b2", Age: 31, Score: 2.5, City: "sh",
				Meta: map[string]any{}, CreatedAt: baseTime}); err != nil {
			t.Fatalf("[%s] Repo.Update：%v", b.name, err)
		}
		if n, _ := repo.Count(ctx, orm.NewQuery[User]().Eq(uName, "repo-b2")); n != 1 {
			t.Errorf("[%s] Repo.Update 后 repo-b2 有 %d 行；期望 1", b.name, n)
		}

		// ---- UpdateSets（链式 Set）----
		affected, err := repo.UpdateSets(ctx,
			orm.NewQuery[User]().Set(orm.ColOf[User]("City"), "tj").Eq(uName, "repo-b2"))
		if err != nil {
			t.Fatalf("[%s] Repo.UpdateSets：%v", b.name, err)
		}
		if affected != 1 {
			t.Errorf("[%s] Repo.UpdateSets 影响 %d 行；期望 1", b.name, affected)
		}
		if b2, _ := repo.SelectOne(ctx, orm.NewQuery[User]().Eq(uName, "repo-b2")); b2 == nil || b2.City != "tj" {
			t.Errorf("[%s] Repo.UpdateSets 结果不对：%+v", b.name, b2)
		}

		// ---- UpdatePartial（map 指定列）----
		affected, err = repo.UpdatePartial(ctx, orm.NewQuery[User]().Eq(uName, "repo-c"),
			map[string]any{"age": 41, "city": "xa"})
		if err != nil {
			t.Fatalf("[%s] Repo.UpdatePartial：%v", b.name, err)
		}
		if affected != 1 {
			t.Errorf("[%s] Repo.UpdatePartial 影响 %d 行；期望 1", b.name, affected)
		}
		if c, _ := repo.SelectOne(ctx, orm.NewQuery[User]().Eq(uName, "repo-c")); c == nil || c.Age != 41 || c.City != "xa" {
			t.Errorf("[%s] Repo.UpdatePartial 结果不对：%+v", b.name, c)
		}
		// 列名拼错必须在拼 SQL 之前就被拒（真库这条 SQL 会被标识符引号兜住，
		// 但那样错误信息就变成数据库的语法错误，排查方向会被带偏）。
		if _, err := repo.UpdatePartial(ctx, orm.NewQuery[User]().Eq(uName, "repo-c"),
			map[string]any{"nmae": "x"}); err == nil {
			t.Errorf("[%s] Repo.UpdatePartial 接受了不存在的列名（应拒绝且不下发 SQL）", b.name)
		}

		// ---- UpdateByIdSets ----
		affected, err = repo.UpdateByIdSets(ctx, a.ID, map[string]any{"score": 9.5})
		if err != nil {
			t.Fatalf("[%s] Repo.UpdateByIdSets：%v", b.name, err)
		}
		if affected != 1 {
			t.Errorf("[%s] Repo.UpdateByIdSets 影响 %d 行；期望 1", b.name, affected)
		}
		if got, _ := repo.SelectById(ctx, a.ID); got == nil || got.Score != 9.5 {
			t.Errorf("[%s] Repo.UpdateByIdSets 未生效：%+v", b.name, got)
		}

		// ---- Upsert / BatchUpsert（冲突键落在 UNIQUE 列上，避开自增主键那条特殊路径）----
		uq := orm.NewRepo[UqUser](db)
		uEmail := orm.ColOf[UqUser]("Email")
		first := UqUser{Email: "repo@x.com", Name: "first"}
		if err := uq.Upsert(ctx, &first, []string{"email"}); err != nil {
			t.Fatalf("[%s] Repo.Upsert：%v", b.name, err)
		}
		if err := uq.Upsert(ctx, &UqUser{Email: "repo@x.com", Name: "second"}, []string{"email"}); err != nil {
			t.Fatalf("[%s] Repo.Upsert（第二次应走更新分支）：%v", b.name, err)
		}
		if n, _ := uq.Count(ctx, orm.NewQuery[UqUser]().Eq(uEmail, "repo@x.com")); n != 1 {
			t.Errorf("[%s] 同一冲突键 Upsert 两次后共 %d 行；期望 1", b.name, n)
		}
		if u, _ := uq.SelectOne(ctx, orm.NewQuery[UqUser]().Eq(uEmail, "repo@x.com")); u == nil || u.Name != "second" {
			t.Errorf("[%s] Upsert 未更新既有行：%+v", b.name, u)
		}
		if err := uq.BatchUpsert(ctx, []UqUser{
			{Email: "p1@x.com", Name: "p1"},
			{Email: "p2@x.com", Name: "p2"},
		}, []string{"email"}); err != nil {
			t.Fatalf("[%s] Repo.BatchUpsert：%v", b.name, err)
		}
		if n, _ := uq.Count(ctx, orm.NewQuery[UqUser]()); n != 3 {
			t.Errorf("[%s] BatchUpsert 后共 %d 行；期望 3", b.name, n)
		}

		// ---- RawQuery / RawOne / RawExec（占位符按方言拼：PG 是 $1/$2，其余是 ?）----
		// 注意 placeholderOf() 返回的是「计数用」的字符（PG 为 "$"），拼 SQL 要自己写序号。
		ph1, ph2 := "?", "?"
		if b.dialect == orm.PG {
			ph1, ph2 = "$1", "$2"
		}
		rawList, err := repo.RawQuery(ctx,
			fmt.Sprintf("SELECT id, name FROM it_users WHERE name LIKE %s ORDER BY id", ph1), "repo%")
		if err != nil {
			t.Fatalf("[%s] Repo.RawQuery：%v", b.name, err)
		}
		if len(rawList) != 3 {
			t.Errorf("[%s] Repo.RawQuery 返回 %d 行；期望 3", b.name, len(rawList))
		}
		rawOne, err := repo.RawOne(ctx,
			fmt.Sprintf("SELECT name FROM it_users WHERE id = %s", ph1), a.ID)
		if err != nil || rawOne.Name != "repo-a" {
			t.Errorf("[%s] Repo.RawOne = %+v, err = %v", b.name, rawOne, err)
		}
		res, err := repo.RawExec(ctx,
			fmt.Sprintf("UPDATE it_users SET city = %s WHERE id = %s", ph1, ph2), "raw", a.ID)
		if err != nil {
			t.Fatalf("[%s] Repo.RawExec：%v", b.name, err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			t.Errorf("[%s] Repo.RawExec 影响 %d 行；期望 1", b.name, n)
		}

		// ---- Delete / DeleteById（逻辑删）与 ForceDelete / ForceDeleteById（物理删）----
		if err := repo.Delete(ctx, orm.NewQuery[User]().Eq(uName, "repo-c")); err != nil {
			t.Fatalf("[%s] Repo.Delete：%v", b.name, err)
		}
		if n, _ := repo.Count(ctx, orm.NewQuery[User]()); n != 2 {
			t.Errorf("[%s] 条件 Delete 后可见行 %d；期望 2", b.name, n)
		}
		if err := repo.DeleteById(ctx, a.ID); err != nil {
			t.Fatalf("[%s] Repo.DeleteById：%v", b.name, err)
		}
		if n, _ := repo.Count(ctx, orm.NewQuery[User]()); n != 1 {
			t.Errorf("[%s] DeleteById 后可见行 %d；期望 1", b.name, n)
		}
		if n, _ := repo.Count(ctx, orm.NewQuery[User]().Unscoped()); n != 3 {
			t.Errorf("[%s] Unscoped 行数 %d；期望 3（逻辑删的物理行仍在）", b.name, n)
		}
		if err := repo.ForceDelete(ctx, orm.NewQuery[User]().Unscoped().Eq(uName, "repo-c")); err != nil {
			t.Fatalf("[%s] Repo.ForceDelete：%v", b.name, err)
		}
		if err := repo.ForceDeleteById(ctx, a.ID); err != nil {
			t.Fatalf("[%s] Repo.ForceDeleteById：%v", b.name, err)
		}
		if n, _ := repo.Count(ctx, orm.NewQuery[User]().Unscoped()); n != 1 {
			t.Errorf("[%s] 物理删两条后 Unscoped 行数 %d；期望 1", b.name, n)
		}

		// ---- Transaction：提交分支 ----
		if err := repo.Transaction(ctx, func(tx *orm.Repo[User]) error {
			return tx.Insert(ctx, &User{Name: "tx-ok", Age: 1, City: "tx",
				Meta: map[string]any{}, CreatedAt: baseTime})
		}); err != nil {
			t.Fatalf("[%s] Repo.Transaction（提交）：%v", b.name, err)
		}
		if n, _ := repo.Count(ctx, orm.NewQuery[User]().Eq(uName, "tx-ok")); n != 1 {
			t.Errorf("[%s] 事务提交后读不到 tx-ok", b.name)
		}

		// ---- Transaction：回滚分支（错误必须原样返回，且写入不得落库）----
		sentinel := errors.New("主动回滚")
		if err := repo.Transaction(ctx, func(tx *orm.Repo[User]) error {
			if err := tx.Insert(ctx, &User{Name: "tx-rollback", Age: 1, City: "tx",
				Meta: map[string]any{}, CreatedAt: baseTime}); err != nil {
				return err
			}
			return sentinel
		}); !errors.Is(err, sentinel) {
			t.Errorf("[%s] Transaction 未把回调错误原样返回：%v", b.name, err)
		}
		if n, _ := repo.Count(ctx, orm.NewQuery[User]().Eq(uName, "tx-rollback")); n != 0 {
			t.Errorf("[%s] 回滚事务的写入仍然落库了（%d 行）", b.name, n)
		}
	})
}

// TestQueryPredicateMatrix 把每个谓词构造器都在真库上执行一遍，并按基准数据核对命中行数。
//
// 基准数据（seedUsers）：
//
//	alice 30 bj 90.5 | bob 25 sh 80 | carol 35 bj 70.25 | dave 25 gz 60
//
// 走 Count 而不是只 Build()：Count 会真的下发一条 SQL，任何「引号/占位符/运算符」
// 层面的方言错误都会在这里当场暴露。
func TestQueryPredicateMatrix(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		seedUsers(t, db)

		check := func(name string, q *orm.Query[User], want int64) {
			t.Helper()
			got, err := orm.Count(ctx, db, q)
			if err != nil {
				t.Errorf("[%s] %s 执行失败：%v", b.name, name, err)
				return
			}
			if got != want {
				t.Errorf("[%s] %s 命中 %d 行；期望 %d", b.name, name, got, want)
			}
		}

		// 比较运算
		check("Ne(city,bj)", orm.NewQuery[User]().Ne(uCity, "bj"), 2)
		check("Gt(age,25)", orm.NewQuery[User]().Gt(uAge, 25), 2)
		check("Ge(age,25)", orm.NewQuery[User]().Ge(uAge, 25), 4)
		check("Lt(age,30)", orm.NewQuery[User]().Lt(uAge, 30), 2)
		check("Le(age,30)", orm.NewQuery[User]().Le(uAge, 30), 3)
		check("Between(age,25,30)", orm.NewQuery[User]().Between(uAge, 25, 30), 3)

		// LIKE 家族（val 里不带 % —— 通配符由框架按方法语义自动补）
		check("Like(name,li)  ∋", orm.NewQuery[User]().Like(uName, "li"), 1)
		check("LikeRight(name,ca)  前缀", orm.NewQuery[User]().LikeRight(uName, "ca"), 1)
		check("LikeLeft(name,ve)   后缀", orm.NewQuery[User]().LikeLeft(uName, "ve"), 1)
		check("NotLike(name,a)", orm.NewQuery[User]().NotLike(uName, "a"), 1)
		check("NotLikeRight(name,a)", orm.NewQuery[User]().NotLikeRight(uName, "a"), 3)
		check("NotLikeLeft(name,e)", orm.NewQuery[User]().NotLikeLeft(uName, "e"), 2)

		// 集合与空值
		check("In(city,bj|sh)", orm.NewQuery[User]().In(uCity, []any{"bj", "sh"}), 3)
		check("NotIn(city,bj)", orm.NewQuery[User]().NotIn(uCity, []any{"bj"}), 2)
		check("IsNull(deleted_at)", orm.NewQuery[User]().IsNull(uDeletedAt), 4)
		check("IsNotNull(deleted_at)", orm.NewQuery[User]().IsNotNull(uDeletedAt), 0)

		// 条件组 / 条件块 / 指针值 / 列表达式两种取法
		check("Eq(bj).Or().Eq(gz)", orm.NewQuery[User]().Eq(uCity, "bj").Or().Eq(uCity, "gz"), 3)
		check("If(true).Eq(bj)", orm.NewQuery[User]().If(true, func(q *orm.Query[User]) {
			q.Eq(uCity, "bj")
		}), 2)
		check("If(false).Eq(bj)", orm.NewQuery[User]().If(false, func(q *orm.Query[User]) {
			q.Eq(uCity, "bj")
		}), 4)
		check("Eq(Ptr(25))", orm.NewQuery[User]().Eq(uAge, orm.Ptr(25)), 2)
		check("Eq(ColOf(Go字段名))", orm.NewQuery[User]().Eq(orm.ColOf[User]("City"), "bj"), 2)
		check("Eq(ColOf(列名))", orm.NewQuery[User]().Eq(orm.ColOf[User]("city"), "bj"), 2)
		check("Table(it_users)", orm.NewQuery[User]().Table("it_users"), 4)

		// JSON 路径比较（三方言渲染方式不同：PG -> ->>，MySQL/SQLite JSON_EXTRACT）。
		// 值必须用**字符串**：PG 的 ->> 出来是 text，拿数字去比会直接报
		// operator does not exist（见 TestJsonQuery 与 integration/README 的已知差异）。
		check("Json(meta.tag = hot)", orm.NewQuery[User]().Json(uMeta, "tag", "=", "hot"), 2)

		// ---- 投影 / 分组 / HAVING / 去重 ----
		grouped, err := orm.SelectList(ctx, db,
			orm.NewQuery[User]().Select("city").GroupBy(uCity).Having(uCity, "=", "bj"))
		if err != nil {
			t.Errorf("[%s] GroupBy+Having 执行失败：%v", b.name, err)
		} else if len(grouped) != 1 {
			t.Errorf("[%s] GroupBy+Having 返回 %d 组；期望 1（bj）", b.name, len(grouped))
		}
		distinct, err := orm.SelectList(ctx, db, orm.NewQuery[User]().Select("city").Distinct())
		if err != nil {
			t.Errorf("[%s] Distinct 执行失败：%v", b.name, err)
		} else if len(distinct) != 3 {
			t.Errorf("[%s] Distinct(city) 返回 %d 行；期望 3", b.name, len(distinct))
		}

		// ---- 排序 / 分页 ----
		asc, err := orm.SelectList(ctx, db,
			orm.NewQuery[User]().Select("name").OrderBy(uAge, true).OrderBy(uName, true))
		if err != nil {
			t.Errorf("[%s] 多列排序执行失败：%v", b.name, err)
		} else if len(asc) != 4 || asc[0].Name != "bob" {
			t.Errorf("[%s] age ASC, name ASC 首行 = %q；期望 bob（25 岁，字典序在前）", b.name, asc[0].Name)
		}
		desc, err := orm.SelectList(ctx, db,
			orm.NewQuery[User]().Select("name").OrderBy(uAge, false))
		if err != nil {
			t.Errorf("[%s] 降序执行失败：%v", b.name, err)
		} else if len(desc) != 4 || desc[0].Name != "carol" {
			t.Errorf("[%s] age DESC 首行 = %q；期望 carol（35 岁）", b.name, desc[0].Name)
		}
		paged, err := orm.SelectList(ctx, db,
			orm.NewQuery[User]().Select("name").OrderBy(uID, true).Limit(2).Offset(1))
		if err != nil {
			t.Errorf("[%s] LIMIT+OFFSET 执行失败：%v", b.name, err)
		} else if len(paged) != 2 {
			t.Errorf("[%s] LIMIT 2 OFFSET 1 返回 %d 行；期望 2", b.name, len(paged))
		}

		// ---- 只有 OFFSET 没有 LIMIT：三种方言的处理方式完全不同（MySQL/SQLite 需补无上限 LIMIT，
		//      PG 拒绝负数 LIMIT 所以不能补），这里必须真跑一次确认生成物能被服务器接受 ----
		offOnly, err := orm.SelectList(ctx, db,
			orm.NewQuery[User]().Select("name").OrderBy(uID, true).Offset(2))
		if err != nil {
			t.Errorf("[%s] Offset-only 执行失败：%v", b.name, err)
		} else if len(offOnly) != 2 {
			t.Errorf("[%s] Offset-only 返回 %d 行；期望 2（4 行跳过 2）", b.name, len(offOnly))
		}

		// ---- Last：原样追加尾部片段（静态字面量；拼用户输入即注入）----
		last, err := orm.SelectList(ctx, db,
			orm.NewQuery[User]().Select("name").OrderBy(uID, true).Last("LIMIT 2"))
		if err != nil {
			t.Errorf("[%s] Last 片段执行失败：%v", b.name, err)
		} else if len(last) != 2 {
			t.Errorf("[%s] Last(LIMIT 2) 返回 %d 行；期望 2", b.name, len(last))
		}
	})
}

// TestJoinVariantsExecutable 六个 JOIN 变体各跑一次，用行数区分行为差异。
//
// 数据：alice 2 单、bob 1 单、carol/dave 无单。
//
//	INNER → 3（只留有单的用户）
//	LEFT  → 5（alice 2 + bob 1 + carol/dave 各一条 NULL 匹配）
//	RIGHT → 3（三张单都有对应用户）
func TestJoinVariantsExecutable(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		users := seedUsers(t, db)
		seedOrders(t, db, users)

		const (
			innerWant = 3
			leftWant  = 5
			rightWant = 3
		)
		sel := func(q *orm.Query[User]) *orm.Query[User] {
			return q.Select("it_users.id", "it_users.name")
		}
		run := func(name string, q *orm.Query[User], want int) {
			t.Helper()
			list, err := orm.SelectList(ctx, db, q)
			if err != nil {
				t.Errorf("[%s] %s 执行失败：%v", b.name, name, err)
				return
			}
			if len(list) != want {
				t.Errorf("[%s] %s 返回 %d 行；期望 %d", b.name, name, len(list), want)
			}
		}

		on := "it_orders.user_id = it_users.id"
		run("Join", sel(orm.NewQuery[User]()).Join("it_orders", on), innerWant)
		run("LeftJoin", sel(orm.NewQuery[User]()).LeftJoin("it_orders", on), leftWant)
		run("JoinAs", sel(orm.NewQuery[User]()).JoinAs("it_orders", "o", "o.user_id = it_users.id"), innerWant)
		run("LeftJoinAs", sel(orm.NewQuery[User]()).LeftJoinAs("it_orders", "o", "o.user_id = it_users.id"), leftWant)

		// RIGHT JOIN：SQLite 3.39+ 才支持；旧版本会直接报语法错误，
		// 这里如实记录版本并跳过，而不是把「引擎不支持」当成「我们写错了」。
		if b.dialect == orm.SQLite {
			var ver string
			if err := db.SQL().QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&ver); err != nil {
				t.Fatalf("[%s] 读取 sqlite_version 失败：%v", b.name, err)
			}
			t.Logf("[%s] SQLite 版本 %s（RIGHT JOIN 需 3.39+）", b.name, ver)
		}
		run("RightJoin", sel(orm.NewQuery[User]()).RightJoin("it_orders", on), rightWant)
		run("RightJoinAs", sel(orm.NewQuery[User]()).RightJoinAs("it_orders", "o", "o.user_id = it_users.id"), rightWant)

		// 主表别名 + 被联表别名，且 Count / SelectList 必须同一口径
		// （Count 曾漏掉 JOIN，别名是更容易漏的一层壳）。
		aliased := orm.NewQuery[User]().Alias("u").
			JoinAs("it_orders", "o", "o.user_id = u.id").
			Select("u.id", "u.name")
		alist, err := orm.SelectList(ctx, db, aliased)
		if err != nil {
			t.Errorf("[%s] Alias+JoinAs 执行失败：%v", b.name, err)
		}
		acnt, err := orm.Count(ctx, db, aliased)
		if err != nil {
			t.Errorf("[%s] Alias+JoinAs 的 Count 执行失败：%v", b.name, err)
		} else if int64(len(alist)) != acnt {
			t.Errorf("[%s] 带别名时 Count=%d 与列表行数=%d 不一致", b.name, acnt, len(alist))
		}
	})
}

// TestWriteOptionsOnRealDB 验证两个写入白名单/过滤器在真库上的实际效果。
//
// 背景（bench/README.md 记录过的事故）：UpdateById / Update 默认写入**全部**可写列，
// 实体上没赋值的字段会被一并写回 —— 典型是 time.Time 零值把 created_at 抹成 0001-01-01。
// OnlyColumns / OmitZero 就是给这种情况兜底的，所以必须证明它们**真的保住了未提及的列**。
func TestWriteOptionsOnRealDB(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		users := seedUsers(t, db)
		target := users[0] // alice，CreatedAt = baseTime + 1h

		// ---- OnlyColumns：白名单内的列照写，白名单外的列保持原值 ----
		if err := orm.UpdateById(ctx, db,
			&User{ID: target.ID, Age: 99}, orm.OnlyColumns("age")); err != nil {
			t.Fatalf("[%s] UpdateById(OnlyColumns(age))：%v", b.name, err)
		}
		got, err := orm.SelectById[User](ctx, db, target.ID)
		if err != nil || got == nil {
			t.Fatalf("[%s] 读回失败：%v", b.name, err)
		}
		if got.Age != 99 {
			t.Errorf("[%s] OnlyColumns(age) 未生效：age=%d；期望 99", b.name, got.Age)
		}
		if got.Name != target.Name {
			t.Errorf("[%s] OnlyColumns(age) 把 name 也改了：%q → %q", b.name, target.Name, got.Name)
		}
		if !got.CreatedAt.Equal(target.CreatedAt) {
			t.Errorf("[%s] OnlyColumns(age) 把 created_at 写了：%v → %v（这正是 bench 里踩过的坑）",
				b.name, target.CreatedAt, got.CreatedAt)
		}

		// ---- OmitZero：零值字段被跳过，同样保住 created_at ----
		if err := orm.UpdateById(ctx, db,
			&User{ID: target.ID, City: "sh"}, orm.OmitZero()); err != nil {
			t.Fatalf("[%s] UpdateById(OmitZero())：%v", b.name, err)
		}
		got2, err := orm.SelectById[User](ctx, db, target.ID)
		if err != nil || got2 == nil {
			t.Fatalf("[%s] 读回失败：%v", b.name, err)
		}
		if got2.City != "sh" {
			t.Errorf("[%s] OmitZero 未写入 city：%q", b.name, got2.City)
		}
		if got2.Age != 99 {
			t.Errorf("[%s] OmitZero 把上一轮写好的 age 覆盖成了 %d；期望保持 99", b.name, got2.Age)
		}
		if !got2.CreatedAt.Equal(target.CreatedAt) {
			t.Errorf("[%s] OmitZero 把 created_at 写了：%v → %v", b.name, target.CreatedAt, got2.CreatedAt)
		}

		// ---- 两者叠加：先取白名单，再按零值过滤 ----
		if err := orm.UpdateById(ctx, db,
			&User{ID: target.ID, City: "", Age: 77},
			orm.OnlyColumns("age", "city"), orm.OmitZero()); err != nil {
			t.Fatalf("[%s] UpdateById(OnlyColumns+OmitZero)：%v", b.name, err)
		}
		got3, _ := orm.SelectById[User](ctx, db, target.ID)
		if got3 == nil || got3.Age != 77 || got3.City != "sh" {
			t.Errorf("[%s] 叠加用法结果不对：age=%v city=%q；期望 77/sh（city 零值被过滤掉）",
				b.name, got3.Age, got3.City)
		}

		// ---- 参数校验：拼错列名 / 指向主键 / 指向逻辑删除列，都应报错且不下发 SQL ----
		bad := []struct {
			name string
			opt  orm.WriteOption
		}{
			{"拼错的列名", orm.OnlyColumns("nmae")},
			{"主键", orm.OnlyColumns("id")},
			{"逻辑删除列", orm.OnlyColumns("deleted_at")},
		}
		for _, c := range bad {
			if err := orm.UpdateById(ctx, db, &User{ID: target.ID, Age: 1}, c.opt); err == nil {
				t.Errorf("[%s] OnlyColumns(%s) 没有报错", b.name, c.name)
			}
		}
		// 空白名单：报错信息要能指明「一列都没有」而不是归咎 OmitZero
		if err := orm.UpdateById(ctx, db, &User{ID: target.ID}, orm.OnlyColumns()); err == nil {
			t.Errorf("[%s] OnlyColumns() 传空没有报错", b.name)
		}

		// ---- 行为取证（非断言）：默认「全列写」在各方言下的后果并不一样 ----
		// MySQL 严格模式的 DATETIME 不接受零值时间，会直接报错（实测 Error 1292，
		// 客户端把零值时间编码成 '0000-00-00' 发过去）；PG / SQLite 接受，于是静默抹掉。
		// 两种结果都不算「bug」（契约就是写全部可写列），但排错时知道差异能省很多时间。
		probe := User{ID: target.ID, Name: "clobber", Age: 50}
		ph := "?"
		if b.dialect == orm.PG {
			ph = "$1"
		}
		if err := orm.UpdateById(ctx, db, &probe); err != nil {
			t.Logf("[%s] 默认全列写（实体未带 created_at）被服务器拒绝：%v"+
				" —— 这是 MySQL 严格模式 DATETIME 的取值范围所致，OnlyColumns / OmitZero 即为规避它而存在",
				b.name, err)
		} else {
			stored, rerr := orm.RawOne[time.Time](ctx, db,
				fmt.Sprintf("SELECT created_at FROM it_users WHERE id = %s", ph), target.ID)
			if rerr != nil {
				t.Logf("[%s] 默认全列写成功；读回 created_at 失败（%v）", b.name, rerr)
			} else {
				t.Logf("[%s] 默认全列写成功，库里 created_at = %v（原值 %v）—— 差异即「零值被写回」",
					b.name, stored, target.CreatedAt)
			}
		}
	})
}

// TestDbConfigMethodsExecutable 真跑一遍 DB 级配置方法（With* / SQL / Transaction / NewDB）。
//
// 这些方法此前只有「调一下不 panic」级别的覆盖，而它们最容易出的错是
// 「clone 时漏拷了某个字段」—— 例如 WithLogger 忘了带 dialect，或 WithExecutor
// 忘了带 prefix。用**可观测的效果**去断言，比断言返回类型有意义得多。
func TestDbConfigMethodsExecutable(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		seedUsers(t, db)

		// ---- WithSoftDeleteField / WithOptimisticField：配置链路可执行 ----
		// （User 已用 ,logic tag 声明软删除，这里验证「约定字段名」这条并行路径不会干扰它）
		cfgDB := db.WithSoftDeleteField("deleted_at").WithOptimisticField("version")
		if n, err := orm.Count(ctx, cfgDB, orm.NewQuery[User]()); err != nil || n != 4 {
			t.Errorf("[%s] WithSoftDeleteField 后 Count = %d, err = %v；期望 4", b.name, n, err)
		}

		// ---- WithLogger + WithLogLevel：回调必须真的被调用，且带上真实 SQL ----
		var buf bytes.Buffer
		infoDB := db.WithLogger(orm.DefaultLogger(&buf)).WithLogLevel(orm.Info)
		if _, err := orm.Count(ctx, infoDB, orm.NewQuery[User]()); err != nil {
			t.Fatalf("[%s] 带日志的 Count：%v", b.name, err)
		}
		if buf.Len() == 0 {
			t.Errorf("[%s] 配置了 Logger + LogLevel(Info) 却一条日志都没有", b.name)
		} else if !strings.Contains(buf.String(), "it_users") {
			t.Errorf("[%s] 日志里没有真实表名：%s", b.name, buf.String())
		}

		// ---- WithSlowThreshold：普通查询不该出日志，慢查询应该出（升级为 Warn）----
		//
		// 这里刻意用一条**耗时足够长**的原生 SQL（8 路自连接 = 4^8 = 65536 行），而不是普通的
		// COUNT(*)：Windows 上 time.Since 的粒度可能让极快的查询测出 0 时长，而慢查询的判定是
		// dur > threshold —— 时长恰好为 0 时任何正阈值都不会命中，断言就会随机器飘
		//（实测 SQLite 内存库的 COUNT(*) 记到的就是 0s，而同一轮里首次 UPDATE 记到 19ms）。
		heavy := "SELECT COUNT(*) FROM it_users a, it_users b, it_users c, it_users d, " +
			"it_users e, it_users f, it_users g, it_users h"
		var slowBuf bytes.Buffer
		warnDB := db.WithLogger(orm.DefaultLogger(&slowBuf)).WithLogLevel(orm.Warn)
		if _, err := orm.RawExec(ctx, warnDB, heavy); err != nil {
			t.Fatalf("[%s] 慢查询用例的重型 SQL 执行失败：%v", b.name, err)
		}
		if slowBuf.Len() != 0 {
			t.Errorf("[%s] LogLevel(Warn) 且未设阈值时不该输出：%s", b.name, slowBuf.String())
		}
		slowDB := warnDB.WithSlowThreshold(time.Nanosecond)
		start := time.Now()
		if _, err := orm.RawExec(ctx, slowDB, heavy); err != nil {
			t.Fatalf("[%s] 慢查询阈值下的重型 SQL 执行失败：%v", b.name, err)
		}
		wall := time.Since(start)
		if wall == 0 {
			t.Logf("[%s] 本机时钟粒度不足（外部实测耗时 0s），慢查询升级断言在本机不可判定", b.name)
		} else if slowBuf.Len() == 0 || !strings.Contains(slowBuf.String(), "WARN") {
			t.Errorf("[%s] 阈值设为 1ns（外部实测耗时 %v）后应输出慢查询 WARN 日志，实际：%s",
				b.name, wall, slowBuf.String())
		}

		// ---- WithHooks：before / after 两个阶段都要收到，且 after 上有耗时 ----
		hook := &recordingHook{}
		hookDB := db.WithHooks(hook)
		if _, err := orm.Count(ctx, hookDB, orm.NewQuery[User]()); err != nil {
			t.Fatalf("[%s] 带 Hook 的 Count：%v", b.name, err)
		}
		phases := map[orm.HookPhase]int{}
		for _, e := range hook.events {
			phases[e.Phase]++
		}
		if phases[orm.HookPhaseBefore] == 0 || phases[orm.HookPhaseAfter] == 0 {
			t.Errorf("[%s] Hook 事件不完整（before=%d after=%d）",
				b.name, phases[orm.HookPhaseBefore], phases[orm.HookPhaseAfter])
		}
		// 写入路径也要触发（exec 类事件）
		if err := orm.Insert(ctx, hookDB, &User{Name: "hook", Age: 1, City: "h",
			Meta: map[string]any{}, CreatedAt: baseTime}); err != nil {
			t.Fatalf("[%s] 带 Hook 的 Insert：%v", b.name, err)
		}
		kinds := map[orm.HookKind]int{}
		for _, e := range hook.events {
			kinds[e.Kind]++
		}
		if kinds[orm.HookKindQuery] == 0 || kinds[orm.HookKindExec] == 0 {
			t.Errorf("[%s] Hook 未同时覆盖 query / exec 两类事件：%v", b.name, kinds)
		}

		// ---- WithExecutor：真的换了执行器，而不是只在结构体里存了个字段 ----
		ce := &countingExecutor{inner: db.SQL()}
		execDB := db.WithExecutor(ce)
		if _, err := orm.SelectList(ctx, execDB, orm.NewQuery[User]()); err != nil {
			t.Fatalf("[%s] WithExecutor 后的查询：%v", b.name, err)
		}
		if err := orm.Insert(ctx, execDB, &User{Name: "exec", Age: 1, City: "e",
			Meta: map[string]any{}, CreatedAt: baseTime}); err != nil {
			t.Fatalf("[%s] WithExecutor 后的写入：%v", b.name, err)
		}
		// PG 的 Insert 走「INSERT ... RETURNING」→ QueryRowContext，因此不能要求
		// ExecContext 一定被调用；这里只要求两条路径合计至少 2 次
		//（一次读经 QueryContext，一次写经 QueryRow 或 Exec）。
		if ce.queries+ce.execs < 2 {
			t.Errorf("[%s] 自定义执行器未被真正使用（query=%d exec=%d）", b.name, ce.queries, ce.execs)
		}
		t.Logf("[%s] 自定义执行器调用统计：query=%d exec=%d（PG 的写入走 QueryRow，exec 为 0 属预期）",
			b.name, ce.queries, ce.execs)

		// ---- NewDB：从「执行器 + 方言」直接构造，不经过 Open ----
		fresh := orm.NewDB(db.SQL(), b.dialect)
		if n, err := orm.Count(ctx, fresh, orm.NewQuery[User]()); err != nil || n == 0 {
			t.Errorf("[%s] NewDB 构造的实例无法查询：n=%d err=%v", b.name, n, err)
		}

		// ---- Transaction（对 *DB 本身）----
		if err := db.Transaction(ctx, func(tx *orm.DB) error {
			return orm.Insert(ctx, tx, &User{Name: "db-tx", Age: 1, City: "t",
				Meta: map[string]any{}, CreatedAt: baseTime})
		}); err != nil {
			t.Fatalf("[%s] db.Transaction：%v", b.name, err)
		}
		if n, _ := orm.Count(ctx, db, orm.NewQuery[User]().Eq(uName, "db-tx")); n != 1 {
			t.Errorf("[%s] db.Transaction 提交后读不到 db-tx", b.name)
		}

		// ---- Open("driver", dsn) 两个字符串参数的写法 + 驱动名→方言映射 ----
		db2, err := orm.Open(b.driver, b.dsn)
		if err != nil {
			t.Fatalf("[%s] orm.Open(%q, dsn)：%v", b.name, b.driver, err)
		}
		defer func() { _ = db2.SQL().Close() }()
		sqlStr, args := orm.DryRun(db2, orm.NewQuery[User]().Ge(uAge, 25))
		if len(args) != 1 {
			t.Errorf("[%s] Open 出来的实例参数个数 %d；期望 1", b.name, len(args))
		}
		hasDollar := strings.Contains(sqlStr, "$1")
		if b.dialect == orm.PG && !hasDollar {
			t.Errorf("[%s] 驱动 %q 应映射到 PG 方言（$n 占位符），实际 SQL：%s", b.name, b.driver, sqlStr)
		}
		if b.dialect != orm.PG && hasDollar {
			t.Errorf("[%s] 驱动 %q 不该生成 $n 占位符，实际 SQL：%s", b.name, b.driver, sqlStr)
		}
		if n, err := orm.Count(ctx, db2, orm.NewQuery[User]()); err != nil || n != 7 {
			// 4 行种子 + hook 用例的 1 行 + exec 用例的 1 行 + db.Transaction 的 1 行
			t.Errorf("[%s] Open 出来的实例查询失败：n=%d err=%v；期望 7", b.name, n, err)
		}
	})
}
