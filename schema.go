package orm

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// 表结构自省（schema introspection）：表是否存在、列是否存在、列元信息清单。
// 与 AutoMigrate 的补列逻辑共用同一套方言判定与主库路由 —— 自省虽是 SELECT，
// 但读写路由会把它发到副本，而副本的 DDL 可能滞后，在副本上看结构会得出
// 与真实结构相反的结论，所以元数据查询一律走 queryPrimaryContext（主库）。

// ColumnMeta 是 DescribeTable 返回的单列元信息。
// 各字段取自数据库系统表的原样文本（不做类型猜测）：DataType 是方言自己的
// 类型名（PG 的 "bigint" / MySQL 的 "varchar" / SQLite 的声明类型等），
// Default 是默认值表达式文本（无默认值为 nil）。
type ColumnMeta struct {
	Name          string
	DataType      string
	Nullable      bool
	Default       *string
	PrimaryKey    bool
	AutoIncrement bool
}

// TableExists 判断表是否存在（table 可带 schema. 前缀，前缀口径与 AutoMigrate 一致）。
// 方言不支持自省（自定义方言未实现 DialectKinder）时返回错误而不是 false ——
// 「不知道」伪装成「不存在」会让调用方误判（例如据此重复建表）。
func TableExists(ctx context.Context, db *DB, table string) (bool, error) {
	cols, err := existingColumns(ctx, db, table)
	if err != nil {
		return false, err
	}
	return len(cols) > 0, nil
}

// ColumnExists 判断列是否存在（大小写不敏感，与 AutoMigrate 的列名归一口径一致）。
// 表不存在时同样返回 false —— 不区分「表缺失」与「列缺失」，需要区分请先 TableExists。
func ColumnExists(ctx context.Context, db *DB, table, column string) (bool, error) {
	cols, err := existingColumns(ctx, db, table)
	if err != nil {
		return false, err
	}
	_, ok := cols[strings.ToLower(column)]
	return ok, nil
}

// DescribeTable 返回表的全部列元信息，顺序即建列顺序
// （PG / MySQL 按 ordinal_position，SQLite 按 cid）。
// 空 table 返回 ErrInvalidArgument；方言不支持自省时报错（理由同 TableExists）。
func DescribeTable(ctx context.Context, db *DB, table string) ([]ColumnMeta, error) {
	if table == "" {
		return nil, fmt.Errorf("orm: DescribeTable: %w：table 为空", ErrInvalidArgument)
	}
	if _, known := dialectKindKnown(db.dialect); !known {
		return nil, fmt.Errorf("orm: 当前方言不支持表结构自省（DescribeTable 需要 DialectKind）")
	}
	schema, name := splitTableName(table)
	query, ok := tableColumnsDetailSQL(db.dialect, schema, name)
	if !ok {
		return nil, fmt.Errorf("orm: 当前方言不支持表结构自省（DescribeTable 需要 DialectKind）")
	}
	rows, err := db.queryPrimaryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("orm: 读取表 %q 列信息失败: %w", table, err)
	}
	return scanColumnMeta(rows)
}

// existingColumns 是 TableExists / ColumnExists 的共用实现，复用 AutoMigrate
// 的列名自省（同一套方言判定、主库路由与大小写归一），不自建第二套查询。
func existingColumns(ctx context.Context, db *DB, table string) (map[string]struct{}, error) {
	if _, known := dialectKindKnown(db.dialect); !known {
		return nil, fmt.Errorf("orm: 当前方言不支持表结构自省（TableExists / ColumnExists 需要 DialectKind）")
	}
	return db.tableColumns(ctx, table)
}

// tableColumnsDetailSQL 生成「查某表全部列详情」的 SQL，三方言输出**同构的 6 列**：
// 列名 / 类型名 / 是否可空('YES'/'NO') / 默认值表达式(可 NULL) / 是否主键(0|1) / 是否自增(0|1)，
// 一个扫描函数即可通吃三个方言。与 tableColumnsSQL 同样的取舍：系统表是方言
// 差异最集中的地方，各写一份不做抽象；表名与库名走字面量而非占位符 ——
// 它们是模型里的标识符（已过 quoteTable 校验），且 SQLite 的 pragma 表值函数
// 对绑定参数的支持各版本不一。
//
// 自增判定：PG 看 is_identity（PG 10+）或 nextval 序列默认值；MySQL 看 extra 的
// auto_increment 标记；SQLite 没有自增标志，按「INTEGER 主键 = rowid 别名」推断
// （SQLite 里 INTEGER PRIMARY KEY 本身就是自增 rowid，WITHOUT ROWID 表除外）。
func tableColumnsDetailSQL(d Dialect, schema, table string) (string, bool) {
	kind, known := dialectKindKnown(d)
	if !known {
		return "", false
	}
	switch kind {
	case "postgres":
		// 未指定模式时用当前模式（与 tableColumnsSQL 同理：写死 'public' 会
		// 漏掉自定义 search_path 的场景）。
		owner := "current_schema()"
		if schema != "" {
			owner = sqlLiteral(schema)
		}
		return fmt.Sprintf(`SELECT c.column_name, c.data_type, c.is_nullable, c.column_default,
       CASE WHEN EXISTS (
           SELECT 1 FROM information_schema.table_constraints tc
           JOIN information_schema.key_column_usage kcu
             ON kcu.constraint_name = tc.constraint_name AND kcu.table_schema = tc.table_schema
           WHERE tc.table_schema = c.table_schema AND tc.table_name = c.table_name
             AND tc.constraint_type = 'PRIMARY KEY' AND kcu.column_name = c.column_name
       ) THEN 1 ELSE 0 END,
       CASE WHEN COALESCE(c.is_identity, 'NO') = 'YES' OR c.column_default LIKE 'nextval%%' THEN 1 ELSE 0 END
FROM information_schema.columns c
WHERE c.table_schema = %s AND c.table_name = %s
ORDER BY c.ordinal_position`, owner, sqlLiteral(table)), true
	case "mysql":
		owner := "DATABASE()"
		if schema != "" {
			owner = sqlLiteral(schema)
		}
		return fmt.Sprintf(`SELECT column_name, data_type, is_nullable, column_default,
       CASE WHEN column_key = 'PRI' THEN 1 ELSE 0 END,
       CASE WHEN extra LIKE '%%auto_increment%%' THEN 1 ELSE 0 END
FROM information_schema.columns
WHERE table_schema = %s AND table_name = %s
ORDER BY ordinal_position`, owner, sqlLiteral(table)), true
	default:
		// SQLite：pragma_table_info 输出 (cid, name, type, notnull, dflt_value, pk)，
		// 在 SQL 侧重排成同构 6 列（notnull 取反成 is_nullable、pk 归一为 0/1）。
		// 注意 notnull / pk / cid 必须带引号引用：裸写 notnull 会被 SQLite 词法
		// 分析拆成 NOT NULL 记号流，报 `near "notnull": syntax error`
		// （AutoMigrate 的旧查询只 select name 才一直没踩到；真库实测确认）。
		if schema != "" {
			return fmt.Sprintf(`SELECT name, type,
       CASE WHEN "notnull" = 0 THEN 'YES' ELSE 'NO' END, dflt_value,
       CASE WHEN "pk" > 0 THEN 1 ELSE 0 END,
       CASE WHEN "pk" = 1 AND instr(upper(type), 'INTEGER') > 0 THEN 1 ELSE 0 END
FROM pragma_table_info(%s, %s) ORDER BY "cid"`, sqlLiteral(table), sqlLiteral(schema)), true
		}
		return fmt.Sprintf(`SELECT name, type,
       CASE WHEN "notnull" = 0 THEN 'YES' ELSE 'NO' END, dflt_value,
       CASE WHEN "pk" > 0 THEN 1 ELSE 0 END,
       CASE WHEN "pk" = 1 AND instr(upper(type), 'INTEGER') > 0 THEN 1 ELSE 0 END
FROM pragma_table_info(%s) ORDER BY "cid"`, sqlLiteral(table)), true
	}
}

// scanColumnMeta 扫描 tableColumnsDetailSQL 的同构 6 列结果集。
func scanColumnMeta(rows *sql.Rows) ([]ColumnMeta, error) {
	defer rows.Close()
	var out []ColumnMeta
	for rows.Next() {
		var name, dtype, nullable string
		var dflt *string
		var pk, auto any // 0/1 标志：驱动可能给 int64（二进制协议）或 []byte("1")（文本协议）
		if err := rows.Scan(&name, &dtype, &nullable, &dflt, &pk, &auto); err != nil {
			return nil, err
		}
		out = append(out, ColumnMeta{
			Name:          name,
			DataType:      dtype,
			Nullable:      strings.EqualFold(nullable, "YES"),
			Default:       dflt,
			PrimaryKey:    flagOne(pk),
			AutoIncrement: flagOne(auto),
		})
	}
	return out, rows.Err()
}

// flagOne 把方言驱动交回的 0/1 标志统一成 bool。
func flagOne(v any) bool {
	switch x := v.(type) {
	case int64:
		return x == 1
	case int:
		return x == 1
	case []byte:
		return string(x) == "1"
	case string:
		return x == "1"
	case bool:
		return x
	}
	return false
}
