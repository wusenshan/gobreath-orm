package integration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	orm "github.com/wusenshan/gobreath-orm"
)

// TestInsertPKWritebackAndRoundtrip 覆盖两条**完全不同的**主键回填路径，
// 这是 mock 绝对验不出来的部分：
//
//	PostgreSQL：不支持 LastInsertId → 必须走 INSERT ... RETURNING "id"
//	MySQL / SQLite：支持 LastInsertId → 走 sql.Result.LastInsertId()
//
// 顺带验证八类列的写入/读出往返（含 JSON、时间、可空软删列）。
func TestInsertPKWritebackAndRoundtrip(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()

		in := User{
			Name:      "roundtrip",
			Age:       7,
			Score:     1.5,
			City:      "zz",
			Meta:      map[string]any{"k": "v", "n": 1.0},
			CreatedAt: baseTime,
		}
		if err := orm.Insert(ctx, db, &in); err != nil {
			t.Fatalf("[%s] Insert 失败：%v", b.name, err)
		}
		if in.ID == 0 {
			t.Fatalf("[%s] 自增主键未回填（PG 应走 RETURNING，MySQL/SQLite 应走 LastInsertId）", b.name)
		}

		got, err := orm.SelectById[User](ctx, db, in.ID)
		if err != nil || got == nil {
			t.Fatalf("[%s] SelectById 失败：%v", b.name, err)
		}
		if got.Name != in.Name || got.Age != in.Age || got.Score != in.Score || got.City != in.City {
			t.Errorf("[%s] 标量列往返不一致：%+v", b.name, got)
		}
		if got.Meta["k"] != "v" {
			t.Errorf("[%s] JSON 列往返不一致：%#v", b.name, got.Meta)
		}
		if !got.CreatedAt.Equal(baseTime) {
			t.Errorf("[%s] 时间列往返不一致：得到 %v（%T）；期望 %v",
				b.name, got.CreatedAt, got.CreatedAt, baseTime)
		}

		// 连插两条，主键必须递增且互不相同。
		second := User{Name: "second", Age: 8, City: "zz", Meta: map[string]any{}, CreatedAt: baseTime}
		if err := orm.Insert(ctx, db, &second); err != nil {
			t.Fatalf("[%s] 第二次 Insert 失败：%v", b.name, err)
		}
		if second.ID == in.ID {
			t.Errorf("[%s] 自增主键重复：%d", b.name, second.ID)
		}
	})
}

// TestSelectVariants 覆盖 SelectOne / Exists / 条件组合。
func TestSelectVariants(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		seedUsers(t, db)

		// SelectOne 命中
		one, err := orm.SelectOne(ctx, db, orm.NewQuery[User]().Eq(uName, "bob"))
		if err != nil || one == nil || one.Age != 25 {
			t.Fatalf("[%s] SelectOne(bob) = %+v, err = %v", b.name, one, err)
		}
		// SelectOne 未命中：应返回错误（nil, nil 会让调用方少写一层判空）
		if _, err := orm.SelectOne(ctx, db, orm.NewQuery[User]().Eq(uName, "nobody")); err == nil {
			t.Errorf("[%s] SelectOne 未命中却没有报错", b.name)
		}

		// Exists
		if ok, err := orm.Exists(ctx, db, orm.NewQuery[User]().Eq(uCity, "bj")); err != nil || !ok {
			t.Errorf("[%s] Exists(city=bj) = %v, err = %v；期望 true", b.name, ok, err)
		}
		if ok, err := orm.Exists(ctx, db, orm.NewQuery[User]().Eq(uCity, "nope")); err != nil || ok {
			t.Errorf("[%s] Exists(city=nope) = %v, err = %v；期望 false", b.name, ok, err)
		}

		// IN + 排序 + 去重
		in, err := orm.SelectList(ctx, db,
			orm.NewQuery[User]().In(uCity, []any{"bj", "gz"}).OrderBy(uID, true))
		if err != nil || len(in) != 3 {
			t.Errorf("[%s] In(city in bj,gz) 命中 %d 行, err = %v；期望 3", b.name, len(in), err)
		}
		distinct, err := orm.SelectList(ctx, db, orm.NewQuery[User]().Distinct().Select("city"))
		if err != nil || len(distinct) != 3 {
			t.Errorf("[%s] Distinct(city) 返回 %d 行, err = %v；期望 3（bj/sh/gz）",
				b.name, len(distinct), err)
		}

		// LIMIT / OFFSET
		page2, err := orm.SelectList(ctx, db,
			orm.NewQuery[User]().OrderBy(uID, true).Limit(2).Offset(2))
		if err != nil || len(page2) != 2 || page2[0].Name != "carol" {
			t.Errorf("[%s] Limit2/Offset2 = %v, err = %v；期望 carol, dave", b.name, names(page2), err)
		}
	})
}

// TestPageMetadata 校验分页元信息（Count 修好 JOIN 之后，Page 是最直接的受益方）。
func TestPageMetadata(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		seedUsers(t, db)

		p1, err := orm.Page(ctx, db, orm.NewQuery[User]().OrderBy(uID, true), 1, 3)
		if err != nil {
			t.Fatalf("[%s] Page 出错：%v", b.name, err)
		}
		if len(p1.List) != 3 || p1.Total != 4 || p1.Pages != 2 || !p1.HasNext || p1.HasPrev {
			t.Errorf("[%s] 第 1 页 = 行数%d total%d pages%d next%v prev%v；期望 3/4/2/true/false",
				b.name, len(p1.List), p1.Total, p1.Pages, p1.HasNext, p1.HasPrev)
		}
		p2, err := orm.Page(ctx, db, orm.NewQuery[User]().OrderBy(uID, true), 2, 3)
		if err != nil {
			t.Fatalf("[%s] Page 第 2 页出错：%v", b.name, err)
		}
		if len(p2.List) != 1 || p2.HasNext || !p2.HasPrev {
			t.Errorf("[%s] 第 2 页 = 行数%d next%v prev%v；期望 1/false/true",
				b.name, len(p2.List), p2.HasNext, p2.HasPrev)
		}
	})
}

// TestUpdateVariants 覆盖 UpdateById / Update / 条件更新 / 零值语义。
func TestUpdateVariants(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		users := seedUsers(t, db)

		// UpdateById：整行覆盖
		users[0].City = "sz"
		users[0].Age = 31
		if err := orm.UpdateById(ctx, db, &users[0]); err != nil {
			t.Fatalf("[%s] UpdateById 失败：%v", b.name, err)
		}
		got, _ := orm.SelectById[User](ctx, db, users[0].ID)
		if got == nil || got.City != "sz" || got.Age != 31 {
			t.Errorf("[%s] UpdateById 未生效：%+v", b.name, got)
		}

		// Update（带条件）：把 sh 的人全改成 nj
		// 注意 CreatedAt 必须显式给值：MySQL 严格模式拒收零值时间（见 TestZeroTimeWrite）。
		if err := orm.Update(ctx, db,
			orm.NewQuery[User]().Eq(uCity, "sh"),
			&User{Name: "bob", Age: 25, Score: 80, City: "nj", Meta: map[string]any{}, CreatedAt: baseTime},
		); err != nil {
			t.Fatalf("[%s] Update 失败：%v", b.name, err)
		}
		if n, _ := orm.Count(ctx, db, orm.NewQuery[User]().Eq(uCity, "nj")); n != 1 {
			t.Errorf("[%s] 条件 Update 后 city=nj 有 %d 行；期望 1", b.name, n)
		}

		// UpdateByIdSets：只改指定列（其余列不动）
		if _, err := orm.UpdateByIdSets[User](ctx, db, users[3].ID,
			map[string]any{"city": "xa"}); err != nil {
			t.Fatalf("[%s] UpdateByIdSets 失败：%v", b.name, err)
		}
		got3, _ := orm.SelectById[User](ctx, db, users[3].ID)
		if got3 == nil || got3.City != "xa" || got3.Name != "dave" {
			t.Errorf("[%s] UpdateByIdSets 结果不对：%+v", b.name, got3)
		}
	})
}

// TestSoftDelete 校验软删除的三条语义：默认过滤、Unscoped 逃逸、ForceDelete 真删。
func TestSoftDelete(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		users := seedUsers(t, db)

		if err := orm.DeleteById[User](ctx, db, users[3].ID); err != nil {
			t.Fatalf("[%s] DeleteById 失败：%v", b.name, err)
		}

		// 默认视图：只剩 3 行，且查不到被删的那条
		if n, _ := orm.Count(ctx, db, orm.NewQuery[User]()); n != 3 {
			t.Errorf("[%s] 软删后 Count = %d；期望 3", b.name, n)
		}
		if _, err := orm.SelectById[User](ctx, db, users[3].ID); err == nil {
			t.Errorf("[%s] SelectById 仍能查到已软删的行", b.name)
		}

		// Unscoped：物理行还在，能看到 4 行
		if n, _ := orm.Count(ctx, db, orm.NewQuery[User]().Unscoped()); n != 4 {
			t.Errorf("[%s] Unscoped Count = %d；期望 4", b.name, n)
		}
		// 而且 deleted_at 确实被写成了非空（这一行要靠 Unscoped 才读得到）
		deadList, err := orm.SelectList(ctx, db,
			orm.NewQuery[User]().Unscoped().Eq(uID, users[3].ID))
		if err != nil {
			t.Fatalf("[%s] Unscoped 读被删行失败：%v", b.name, err)
		}
		if len(deadList) != 1 || deadList[0].DeletedAt == nil {
			t.Errorf("[%s] deleted_at 未被写入：%+v", b.name, deadList)
		}

		// ForceDelete：物理删除
		if err := orm.ForceDeleteById[User](ctx, db, users[3].ID); err != nil {
			t.Fatalf("[%s] ForceDeleteById 失败：%v", b.name, err)
		}
		if n, _ := orm.Count(ctx, db, orm.NewQuery[User]().Unscoped()); n != 3 {
			t.Errorf("[%s] ForceDelete 后 Unscoped Count = %d；期望 3", b.name, n)
		}
	})
}

// UqUser 有一个非自增的 UNIQUE 列，用于验证「冲突键不是自增主键」时的 Upsert。
type UqUser struct {
	ID    int64  `db:"id,pk,autoincrement"`
	Email string `db:"email,unique"`
	Name  string `db:"name"`
}

func (UqUser) TableName() string { return "it_uq_users" }

// TestUpsertByUniqueColumn 校验三方言的冲突处理（PG/SQLite 走 ON CONFLICT，MySQL 走 ON DUPLICATE KEY）。
//
// 这里刻意让冲突键落在**普通 UNIQUE 列**上：这条路径是通的，
// 顺带把「自增主键」那条坏路径隔离出来（见 TestUpsertByAutoIncPK）。
func TestUpsertByUniqueColumn(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()

		first := UqUser{Email: "a@x.com", Name: "first"}
		if err := orm.Upsert(ctx, db, &first, []string{"email"}); err != nil {
			t.Fatalf("[%s] 首次 Upsert 失败：%v", b.name, err)
		}
		// 自增主键回填要与 Insert 对齐（PG/SQLite 走 RETURNING，MySQL 走 LAST_INSERT_ID 技巧）。
		if first.ID == 0 {
			t.Errorf("[%s] Upsert 未回填自增主键（Insert 会回填，两者应一致）", b.name)
		}

		// 同 email 再写一次：应命中唯一键冲突 → 更新而不是新增
		second := UqUser{Email: "a@x.com", Name: "second"}
		if err := orm.Upsert(ctx, db, &second, []string{"email"}); err != nil {
			t.Fatalf("[%s] 二次 Upsert 失败：%v", b.name, err)
		}
		if n, _ := orm.Count(ctx, db, orm.NewQuery[UqUser]()); n != 1 {
			t.Errorf("[%s] Upsert 后 Count = %d；期望 1（不新增）", b.name, n)
		}
		got, _ := orm.SelectOne(ctx, db, orm.NewQuery[UqUser]().Eq(
			orm.Col[UqUser](func(u *UqUser) *string { return &u.Email }), "a@x.com"))
		if got == nil || got.Name != "second" {
			t.Errorf("[%s] Upsert 未更新：%+v", b.name, got)
		}
	})
}

// TestUpsertByAutoIncPK —— 自增主键上的「存在则更新」。
//
// 这条曾经是坏的：三种方言都会静默新增一行。根因是 writableCols 跳过 autoInc 列，
// INSERT 语句里没有 "id"，于是 ON CONFLICT ("id") / ON DUPLICATE KEY 永不命中。
// 现在 Upsert 会把「已赋非零值的自增冲突键」补回列清单，所以这里就是一条普通回归测试。
func TestUpsertByAutoIncPK(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()

		u := User{Name: "up", Age: 1, City: "x", Meta: map[string]any{}, CreatedAt: baseTime}
		if err := orm.Insert(ctx, db, &u); err != nil {
			t.Fatalf("[%s] 首次 Insert 失败：%v", b.name, err)
		}
		id := u.ID

		u.Name, u.Age = "up2", 42
		if err := orm.Upsert(ctx, db, &u, []string{"id"}); err != nil {
			t.Fatalf("[%s] Upsert 失败：%v", b.name, err)
		}

		n, _ := orm.Count(ctx, db, orm.NewQuery[User]())
		got, _ := orm.SelectById[User](ctx, db, id)
		if n != 1 {
			t.Errorf("[%s] 自增主键 Upsert 后又多了一行：Count=%d（期望 1）", b.name, n)
		}
		if got == nil || got.Age != 42 || got.Name != "up2" {
			t.Errorf("[%s] 自增主键 Upsert 未更新目标行：%+v", b.name, got)
		}
		if u.ID != id {
			t.Errorf("[%s] Upsert 后实体主键被改成 %d；期望仍为 %d", b.name, u.ID, id)
		}
	})
}

// TestZeroTimeWrite 登记「未赋值的 time.Time 字段」在各方言下的写行为差异。
//
// 这不是缺陷，而是客观的方言差异，且**有意保留**：零值 time.Time 会被
// go-sql-driver 格式化成 '0000-00-00 00:00:00'，MySQL 默认严格模式会直接拒绝。
// 与其在 ORM 里悄悄把零值时间改写成 NULL（那是替调用方改语义、会掩盖漏赋值），
// 不如让错误直接暴露：时间列请显式赋值，需要 NULL 语义就用指针字段。
func TestZeroTimeWrite(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		u := User{Name: "zt", Age: 1, City: "x", Meta: map[string]any{}} // CreatedAt 留零值
		err := orm.Insert(ctx, db, &u)
		switch {
		case err == nil:
			t.Logf("[%s] 零值 time.Time 写入成功（PG 接受公元 1 年，SQLite 不校验）", b.name)
		case b.name == "mysql":
			t.Skipf("已确认的方言差异（保留）：MySQL 严格模式拒绝零值时间，请显式赋值或用指针字段：%v", err)
		default:
			t.Errorf("[%s] 零值时间写入失败：%v", b.name, err)
		}
	})
}

// TestTransactionRollback 校验事务回滚后数据真的不落地。
func TestTransactionRollback(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		boom := errors.New("boom")

		err := db.Transaction(ctx, func(tx *orm.DB) error {
			if err := orm.Insert(ctx, tx, &User{Name: "tx", Age: 1, City: "x",
				Meta: map[string]any{}, CreatedAt: baseTime}); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("[%s] 事务错误未透传：%v", b.name, err)
		}
		if n, _ := orm.Count(ctx, db, orm.NewQuery[User]()); n != 0 {
			t.Errorf("[%s] 回滚后还有 %d 行；期望 0", b.name, n)
		}

		// 正常提交
		if err := db.Transaction(ctx, func(tx *orm.DB) error {
			return orm.Insert(ctx, tx, &User{Name: "ok", Age: 1, City: "x",
				Meta: map[string]any{}, CreatedAt: baseTime})
		}); err != nil {
			t.Fatalf("[%s] 事务提交失败：%v", b.name, err)
		}
		if n, _ := orm.Count(ctx, db, orm.NewQuery[User]()); n != 1 {
			t.Errorf("[%s] 提交后 Count = %d；期望 1", b.name, n)
		}
	})
}

// TestOptimisticLock 校验乐观锁：版本匹配才更新，陈旧写入被拒。
func TestOptimisticLock(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()

		doc := Doc{Title: "v1", Version: 1}
		if err := orm.Insert(ctx, db, &doc); err != nil {
			t.Fatalf("[%s] Insert Doc 失败：%v", b.name, err)
		}

		loaded, err := orm.SelectById[Doc](ctx, db, doc.ID)
		if err != nil || loaded == nil {
			t.Fatalf("[%s] 读 Doc 失败：%v", b.name, err)
		}
		loaded.Title = "v2"
		if err := orm.UpdateById(ctx, db, loaded); err != nil {
			t.Fatalf("[%s] 正常更新应成功：%v", b.name, err)
		}
		after, _ := orm.SelectById[Doc](ctx, db, doc.ID)
		if after == nil || after.Version != 2 || after.Title != "v2" {
			t.Fatalf("[%s] 版本未自增：%+v", b.name, after)
		}

		// 用陈旧版本（1）再写一次：必须被拒，否则并发覆盖就无声发生了。
		stale := Doc{ID: doc.ID, Title: "stale", Version: 1}
		err = orm.UpdateById(ctx, db, &stale)
		if err == nil {
			t.Errorf("[%s] 陈旧版本更新竟然成功了（并发覆盖会静默发生）", b.name)
		} else if !errors.Is(err, orm.ErrOptimisticLock) {
			t.Errorf("[%s] 期望 ErrOptimisticLock，实际：%v", b.name, err)
		} else {
			t.Logf("[%s] 陈旧版本被拒（ErrOptimisticLock）✓", b.name)
		}
	})
}

// TestForUpdate 校验悲观锁子句在真库里合法。
// SQLite 不支持行级锁，方言返回空串（这是设计而非缺失），因此断言也按能力区分。
func TestForUpdate(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		users := seedUsers(t, db)

		err := db.Transaction(ctx, func(tx *orm.DB) error {
			list, err := orm.SelectList(ctx, tx, orm.NewQuery[User]().ForUpdate().Eq(uID, users[0].ID))
			if err != nil {
				return err
			}
			if len(list) != 1 {
				t.Errorf("[%s] FOR UPDATE 查询返回 %d 行；期望 1", b.name, len(list))
			}
			return nil
		})
		if err != nil {
			t.Fatalf("[%s] 事务内 FOR UPDATE 失败：%v", b.name, err)
		}

		sqlStr, _ := orm.DryRun(db, orm.NewQuery[User]().ForUpdate())
		if b.name == "sqlite" {
			if sqlStr != "" && containsFold(sqlStr, "for update") {
				t.Errorf("[%s] SQLite 不该生成 FOR UPDATE：%s", b.name, sqlStr)
			}
		} else if !containsFold(sqlStr, "for update") {
			t.Errorf("[%s] %s 应生成 FOR UPDATE：%s", b.name, b.name, sqlStr)
		}
	})
}

// TestPreloadHasManyAndBelongsTo 校验两种关系的预加载在真库上真的把子表查回来了。
func TestPreloadHasManyAndBelongsTo(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		users := seedUsers(t, db)

		posts := []Post{
			{UserID: users[0].ID, Title: "p1"},
			{UserID: users[0].ID, Title: "p2"},
			{UserID: users[1].ID, Title: "p3"},
		}
		for i := range posts {
			if err := orm.Insert(ctx, db, &posts[i]); err != nil {
				t.Fatalf("[%s] Insert Post 失败：%v", b.name, err)
			}
		}

		list, err := orm.SelectList(ctx, db, orm.NewQuery[RelUser]().OrderBy(ruID, true))
		if err != nil {
			t.Fatalf("[%s] SelectList RelUser 失败：%v", b.name, err)
		}
		if len(list) != 4 {
			t.Fatalf("[%s] RelUser 返回 %d 行；期望 4", b.name, len(list))
		}
		if err := orm.Preload(ctx, db, &list, "Posts"); err != nil {
			t.Fatalf("[%s] Preload(has_many) 失败：%v", b.name, err)
		}
		wantCounts := []int{2, 1, 0, 0}
		for i, want := range wantCounts {
			if len(list[i].Posts) != want {
				t.Errorf("[%s] %s 的 Posts 有 %d 条；期望 %d",
					b.name, list[i].Name, len(list[i].Posts), want)
			}
		}

		// belongs_to：comments.user_id → accounts.id
		acc := Account{Name: "shop"}
		if err := orm.Insert(ctx, db, &acc); err != nil {
			t.Fatalf("[%s] Insert Account 失败：%v", b.name, err)
		}
		cm := Comment{Body: "hello", UserID: acc.ID}
		if err := orm.Insert(ctx, db, &cm); err != nil {
			t.Fatalf("[%s] Insert Comment 失败：%v", b.name, err)
		}
		cms, err := orm.SelectList(ctx, db, orm.NewQuery[Comment]().OrderBy(cUserID, true))
		if err != nil || len(cms) != 1 {
			t.Fatalf("[%s] SelectList Comment 失败：%v", b.name, err)
		}
		if err := orm.Preload(ctx, db, &cms, "Author"); err != nil {
			t.Fatalf("[%s] Preload(belongs_to) 失败：%v", b.name, err)
		}
		if cms[0].Author == nil || cms[0].Author.Name != "shop" {
			t.Errorf("[%s] belongs_to 未填充：%+v", b.name, cms[0].Author)
		}
	})
}

// TestAutoMigrateIsIdempotent 校验 AutoMigrate 反复执行不报错（CREATE TABLE IF NOT EXISTS）。
func TestAutoMigrateIsIdempotent(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		seedUsers(t, db)
		for i := 0; i < 3; i++ {
			if err := db.AutoMigrate(ctx, &User{}, &Order{}); err != nil {
				t.Fatalf("[%s] 第 %d 次 AutoMigrate 失败：%v", b.name, i+1, err)
			}
		}
		if n, _ := orm.Count(ctx, db, orm.NewQuery[User]()); n != 4 {
			t.Errorf("[%s] 重复 AutoMigrate 影响了数据：Count = %d", b.name, n)
		}
	})
}

// TestAutoMigrateIndexTag 校验 db tag 的 ,index 修饰符，并回归 MySQL 的方言坑：
// MySQL（含 8.x）不支持 CREATE INDEX IF NOT EXISTS（那是 MariaDB 扩展，带上会 Error 1064），
// 所以 migrate.go 对 MySQL 生成朴素 CREATE INDEX，幂等性改由 AutoMigrate 忽略
// 「索引已存在」错误兜住 —— 所以这里连跑两次，既是建索引验证也是幂等验证。
func TestAutoMigrateIndexTag(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		if _, err := orm.RawExec(ctx, db, "DROP TABLE IF EXISTS it_idx_users"); err != nil {
			t.Fatalf("DROP 失败：%v", err)
		}
		for i := 0; i < 2; i++ {
			if err := db.AutoMigrate(ctx, &IdxUser{}); err != nil {
				t.Fatalf("[%s] 第 %d 次 AutoMigrate(IdxUser) 失败：%v", b.name, i+1, err)
			}
		}
		if _, err := orm.RawExec(ctx, db, "DROP TABLE IF EXISTS it_idx_users"); err != nil {
			t.Fatalf("清理失败：%v", err)
		}
	})
}

// TestTablePrefixCRUD 校验表前缀：自动推导的表名加前缀，显式 TableName() 的不加。
func TestTablePrefixCRUD(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		pdb := db.WithPrefix("t_")

		p := PrefUser{Name: "p1"}
		if err := orm.Insert(ctx, pdb, &p); err != nil {
			t.Fatalf("[%s] 带前缀 Insert 失败：%v", b.name, err)
		}
		if p.ID == 0 {
			t.Errorf("[%s] 带前缀插入后主键未回填", b.name)
		}
		if n, err := orm.Count(ctx, pdb, orm.NewQuery[PrefUser]()); err != nil || n != 1 {
			t.Errorf("[%s] 带前缀 Count = %d, err = %v；期望 1", b.name, n, err)
		}

		// 真的是建在 t_pref_users 上，而不是 pref_users
		if _, err := orm.RawExec(ctx, db, "SELECT id FROM t_pref_users"); err != nil {
			t.Errorf("[%s] 前缀表 t_pref_users 不存在：%v", b.name, err)
		}
		if _, err := orm.RawExec(ctx, db, "SELECT id FROM pref_users"); err == nil {
			t.Errorf("[%s] 竟然建了不带前缀的 pref_users", b.name)
		}

		// 同一条 DB 上、不加前缀的查询打的是原始表名（it_users 与 t_ 前缀无关，
		// 因为 User 用 TableName() 显式指定了物理全名）。
		if err := orm.Insert(ctx, db, &User{Name: "noprefix", Age: 1, City: "x",
			Meta: map[string]any{}, CreatedAt: time.Now()}); err != nil {
			t.Fatalf("[%s] 显式表名 Insert 失败：%v", b.name, err)
		}
	})
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}
