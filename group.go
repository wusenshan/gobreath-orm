package orm

import (
	"context"
)

// 分组聚合查询：把 GROUP BY 的结果映射回结构体。
//
// 能力来源有两处，缺一不可：
//  1. SelectSum / SelectCount 等构造器给投影加上「表达式 + 别名」（见 query.go）；
//  2. 这里的 Aggregate 把结果扫进调用方自定义的 DTO。
//
// 第 2 步不需要任何新的元数据：既有行映射本来就是按「结果集列名 == 字段的 db 列名」
// 匹配的（buildScanPlan），而带 AS 的聚合列交给 rows.Columns() 的正是别名 ——
// 所以只要 DTO 里的字段名 / db tag 对得上别名，列就落得进去。
// 这也是为什么要给 DTO 单独定义结构体：聚合结果不是「一行表记录」，
// 混进实体模型会让写路径（INSERT 列清单、AutoMigrate）也背上一个不属于它的列。

// Aggregate 执行带 GROUP BY / 聚合的查询，并把结果逐行扫进 DTO。
//
// 查询条件仍然由实体 T 表达（列名、软删除、方言、前缀都来自 T 的元数据），
// 只有结果集的形状由 DTO 决定 —— 于是类型安全两头都不丢：条件写错列名在编译期
// 就被列表达式拦下，结果字段名写错则是「该字段恒零值」，可用 DryRun 对照 SQL 排查。
//
//	type DeptStat struct {
//	    Dept  string  `db:"dept"`
//	    Total float64 `db:"total"`
//	    Cnt   int64   `db:"cnt"`
//	}
//
//	stats, err := orm.Aggregate[DeptStat](ctx, db,
//	    orm.NewQuery[Sale]().
//	        SelectCol(saleCols.Dept).            // 分组列：列名即结果集列名
//	        SelectSum(saleCols.Amount, "total"). // SUM("amount") AS "total"
//	        SelectCount("cnt").                  // COUNT(*) AS "cnt"
//	        GroupBy(saleCols.Dept).
//	        HavingSum(saleCols.Amount, ">", 100))
//
// DTO 只写第一个类型参数（第二个 T 由传入的 *Query[T] 推导）。
//
// 几点约定：
//   - 调用了 Select* 之后投影即被完全指定，未被选中的列不会出现在结果集里，
//     DTO 里多余的字段保持零值（反之亦然：DTO 缺一个别名，那一列的数据就丢掉）；
//   - MySQL 的 ONLY_FULL_GROUP_BY / PG 都要求非聚合投影列必须出现在 GROUP BY 中，
//     分组列请自行 GroupBy 补齐，否则数据库会报错（框架不替你猜分组键）；
//   - 结果行数由分组决定，想要分页请用 Page / Count 联动：Count 在带 GROUP BY 或
//     DISTINCT 时会自动改成子查询计数，总数与列表行数口径一致。
func Aggregate[DTO any, T any](ctx context.Context, db *DB, q *Query[T]) ([]DTO, error) {
	// 与其它列表入口同一条口径：只补 db 上下文，不修改调用方的 q
	// （判据：调用前后 q.ToSQL() 逐字不变）。
	sqlStr, args := q.applyLogic(getMeta[T](), db).WithDialect(db.dialect).WithPrefix(db.prefix).Build()
	rows, err := db.queryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRows[DTO](rows)
}
