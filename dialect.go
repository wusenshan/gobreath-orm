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
}

type postgresDialect struct{}

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
	return fmt.Sprintf("json_contains(%s, %s)", d.QuoteIdent(col), ph)
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
