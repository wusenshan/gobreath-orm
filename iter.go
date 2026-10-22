package orm

import (
	"context"
	"iter"
)

// 流式遍历（cursor iteration）：单条连接持有一个游标，逐行产出实体，
// 面向大表扫描 / 批量导出 / ETL —— 这类场景把全量结果拉进内存（SelectList）
// 可能一次性吃掉数 GB。
//
// 实现取「单游标」而不是「分批 OFFSET 翻页」：
//   - OFFSET 翻页在没有稳定排序（或遍历期间有写入）时会跳行 / 重复行，
//     而框架无法替调用方保证排序键稳定 —— 这是把静默错误塞给了最不看数据的人；
//   - 单游标下数据库按语句快照或锁语义给出行流，语义就是「这一条 SQL 的结果集」，
//     不引入框架层的第二个正确性条件。
//
// 代价与边界（务必读）：
//   - 游标在整个遍历期间占用一个连接（database/sql 层面 rows 未 Close 连接不归还）。
//     遍历体内再通过同一个 *DB 执行查询/写入用的是连接池的其他连接，不受影响；
//     但连接池小时大量并发遍历会互相等连接。
//   - 中途 break / return 会立即关闭游标并归还连接（yield 返回 false 即停止）。
//   - 遍历期间对同一张表做写入后继续读游标，读到的行为取决于方言的事务隔离
//     （MySQL InnoDB / PG 默认一致性读，游标始终读语句开始时的快照；别的方言不保证）。
//     需要跨方言一致语义请自己包事务。
//   - 排序、分页、JOIN、GroupBy、Distinct 等由传入的 q 决定，框架不做二次加工
//     （Limit 也会照常生效——遍历到 LIMIT 就停）。

// QueryIter 流式遍历查询结果，逐行产出（实体, nil）；出错时产出（nil, err）一次后终止，
// 消费方收到 err 应停止 range。逻辑删除过滤与 SelectList 完全一致（Unscoped 例外）。
//
//	for u, err := range orm.QueryIter[User](ctx, db, q) {
//	    if err != nil {
//	        return err // 行映射或网络错误
//	    }
//	    if u.ID > 1000 {
//	        break // 立即关闭游标，后面的行不再读取
//	    }
//	    process(u)
//	}
//
// 空结果集产出行数为零、不产出错误 —— 与 SelectList 返回空切片同语义。
func QueryIter[T any](ctx context.Context, db *DB, q *Query[T]) iter.Seq2[*T, error] {
	return func(yield func(*T, error) bool) {
		meta := getMeta[T]()
		sqlStr, args := q.applyLogic(meta, db).WithDialect(db.dialect).WithPrefix(db.prefix).Build()
		rows, err := db.queryContext(ctx, sqlStr, args...)
		if err != nil {
			yield(nil, err)
			return
		}
		defer rows.Close()
		sc, err := newRowScanner(rows, (*T)(nil))
		if err != nil {
			yield(nil, err)
			return
		}
		for rows.Next() {
			var t T
			if err := sc.scanInto(rows, &t); err != nil {
				yield(nil, err)
				return
			}
			if !yield(&t, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(nil, err)
		}
	}
}
