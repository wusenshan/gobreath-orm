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
