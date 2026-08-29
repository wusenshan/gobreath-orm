package orm

import (
	"context"
	"database/sql"
	"reflect"
	"time"
)

// RawQuery 执行任意原生 SQL（JOIN、子查询、报表、方言专属语法均可），
// 并把结果集自动扫描为 []T。复杂查询的兜底出口，定位对标 MyBatis-Plus 交给 XML 的那部分。
//
// T 既可以是表结构体，也可以是任意"视图结构体"（DTO）：
//   - 字段无 db tag 时，列名按字段名的 snake_case 匹配（UserName → user_name）；
//   - 结果集中未匹配的列直接忽略，DTO 只需声明关心的列；
//   - SQL 里写了别名（AS xxx）时，别名即返回列名，按 snake_case 命名即可命中字段；
//   - T 为标量类型（int64 / string / time.Time 等）时按单列扫描。
//
// 参数只走占位符绑定，不拼接；SQL 日志 / 慢查询与普通 CRUD 走同一管线；
// 传入事务内的 *DB 即在事务中执行。
func RawQuery[T any](ctx context.Context, db *DB, query string, args ...any) ([]T, error) {
	rows, err := db.queryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		var t T
		if err := scanRaw(rows, &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RawOne 同 RawQuery，但只取首行；无结果时返回 ErrNotFound。
// T 为标量类型时可直接查 COUNT(*) / MAX(x) 等：
//
//	n, err := orm.RawOne[int64](ctx, db, "SELECT COUNT(*) FROM users WHERE age > ?", 18)
func RawOne[T any](ctx context.Context, db *DB, query string, args ...any) (T, error) {
	var zero T
	rows, err := db.queryContext(ctx, query, args...)
	if err != nil {
		return zero, err
	}
	defer rows.Close()
	if !rows.Next() {
		if e := rows.Err(); e != nil {
			return zero, e
		}
		return zero, ErrNotFound
	}
	var t T
	if err := scanRaw(rows, &t); err != nil {
		return zero, err
	}
	return t, nil
}

// RawExec 执行原生增删改 / DDL（CREATE INDEX、TRUNCATE、批量 UPDATE ... JOIN 等），
// 返回 sql.Result。参数同样只走绑定。
func RawExec(ctx context.Context, db *DB, query string, args ...any) (sql.Result, error) {
	return db.execContext(ctx, query, args...)
}

// scanRaw 按目标类型分发：普通结构体走列名映射（支持别名与 DTO），
// 标量与 time.Time 直接单列 Scan。
func scanRaw(rows *sql.Rows, dest any) error {
	typ := reflect.TypeOf(dest)
	for typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	if typ.Kind() == reflect.Struct && typ != reflect.TypeOf(time.Time{}) {
		return scanStruct(rows, dest)
	}
	return rows.Scan(dest)
}
