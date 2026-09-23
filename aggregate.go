package orm

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// 聚合函数与单列投影（Pluck）—— 补齐 CRUD 基本盘里缺的一角。
//
// 与 Count 的分工：
//   - Count 走 agg("COUNT", "")，语义是「数行数」，会带上 JOIN 但不参与 GROUP BY；
//   - Sum / Avg / Max / Min 走 agg(fn, col)，投影被替换成聚合表达式。
//
// 两者共用 Query.fromClause 与同一套 WHERE 渲染，避免再出现「Count 漏掉 JOIN」
// 这类两处口径不一致的问题。

// colRef 聚合 / 投影函数接受的列引用：ColExpr 与 TColExpr 均满足。
//
// 关键点是它不接受裸字符串 —— 列名只能来自 Col / TCol / ColOf（结构体 db tag 白名单）
// 或 ormgen 生成的列集，所以这批新 API 不给「三层防注入」开后门。
type colRef interface{ Name() string }

// resolveAggCol 校验列确实属于 T 的模型，并返回列名。
//
// ColExpr 的类型是擦除的，orm.Sum(ctx, db, queryOfOrder, orm.Col[User](...)) 能通过编译，
// 却会生成一条列不存在的 SQL。这里显式拦掉，把「跑到数据库才报错」提前成「调用即报错」。
func resolveAggCol[T any](col colRef) (string, error) { return resolveColName[T](col.Name()) }

// resolveColName 校验列名确实属于 T 的模型。
func resolveColName[T any](name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("orm: 空列表达式，请用 orm.Col / orm.TCol 选择列")
	}
	meta := getMeta[T]()
	if fieldInfoForCol(meta, name) == nil {
		return "", fmt.Errorf("orm: 列 %q 不属于模型 %s（列表达式来自另一个类型？）", name, meta.table)
	}
	return name, nil
}

// aggReady 把查询补齐到「可执行」状态：方言、表前缀、软删除过滤，
// 并拒绝 GROUP BY / HAVING —— 那种查询会返回多行，单值接口只能取到第一行，
// 静默返回部分结果比直接报错危险得多。
func aggReady[T any](q *Query[T], db *DB, fn string) (*Query[T], error) {
	qq := q.applyLogic(getMeta[T](), db).WithDialect(db.dialect).WithPrefix(db.prefix)
	if len(qq.groupBy) > 0 || len(qq.havings) > 0 {
		return nil, fmt.Errorf(
			"orm: %s 不支持带 GROUP BY / HAVING 的查询（会返回多行，单值接口无法表达）；"+
				"请改用 SelectList + Select(聚合表达式 AS 别名) 扫进自定义 DTO", fn)
	}
	return qq, nil
}

// aggScalar 执行单值聚合，返回驱动原生类型；[]byte 归一为 string。
// 归一的原因是 PG 的数值聚合（SUM over bigint / numeric）经 database/sql 回来是 []byte，
// 直接交给调用方会出现「看着像数字的字节串」。
func aggScalar[T any](ctx context.Context, db *DB, q *Query[T], fn, col string) (any, error) {
	qq, err := aggReady(q, db, fn)
	if err != nil {
		return nil, err
	}
	sqlStr, args := qq.agg(fn, col).Build()
	var v any
	if err := db.queryRowContext(ctx, sqlStr, args...).Scan(&v); err != nil {
		return nil, err
	}
	if b, ok := v.([]byte); ok {
		return string(b), nil
	}
	return v, nil
}

// aggScalarAs 执行单值聚合并扫描成强类型 F。
// 结果为 NULL（例如对空结果集 SUM）时返回 F 的零值而不是报错，与 sql.Null 的 Valid=false 同义。
func aggScalarAs[T any, F any](ctx context.Context, db *DB, q *Query[T], fn, col string) (F, error) {
	var zero F
	qq, err := aggReady(q, db, fn)
	if err != nil {
		return zero, err
	}
	sqlStr, args := qq.agg(fn, col).Build()
	// 时间列走单独路径：聚合表达式不带列类型信息，部分驱动只能按 TEXT 返回
	// （实测 SQLite），sql.Null[time.Time] 会直接扫描失败。详见 aggScalarAsTime。
	if tp, ok := any(&zero).(*time.Time); ok {
		t, err := aggScalarAsTime(ctx, db, sqlStr, args)
		if err != nil {
			return zero, err
		}
		*tp = t
		return zero, nil
	}
	var v sql.Null[F]
	if err := db.queryRowContext(ctx, sqlStr, args...).Scan(&v); err != nil {
		return zero, err
	}
	return v.V, nil
}

// aggScalarAsTime 执行聚合并把结果归一为 time.Time，用于 MaxOf / MinOf 作用在时间列上。
//
// 存在的理由：MAX(col) / MIN(col) 在 SQL 里是一个**没有声明类型**的表达式，
// 驱动拿不到列类型信息，只能按存储形态交回来。实测 modernc.org/sqlite 就是按 TEXT 返回，
// 于是 sql.Null[time.Time] 扫描失败 —— 而同一字段走 SelectById 读原始列完全正常，
// 因为原始列声明了 DATETIME。（PG / MySQL 的驱动会按列类型还原成 time.Time，走这里也无损。）
//
// 实现上先扫成 any 再归一，顺带容忍各方言的时间文本格式差异。
func aggScalarAsTime(ctx context.Context, db *DB, sqlStr string, args []any) (time.Time, error) {
	var raw any
	if err := db.queryRowContext(ctx, sqlStr, args...).Scan(&raw); err != nil {
		return time.Time{}, err
	}
	return coerceTime(raw)
}

// timeTextLayouts 覆盖各方言驱动可能交回的时间文本写法，按「信息量从多到少」排列。
//
// 其中 `2006-01-02 15:04:05.999999999 -0700 MST` 是 Go 的 time.Time.String() 形态：
// SQLite 驱动（modernc）把时间列落成 TEXT 后，聚合表达式再读出来就是这个样子
// （真库实测："2026-09-01 16:00:00 +0000 UTC"）。
var timeTextLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.999999999 -0700 MST",
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

// coerceTime 把驱动返回的值归一为 time.Time；NULL 归一为零值（与 sql.Null 的 Valid=false 同义）。
func coerceTime(raw any) (time.Time, error) {
	switch v := raw.(type) {
	case nil:
		return time.Time{}, nil
	case time.Time:
		return v, nil
	case []byte:
		return parseTimeText(string(v))
	case string:
		return parseTimeText(v)
	case int64: // 少数驱动/存储形态把时间落成 unix 秒
		return time.Unix(v, 0).UTC(), nil
	default:
		return time.Time{}, fmt.Errorf("orm: 聚合结果 (%T) %v 无法归一为 time.Time", raw, raw)
	}
}

// parseTimeText 按已知布局解析时间文本；全部不匹配时返回错误而不是静默零值。
func parseTimeText(s string) (time.Time, error) {
	for _, layout := range timeTextLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("orm: 无法把 %q 解析为 time.Time（已知布局均不匹配）", s)
}

// Sum 求某列之和。列类型无关，统一以 float64 返回；数据库返回 NULL（空结果集）时为 0。
//
//	total, err := orm.Sum(ctx, db, orm.NewQuery[Order]().Eq(orderCols.Status, "paid"),
//	    orm.Col[Order](func(o *Order) *int { return &o.Amount }))
func Sum[T any](ctx context.Context, db *DB, q *Query[T], col colRef) (float64, error) {
	name, err := resolveAggCol[T](col)
	if err != nil {
		return 0, err
	}
	return aggScalarAs[T, float64](ctx, db, q, "SUM", name)
}

// Avg 求某列平均值，语义同 SQL AVG；空结果集为 0。
func Avg[T any](ctx context.Context, db *DB, q *Query[T], col colRef) (float64, error) {
	name, err := resolveAggCol[T](col)
	if err != nil {
		return 0, err
	}
	return aggScalarAs[T, float64](ctx, db, q, "AVG", name)
}

// Max 返回某列最大值，类型为驱动原生类型（数值列 → int64/float64/string，时间列 → time.Time）。
// 需要确定的返回类型时用 MaxOf。
func Max[T any](ctx context.Context, db *DB, q *Query[T], col colRef) (any, error) {
	name, err := resolveAggCol[T](col)
	if err != nil {
		return nil, err
	}
	return aggScalar[T](ctx, db, q, "MAX", name)
}

// Min 返回某列最小值，语义同 SQL MIN。
func Min[T any](ctx context.Context, db *DB, q *Query[T], col colRef) (any, error) {
	name, err := resolveAggCol[T](col)
	if err != nil {
		return nil, err
	}
	return aggScalar[T](ctx, db, q, "MIN", name)
}

// SumOf 是 Sum 的强类型版本：F 由 TCol 携带的字段类型推导，PG 的 numeric → float64/int
// 这类转换交给 database/sql 完成，不必自己断言。
//
//	total, err := orm.SumOf(ctx, db, q, orm.TCol(func(o *Order) *int64 { return &o.Amount }))
func SumOf[T any, F any](ctx context.Context, db *DB, q *Query[T], col TColExpr[T, F]) (F, error) {
	var zero F
	name, err := resolveAggCol[T](col)
	if err != nil {
		return zero, err
	}
	return aggScalarAs[T, F](ctx, db, q, "SUM", name)
}

// AvgOf 是 Avg 的强类型版本。
func AvgOf[T any, F any](ctx context.Context, db *DB, q *Query[T], col TColExpr[T, F]) (F, error) {
	var zero F
	name, err := resolveAggCol[T](col)
	if err != nil {
		return zero, err
	}
	return aggScalarAs[T, F](ctx, db, q, "AVG", name)
}

// MaxOf 是 Max 的强类型版本。时间列取最新值时最顺手：
//
//	last, err := orm.MaxOf(ctx, db, q, orm.TCol(func(u *User) *time.Time { return &u.CreatedAt }))
func MaxOf[T any, F any](ctx context.Context, db *DB, q *Query[T], col TColExpr[T, F]) (F, error) {
	var zero F
	name, err := resolveAggCol[T](col)
	if err != nil {
		return zero, err
	}
	return aggScalarAs[T, F](ctx, db, q, "MAX", name)
}

// MinOf 是 Min 的强类型版本。
func MinOf[T any, F any](ctx context.Context, db *DB, q *Query[T], col TColExpr[T, F]) (F, error) {
	var zero F
	name, err := resolveAggCol[T](col)
	if err != nil {
		return zero, err
	}
	return aggScalarAs[T, F](ctx, db, q, "MIN", name)
}

// Pluck 只查一列并返回强类型切片，生成 SELECT col FROM ...。
// 相比 SelectList 拿 []T 再循环取值，它不构造整个模型、也不白读其余列。
// 类型全部从列表达式推导，调用点不需要写任何类型参数：
//
//	ids, err := orm.Pluck(ctx, db, q, orm.TCol(func(u *User) *int64 { return &u.ID }))
//
// F 应为标量类型；列值为 NULL 时该位置填入 F 的零值（保持下标与结果集对齐）。
//
// 列集由 ormgen 生成（类型是 orm.ColExpr，不带字段类型）时用 PluckCol。
func Pluck[T any, F any](ctx context.Context, db *DB, q *Query[T], col TColExpr[T, F]) ([]F, error) {
	return pluck[T, F](ctx, db, q, col.Name())
}

// PluckCol 是 Pluck 的「列类型未知」版本，接受任意列表达式（含 ormgen 生成的 ColExpr）。
// 因为 Go 无法从接口类型的参数推导类型参数，这里的元素类型必须显式给出：
//
//	ids, err := orm.PluckCol[User, int64](ctx, db, q, UserCols.ID)
//
// 这是当前 Go 泛型的限制，不是可省的样板：F 一旦能被推导，就说明列集已经带了字段类型
// （TCol，或后续 ormgen 生成的强类型列集），那时应该直接用 Pluck。
func PluckCol[T any, F any](ctx context.Context, db *DB, q *Query[T], col colRef) ([]F, error) {
	return pluck[T, F](ctx, db, q, col.Name())
}

// pluck 是 Pluck / PluckCol 的共用实现。
func pluck[T any, F any](ctx context.Context, db *DB, q *Query[T], name string) ([]F, error) {
	if _, err := resolveColName[T](name); err != nil {
		return nil, err
	}
	qq := q.applyLogic(getMeta[T](), db).WithDialect(db.dialect).WithPrefix(db.prefix)
	c := *qq
	c.selects = []string{name}
	c.noVecCol = true // 单列投影：不要再追加向量距离列
	sqlStr, args := c.Build()

	rows, err := db.queryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]F, 0, 16)
	for rows.Next() {
		var v sql.Null[F]
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		var zero F
		if v.Valid {
			out = append(out, v.V)
		} else {
			out = append(out, zero)
		}
	}
	return out, rows.Err()
}

// DryRun 返回该查询在 db 上下文（方言 / 表前缀 / 软删除条件）下真正会执行的 SQL 与参数，
// 不访问数据库。对标 GORM 的 DryRun：单测断言 SQL、排查慢查询、把语句交给 DBA 复核都用它。
//
// 只要查询本身不带 db 上下文（表前缀、软删除字段来源），也可以用 Query.ToSQL()。
func DryRun[T any](db *DB, q *Query[T]) (string, []any) {
	return q.applyLogic(getMeta[T](), db).WithDialect(db.dialect).WithPrefix(db.prefix).Build()
}
