package orm

import (
	"context"
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"
)

// 分组聚合检索的回归用例。
//
// 覆盖三件事：① 聚合投影与别名能正确渲染成 SQL（三方言引号各不相同）；
// ② 结果集的别名列能被既有行映射命中扫进 DTO；③ Count 在 DISTINCT / GROUP BY
// 下改成子查询计数，与列表行数口径一致。

type aggSale struct {
	Id     int64  `db:"id,pk,autoincrement"`
	Dept   string `db:"dept"`
	Amount int64  `db:"amount"`
}

type aggStat struct {
	Dept  string `db:"dept"`
	Total int64  `db:"total"`
	Cnt   int64  `db:"cnt"`
}

func aggSaleCols() (dept, amount ColExpr) {
	return ColOf[aggSale]("Dept"), ColOf[aggSale]("Amount")
}

// has - 断言 SQL 包含片段。
func mustContain(t *testing.T, sql, want string) {
	t.Helper()
	if !strings.Contains(sql, want) {
		t.Fatalf("SQL 缺少片段 %q\n实际：%s", want, sql)
	}
}

func TestAggregateBuildsSQLPerDialect(t *testing.T) {
	dept, amount := aggSaleCols()
	cases := []struct {
		name string
		d    Dialect
		want string
	}{
		{"pg", PG,
			`SELECT "dept", SUM("amount") AS "total", COUNT(*) AS "cnt" FROM "agg_sales" ` +
				`WHERE "dept" != $1 GROUP BY "dept" HAVING SUM("amount") > $2 ORDER BY "dept" ASC`},
		{"mysql", MySQL,
			"SELECT `dept`, SUM(`amount`) AS `total`, COUNT(*) AS `cnt` FROM `agg_sales` " +
				"WHERE `dept` != ? GROUP BY `dept` HAVING SUM(`amount`) > ? ORDER BY `dept` ASC"},
		{"sqlite", SQLite,
			`SELECT "dept", SUM("amount") AS "total", COUNT(*) AS "cnt" FROM "agg_sales" ` +
				`WHERE "dept" != ? GROUP BY "dept" HAVING SUM("amount") > ? ORDER BY "dept" ASC`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := NewQuery[aggSale]().
				SelectCol(dept).
				SelectSum(amount, "total").
				SelectCount("cnt").
				Ne(dept, "zzz").
				GroupBy(dept).
				HavingSum(amount, ">", 100).
				OrderBy(dept, true)
			got, args := q.WithDialect(c.d).Build()
			if got != c.want {
				t.Fatalf("SQL 不符\n想要：%s\n实际：%s", c.want, got)
			}
			if len(args) != 2 {
				t.Fatalf("参数个数应为 2（WHERE + HAVING），实际 %d：%v", len(args), args)
			}
		})
	}
}

func TestAggregateScansAliasColumnsIntoDTO(t *testing.T) {
	dept, amount := aggSaleCols()
	// mock 驱动按结果集列名匹配：这里正是 SQL 里 AS 出来的别名。
	mockRegistry["SUM("] = &mockRows{
		cols: []string{"dept", "total", "cnt"},
		data: [][]driver.Value{
			{"a", int64(30), int64(2)},
			{"b", int64(5), int64(1)},
		},
	}
	defer delete(mockRegistry, "SUM(")

	db := newMockDB(t)
	stats, err := Aggregate[aggStat](ctx(), db, NewQuery[aggSale]().
		SelectCol(dept).SelectSum(amount, "total").SelectCount("cnt").GroupBy(dept))
	if err != nil {
		t.Fatalf("Aggregate 失败：%v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("应有 2 行分组结果，实际 %d", len(stats))
	}
	if stats[0].Dept != "a" || stats[0].Total != 30 || stats[0].Cnt != 2 {
		t.Fatalf("第 1 行数据不符：%+v（别名列没落进字段？）", stats[0])
	}
	if stats[1].Dept != "b" || stats[1].Total != 5 || stats[1].Cnt != 1 {
		t.Fatalf("第 2 行数据不符：%+v", stats[1])
	}
}

func TestAggregateRejectsUnnamedAggregateColumn(t *testing.T) {
	_, amount := aggSaleCols()
	// 别名非法 / 为空必须 panic：各方言对无名聚合列的命名不一致，
	// 不起别名的话没法用稳定的字段名把结果扫回结构体 —— 静默落空比报错更糟。
	// "drop" 这类单词是合法别名（会被方言加引号包起来，不构成注入），不该被拒；
	// 被拒的应当是那些可能闭合引号或截断语句的字符。
	for _, bad := range []string{"", "a-b", "1abc", "total ", "sum(amount)", "a\"b", "a`b", "a'b", "a;b"} {
		func() {
			defer func() {
				if r := recover(); r == nil {
					t.Fatalf("别名 %q 应当被拒（没有 panic）", bad)
				}
			}()
			NewQuery[aggSale]().SelectSum(amount, bad)
		}()
	}
}

func TestAggregateDoesNotMutateCallerQuery(t *testing.T) {
	dept, amount := aggSaleCols()
	db := newMockDB(t)
	mockRegistry["SUM("] = &mockRows{cols: []string{"dept"}, data: [][]driver.Value{{"a"}}}
	defer delete(mockRegistry, "SUM(")

	q := NewQuery[aggSale]().SelectCol(dept).SelectSum(amount, "total").GroupBy(dept)
	before, _ := q.ToSQL()
	if _, err := Aggregate[aggStat](ctx(), db, q); err != nil {
		t.Fatalf("Aggregate 失败：%v", err)
	}
	if after, _ := q.ToSQL(); after != before {
		t.Fatalf("框架入口修改了调用方的 Query：\n之前：%s\n之后：%s", before, after)
	}
}

func TestHavingAggCountAndAvg(t *testing.T) {
	dept, amount := aggSaleCols()
	q := NewQuery[aggSale]().
		SelectCol(dept).SelectAvg(amount, "avg_amt").SelectCount("cnt").
		GroupBy(dept).
		HavingCount(">=", 3).
		HavingAvg(amount, "<", 1000)
	got, args := q.WithDialect(PG).Build()
	mustContain(t, got, `AVG("amount") AS "avg_amt"`)
	mustContain(t, got, `HAVING COUNT(*) >= $1 AND AVG("amount") < $2`)
	if len(args) != 2 {
		t.Fatalf("HAVING 参数应逐个分配占位符，实际 %d 个：%v", len(args), args)
	}
}

// ---- Count 口径 ----

func TestCountOfPlainQueryUsesCountStar(t *testing.T) {
	dept, _ := aggSaleCols()
	db := newMockDB(t)
	mockRegistry["COUNT"] = &mockRows{cols: []string{"c"}, data: [][]driver.Value{{int64(7)}}}
	defer delete(mockRegistry, "COUNT")

	n, err := Count(ctx(), db, NewQuery[aggSale]().Eq(dept, "a").OrderBy(dept, true).Limit(5))
	if err != nil {
		t.Fatalf("Count 失败：%v", err)
	}
	if n != 7 {
		t.Fatalf("Count 应为 7，实际 %d", n)
	}
	if strings.Contains(recQuery, "LIMIT") || strings.Contains(recQuery, "ORDER BY") {
		t.Fatalf("Count 不应带 ORDER BY / LIMIT（会改变语义）：%s", recQuery)
	}
	mustContain(t, recQuery, `SELECT COUNT(*) FROM "agg_sales" WHERE "dept" = ?`)
}

func TestCountOfDistinctWrapsSubquery(t *testing.T) {
	_, amount := aggSaleCols()
	db := newMockDB(t)
	mockRegistry["COUNT(*) FROM (SELECT"] = &mockRows{cols: []string{"c"}, data: [][]driver.Value{{int64(3)}}}
	defer delete(mockRegistry, "COUNT(*) FROM (SELECT")

	// Distinct 查询：总数必须去重，此前会被退化为「不计 DISTINCT 的 COUNT(*)」。
	n, err := Count(ctx(), db, NewQuery[aggSale]().SelectCol(amount).Distinct().OrderBy(amount, true).Limit(10))
	if err != nil {
		t.Fatalf("Count 失败：%v", err)
	}
	if n != 3 {
		t.Fatalf("Count 应为 3，实际 %d", n)
	}
	want := `SELECT COUNT(*) FROM (SELECT DISTINCT "amount" FROM "agg_sales") "_t"`
	if recQuery != want {
		t.Fatalf("Distinct 计数应包子查询\n想要：%s\n实际：%s", want, recQuery)
	}
}

func TestCountOfGroupByKeepsHavingAndCountsGroups(t *testing.T) {
	dept, amount := aggSaleCols()
	db := newMockDB(t)
	mockRegistry["COUNT(*) FROM (SELECT"] = &mockRows{cols: []string{"c"}, data: [][]driver.Value{{int64(4)}}}
	defer delete(mockRegistry, "COUNT(*) FROM (SELECT")

	// GroupBy + Having：总数要数「分组后还剩几组」，不是全表行数。
	q := NewQuery[aggSale]().SelectCol(dept).GroupBy(dept).HavingSum(amount, ">", 100)
	n, err := Count(ctx(), db, q)
	if err != nil {
		t.Fatalf("Count 失败：%v", err)
	}
	if n != 4 {
		t.Fatalf("Count 应为 4，实际 %d", n)
	}
	want := `SELECT COUNT(*) FROM (SELECT "dept" FROM "agg_sales" GROUP BY "dept" HAVING SUM("amount") > ?) "_t"`
	if recQuery != want {
		t.Fatalf("GroupBy 计数应保留 GROUP BY / HAVING 并包子查询\n想要：%s\n实际：%s", want, recQuery)
	}
	if len(recArgs) != 1 || recArgs[0] != int64(100) {
		t.Fatalf("HAVING 的参数应照常绑定，实际 %v", recArgs)
	}
}

func TestPageTotalMatchesGroupedListLength(t *testing.T) {
	dept, amount := aggSaleCols()
	db := newMockDB(t)
	mockRegistry["COUNT(*) FROM (SELECT"] = &mockRows{cols: []string{"c"}, data: [][]driver.Value{{int64(2)}}}
	mockRegistry["GROUP BY"] = &mockRows{
		cols: []string{"dept", "total"},
		data: [][]driver.Value{{"a", int64(30)}, {"b", int64(5)}},
	}
	defer func() {
		delete(mockRegistry, "COUNT(*) FROM (SELECT")
		delete(mockRegistry, "GROUP BY")
	}()

	// Page 的 Total 与 List 长度口径一致：分组查询下 total 应为分组数。
	res, err := Page(ctx(), db, NewQuery[aggSale]().SelectCol(dept).SelectSum(amount, "total").GroupBy(dept), 1, 10)
	if err != nil {
		t.Fatalf("Page 失败：%v", err)
	}
	if res.Total != int64(len(res.List)) {
		t.Fatalf("分页总数与列表行数不一致：Total=%d len(List)=%d", res.Total, len(res.List))
	}
}

func ctx() context.Context { return context.Background() }

// ---- COUNT(DISTINCT col) ----

// aggDistinctSale 用于 SelectCountDistinct：需要「部门 + 客户」两列才能表达
// 「每个部门有多少个不同客户」。
type aggDistinctSale struct {
	Id       int64  `db:"id,pk,autoincrement"`
	Dept     string `db:"dept"`
	Customer string `db:"customer"`
}

// TestSelectCountDistinctBuildsSQL 三方言渲染 COUNT(DISTINCT col)，且列名/别名都走方言引号。
func TestSelectCountDistinctBuildsSQL(t *testing.T) {
	dept := ColOf[aggDistinctSale]("Dept")
	customer := ColOf[aggDistinctSale]("Customer")
	cases := []struct {
		name string
		d    Dialect
		want string
	}{
		{"pg", PG,
			`SELECT "dept", COUNT(DISTINCT "customer") AS "customers" FROM "agg_distinct_sales" GROUP BY "dept"`},
		{"mysql", MySQL,
			"SELECT `dept`, COUNT(DISTINCT `customer`) AS `customers` FROM `agg_distinct_sales` GROUP BY `dept`"},
		{"sqlite", SQLite,
			`SELECT "dept", COUNT(DISTINCT "customer") AS "customers" FROM "agg_distinct_sales" GROUP BY "dept"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sqlStr, _ := NewQuery[aggDistinctSale]().WithDialect(c.d).
				SelectCol(dept).
				SelectCountDistinct(customer, "customers").
				GroupBy(dept).
				ToSQL()
			if sqlStr != c.want {
				t.Fatalf("SQL 不符\nwant: %s\n got: %s", c.want, sqlStr)
			}
		})
	}
}

// TestSelectCountDistinctRequiresAlias：别名仍是必填的，与其它聚合投影同一口径。
func TestSelectCountDistinctRequiresAlias(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("缺别名应 panic")
		}
		if !strings.Contains(fmt.Sprint(r), "别名") {
			t.Fatalf("panic 文案应说明别名必填，实际: %v", r)
		}
	}()
	NewQuery[aggDistinctSale]().
		SelectCountDistinct(ColOf[aggDistinctSale]("Customer"), "")
}

// TestSelectCountDistinctIsNotSelectDistinct：COUNT(DISTINCT col) 不能退化成
// 「整行去重后计数」——两者语义不同，SQL 里必须能看到 DISTINCT 在括号内。
func TestSelectCountDistinctIsNotSelectDistinct(t *testing.T) {
	sqlStr, _ := NewQuery[aggDistinctSale]().WithDialect(PG).
		SelectCountDistinct(ColOf[aggDistinctSale]("Customer"), "customers").
		GroupBy(ColOf[aggDistinctSale]("Dept")).
		ToSQL()
	if strings.HasPrefix(sqlStr, "SELECT DISTINCT") {
		t.Fatalf("不应把 Distinct() 的整行去重混进来: %s", sqlStr)
	}
	if !strings.Contains(sqlStr, "COUNT(DISTINCT \"customer\")") {
		t.Fatalf("应渲染括号内的 DISTINCT: %s", sqlStr)
	}
}
