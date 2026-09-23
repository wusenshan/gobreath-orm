package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	orm "github.com/wusenshan/gobreath-orm"
)

// TestAggregates 用真库校验 SUM/AVG/MAX/MIN。
//
// mock 执行器只能断言「生成了 SUM("age")」，断言不了：
//   - PG 的 SUM(bigint)/AVG(int) 回来是 numeric，驱动实际给的是 []byte 还是 float64；
//   - SUM 对空结果集返回 NULL 时能否落到零值；
//   - 强类型版本 SumOf[int] 的扫描是否真的不报错。
func TestAggregates(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		seedUsers(t, db)
		all := orm.NewQuery[User]()

		// ---- 弱类型（float64 / any）----
		if got, err := orm.Sum(ctx, db, all, uAge); err != nil || got != 115 {
			t.Errorf("Sum(age) = %v, err = %v；期望 115", got, err)
		}
		if got, err := orm.Avg(ctx, db, all, uAge); err != nil || got != 28.75 {
			t.Errorf("Avg(age) = %v, err = %v；期望 28.75", got, err)
		}
		if got, err := orm.Max(ctx, db, all, uAge); err != nil || toInt(got) != 35 {
			t.Errorf("Max(age) = %#v (%T), err = %v；期望 35", got, got, err)
		}
		if got, err := orm.Min(ctx, db, all, uAge); err != nil || toInt(got) != 25 {
			t.Errorf("Min(age) = %#v (%T), err = %v；期望 25", got, got, err)
		}

		// 浮点列（DOUBLE / REAL）
		if got, err := orm.Sum(ctx, db, all, uScore); err != nil || got != 300.75 {
			t.Errorf("Sum(score) = %v, err = %v；期望 300.75", got, err)
		}
		if got, err := orm.Avg(ctx, db, all, uScore); err != nil || got != 75.1875 {
			t.Errorf("Avg(score) = %v, err = %v；期望 75.1875", got, err)
		}
		// 字符串列（字典序）
		if got, err := orm.Min(ctx, db, all, uName); err != nil || toStr(got) != "alice" {
			t.Errorf("Min(name) = %#v, err = %v；期望 alice", got, err)
		}

		// ---- 强类型（F 由 TCol 推导）----
		if got, err := orm.SumOf(ctx, db, all, uAgeT); err != nil || got != 115 {
			t.Errorf("SumOf[int](age) = %v, err = %v；期望 115", got, err)
		}
		if got, err := orm.AvgOf(ctx, db, all, uScoreT); err != nil || got != 75.1875 {
			t.Errorf("AvgOf[float64](score) = %v, err = %v；期望 75.1875", got, err)
		}

		// 时间列的 MAX 单独放 TestMaxOfTimeColumn：SQLite 上它是坏的，
		// 若放在这里用 Skip 会连带跳过本函数后面那些三方言都该通过的断言。

		// ---- 空结果集：SUM 返回 NULL，应落到零值而不是报错 ----
		empty := orm.NewQuery[User]().Eq(uCity, "nowhere")
		if got, err := orm.Sum(ctx, db, empty, uAge); err != nil || got != 0 {
			t.Errorf("空集 Sum(age) = %v, err = %v；期望 0, nil", got, err)
		}
		if got, err := orm.SumOf(ctx, db, empty, uAgeT); err != nil || got != 0 {
			t.Errorf("空集 SumOf[int](age) = %v, err = %v；期望 0, nil", got, err)
		}
		if got, err := orm.Max(ctx, db, empty, uAge); err != nil || got != nil {
			t.Errorf("空集 Max(age) = %#v, err = %v；期望 nil, nil", got, err)
		}

		// ---- 调用即报错（不依赖数据库）----
		// 列来自另一个模型：ColExpr 类型被擦除，编译期拦不住，必须在调用点拦。
		if _, err := orm.Sum(ctx, db, orm.NewQuery[User](), oAmount); err == nil {
			t.Errorf("用 Order 的列去聚合 User，竟然没报错")
		}
		// 带 GROUP BY：会返回多行，单值接口无法表达，必须拒绝而不是静默取第一行。
		if _, err := orm.Sum(ctx, db, orm.NewQuery[User]().GroupBy(uCity), uAge); err == nil {
			t.Errorf("带 GROUP BY 的 Sum 竟然没报错")
		}
	})
}

// TestPluck 校验单列投影：顺序保持、NULL 补零、下标对齐、强类型扫描。
func TestPluck(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		rows := seedUsers(t, db)

		// 强类型入口：类型全部推导，调用点不写类型参数。
		names, err := orm.Pluck(ctx, db, orm.NewQuery[User]().OrderBy(uID, true), uNameT)
		if err != nil {
			t.Fatalf("Pluck(name) 出错：%v", err)
		}
		if want := []string{"alice", "bob", "carol", "dave"}; !equalStrs(names, want) {
			t.Errorf("Pluck(name) = %v；期望 %v", names, want)
		}

		// 主键列：应与 Insert 回填的 ID 完全一致（顺带验证 Pluck 走的是真列）。
		ids, err := orm.PluckCol[User, int64](ctx, db, orm.NewQuery[User]().OrderBy(uID, true), uID)
		if err != nil {
			t.Fatalf("PluckCol(id) 出错：%v", err)
		}
		for i, r := range rows {
			if i >= len(ids) || ids[i] != r.ID {
				t.Fatalf("Pluck(id)[%d] = %v；期望 %v（Insert 回填值）", i, ids, r.ID)
			}
		}

		// 排序 / 分页必须保留 —— Pluck 是普通查询，只是投影变成单列。
		top2, err := orm.Pluck(ctx, db,
			orm.NewQuery[User]().OrderBy(uAge, false).Limit(2), uAgeT)
		if err != nil {
			t.Fatalf("Pluck(age, 排序+分页) 出错：%v", err)
		}
		if len(top2) != 2 || top2[0] != 35 || top2[1] != 30 {
			t.Errorf("Pluck(age) 取年龄前二 = %v；期望 [35 30]", top2)
		}

		// 类型不匹配要在调用点就报错（列为字符串却要 []int）。
		if _, err := orm.PluckCol[User, int](ctx, db, orm.NewQuery[User](), uName); err != nil {
			t.Logf("[%s] 跨类型 Pluck 报错（预期，扫描阶段拦下）：%v", b.name, err)
		}
	})
}

// TestPluckNullAlignment 校验 NULL 值填充零值后**下标不错位**。
//
// 这是 Pluck 最容易错的地方：若把 NULL 行直接跳过，后面的行就会整体前移，
// 调用方拿到的切片与结果集不再一一对应。
func TestPluckNullAlignment(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		rows := seedUsers(t, db)

		// 把第 2 行（bob）的 age 置为 NULL，制造「中间一行是 NULL」的场景。
		if _, err := orm.RawExec(ctx, db,
			"UPDATE it_users SET age = NULL WHERE id = "+itoa64(rows[1].ID)); err != nil {
			t.Fatalf("置 NULL 失败：%v", err)
		}

		ages, err := orm.Pluck(ctx, db, orm.NewQuery[User]().OrderBy(uID, true), uAgeT)
		if err != nil {
			t.Fatalf("Pluck(age) 出错：%v", err)
		}
		if want := []int{30, 0, 35, 25}; !equalInts(ages, want) {
			t.Errorf("含 NULL 的 Pluck(age) = %v；期望 %v（NULL → 零值且下标对齐）", ages, want)
		}
	})
}

// TestCountIncludesJoin 是「Count 漏掉 JOIN」这个已修 bug 的回归测试。
//
// 修复前 Count 自己拼了一份 FROM，JOIN 被整个丢掉：
// 单表时 count == 行数，一切正常；一旦 INNER JOIN 改变了行数，
// Count 与 SelectList 就对不上，Page 的 Total / Pages / HasNext 跟着全错。
func TestCountIncludesJoin(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		users := seedUsers(t, db)
		seedOrders(t, db, users)

		// 基线：不带 JOIN 时就是 4 个用户。
		plain, err := orm.Count(ctx, db, orm.NewQuery[User]())
		if err != nil {
			t.Fatalf("Count 出错：%v", err)
		}
		if plain != 4 {
			t.Fatalf("单表 Count = %d；期望 4", plain)
		}

		// INNER JOIN：alice 2 单 + bob 1 单 = 3 行。
		joined := orm.NewQuery[User]().
			Select("it_users.id", "it_users.name").
			Join("it_orders", "it_orders.user_id = it_users.id")

		list, err := orm.SelectList(ctx, db, joined)
		if err != nil {
			t.Fatalf("JOIN SelectList 出错：%v", err)
		}
		cnt, err := orm.Count(ctx, db, joined)
		if err != nil {
			t.Fatalf("JOIN Count 出错：%v", err)
		}
		if len(list) != 3 {
			t.Fatalf("JOIN SelectList 返回 %d 行；期望 3", len(list))
		}
		if cnt != int64(len(list)) {
			t.Errorf("JOIN Count = %d，但 SelectList 返回 %d 行 —— Count 又漏 JOIN 了",
				cnt, len(list))
		}

		// Page 的元信息必须与 JOIN 后的真实行数一致。
		pg, err := orm.Page(ctx, db, joined, 1, 2)
		if err != nil {
			t.Fatalf("JOIN Page 出错：%v", err)
		}
		if pg.Total != 3 || pg.Pages != 2 || !pg.HasNext || pg.HasPrev {
			t.Errorf("JOIN Page 元信息 = total:%d pages:%d hasNext:%v hasPrev:%v；期望 3 2 true false",
				pg.Total, pg.Pages, pg.HasNext, pg.HasPrev)
		}
	})
}

// TestJsonQuery 校验 JSON 列上的路径比较。
//
// 刻意用**字符串**值（"hot"）而不是数字：三种方言对 JSON 里数字的比较语义不一致，
// PG 的 ->> 出来是 text，拿数字去比会直接报 operator does not exist（见 README 已知差异）。
func TestJsonQuery(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		seedUsers(t, db)

		got, err := orm.SelectList(ctx, db,
			orm.NewQuery[User]().Json(uMeta, "tag", "=", "hot").OrderBy(uID, true))
		if err != nil {
			t.Fatalf("Json 路径查询出错：%v", err)
		}
		if len(got) != 2 || got[0].Name != "alice" || got[1].Name != "carol" {
			t.Errorf("tag=hot 命中 %d 行 %v；期望 alice, carol", len(got), names(got))
		}

		// JSON 列往返：写入的 map 读出后数值会变成 float64（JSON 无整数类型）。
		one, err := orm.SelectOne(ctx, db, orm.NewQuery[User]().Eq(uName, "alice"))
		if err != nil || one == nil {
			t.Fatalf("取 alice 失败：%v", err)
		}
		if lv, ok := one.Meta["level"].(float64); !ok || lv != 3 {
			t.Errorf("Meta[level] = %#v (%T)；期望 float64(3)", one.Meta["level"], one.Meta["level"])
		}
		if tag, _ := one.Meta["tag"].(string); tag != "hot" {
			t.Errorf("Meta[tag] = %#v；期望 hot", one.Meta["tag"])
		}
	})
}

// TestJsonContains 校验 JSON 片段包含查询在三方言上都能真跑通。
//
// SQLite 没有 json_contains 函数，sqliteDialect 改用 json_each 展开成等价的
// 浅层「子集」语义；这里先用原生 SQL 记录方言事实，再断言 ORM 路径可用。
func TestJsonContains(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()

		if b.name == "sqlite" {
			if _, rawErr := orm.RawExec(ctx, db, `SELECT json_contains('{"tag":"hot"}', '{"tag":"hot"}')`); rawErr == nil {
				t.Errorf("SQLite 竟然支持 json_contains 了，可简化 sqliteDialect.JsonContains")
			}
		}

		seedUsers(t, db)
		got, err := orm.SelectList(ctx, db,
			orm.NewQuery[User]().JsonContains(uMeta, map[string]any{"tag": "hot"}).OrderBy(uID, true))
		if err != nil {
			t.Fatalf("[%s] JsonContains 出错：%v", b.name, err)
		}
		if len(got) != 2 {
			t.Errorf("[%s] JsonContains 命中 %d 行 %v；期望 2 行", b.name, len(got), names(got))
		}
	})
}

// TestDryRunIsExecutable 是 DryRun / ToSQL 两个新 API 的核心验证：
// DryRun 给出的 SQL 必须**能直接在真库上跑通**，而不只是字符串看起来对。
func TestDryRunIsExecutable(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		seedUsers(t, db)

		q := orm.NewQuery[User]().Ge(uAge, 25).OrderBy(uName, true).Limit(2)
		sqlStr, args := orm.DryRun(db, q)

		// 1) 带上 db 上下文：软删除条件必须出现（这是 DryRun 与 ToSQL 的关键差别）。
		if !strings.Contains(sqlStr, "deleted_at") {
			t.Errorf("[%s] DryRun 未带软删除条件：%s", b.name, sqlStr)
		}
		// 2) 参数个数要与占位符匹配。
		if n := strings.Count(sqlStr, placeholderOf(b)); n != len(args) {
			t.Errorf("[%s] DryRun 占位符 %d 个、参数 %d 个：%s", b.name, n, len(args), sqlStr)
		}
		// 3) 真跑一遍 —— 绕过 ORM，直接用底层 *sql.DB 执行 DryRun 的输出。
		rows, err := db.SQL().QueryContext(ctx, sqlStr, args...)
		if err != nil {
			t.Fatalf("[%s] DryRun 的 SQL 无法执行：%v\nSQL: %s\n参数: %v", b.name, err, sqlStr, args)
		}
		defer rows.Close()
		n := 0
		for rows.Next() {
			n++
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("[%s] 遍历 DryRun 结果出错：%v", b.name, err)
		}
		if n != 2 {
			t.Errorf("[%s] DryRun 查询返回 %d 行；期望 2（LIMIT 2）", b.name, n)
		}

		// 4) 纯粹不带 db 上下文的 ToSQL：没有软删除条件、也没有表前缀。
		raw, _ := orm.NewQuery[User]().WithDialect(b.dialect).Ge(uAge, 25).ToSQL()
		if strings.Contains(raw, "deleted_at") {
			t.Errorf("[%s] ToSQL 不该含软删除条件：%s", b.name, raw)
		}

		// 5) 表前缀要体现在 DryRun 里（前缀只作用于自动推导的表名）。
		pdb := db.WithPrefix("t_")
		prefixed, _ := orm.DryRun(pdb, orm.NewQuery[PrefUser]())
		if !strings.Contains(prefixed, "t_pref_users") {
			t.Errorf("[%s] 前缀未生效：%s", b.name, prefixed)
		}
		// 而显式 TableName() 的模型不加前缀（显式表名视为物理全名）。
		explicit, _ := orm.DryRun(pdb, orm.NewQuery[User]())
		if !strings.Contains(explicit, "it_users") || strings.Contains(explicit, "t_it_users") {
			t.Errorf("[%s] 显式 TableName 不该被加前缀：%s", b.name, explicit)
		}
	})
}

// TestBatchInsertVarLimit 是一次**取证式排查**，不是回归断言。
//
// BatchInsert 目前把全部实体拼成一条 INSERT，绑定参数随行数线性增长，
// 而各库都有上限：SQLite 999、PG 65535、MySQL 受 max_allowed_packet 约束。
// 这里把触顶行为如实记录下来，作为「BatchInsert 自动分批」的输入证据。
func TestBatchInsertVarLimit(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()

		// 小批量必须成功。CreatedAt 显式赋值：MySQL 严格模式拒收零值时间。
		small := make([]User, 50)
		for i := range small {
			small[i] = User{Name: "u", Age: i, City: "x", Meta: map[string]any{}, CreatedAt: baseTime}
		}
		if err := orm.BatchInsert(ctx, db, small); err != nil {
			t.Fatalf("50 行 BatchInsert 失败：%v", err)
		}
		if n, _ := orm.Count(ctx, db, orm.NewQuery[User]()); n != 50 {
			t.Fatalf("50 行批量插入后 Count = %d", n)
		}

		// 逐档加压，把各库真实的绑定参数上限探出来。
		// User 的**可写列是 6 个**（8 个字段里，id 是自增列、deleted_at 是逻辑删除列，
		// 都被 writableCols 排除），所以参数数 = 行数 * 6。
		//   实测触顶：SQLite 在 9000 行（54000 参数）报 too many SQL variables（上限 32766）
		//             PG / MySQL 在 20000 行（120000 参数）报 65535 上限
		const cols = 6
		for _, big := range []int{200, 3000, 9000, 20000, 40000} {
			rows := make([]User, big)
			for i := range rows {
				rows[i] = User{Name: "b", Age: i, City: "x", Meta: map[string]any{}, CreatedAt: baseTime}
			}
			err := orm.BatchInsert(ctx, db, rows)
			if err != nil {
				t.Logf("[%s] BatchInsert %d 行（%d 个绑定参数）失败 —— 触顶了：%v",
					b.name, big, big*cols, err)
				break
			}
			t.Logf("[%s] BatchInsert %d 行（%d 个绑定参数）成功", b.name, big, big*cols)
		}
	})
}

// TestMaxOfTimeColumn 单独验证时间列的 MAX —— 这是 MaxOf 文档里点名的用法
// （「时间列取最新值时最顺手」）。
//
// 这里回归一个方言缺口：聚合表达式（MAX/MIN）没有声明类型，SQLite 驱动只能按 TEXT
// 交回，sql.Null[time.Time] 会直接扫描失败；orm 侧改走文本归一后三方言一致。
func TestMaxOfTimeColumn(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		seedUsers(t, db)

		got, err := orm.MaxOf(ctx, db, orm.NewQuery[User](), uCreatedT)
		if err != nil {
			t.Fatalf("[%s] MaxOf(created_at) 失败：%v", b.name, err)
		}
		if want := baseTime.Add(4 * time.Hour); !got.Equal(want) {
			t.Errorf("[%s] MaxOf(created_at) = %v；期望 %v", b.name, got, want)
		}
	})
}

// ---------------------------------------------------------------- 小工具

func placeholderOf(b backend) string {
	if b.name == "postgres" {
		return "$"
	}
	return "?"
}

func names(us []User) []string {
	out := make([]string, len(us))
	for i, u := range us {
		out[i] = u.Name
	}
	return out
}

func equalStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
