package orm

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
	"time"
)

// ---- 聚合 / Pluck 测试用模型 ----

// aggUser 含数值列、时间列与逻辑删除列：用来验证聚合函数是否尊重软删除过滤，
// 以及时间列能否强类型返回。
type aggUser struct {
	Id        int64      `db:"id,pk,autoincrement"`
	Name      string     `db:"name"`
	Age       int        `db:"age"`
	Score     float64    `db:"score"`
	CreatedAt time.Time  `db:"created_at"`
	DeletedAt *time.Time `db:"deleted_at,logic"`
}

// aggGadget 的列与 aggUser 完全不重叠，用于验证「列不属于模型」的兜底报错。
type aggGadget struct {
	Id    int64  `db:"id,pk"`
	Label string `db:"label"`
}

func aggAgeCol() ColExpr {
	return Col[aggUser](func(u *aggUser) *int { return &u.Age })
}

// TestAggregateSQLAndValues 走真实执行路径（mock 驱动记录 SQL），
// 同时断言生成的 SQL 形状、返回值，以及软删除条件是否被带上。
func TestAggregateSQLAndValues(t *testing.T) {
	ctx := context.Background()
	db := mockDB(t, PG)

	mockRegistry[`SUM("age")`] = &mockRows{
		cols: []string{"sum"}, data: [][]driver.Value{{int64(120)}},
	}
	mockRegistry[`AVG("score")`] = &mockRows{
		cols: []string{"avg"}, data: [][]driver.Value{{4.5}},
	}
	mockRegistry[`MIN("age")`] = &mockRows{
		cols: []string{"min"}, data: [][]driver.Value{{int64(7)}},
	}

	q := func() *Query[aggUser] { return NewQuery[aggUser]().Eq(aggAgeCol(), 18) }

	sum, err := Sum(ctx, db, q(), aggAgeCol())
	if err != nil {
		t.Fatalf("Sum 出错: %v", err)
	}
	if sum != 120 {
		t.Fatalf("Sum 应为 120，实际 %v", sum)
	}
	if !strings.Contains(recQuery, `SELECT SUM("age") FROM "agg_users"`) {
		t.Fatalf("Sum SQL 形状错误: %s", recQuery)
	}
	if !strings.Contains(recQuery, `"age" = $1`) {
		t.Fatalf("Sum 应保留 WHERE 条件: %s", recQuery)
	}
	// 软删除过滤必须对聚合同样生效，否则统计会把已删除行算进来。
	if !strings.Contains(recQuery, `"deleted_at" IS NULL`) {
		t.Fatalf("Sum 应自动带上软删除过滤: %s", recQuery)
	}

	avg, err := Avg(ctx, db, q(), Col[aggUser](func(u *aggUser) *float64 { return &u.Score }))
	if err != nil {
		t.Fatalf("Avg 出错: %v", err)
	}
	if avg != 4.5 {
		t.Fatalf("Avg 应为 4.5，实际 %v", avg)
	}
	if !strings.Contains(recQuery, `SELECT AVG("score") FROM "agg_users"`) {
		t.Fatalf("Avg SQL 形状错误: %s", recQuery)
	}

	// PG 引号风格：列名用双引号
	min, err := Min(ctx, db, q(), aggAgeCol())
	if err != nil {
		t.Fatalf("Min 出错: %v", err)
	}
	if min != int64(7) {
		t.Fatalf("Min 应为 int64(7)，实际 %#v", min)
	}
}

// TestAggregateDialectQuoting 确认聚合列名走各方言自己的引号风格。
func TestAggregateDialectQuoting(t *testing.T) {
	ctx := context.Background()
	db := mockDB(t, MySQL)
	mockRegistry[`MAX(`+"`age`"+`)`] = &mockRows{
		cols: []string{"max"}, data: [][]driver.Value{{int64(99)}},
	}
	if _, err := Max(ctx, db, NewQuery[aggUser](), aggAgeCol()); err != nil {
		t.Fatalf("Max 出错: %v", err)
	}
	if !strings.Contains(recQuery, "SELECT MAX(`age`) FROM `agg_users`") {
		t.Fatalf("MySQL 反引号风格错误: %s", recQuery)
	}
}

// TestAggregateTimeColumn 时间列取最新值：Max 返回驱动原生 time.Time，
// MaxOf 返回强类型 time.Time。
func TestAggregateTimeColumn(t *testing.T) {
	ctx := context.Background()
	db := mockDB(t, PG)
	want := time.Date(2026, 9, 23, 21, 0, 0, 0, time.UTC)
	mockRegistry[`MAX("created_at")`] = &mockRows{
		cols: []string{"max"}, data: [][]driver.Value{{want}},
	}

	got, err := MaxOf(ctx, db, NewQuery[aggUser](), TCol(func(u *aggUser) *time.Time { return &u.CreatedAt }))
	if err != nil {
		t.Fatalf("MaxOf 出错: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("MaxOf 应为 %v，实际 %v", want, got)
	}
	if !strings.Contains(recQuery, `MAX("created_at")`) {
		t.Fatalf("MaxOf SQL 形状错误: %s", recQuery)
	}
}

// TestAggregateNullIsZero 空结果集的 SUM/AVG 在 SQL 里是 NULL，应归一为零值而非报错。
func TestAggregateNullIsZero(t *testing.T) {
	ctx := context.Background()
	db := mockDB(t, PG)
	mockRegistry[`SUM("score")`] = &mockRows{
		cols: []string{"sum"}, data: [][]driver.Value{{nil}},
	}
	got, err := Sum(ctx, db, NewQuery[aggUser](), Col[aggUser](func(u *aggUser) *float64 { return &u.Score }))
	if err != nil {
		t.Fatalf("Sum(NULL) 不应报错: %v", err)
	}
	if got != 0 {
		t.Fatalf("Sum(NULL) 应为 0，实际 %v", got)
	}
}

// TestAggregateGroupByRejected GROUP BY 会返回多行，单值接口必须拒绝而不是静默取第一行。
func TestAggregateGroupByRejected(t *testing.T) {
	db := mockDB(t, PG)
	q := NewQuery[aggUser]().GroupBy(Col[aggUser](func(u *aggUser) *string { return &u.Name }))
	if _, err := Sum(context.Background(), db, q, aggAgeCol()); err == nil {
		t.Fatal("带 GROUP BY 的 Sum 应报错")
	} else if !strings.Contains(err.Error(), "GROUP BY") {
		t.Fatalf("错误信息应点明 GROUP BY: %v", err)
	}

	q2 := NewQuery[aggUser]().Having(aggAgeCol(), ">", 1)
	if _, err := Max(context.Background(), db, q2, aggAgeCol()); err == nil {
		t.Fatal("带 HAVING 的 Max 应报错")
	}
}

// TestAggregateColumnFromOtherModel ColExpr 类型是擦除的，
// 把 User 的列用到 Query[aggUser] 上能通过编译，必须在运行期立刻拦掉。
func TestAggregateColumnFromOtherModel(t *testing.T) {
	db := mockDB(t, PG)
	foreign := Col[aggGadget](func(g *aggGadget) *string { return &g.Label })
	_, err := Sum(context.Background(), db, NewQuery[aggUser](), foreign)
	if err == nil {
		t.Fatal("跨模型的列表达式应报错")
	}
	if !strings.Contains(err.Error(), "不属于模型") {
		t.Fatalf("错误信息应点明归属问题: %v", err)
	}
}

// TestPluck 单列投影：取值、NULL 补零、SQL 形状，以及两种调用姿势。
func TestPluck(t *testing.T) {
	ctx := context.Background()
	db := mockDB(t, PG)
	mockRegistry[`SELECT "name" FROM "agg_users"`] = &mockRows{
		cols: []string{"name"},
		data: [][]driver.Value{{"alice"}, {nil}, {"carol"}},
	}
	mockRegistry[`SELECT "age" FROM "agg_users"`] = &mockRows{
		cols: []string{"age"}, data: [][]driver.Value{{int64(18)}, {int64(30)}},
	}

	// 姿势一：TCol 携带类型，全部推导（无需写类型参数）。
	names, err := Pluck(ctx, db, NewQuery[aggUser](), TCol(func(u *aggUser) *string { return &u.Name }))
	if err != nil {
		t.Fatalf("Pluck 出错: %v", err)
	}
	if len(names) != 3 || names[0] != "alice" || names[1] != "" || names[2] != "carol" {
		t.Fatalf("Pluck 结果错误: %#v", names)
	}
	if !strings.Contains(recQuery, `SELECT "name" FROM "agg_users"`) {
		t.Fatalf("Pluck 应只查单列: %s", recQuery)
	}
	if strings.Contains(recQuery, "*") {
		t.Fatalf("Pluck 不应出现 SELECT *: %s", recQuery)
	}

	// 姿势二：用 ormgen 生成的列集（类型是 orm.ColExpr，不带字段类型），
	// 类型无法推导，走 PluckCol 并显式给出元素类型。
	ages, err := PluckCol[aggUser, int](ctx, db, NewQuery[aggUser](), ColOf[aggUser]("Age"))
	if err != nil {
		t.Fatalf("Pluck[ColExpr] 出错: %v", err)
	}
	if len(ages) != 2 || ages[0] != 18 || ages[1] != 30 {
		t.Fatalf("Pluck 结果错误: %#v", ages)
	}
}

// TestPluckKeepsOrderAndLimit 与聚合不同，Pluck 是普通查询，排序 / 分页必须保留。
func TestPluckKeepsOrderAndLimit(t *testing.T) {
	ctx := context.Background()
	db := mockDB(t, PG)
	mockRegistry[`ORDER BY "score" DESC`] = &mockRows{
		cols: []string{"score"}, data: [][]driver.Value{{4.5}},
	}
	scoreCol := Col[aggUser](func(u *aggUser) *float64 { return &u.Score })
	q := NewQuery[aggUser]().OrderBy(scoreCol, false).Limit(5)
	// scoreCol 是类型擦除的 ColExpr，F 无法推导，故走 PluckCol 并显式给出类型（TCol 写法可全省略）。
	if _, err := PluckCol[aggUser, float64](ctx, db, q, scoreCol); err != nil {
		t.Fatalf("Pluck 出错: %v", err)
	}
	if !strings.Contains(recQuery, `ORDER BY "score" DESC`) {
		t.Fatalf("Pluck 应保留 ORDER BY: %s", recQuery)
	}
	if !strings.Contains(recQuery, `LIMIT 5`) {
		t.Fatalf("Pluck 应保留 LIMIT: %s", recQuery)
	}
}

// TestAggregateDropsVectorDistColumn 聚合 / 单列投影时不能再追加向量距离列：
// 那会让结果集从 1 列变成 2 列，Scan 单值直接报 "expected 1 destination"。
func TestAggregateDropsVectorDistColumn(t *testing.T) {
	nearest := func() *Query[Article] {
		return NewQuery[Article]().Nearest(vecCol(t), []float32{0.1, 0.2}, 5)
	}

	// 普通查询：距离列与按距离排序照旧（回归保护，别把向量检索改坏）。
	plain, _ := nearest().Build()
	if !strings.Contains(plain, "AS dist") {
		t.Fatalf("普通向量查询应保留距离列: %s", plain)
	}

	agg, _ := nearest().agg("SUM", "id").Build()
	if strings.Contains(agg, "AS dist") {
		t.Fatalf("聚合查询不应带距离列: %s", agg)
	}
	if strings.Contains(agg, "ORDER BY") {
		t.Fatalf("聚合查询不应按距离排序: %s", agg)
	}
	if strings.Contains(agg, "LIMIT") {
		t.Fatalf("聚合查询不应带 LIMIT（Nearest 设的 k 会被清掉）: %s", agg)
	}
	if !strings.Contains(agg, "SELECT SUM(\"id\") FROM") {
		t.Fatalf("聚合投影错误: %s", agg)
	}
}

// TestCountIncludesJoin Count 此前手搓 FROM、把 JOIN 整个漏掉，
// 导致带 JOIN 的分页总数与列表行数口径不一致。
func TestCountIncludesJoin(t *testing.T) {
	ctx := context.Background()
	db := mockDB(t, PG)

	// mockRegistry 的键是按「子串命中」匹配的，而 raw_test.go 已经占用了 "COUNT(*)";
	// 这条 SQL 会被两个键同时命中，map 遍历顺序又是随机的 —— 这里临时接管、结束还原，
	// 保证结果确定且不影响其它测试。
	prev, hadPrev := mockRegistry[`COUNT(*)`]
	mockRegistry[`COUNT(*)`] = &mockRows{cols: []string{"count"}, data: [][]driver.Value{{int64(3)}}}
	t.Cleanup(func() {
		if hadPrev {
			mockRegistry[`COUNT(*)`] = prev
		} else {
			delete(mockRegistry, `COUNT(*)`)
		}
	})

	q := NewQuery[aggUser]().
		LeftJoin("orders", "orders.user_id = agg_users.id").
		Eq(aggAgeCol(), 18)
	n, err := Count(ctx, db, q)
	if err != nil {
		t.Fatalf("Count 出错: %v", err)
	}
	if n != 3 {
		t.Fatalf("Count 应为 3，实际 %d", n)
	}
	if !strings.Contains(recQuery, `LEFT JOIN "orders" ON orders.user_id = agg_users.id`) {
		t.Fatalf("Count 必须带上 JOIN: %s", recQuery)
	}
	if !strings.Contains(recQuery, `SELECT COUNT(*) FROM "agg_users"`) {
		t.Fatalf("Count SQL 形状错误: %s", recQuery)
	}
	if !strings.Contains(recQuery, `"deleted_at" IS NULL`) {
		t.Fatalf("Count 应带软删除过滤: %s", recQuery)
	}
}

// TestToSQLVsDryRun ToSQL 只看查询自身状态；DryRun 会补上 db 的表前缀与软删除条件。
func TestToSQLVsDryRun(t *testing.T) {
	db := mockDB(t, PG).WithPrefix("t_")
	q := NewQuery[aggUser]()

	raw, _ := q.ToSQL()
	if strings.Contains(raw, "t_") {
		t.Fatalf("ToSQL 不应包含 db 级表前缀: %s", raw)
	}
	if strings.Contains(raw, "deleted_at") {
		t.Fatalf("ToSQL 不应包含软删除条件: %s", raw)
	}

	real, _ := DryRun(db, q)
	if !strings.Contains(real, `FROM "t_agg_users"`) {
		t.Fatalf("DryRun 应补上表前缀: %s", real)
	}
	if !strings.Contains(real, `"deleted_at" IS NULL`) {
		t.Fatalf("DryRun 应补上软删除条件: %s", real)
	}
}

// TestCoerceTime 覆盖「聚合结果归一为 time.Time」的各条分支。
// 起因：MAX()/MIN() 是没有声明类型的表达式，部分驱动（实测 SQLite）只能按 TEXT 交回。
func TestCoerceTime(t *testing.T) {
	want := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	ok := []struct {
		name string
		in   any
		want time.Time
	}{
		{"time.Time 原样返回", want, want},
		{"RFC3339", "2024-01-02T03:04:05Z", want},
		{"SQLite 惯用文本", "2024-01-02 03:04:05", want},
		{"[]byte 文本", []byte("2024-01-02 03:04:05"), want},
		{"带偏移量文本", "2024-01-02 03:04:05+00:00", want},
		{"Go String() 形态（SQLite 实测）", "2024-01-02 03:04:05 +0000 UTC", want},
		{"unix 秒", int64(1704164645), want},
		{"NULL → 零值", nil, time.Time{}},
	}
	for _, c := range ok {
		got, err := coerceTime(c.in)
		if err != nil {
			t.Errorf("[%s] 意外报错: %v", c.name, err)
			continue
		}
		if !got.Equal(c.want) {
			t.Errorf("[%s] = %v；期望 %v", c.name, got, c.want)
		}
	}
	if _, err := coerceTime("not-a-time"); err == nil {
		t.Error("无法解析的文本应报错，而不是静默返回零值")
	}
}

// TestMaxOfTimeColumnSQLiteText 复现 SQLite 的真实返回形态：
// 驱动拿不到聚合表达式的列类型，只能按 TEXT 交回 —— 修复后应能解析，而不是扫描失败。
func TestMaxOfTimeColumnSQLiteText(t *testing.T) {
	ctx := context.Background()
	db := mockDB(t, SQLite)

	const key = `MAX("created_at")`
	prev, had := mockRegistry[key]
	mockRegistry[key] = &mockRows{
		cols: []string{"max"},
		data: [][]driver.Value{{"2026-09-23 21:00:00+00:00"}},
	}
	t.Cleanup(func() {
		if had {
			mockRegistry[key] = prev
		} else {
			delete(mockRegistry, key)
		}
	})

	got, err := MaxOf(ctx, db, NewQuery[aggUser](), TCol(func(u *aggUser) *time.Time { return &u.CreatedAt }))
	if err != nil {
		t.Fatalf("SQLite 文本形态的时间聚合应能解析: %v", err)
	}
	if want := time.Date(2026, 9, 23, 21, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("MaxOf = %v；期望 %v", got, want)
	}
}
