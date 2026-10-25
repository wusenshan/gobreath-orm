package integration

import (
	"context"
	"testing"

	orm "github.com/wusenshan/gobreath-orm"
)

// GrpSale 一张够用的销售明细表：按部门分组统计金额与笔数。
type GrpSale struct {
	ID     int64  `db:"id,pk,autoincrement"`
	Dept   string `db:"dept"`
	Amount int64  `db:"amount"`
}

func (GrpSale) TableName() string { return "grp_sales" }

// GrpDeptStat 结果 DTO：字段名 / db tag 对上 SelectSum / SelectCount 起的别名，
// 分组结果就能落进来（不需要任何新的元数据机制）。
type GrpDeptStat struct {
	Dept  string `db:"dept"`
	Total int64  `db:"total"`
	Cnt   int64  `db:"cnt"`
}

var grpCols = struct {
	Dept   orm.ColExpr
	Amount orm.ColExpr
}{
	Dept:   orm.ColOf[GrpSale]("Dept"),
	Amount: orm.ColOf[GrpSale]("Amount"),
}

// TestAggregateScansGroupResultIntoDTO —— 单元测试只能证明 SQL 形状，这里证明的是
// 「真数据库的 GROUP BY 结果集 → rows.Columns() 给出别名 → 落到 DTO 字段」这条链路
// 在四种后端上都成立（驱动返回的聚合值类型各不相同：PG 的 SUM(bigint) 回来是
// int64，MySQL 可能是 []byte / decimal，SQLite 是 int64）。
func TestAggregateScansGroupResultIntoDTO(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		if _, err := orm.RawExec(ctx, db, "DROP TABLE IF EXISTS grp_sales"); err != nil {
			t.Fatalf("[%s] DROP 失败：%v", b.name, err)
		}
		if err := db.AutoMigrate(ctx, &GrpSale{}); err != nil {
			t.Fatalf("[%s] AutoMigrate 失败：%v", b.name, err)
		}
		for _, s := range []GrpSale{
			{Dept: "a", Amount: 10},
			{Dept: "a", Amount: 20},
			{Dept: "b", Amount: 5},
		} {
			row := s
			if err := orm.Insert(ctx, db, &row); err != nil {
				t.Fatalf("[%s] Insert 失败：%v", b.name, err)
			}
		}

		stats, err := orm.Aggregate[GrpDeptStat](ctx, db, orm.NewQuery[GrpSale]().
			SelectCol(grpCols.Dept).
			SelectSum(grpCols.Amount, "total").
			SelectCount("cnt").
			GroupBy(grpCols.Dept).
			HavingSum(grpCols.Amount, ">", 1).
			OrderBy(grpCols.Dept, true))
		if err != nil {
			t.Fatalf("[%s] Aggregate 失败：%v", b.name, err)
		}
		if len(stats) != 2 {
			t.Fatalf("[%s] 应有 2 个分组（a / b），实际 %d：%+v", b.name, len(stats), stats)
		}
		if stats[0].Dept != "a" || stats[0].Total != 30 || stats[0].Cnt != 2 {
			t.Fatalf("[%s] 分组 a 统计错误：%+v（期望 Dept=a Total=30 Cnt=2）", b.name, stats[0])
		}
		if stats[1].Dept != "b" || stats[1].Total != 5 || stats[1].Cnt != 1 {
			t.Fatalf("[%s] 分组 b 统计错误：%+v（期望 Dept=b Total=5 Cnt=1）", b.name, stats[1])
		}
	})
}

// TestAggregateCountMatchesGroupedList —— Count / Page 的 total 必须是「分组数」：
// 列表 2 行，总数也必须是 2（改造前 Cache Count 会把 groupBy 清掉，
// 于是不管分几组都返回全表行数 3，分页器页数与实际行数对不上）。
func TestAggregateCountMatchesGroupedList(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		if _, err := orm.RawExec(ctx, db, "DROP TABLE IF EXISTS grp_sales"); err != nil {
			t.Fatalf("[%s] DROP 失败：%v", b.name, err)
		}
		if err := db.AutoMigrate(ctx, &GrpSale{}); err != nil {
			t.Fatalf("[%s] AutoMigrate 失败：%v", b.name, err)
		}
		for _, s := range []GrpSale{
			{Dept: "a", Amount: 10},
			{Dept: "a", Amount: 20},
			{Dept: "b", Amount: 5},
			{Dept: "c", Amount: 1}, // HAVING SUM > 5 会把它滤掉
		} {
			row := s
			if err := orm.Insert(ctx, db, &row); err != nil {
				t.Fatalf("[%s] Insert 失败：%v", b.name, err)
			}
		}

		q := orm.NewQuery[GrpSale]().
			SelectCol(grpCols.Dept).
			SelectSum(grpCols.Amount, "total").
			GroupBy(grpCols.Dept).
			HavingSum(grpCols.Amount, ">", 5) // a=30, b=5 → b 也被滤掉，只剩 a、c? c=1 不满足
		total, err := orm.Count(ctx, db, q)
		if err != nil {
			t.Fatalf("[%s] Count 失败：%v", b.name, err)
		}
		list, err := orm.Aggregate[GrpDeptStat](ctx, db, q)
		if err != nil {
			t.Fatalf("[%s] Aggregate 失败：%v", b.name, err)
		}
		if int64(len(list)) != total {
			t.Fatalf("[%s] Count 与 list 长度不符：Count=%d len=%d（%v）", b.name, total, len(list), list)
		}
		if total != 1 {
			t.Fatalf("[%s] total 应为 1（只有分组 a 满足 HAVING SUM > 5），实际 %d：%+v", b.name, total, list)
		}
	})
}

// TestDistinctCountIsDeduplicated —— Distinct 的总数必须去重：
// 3 行数据里 dept 只有 2 个不同值，带 Distinct 的 Count 必须是 2。
func TestDistinctCountIsDeduplicated(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		if _, err := orm.RawExec(ctx, db, "DROP TABLE IF EXISTS grp_sales"); err != nil {
			t.Fatalf("[%s] DROP 失败：%v", b.name, err)
		}
		if err := db.AutoMigrate(ctx, &GrpSale{}); err != nil {
			t.Fatalf("[%s] AutoMigrate 失败：%v", b.name, err)
		}
		for _, s := range []GrpSale{{Dept: "a", Amount: 1}, {Dept: "a", Amount: 2}, {Dept: "b", Amount: 3}} {
			row := s
			if err := orm.Insert(ctx, db, &row); err != nil {
				t.Fatalf("[%s] Insert 失败：%v", b.name, err)
			}
		}

		q := orm.NewQuery[GrpSale]().SelectCol(grpCols.Dept).Distinct()
		total, err := orm.Count(ctx, db, q)
		if err != nil {
			t.Fatalf("[%s] Count 失败：%v", b.name, err)
		}
		if total != 2 {
			t.Fatalf("[%s] Distinct 去重后应为 2 行，实际 %d", b.name, total)
		}
		// Distinct 查询与自己无关的 Pluck 不应受影响，这里只确认 fast path 没把
		// SELECT 投影搞坏（selects 被误清空的话 Distinct(*) 会让结果变成 3）。
		if list, err := orm.SelectList(ctx, db, orm.NewQuery[GrpSale]().SelectCol(grpCols.Dept).Distinct()); err != nil {
			t.Fatalf("[%s] SelectList Distinct 失败：%v", b.name, err)
		} else if len(list) != 2 {
			t.Fatalf("[%s] Distinct 列表应为 2 行，实际 %d", b.name, len(list))
		}
	})
}

// ---- COUNT(DISTINCT col) ----

// GrpCustomerSale 支撑「每个部门有多少个不同客户」这个最典型的分组去重统计。
type GrpCustomerSale struct {
	ID       int64  `db:"id,pk,autoincrement"`
	Dept     string `db:"dept"`
	Customer string `db:"customer"`
}

func (GrpCustomerSale) TableName() string { return "grp_customer_sales" }

type GrpCustomerStat struct {
	Dept      string `db:"dept"`
	Customers int64  `db:"customers"`
}

var grpCustomerCols = struct {
	Dept     orm.ColExpr
	Customer orm.ColExpr
}{
	Dept:     orm.ColOf[GrpCustomerSale]("Dept"),
	Customer: orm.ColOf[GrpCustomerSale]("Customer"),
}

// TestSelectCountDistinctOnRealDB —— COUNT(DISTINCT col) 必须真的按列去重，
// 而不是退化成「整行去重后计数」（数据里 a 部门有重复客户，两者结果不同：2 vs 3）。
func TestSelectCountDistinctOnRealDB(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		if _, err := orm.RawExec(ctx, db, "DROP TABLE IF EXISTS grp_customer_sales"); err != nil {
			t.Fatalf("[%s] DROP 失败：%v", b.name, err)
		}
		if err := db.AutoMigrate(ctx, &GrpCustomerSale{}); err != nil {
			t.Fatalf("[%s] AutoMigrate 失败：%v", b.name, err)
		}
		for _, s := range []GrpCustomerSale{
			{Dept: "a", Customer: "alice"},
			{Dept: "a", Customer: "alice"}, // 同部门重复客户
			{Dept: "a", Customer: "bob"},
			{Dept: "b", Customer: "carol"},
		} {
			row := s
			if err := orm.Insert(ctx, db, &row); err != nil {
				t.Fatalf("[%s] Insert 失败：%v", b.name, err)
			}
		}

		stats, err := orm.Aggregate[GrpCustomerStat](ctx, db, orm.NewQuery[GrpCustomerSale]().
			SelectCol(grpCustomerCols.Dept).
			SelectCountDistinct(grpCustomerCols.Customer, "customers").
			GroupBy(grpCustomerCols.Dept).
			OrderBy(grpCustomerCols.Dept, true))
		if err != nil {
			t.Fatalf("[%s] COUNT(DISTINCT) 执行失败：%v", b.name, err)
		}
		if len(stats) != 2 {
			t.Fatalf("[%s] 应返回 2 个分组，实际 %d: %+v", b.name, len(stats), stats)
		}
		want := map[string]int64{"a": 2, "b": 1}
		for _, s := range stats {
			if s.Customers != want[s.Dept] {
				t.Fatalf("[%s] 部门 %q 的去重客户数 = %d；期望 %d（整行去重会给出 3）",
					b.name, s.Dept, s.Customers, want[s.Dept])
			}
		}
	})
}
