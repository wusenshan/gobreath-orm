package orm

import (
	"context"
	"fmt"
	"reflect"
	"strings"
)

// AutoMigrate 根据传入的实体（指针或零值结构体）自动建表并补齐缺失列，
// 使数据库结构与 Go 模型保持一致。
//
// 当前策略是「只增不减」：
//   - 表不存在 → CREATE TABLE IF NOT EXISTS 建出全部列；
//   - 表已存在 → 逐个比对模型列与已有列，**只补缺失的列**（ALTER TABLE ADD COLUMN），
//     不删列、不改列类型、不删索引，因此可安全地反复调用而不丢失数据。
//
// 实体可传指针或值：orm.AutoMigrate(ctx, db, &User{}, &Article{})。
// 列类型由 Go 类型按当前方言推导；支持主键/自增、`,vector(N)` 向量列、`,json` 列、
// `,unique` 唯一约束、`,index` 二级索引。
//
// 补齐缺失列时的两条硬边界（都返回错误而不是静默放过）：
//   - **主键 / 自增列缺失直接报错**：在已有表上补主键需要回填唯一值，SQLite 更是
//     根本不支持（ALTER TABLE ADD COLUMN 不允许 PRIMARY KEY / AUTOINCREMENT），
//     框架无法代为决定已有行该拿什么主键 —— 请手工迁移或重建表。
//   - **`,unique` 列补齐后加的是唯一索引而非内联 UNIQUE 约束**：SQLite 的
//     ADD COLUMN 不接受 UNIQUE 修饰，三方言统一走 CREATE UNIQUE INDEX，
//     唯一性保证等价（报错文案与 `ON CONFLICT` 的目标名不同，见文档「已知限制」）。
//
// 已知限制：
//   - 只补列，不改列：已有列的类型 / 长度变化不会被检测（PG 的 `vector(1536)` → `vector(768)`
//     同理），需手动 ALTER。
//   - 复合唯一/复合索引暂不支持，仅单列 `,unique` / `,index`。
//   - 向量维度需通过 `,vector(N)` 显式声明；未声明时默认 1536，建议显式指定以对齐模型维度。
//   - 自实现方言若不声明 DialectKinder 也不是内置三方言，框架无从知道它的系统表长什么样，
//     此时退化为「仅建表」（v0.1.12 及以前的行为）。
func (db *DB) AutoMigrate(ctx context.Context, models ...any) error {
	for _, m := range models {
		meta, err := metaOfAny(m)
		if err != nil {
			return err
		}
		create, indexes := migratePlan(meta, db.dialect, db.prefix)
		// 先读已有列：表还不存在时得到空集合，随后的 CREATE TABLE 会把列一次建全，
		// 也就不需要再补列。读不到（方言不支持自省）时 existing 为 nil，走「仅建表」。
		existing, err := db.tableColumns(ctx, meta.finalTable(db.prefix))
		if err != nil {
			return err
		}
		if _, err := db.execContext(ctx, create); err != nil {
			return fmt.Errorf("orm: AutoMigrate 执行失败 (%s): %w", create, err)
		}
		if len(existing) > 0 {
			// 索引必须在补列之后建：缺的列恰好带 ,index 时，
			// 先建索引会因「列不存在」直接失败（且不是「索引已存在」，不会被幂等兜底吞掉）。
			adds, err := addColumnStatements(meta, db.dialect, db.prefix, missingFields(meta, existing))
			if err != nil {
				return err
			}
			for _, s := range adds {
				if _, err := db.execContext(ctx, s); err != nil {
					return fmt.Errorf("orm: AutoMigrate 执行失败 (%s): %w", s, err)
				}
			}
		}
		for _, s := range indexes {
			if _, err := db.execContext(ctx, s); err != nil {
				// MySQL 没有 CREATE INDEX IF NOT EXISTS，重复执行靠忽略「索引已存在」实现幂等。
				if isDuplicateIndexErr(err) {
					continue
				}
				return fmt.Errorf("orm: AutoMigrate 执行失败 (%s): %w", s, err)
			}
		}
	}
	return nil
}

// metaOfAny 从任意实体（指针或值）解析模型元数据；无法解析时报错。
func metaOfAny(m any) (*modelMeta, error) {
	v := reflect.ValueOf(m)
	for v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return nil, fmt.Errorf("orm: AutoMigrate 收到 nil 指针，无法推导表结构")
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil, fmt.Errorf("orm: AutoMigrate 仅接受结构体或结构体指针，收到 %T", m)
	}
	return getMetaByType(v.Type()), nil
}

// migrateStatements 返回某模型在当前方言下需要执行的 DDL 语句列表（建表 + 二级索引）。
// 抽取为纯函数便于离线测试断言，AutoMigrate 仅负责依次执行。
func migrateStatements(meta *modelMeta, d Dialect, prefix string) []string {
	create, indexes := migratePlan(meta, d, prefix)
	return append([]string{create}, indexes...)
}

// migratePlan 把 DDL 拆成「建表」与「二级索引」两部分返回。
//
// 拆开是为了让 AutoMigrate 能在两者之间插入「补齐缺失列」——索引必须建在列已存在之后。
func migratePlan(meta *modelMeta, d Dialect, prefix string) (string, []string) {
	table := quoteTable(meta.finalTable(prefix), d)
	var cols []string
	var indexes []string
	for i := range meta.fields {
		f := meta.fields[i]
		if f.ignore {
			continue
		}
		def := fmt.Sprintf("%s %s", d.QuoteIdent(f.colName), columnSQL(f, d))
		if f.unique {
			def += " UNIQUE"
		}
		cols = append(cols, "  "+def)
		if f.index {
			indexes = append(indexes, createIndexSQL(d, indexName(prefix, meta, f.colName), table, f.colName))
		}
	}
	// 主键约束：若已在内联列定义（PG SERIAL PRIMARY KEY / MySQL AUTO_INCREMENT PRIMARY KEY /
	// SQLite INTEGER PRIMARY KEY AUTOINCREMENT）则不再追加独立 PRIMARY KEY 子句。
	create := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (\n%s\n)", table, strings.Join(cols, ",\n"))
	return create, indexes
}

// indexName 生成索引名：<前缀><表名>_<列名>，表名里的 "." 换成 "_"（避免跨库名带点）。
func indexName(prefix string, meta *modelMeta, col string) string {
	return fmt.Sprintf("idx_%s_%s", strings.ReplaceAll(meta.finalTable(prefix), ".", "_"), col)
}

// missingFields 返回模型里有、但表里没有的列（列名为空集合时即「全部缺失」）。
//
// 比较一律按小写，且**两侧都归一**：PG 的 information_schema 返回小写列名，
// SQLite 的 PRAGMA 与 MySQL 的 information_schema 则返回定义时的原样大小写；
// 只归一模型侧的话，遇到建表时用了大写列名的库会误判成「整列都缺」，
// 进而在一个已存在的列上再 ADD COLUMN 一次（报错倒是会报，但本不该发生）。
func missingFields(meta *modelMeta, existing map[string]struct{}) []fieldInfo {
	have := make(map[string]struct{}, len(existing))
	for k := range existing {
		have[strings.ToLower(k)] = struct{}{}
	}
	var miss []fieldInfo
	for i := range meta.fields {
		f := meta.fields[i]
		if f.ignore {
			continue
		}
		if _, ok := have[strings.ToLower(f.colName)]; ok {
			continue
		}
		miss = append(miss, f)
	}
	return miss
}

// addColumnStatements 生成「补齐缺失列」的 DDL：每列一条 ALTER TABLE ADD COLUMN，
// 带 `,unique` 的列再补一条 CREATE UNIQUE INDEX（SQLite 的 ADD COLUMN 不接受 UNIQUE 修饰）。
//
// 主键 / 自增列缺失返回错误：在已有表上补主键要给存量行凭空造唯一值，SQLite 更是不支持，
// 框架不该替调用方做这个决定。
func addColumnStatements(meta *modelMeta, d Dialect, prefix string, missing []fieldInfo) ([]string, error) {
	if len(missing) == 0 {
		return nil, nil
	}
	table := quoteTable(meta.finalTable(prefix), d)
	stmts := make([]string, 0, len(missing))
	for i := range missing {
		f := missing[i]
		if f.pk || f.autoInc {
			return nil, fmt.Errorf(
				"orm: 表 %s 已存在但缺少主键/自增列 %q —— 已有表上补主键需回填唯一值，"+
					"SQLite 的 ALTER TABLE ADD COLUMN 更是直接不支持 PRIMARY KEY / AUTOINCREMENT；"+
					"请手工迁移或重建表",
				meta.finalTable(prefix), f.colName)
		}
		stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s",
			table, d.QuoteIdent(f.colName), columnSQL(f, d)))
		if f.unique {
			uidx := "uidx_" + strings.TrimPrefix(indexName(prefix, meta, f.colName), "idx_")
			stmts = append(stmts, createUniqueIndexSQL(d, uidx, table, f.colName))
		}
	}
	return stmts, nil
}

// createIndexSQL 生成二级索引 DDL。
//
// PG 与 SQLite 支持 `CREATE INDEX IF NOT EXISTS`；MySQL（含 8.x）**不支持**该子句
// （那是 MariaDB 的扩展），带上会直接 Error 1064 语法错误 —— 真库实测确认。
// 所以 MySQL 生成朴素 `CREATE INDEX`，幂等性改由 AutoMigrate 忽略「索引已存在」错误来保证
// （见 isDuplicateIndexErr），两者合起来仍是幂等的。
func createIndexSQL(d Dialect, idxName, table, col string) string {
	if dialectKind(d) == "mysql" {
		return fmt.Sprintf("CREATE INDEX %s ON %s (%s)",
			d.QuoteIdent(idxName), table, d.QuoteIdent(col))
	}
	return fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s (%s)",
		d.QuoteIdent(idxName), table, d.QuoteIdent(col))
}

// createUniqueIndexSQL 生成唯一索引 DDL（补齐缺失的 `,unique` 列时使用）。
//
// 为什么不用内联 `ADD COLUMN ... UNIQUE`：SQLite 的 ALTER TABLE ADD COLUMN 明确不接受
// UNIQUE / PRIMARY KEY 修饰，而三方言都支持 CREATE UNIQUE INDEX —— 后者同时能复用
// AutoMigrate 已有的「索引已存在就忽略」幂等机制。
func createUniqueIndexSQL(d Dialect, idxName, table, col string) string {
	if dialectKind(d) == "mysql" {
		return fmt.Sprintf("CREATE UNIQUE INDEX %s ON %s (%s)",
			d.QuoteIdent(idxName), table, d.QuoteIdent(col))
	}
	return fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s (%s)",
		d.QuoteIdent(idxName), table, d.QuoteIdent(col))
}

// tableColumns 读出某表已有的列名集合（已按小写归一）；表不存在时返回空集合。
//
// 返回 nil 表示「该方言无法自省」（自实现方言且未声明 DialectKinder），
// 此时 AutoMigrate 退化为「只建表」，不报错 —— 框架没有该方言系统表的信息，
// 报了也只是把可用的建表路径一并堵死。
//
// 元数据查询走**主库**（queryPrimaryContext）：读写分离下副本的结构可能滞后于主库，
// 在副本上看列会得出与真实结构相反的结论（漏补列 / 重复补列）。
func (db *DB) tableColumns(ctx context.Context, table string) (map[string]struct{}, error) {
	schema, name := splitTableName(table)
	query, ok := tableColumnsSQL(db.dialect, schema, name)
	if !ok {
		return nil, nil
	}
	rows, err := db.queryPrimaryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("orm: 读取表 %q 已有列失败: %w", table, err)
	}
	defer rows.Close()
	cols := make(map[string]struct{})
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, fmt.Errorf("orm: 读取表 %q 列名失败: %w", table, err)
		}
		cols[strings.ToLower(c)] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("orm: 读取表 %q 已有列失败: %w", table, err)
	}
	return cols, nil
}

// tableColumnsSQL 生成「查某表有哪些列」的 SQL，返回单行单列（列名）的结果集。
//
// 三方言各写一份，不做抽象：系统表的形态本就是方言差异最集中的地方
// （information_schema / PRAGMA 表值函数），硬套一层只会把差异藏起来。
// 表名与库名走字面量而非占位符 —— 它们是模型里的标识符（已过 quoteTable 校验），
// 且 SQLite 的 pragma 表值函数对绑定参数的支持各版本不一。
func tableColumnsSQL(d Dialect, schema, table string) (string, bool) {
	kind, known := dialectKindKnown(d)
	if !known {
		return "", false
	}
	switch kind {
	case "postgres":
		// 未指定库/模式时用当前模式：PG 的表建在 search_path 的第一个模式上，
		// 写死 'public' 会漏掉自定义 search_path 的场景。
		owner := "current_schema()"
		if schema != "" {
			owner = sqlLiteral(schema)
		}
		return fmt.Sprintf(
			"SELECT column_name FROM information_schema.columns WHERE table_schema = %s AND table_name = %s",
			owner, sqlLiteral(table)), true
	case "mysql":
		owner := "DATABASE()"
		if schema != "" {
			owner = sqlLiteral(schema)
		}
		return fmt.Sprintf(
			"SELECT column_name FROM information_schema.columns WHERE table_schema = %s AND table_name = %s",
			owner, sqlLiteral(table)), true
	default:
		// SQLite：pragma_table_info 是表值函数（3.16+），第二参数是附加库名（3.20+）。
		// 直接写 `PRAGMA table_info(...)` 会返回 6 列，无法与其他方言共用同一个 Scan。
		if schema != "" {
			return fmt.Sprintf("SELECT name FROM pragma_table_info(%s, %s)", sqlLiteral(table), sqlLiteral(schema)), true
		}
		return fmt.Sprintf("SELECT name FROM pragma_table_info(%s)", sqlLiteral(table)), true
	}
}

// splitTableName 把 "schema.table" 拆成两段；无点号时 schema 为空。
func splitTableName(table string) (string, string) {
	if i := strings.Index(table, "."); i >= 0 {
		return table[:i], table[i+1:]
	}
	return "", table
}

// sqlLiteral 生成单引号字符串字面量（内部单引号按 SQL 标准双写转义）。
func sqlLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// isDuplicateIndexErr 判断错误是否只是「这个名字的索引已经建好了」。
//
// 这里刻意用错误文本匹配而不是驱动错误码：根模块保持零依赖，不能 import 各驱动去
// 断言其错误类型（mysql.MySQLError / pgconn.PgError）。匹配面收窄到两个明确的措辞，
// 避免把「唯一约束冲突」之类的真实错误也吞掉：
//   - MySQL 1061 "Duplicate key name 'idx_...'"
//   - PG 42P07 / SQLite "relation ... already exists" / "... already exists"
//
// 另：调用点只在 AutoMigrate 执行 DDL 时使用；CREATE TABLE 自带 IF NOT EXISTS，
// 不会依赖这条兜底。
func isDuplicateIndexErr(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "duplicate key name") || strings.Contains(s, "already exists")
}

// DialectKinder 是可选接口：方言可以实现它来声明自己属于哪一类
// （"postgres" / "mysql" / "sqlite"），供迁移 DDL 与分页等按类分支的逻辑使用。
//
// 为什么需要它：内置三方言之外的类型以前一律按 "sqlite" 兜底，于是
// 「包装内置方言」（`type my struct{ orm.Dialect }`）或自定义方言会拿到
// `INTEGER PRIMARY KEY AUTOINCREMENT` / `DATETIME`（PG 不认）以及
// `CREATE INDEX IF NOT EXISTS`（MySQL 1064）—— 真库上直接语法错误。
// 实现本接口即可得到正确分类；不实现则维持原有的 SQLite 兜底行为（向后兼容）。
type DialectKinder interface{ DialectKind() string }

// dialectKind 返回方言种类字符串（用于列类型映射与分页等按类分支的逻辑）。
// 无法识别时按 SQLite 兜底（同 dialectKindKnown）。
func dialectKind(d Dialect) string {
	kind, _ := dialectKindKnown(d)
	return kind
}

// dialectKindKnown 在 dialectKind 的基础上多返回一个「是否真的认得这个方言」。
//
// 兜底值与「已知」必须分开：列类型映射可以拿 SQLite 当宽松兜底，
// 但**读系统表**不能 —— 对一个自实现方言发 `pragma_table_info` 只是换来一句语法错误，
// 拿它去猜列是否齐全更是错得离谱。这类调用点必须自己判断 known。
func dialectKindKnown(d Dialect) (string, bool) {
	if k, ok := d.(DialectKinder); ok {
		if s := strings.ToLower(strings.TrimSpace(k.DialectKind())); s != "" {
			return s, true
		}
	}
	switch d.(type) {
	case postgresDialect, *postgresDialect:
		return "postgres", true
	case mysqlDialect, *mysqlDialect:
		return "mysql", true
	case sqliteDialect, *sqliteDialect:
		return "sqlite", true
	default:
		return "sqlite", false // 未知方言按 SQLite 的宽松类型兜底，避免迁移直接失败
	}
}

// columnSQL 根据字段 Go 类型与方言推导列定义（含主键/自增修饰，不含 UNIQUE）。
func columnSQL(f fieldInfo, d Dialect) string {
	kind := dialectKind(d)
	if f.vector {
		dim := f.vectorDim
		if dim == 0 {
			dim = 1536 // 未显式声明维度时的兜底；建议用 ,vector(N) 显式指定
		}
		switch kind {
		case "postgres":
			return fmt.Sprintf("vector(%d)", dim)
		case "mysql":
			return fmt.Sprintf("VECTOR(%d)", dim)
		default:
			return "TEXT" // SQLite 无原生向量类型，存文本；生产建议用 PG/MySQL
		}
	}
	if f.json {
		switch kind {
		case "postgres":
			return "JSONB"
		case "mysql":
			return "JSON"
		default:
			return "TEXT"
		}
	}
	// 主键 + 自增：以内联约束表达，避免重复 PRIMARY KEY
	if f.pk && f.autoInc {
		switch kind {
		case "postgres":
			if isInt64Type(f.typ) {
				return "BIGSERIAL PRIMARY KEY"
			}
			return "SERIAL PRIMARY KEY"
		case "mysql":
			return "BIGINT AUTO_INCREMENT PRIMARY KEY"
		default:
			return "INTEGER PRIMARY KEY AUTOINCREMENT"
		}
	}

	base := baseTypeSQL(f.typ, kind)
	if f.pk {
		return base + " PRIMARY KEY"
	}
	return base
}

// baseTypeSQL 把 Go 类型映射到方言无关的列类型（不含主键/自增修饰）。
func baseTypeSQL(t reflect.Type, kind string) string {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Bool:
		if kind == "mysql" {
			return "TINYINT(1)"
		}
		return "BOOLEAN"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32:
		if kind == "mysql" {
			return "INT"
		}
		return "INTEGER"
	case reflect.Int64, reflect.Uint64:
		if kind == "mysql" {
			return "BIGINT"
		}
		return "BIGINT"
	case reflect.Float32:
		if kind == "mysql" {
			return "FLOAT"
		}
		return "REAL"
	case reflect.Float64:
		if kind == "mysql" {
			return "DOUBLE"
		}
		return "DOUBLE PRECISION"
	case reflect.String:
		if kind == "mysql" {
			return "VARCHAR(255)"
		}
		return "TEXT"
	default:
		// time.Time / []byte / 未知类型：按惯例兜底
		switch {
		case isTimeType(t):
			if kind == "mysql" {
				return "DATETIME"
			}
			if kind == "sqlite" {
				return "DATETIME"
			}
			return "TIMESTAMP"
		case isBytesType(t):
			switch kind {
			case "postgres":
				return "BYTEA"
			case "mysql":
				return "BLOB"
			default:
				return "BLOB"
			}
		default:
			if kind == "mysql" {
				return "TEXT"
			}
			return "TEXT"
		}
	}
}

func isInt64Type(t reflect.Type) bool {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t.Kind() == reflect.Int64 || t.Kind() == reflect.Uint64
}

func isBytesType(t reflect.Type) bool {
	return t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8
}
