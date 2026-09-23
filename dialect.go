package orm

import (
	"fmt"
	"strings"
)

// Dialect 封装不同数据库的标识符引号、占位符与 JSON 表达式差异。
// 新增数据库支持只需实现该接口并在 dialectForDriver 注册。
type Dialect interface {
	QuoteIdent(name string) string
	Placeholder(idx int) string // idx 为 1-based 参数序号
	// JsonPath 返回按路径提取 JSON 列内值的 SQL 表达式（用于比较）。
	// 如 PG 生成 "col"->'a'->>'b'，MySQL/SQLite 生成 JSON_EXTRACT("col", '$.a.b')。
	JsonPath(col, path string) string
	// JsonContains 返回「列 包含 候选值」的 SQL 片段（含占位符 ph）。
	// 如 PG 生成 "col" @> $1::jsonb，MySQL 生成 JSON_CONTAINS("col", ?)。
	JsonContains(col, ph string) string
	// ForUpdateClause 返回悲观锁子句（SELECT 末尾）。PG/MySQL 为 " FOR UPDATE"；
	// SQLite 不支持行级锁（锁在连接/事务级别），返回空串以避免老版本直接报错。
	ForUpdateClause() string
	// VectorDistance 返回向量距离表达式，用于 ORDER BY / 阈值过滤 / 距离投影。
	// col 为已加引号的列名，ph 为占位符（向量参数恒为第一个占位符），m 为距离度量。
	// PG：   "<col>" <=> <ph> / "<col>" <-> <ph> / "<col>" <#> <ph> / "<col>" <+> <ph>
	// MySQL：VECTOR_DISTANCE(`col`, STRING_TO_VECTOR(<ph>), 'COSINE'|'EUCLIDEAN'|'DOT'|'MANHATTAN')
	// SQLite：无原生向量类型，返回与 PG 同形表达式（仅供 SQL 生成，运行时需换 PG/MySQL）。
	VectorDistance(col, ph string, m VectorMetric) string
	// VectorBind 返回「绑定一个文本向量参数」的占位符包裹形式，用于 INSERT/UPDATE 向量列。
	// PG/SQLite 直接返回 ph（驱动/列类型自行解析文本）；MySQL 包裹为 STRING_TO_VECTOR(ph)。
	VectorBind(ph string) string
	// UpsertSuffix 返回 INSERT ... 之后的「冲突处理」后缀片段（upsert 方言差异核心）。
	// conflictCols 为冲突键（通常 PK / 唯一索引列），updateCols 为需要更新的列（已排除冲突键）。
	//   - PG / SQLite： ON CONFLICT (conflict) DO UPDATE SET col = EXCLUDED.col, ...
	//     若 updateCols 为空 → ON CONFLICT (conflict) DO NOTHING（避免 DO UPDATE SET 空列表语法错误）；
	//   - MySQL：      ON DUPLICATE KEY UPDATE col = VALUES(col), ...（忽略 conflictCols，依唯一键自动判定）；
	//     若 updateCols 为空 → ON DUPLICATE KEY UPDATE <conflict[0]> = <conflict[0]>（无操作占位，避免语法错误）。
	UpsertSuffix(conflictCols, updateCols []string) string
	// SupportsLastInsertID 返回驱动是否支持 sql.Result.LastInsertId() 回填自增主键。
	// MySQL / SQLite 支持；PostgreSQL（pgx 经 database/sql）不支持，须改用 INSERT ... RETURNING 子句。
	SupportsLastInsertID() bool
	// InsertReturning 返回 INSERT 用于回填自增主键的 RETURNING 子句（含前导空格）；
	// 仅在不支持 LastInsertId 的方言（PostgreSQL）生效，如 ` RETURNING "id"`；
	// 支持 LastInsertId 的方言（MySQL / SQLite）返回空串（走 LastInsertId 路径）。
	InsertReturning(pkCol string) string
}

// ---- upsert 主键回填的可选方言能力 ----
//
// 下面两个接口**刻意不并入 Dialect**：Dialect 是公开契约，加方法会逼所有自定义方言跟着改。
// 这里用类型断言探测（见 Upsert）：方言不实现时，Upsert 只是不回填自增主键，不会报错，
// 与 BatchInsert / BatchUpsert 的行为一致。

// upsertReturningDialect 由支持 `INSERT ... ON CONFLICT ... RETURNING` 的方言实现。
// PG 与 SQLite 3.35+ 都支持，且 DO UPDATE 命中时返回的是**被更新的那一行**，因此可靠。
type upsertReturningDialect interface {
	UpsertReturning(pkCol string) string
}

// upsertPKCapturingDialect 由「不能 RETURNING，但能让 LAST_INSERT_ID() 带回目标行主键」的方言实现。
// MySQL 需要它：ON DUPLICATE KEY UPDATE 走到更新分支时，LAST_INSERT_ID() 并不指向该行。
type upsertPKCapturingDialect interface {
	UpsertSuffixCapturePK(conflict, update []string, pkCol string) string
}

type postgresDialect struct{}

func (d postgresDialect) UpsertReturning(pkCol string) string {
	return " RETURNING " + d.QuoteIdent(pkCol)
}

func (d postgresDialect) QuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
func (postgresDialect) Placeholder(idx int) string { return "$" + itoa(idx) }
func (d postgresDialect) JsonPath(col, path string) string {
	segs := strings.Split(path, ".")
	var b strings.Builder
	b.WriteString(d.QuoteIdent(col))
	for i, s := range segs {
		if i == len(segs)-1 {
			b.WriteString("->>'")
		} else {
			b.WriteString("->'")
		}
		b.WriteString(strings.ReplaceAll(s, "'", "''"))
		b.WriteString("'")
	}
	return b.String()
}
func (d postgresDialect) JsonContains(col, ph string) string {
	return d.QuoteIdent(col) + " @> " + ph + "::jsonb"
}
func (postgresDialect) ForUpdateClause() string { return " FOR UPDATE" }
func (d postgresDialect) VectorDistance(col, ph string, m VectorMetric) string {
	switch m {
	case Cosine:
		return d.QuoteIdent(col) + " <=> " + ph
	case InnerProduct:
		return d.QuoteIdent(col) + " <#> " + ph
	case L1:
		return d.QuoteIdent(col) + " <+> " + ph
	default: // L2
		return d.QuoteIdent(col) + " <-> " + ph
	}
}
func (postgresDialect) VectorBind(ph string) string { return ph }
func (postgresDialect) SupportsLastInsertID() bool { return false }
func (d postgresDialect) InsertReturning(pkCol string) string {
	return " RETURNING " + d.QuoteIdent(pkCol)
}
func (d postgresDialect) UpsertSuffix(conflict, update []string) string {
	if len(update) == 0 {
		return fmt.Sprintf("ON CONFLICT (%s) DO NOTHING", quoteCols(conflict, d))
	}
	sets := make([]string, len(update))
	for i, c := range update {
		sets[i] = d.QuoteIdent(c) + " = EXCLUDED." + d.QuoteIdent(c)
	}
	return fmt.Sprintf("ON CONFLICT (%s) DO UPDATE SET %s", quoteCols(conflict, d), strings.Join(sets, ", "))
}

type mysqlDialect struct{}

func (d mysqlDialect) QuoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}
func (mysqlDialect) Placeholder(idx int) string { return "?" }
func (d mysqlDialect) JsonPath(col, path string) string {
	p := "$." + strings.Join(strings.Split(path, "."), ".")
	return fmt.Sprintf("JSON_EXTRACT(%s, '%s')", d.QuoteIdent(col), strings.ReplaceAll(p, "'", "''"))
}
func (d mysqlDialect) JsonContains(col, ph string) string {
	return fmt.Sprintf("JSON_CONTAINS(%s, %s)", d.QuoteIdent(col), ph)
}
func (mysqlDialect) ForUpdateClause() string { return " FOR UPDATE" }
func (d mysqlDialect) VectorDistance(col, ph string, m VectorMetric) string {
	var metric string
	switch m {
	case Cosine:
		metric = "COSINE"
	case InnerProduct:
		metric = "DOT"
	case L1:
		metric = "MANHATTAN"
	default: // L2
		metric = "EUCLIDEAN"
	}
	return fmt.Sprintf("VECTOR_DISTANCE(%s, STRING_TO_VECTOR(%s), '%s')", d.QuoteIdent(col), ph, metric)
}
func (mysqlDialect) VectorBind(ph string) string { return "STRING_TO_VECTOR(" + ph + ")" }
func (mysqlDialect) SupportsLastInsertID() bool  { return true }
func (mysqlDialect) InsertReturning(pkCol string) string { return "" }
func (d mysqlDialect) UpsertSuffix(conflict, update []string) string {
	if len(update) == 0 {
		return fmt.Sprintf("ON DUPLICATE KEY UPDATE %s = %s", d.QuoteIdent(conflict[0]), d.QuoteIdent(conflict[0]))
	}
	sets := make([]string, len(update))
	for i, c := range update {
		sets[i] = d.QuoteIdent(c) + " = VALUES(" + d.QuoteIdent(c) + ")"
	}
	return "ON DUPLICATE KEY UPDATE " + strings.Join(sets, ", ")
}

// UpsertSuffixCapturePK 让 upsert 顺带把目标行主键带回 LAST_INSERT_ID()。
// MySQL 官方文档给的写法：在 UPDATE 列表末尾加 `id` = LAST_INSERT_ID(`id`)。
// 之所以需要：ON DUPLICATE KEY UPDATE 走「更新已有行」分支时，LAST_INSERT_ID() 不指向该行；
// 加上这一段后，无论本次是插入还是更新，`res.LastInsertId()` 都返回目标行主键。
// 右值取自当前行（不是 VALUES(id)），所以对数据本身是无操作。
func (d mysqlDialect) UpsertSuffixCapturePK(conflict, update []string, pkCol string) string {
	capture := d.QuoteIdent(pkCol) + " = LAST_INSERT_ID(" + d.QuoteIdent(pkCol) + ")"
	if len(update) == 0 {
		// 原本是 `id` = `id` 那样的无操作占位，这里正好换成主键捕获。
		return "ON DUPLICATE KEY UPDATE " + capture
	}
	return d.UpsertSuffix(conflict, update) + ", " + capture
}

type sqliteDialect struct{}

func (d sqliteDialect) QuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
func (d sqliteDialect) Placeholder(idx int) string { return "?" }
func (d sqliteDialect) JsonPath(col, path string) string {
	p := "$." + strings.Join(strings.Split(path, "."), ".")
	return fmt.Sprintf("json_extract(%s, '%s')", d.QuoteIdent(col), strings.ReplaceAll(p, "'", "''"))
}
func (d sqliteDialect) JsonContains(col, ph string) string {
	// SQLite 没有 json_contains 函数（原实现生成的 json_contains(...) 会直接报
	// "no such function"，真库实测确认）。这里用 json_each 展开成等价语义：
	// 「候选对象的每个键值对，都能在列对象里找到同名、同值、同类型的项」= 子集/包含。
	// 与 PG 的 `@>`、MySQL 的 JSON_CONTAINS(col, candidate) 对齐，空对象恒为子集（恒真）。
	// 注意：只做**浅层**键值包含，不递归比较嵌套对象/数组；需要深层包含请用 PG 的 jsonb 路径。
	q := d.QuoteIdent(col)
	return fmt.Sprintf("NOT EXISTS (SELECT 1 FROM json_each(%s) AS p WHERE NOT EXISTS ("+
		"SELECT 1 FROM json_each(%s) AS c WHERE c.key = p.key AND c.value = p.value AND c.type = p.type))",
		ph, q)
}
func (sqliteDialect) ForUpdateClause() string { return "" }
func (d sqliteDialect) VectorDistance(col, ph string, m VectorMetric) string {
	switch m {
	case Cosine:
		return d.QuoteIdent(col) + " <=> " + ph
	case InnerProduct:
		return d.QuoteIdent(col) + " <#> " + ph
	case L1:
		return d.QuoteIdent(col) + " <+> " + ph
	default: // L2
		return d.QuoteIdent(col) + " <-> " + ph
	}
}
func (sqliteDialect) VectorBind(ph string) string { return ph }
func (sqliteDialect) SupportsLastInsertID() bool  { return true }
func (sqliteDialect) InsertReturning(pkCol string) string { return "" }

// UpsertReturning 见 upsertReturningDialect。SQLite 3.35+ 支持 RETURNING，
// 且 ON CONFLICT DO UPDATE 命中时返回的是被更新的那一行 ——
// 这一点很关键：last_insert_rowid() 在 DO UPDATE 后**不会**更新，
// 所以 SQLite 侧 upsert 的唯一可靠回填方式就是 RETURNING。
func (d sqliteDialect) UpsertReturning(pkCol string) string {
	return " RETURNING " + d.QuoteIdent(pkCol)
}
func (d sqliteDialect) UpsertSuffix(conflict, update []string) string {
	if len(update) == 0 {
		return fmt.Sprintf("ON CONFLICT(%s) DO NOTHING", quoteCols(conflict, d))
	}
	sets := make([]string, len(update))
	for i, c := range update {
		sets[i] = d.QuoteIdent(c) + " = excluded." + d.QuoteIdent(c)
	}
	return fmt.Sprintf("ON CONFLICT(%s) DO UPDATE SET %s", quoteCols(conflict, d), strings.Join(sets, ", "))
}

// 内置方言实例，开箱即用。
var (
	PG     Dialect = postgresDialect{}
	MySQL  Dialect = mysqlDialect{}
	SQLite Dialect = sqliteDialect{}
)

// positionalPlaceholder 报告方言的占位符是否是「位置型」——即同一个占位符在同一 SQL 里
// 出现 N 次，就必须传 N 个参数。
//
// PG 的 $n 是**引用型**：`"col" <-> $1 AS dist` 与 `ORDER BY "col" <-> $1` 两处引用的是
// 同一个参数，只占一个槽位。MySQL / SQLite 的 ? 是**位置型**：服务端按出现次序逐个消耗
// 参数，同一个值要在两处出现就得传两遍，否则 database/sql 直接报
// "sql: expected N arguments, got M"（见 convert.go 的 driverArgsConnLocked）。
//
// 判据取「两个不同序号是否渲染成同一个字符串」，而不是给 Dialect 接口加方法：后者会让
// 外部自实现方言编译不过。这个判据反而自带方言无关性 —— 自实现方言只要用 ? 就会被
// 正确识别为位置型。
func positionalPlaceholder(d Dialect) bool {
	return d.Placeholder(1) == d.Placeholder(2)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
