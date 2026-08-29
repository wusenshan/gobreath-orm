package orm

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// where 一条动态过滤条件。
type where struct {
	col  string
	op   string // = != > >= < <= LIKE IN NOT IN BETWEEN IS NULL IS NOT NULL
	vals []any
	raw  bool // true 时 op 为完整片段（如 "IS NULL"），无占位符
	// JSON 相关：jsonPath 非空表示按路径查询 JSON 列；jsonContains 表示 @>/JSON_CONTAINS 包含查询
	jsonPath     string
	jsonContains bool
}

// order 排序项；vec=true 表示按向量距离排序。
type order struct {
	col string
	asc bool
	vec bool
}

// Query[T] 泛型查询构造器，对标 MyBatis-Plus 的 LambdaQueryWrapper。
// 字段通过 Col[T](picker) 选择，条件方法链调用，自动处理占位符与 AND/OR 拼接。
type Query[T any] struct {
	dialect       Dialect
	table         string
	tableExplicit bool   // true 表示 .Table() 显式指定，前缀不再叠加
	prefix        string // 来自 DB 的表前缀（仅在自动推导名上生效）
	selects       []string
	groups        [][]where // 每组内部 OR 连接，组间 AND 连接
	orMode        bool      // 下一个条件是否与前一个 OR
	orders        []order
	groupBy       []string
	havings       []where
	limit         int
	offset        int
	forUpdate     bool   // 悲观锁：SELECT 末尾追加 FOR UPDATE（SQLite 自动降级为空）
	last          string // 自定义 SQL 结尾（原样拼接，对标 MyBatis-Plus 的 last()）
	unscoped      bool   // true 时关闭逻辑删除自动过滤（Unscoped / 物理删除逃生通道）
	hasVector     bool
	vecCol        string
	vector        any
	vecFilterOn   bool
	vecFilter     float64
}

// NewQuery 创建针对类型 T 对应表的查询构造器。
// 表名优先取 T 的 TableName()（若实现 tableNamer 接口），否则按结构体名自动推导（如 User → users）。
// 如需覆盖（如联表、分表、视图），可链式调用 .Table(name)。
func NewQuery[T any]() *Query[T] {
	meta := getMeta[T]()
	return &Query[T]{dialect: PG, table: meta.table, tableExplicit: meta.explicitTable}
}

// Table 覆盖查询所使用的表名（默认取自 T 的 TableName() / 自动推导）。
// 显式指定的表名视为物理全名，DB 前缀不再叠加（避免 t_t_users 双前缀）。
func (q *Query[T]) Table(name string) *Query[T] {
	q.table = name
	q.tableExplicit = true
	return q
}

// WithPrefix 设置表前缀（通常从 DB 自动带入）。仅在自动推导的表名上生效；
// 若本查询已通过 .Table() 显式指定表名，则前缀被忽略。
func (q *Query[T]) WithPrefix(prefix string) *Query[T] {
	q.prefix = prefix
	return q
}

// finalTable 结合前缀得到最终物理表名。
func (q *Query[T]) finalTable() string {
	return applyPrefix(q.table, q.prefix, q.tableExplicit)
}

// WithDialect 设置方言（Postgres/MySQL/SQLite），影响引号与占位符。
func (q *Query[T]) WithDialect(d Dialect) *Query[T] {
	q.dialect = d
	return q
}

// Select 指定返回列；不调用则默认 *。向量检索时会自动追加距离列。
func (q *Query[T]) Select(cols ...string) *Query[T] {
	q.selects = cols
	return q
}

func (q *Query[T]) addWhere(col, op string, vals []any) *Query[T] {
	return q.appendWhere(where{col: col, op: op, vals: vals})
}

func (q *Query[T]) addRaw(col, frag string) *Query[T] {
	return q.appendWhere(where{col: col, op: frag, raw: true})
}

// appendWhere 把一条条件加入当前条件组（受 Or() 影响：组内 OR、组间 AND）。
func (q *Query[T]) appendWhere(w where) *Query[T] {
	if q.orMode && len(q.groups) > 0 {
		q.groups[len(q.groups)-1] = append(q.groups[len(q.groups)-1], w)
	} else {
		q.groups = append(q.groups, []where{w})
	}
	q.orMode = false
	return q
}

func (q *Query[T]) Eq(col ColExpr, val any) *Query[T]    { return q.addWhere(col.name, "=", []any{val}) }
func (q *Query[T]) Ne(col ColExpr, val any) *Query[T]    { return q.addWhere(col.name, "!=", []any{val}) }
func (q *Query[T]) Gt(col ColExpr, val any) *Query[T]    { return q.addWhere(col.name, ">", []any{val}) }
func (q *Query[T]) Ge(col ColExpr, val any) *Query[T]    { return q.addWhere(col.name, ">=", []any{val}) }
func (q *Query[T]) Lt(col ColExpr, val any) *Query[T]    { return q.addWhere(col.name, "<", []any{val}) }
func (q *Query[T]) Le(col ColExpr, val any) *Query[T]    { return q.addWhere(col.name, "<=", []any{val}) }

// Like 包含匹配（模糊查询），内部自动在两侧加 %，调用方无需自己拼接百分号。
// 等价于 LIKE '%val%'。若 val 本身含 % 或 _，则按 LIKE 通配符规则解释。
func (q *Query[T]) Like(col ColExpr, val string) *Query[T] {
	return q.addWhere(col.name, "LIKE", []any{"%" + val + "%"})
}

// LikeRight 前缀匹配（以 val 开头），内部自动在右侧加 %。等价于 LIKE 'val%'。
func (q *Query[T]) LikeRight(col ColExpr, val string) *Query[T] {
	return q.addWhere(col.name, "LIKE", []any{val + "%"})
}

// LikeLeft 后缀匹配（以 val 结尾），内部自动在左侧加 %。等价于 LIKE '%val'。
func (q *Query[T]) LikeLeft(col ColExpr, val string) *Query[T] {
	return q.addWhere(col.name, "LIKE", []any{"%" + val})
}

// NotLike 反向包含匹配，等价于 NOT LIKE '%val%'。
func (q *Query[T]) NotLike(col ColExpr, val string) *Query[T] {
	return q.addWhere(col.name, "NOT LIKE", []any{"%" + val + "%"})
}

// NotLikeRight 反向前缀匹配，等价于 NOT LIKE 'val%'。
func (q *Query[T]) NotLikeRight(col ColExpr, val string) *Query[T] {
	return q.addWhere(col.name, "NOT LIKE", []any{val + "%"})
}

// NotLikeLeft 反向后缀匹配，等价于 NOT LIKE '%val'。
func (q *Query[T]) NotLikeLeft(col ColExpr, val string) *Query[T] {
	return q.addWhere(col.name, "NOT LIKE", []any{"%" + val})
}
func (q *Query[T]) In(col ColExpr, vals []any) *Query[T] {
	return q.addWhere(col.name, "IN", vals)
}
func (q *Query[T]) NotIn(col ColExpr, vals []any) *Query[T] {
	return q.addWhere(col.name, "NOT IN", vals)
}
func (q *Query[T]) Between(col ColExpr, lo, hi any) *Query[T] {
	return q.addWhere(col.name, "BETWEEN", []any{lo, hi})
}
func (q *Query[T]) IsNull(col ColExpr) *Query[T]     { return q.addRaw(col.name, "IS NULL") }
func (q *Query[T]) IsNotNull(col ColExpr) *Query[T]  { return q.addRaw(col.name, "IS NOT NULL") }

// Json 在 JSON 列上按路径做比较（路径串形如 "a.b.c"，无法用结构体字段 picker 选取，故为字符串）。
// 支持的 op：= != > >= < <= LIKE。渲染按方言展开：
// PG → "col"->'a'->>'b' = $1；MySQL/SQLite → JSON_EXTRACT("col", '$.a.b') = ?。
func (q *Query[T]) Json(col ColExpr, path, op string, val any) *Query[T] {
	return q.appendWhere(where{col: col.name, op: op, vals: []any{val}, jsonPath: path})
}

// JsonContains 查询「JSON 列包含给定 JSON 片段」（最常用的 jsonb 场景）。
// PG → "col" @> $1::jsonb；MySQL → JSON_CONTAINS("col", ?)；SQLite → json_contains("col", ?)。
// val 可为 map/struct/slice，或已序列化的 []byte/string/json.RawMessage。
func (q *Query[T]) JsonContains(col ColExpr, val any) *Query[T] {
	return q.appendWhere(where{col: col.name, vals: []any{toJSONRaw(val)}, jsonContains: true})
}

// toJSONRaw 把任意值规整成可直接作为 JSON 参数的字节（已是字节/字符串则原样使用）。
func toJSONRaw(v any) any {
	switch x := v.(type) {
	case json.RawMessage:
		return x
	case []byte:
		return x
	case string:
		return []byte(x)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			panic(fmt.Sprintf("orm: JsonContains 参数无法序列化: %v", err))
		}
		return b
	}
}

// Or 使下一个条件与前一个用 OR 连接（MyBatis-Plus .or() 语义）。
func (q *Query[T]) Or() *Query[T] {
	q.orMode = true
	return q
}

// If 当 cond 为 true 时才执行 apply，否则整段条件被忽略。
// 等价于 MyBatis-Plus 的三参数条件（如 eq(boolean, R, Object)）语义；
// Go 不支持方法重载，故用「条件块」统一实现，可覆盖任意组合的条件，无需为每个方法各写 twin 版本。
func (q *Query[T]) If(cond bool, apply func(*Query[T])) *Query[T] {
	if cond {
		apply(q)
	}
	return q
}

func (q *Query[T]) OrderBy(col ColExpr, asc bool) *Query[T] {
	q.orders = append(q.orders, order{col: col.name, asc: asc})
	return q
}

func (q *Query[T]) GroupBy(col ColExpr) *Query[T] {
	q.groupBy = append(q.groupBy, col.name)
	return q
}

func (q *Query[T]) Having(col ColExpr, op string, val any) *Query[T] {
	q.havings = append(q.havings, where{col: col.name, op: op, vals: []any{val}})
	return q
}

// Nearest 向量近邻检索：ORDER BY <embedding> <-> $1 LIMIT k（Postgres 语法，其它库需适配）。
func (q *Query[T]) Nearest(col ColExpr, vec any, k int) *Query[T] {
	q.hasVector = true
	q.vecCol = col.name
	q.vector = vec
	q.limit = k
	q.orders = append(q.orders, order{vec: true, asc: true})
	return q
}

// WithinDistance 增加向量距离阈值过滤：<embedding> <-> $1 < threshold。
func (q *Query[T]) WithinDistance(col ColExpr, vec any, threshold float64) *Query[T] {
	q.hasVector = true
	q.vecCol = col.name
	q.vector = vec
	q.vecFilterOn = true
	q.vecFilter = threshold
	return q
}

func (q *Query[T]) Limit(n int) *Query[T]  { q.limit = n; return q }
func (q *Query[T]) Offset(n int) *Query[T] { q.offset = n; return q }

// ForUpdate 在 SELECT 末尾追加悲观行锁（FOR UPDATE），用于「先查后改」防并发覆盖。
// 方言感知：Postgres / MySQL 生成 " FOR UPDATE"；SQLite 无行级锁，自动降级为空串（避免报错）。
// 注意：FOR UPDATE 必须在事务中才真正生效，建议配合 db.Transaction 使用。
func (q *Query[T]) ForUpdate() *Query[T] {
	q.forUpdate = true
	return q
}

// Last 在生成的 SQL 最末尾原样拼接一段自定义片段（对标 MyBatis-Plus 的 last()）。
// 典型用途：方言特有语法（如 "FOR UPDATE SKIP LOCKED"、"OFFSET ... FETCH ..."、
// 窗口函数尾、数据库提示等）。
//
// ⚠️ 安全提示：Last 的内容不经占位符参数化、直接拼接进 SQL，仅可用于可信/静态片段，
// 切勿拼接任何来自用户输入的字符串，否则会造成 SQL 注入。
func (q *Query[T]) Last(sql string) *Query[T] {
	q.last = sql
	return q
}

// Unscoped 关闭本次查询/删除的逻辑删除自动过滤，用于查询已删除数据或物理删除。
func (q *Query[T]) Unscoped() *Query[T] {
	q.unscoped = true
	return q
}

// applyLogic 若模型存在生效的软删除列且未显式 Unscoped，返回一个追加了「未删除」过滤条件的新查询。
// 条件作为独立的 AND 组追加，与原条件正确衔接；新查询标记 unscoped 以防重复叠加。
func (q *Query[T]) applyLogic(meta *modelMeta, db *DB) *Query[T] {
	li := resolveLogic(meta, db)
	if li == nil || q.unscoped {
		return q
	}
	c := *q
	c.groups = append(append([][]where{}, q.groups...), []where{{
		col: li.col, op: li.notDeletedCond(), raw: true,
	}})
	c.unscoped = true
	return &c
}

// logicInfo 解析后实际生效的软删除列信息。
type logicInfo struct {
	col    string
	isTime bool // time.Time/*time.Time：未删除判定 IS NULL，软删写当前时间
	isBool bool // bool：未删除判定 = false，软删写 true
	// 其余（int 系列）：未删除判定 = 0，软删写 1
}

// notDeletedCond 返回「未删除」判定的 SQL 片段（不含列名）。
func (li *logicInfo) notDeletedCond() string {
	if li.isTime {
		return "IS NULL"
	}
	if li.isBool {
		return "= false"
	}
	return "= 0"
}

// deletedValue 返回软删除时写入逻辑列的值。
func (li *logicInfo) deletedValue() any {
	if li.isTime {
		return time.Now()
	}
	if li.isBool {
		return true
	}
	return 1
}

// resolveLogic 解析模型实际生效的软删除列，优先级：
//  1. db:"...,logic" tag 显式声明（单表级别，不依赖全局配置，总是生效）；
//  2. DB 的约定字段名（Config.SoftDeleteField）：列名或 Go 字段名与其相等、
//     且类型为 time/int/bool 的字段自动启用；类型不支持则不启用（保守处理，
//     可用 ,nologic tag 显式退出匹配）；
//  3. 都不满足 → 返回 nil，即物理删除。
func resolveLogic(meta *modelMeta, db *DB) *logicInfo {
	if meta.logicCol != nil {
		return &logicInfo{col: meta.logicCol.colName, isTime: meta.logicIsTime, isBool: isBoolType(meta.logicCol.typ)}
	}
	name := db.softDeleteField
	if name == "" {
		return nil
	}
	for i := range meta.fields {
		f := &meta.fields[i]
		if f.ignore || f.autoInc || f.nologic {
			continue
		}
		if f.colName != name && f.goName != name {
			continue
		}
		switch {
		case isTimeType(f.typ):
			return &logicInfo{col: f.colName, isTime: true}
		case isBoolType(f.typ):
			return &logicInfo{col: f.colName, isBool: true}
		case isIntType(f.typ):
			return &logicInfo{col: f.colName}
		default:
			return nil
		}
	}
	return nil
}

// logicSuffix 返回逻辑删除列的「未删除」判定片段（不含 AND 前缀）。
// time 类型 → "col IS NULL"；bool → "col = false"；int → "col = 0"；
// 无生效逻辑列或已 Unscoped 时返回空串。
func logicSuffix(li *logicInfo, d Dialect, unscoped bool) string {
	if li == nil || unscoped {
		return ""
	}
	return d.QuoteIdent(li.col) + " " + li.notDeletedCond()
}

// Build 生成最终 SQL 与参数。向量（若有）恒为第一个占位符。
func (q *Query[T]) Build() (string, []any) {
	d := q.dialect
	args := []any{}
	idx := 0
	add := func(v any) int {
		idx++
		args = append(args, v)
		return idx
	}

	if q.hasVector {
		add(q.vector)
	}

	sel := "*"
	if len(q.selects) > 0 {
		quoted := make([]string, len(q.selects))
		for i, c := range q.selects {
			quoted[i] = d.QuoteIdent(c)
		}
		sel = strings.Join(quoted, ", ")
	}
	if q.hasVector {
		dist := fmt.Sprintf("%s <-> %s AS dist", d.QuoteIdent(q.vecCol), d.Placeholder(1))
		if sel == "*" {
			sel = dist
		} else {
			sel = sel + ", " + dist
		}
	}
	sql := fmt.Sprintf("SELECT %s FROM %s", sel, quoteTable(q.finalTable(), d))

	if w := whereSQL(q.groups, d, add); w != "" {
		sql += " WHERE " + w
	}

	if q.hasVector && q.vecFilterOn {
		clause := fmt.Sprintf("%s <-> %s < %s", d.QuoteIdent(q.vecCol), d.Placeholder(1), d.Placeholder(add(q.vecFilter)))
		if len(q.groups) > 0 {
			sql += " AND " + clause
		} else {
			sql += " WHERE " + clause
		}
	}

	if len(q.groupBy) > 0 {
		quoted := make([]string, len(q.groupBy))
		for i, c := range q.groupBy {
			quoted[i] = d.QuoteIdent(c)
		}
		sql += " GROUP BY " + strings.Join(quoted, ", ")
	}
	if len(q.havings) > 0 {
		var hs []string
		for _, w := range q.havings {
			n := add(w.vals[0])
			hs = append(hs, fmt.Sprintf("%s %s %s", d.QuoteIdent(w.col), w.op, d.Placeholder(n)))
		}
		sql += " HAVING " + strings.Join(hs, " AND ")
	}

	if len(q.orders) > 0 {
		ords := make([]string, 0, len(q.orders))
		for _, o := range q.orders {
			if o.vec {
				ords = append(ords, fmt.Sprintf("%s <-> %s %s", d.QuoteIdent(q.vecCol), d.Placeholder(1), ascDesc(o.asc)))
			} else {
				ords = append(ords, fmt.Sprintf("%s %s", d.QuoteIdent(o.col), ascDesc(o.asc)))
			}
		}
		sql += " ORDER BY " + strings.Join(ords, ", ")
	} else if q.hasVector {
		sql += fmt.Sprintf(" ORDER BY %s <-> %s", d.QuoteIdent(q.vecCol), d.Placeholder(1))
	}

	if q.limit > 0 {
		sql += fmt.Sprintf(" LIMIT %d", q.limit)
	}
	if q.offset > 0 {
		sql += fmt.Sprintf(" OFFSET %d", q.offset)
	}

	if q.forUpdate {
		sql += d.ForUpdateClause()
	}
	if q.last != "" {
		sql += " " + strings.TrimSpace(q.last)
	}
	return sql, args
}

// whereSQL 把条件组渲染成 WHERE 子句（不含前缀 "WHERE"）。各组 AND，组内 OR。
func whereSQL(groups [][]where, d Dialect, add func(any) int) string {
	if len(groups) == 0 {
		return ""
	}
	var groupStrs []string
	for _, g := range groups {
		var parts []string
		for _, w := range g {
			if w.raw {
				parts = append(parts, fmt.Sprintf("%s %s", d.QuoteIdent(w.col), w.op))
				continue
			}
			if w.jsonContains {
				ph := d.Placeholder(add(w.vals[0]))
				parts = append(parts, d.JsonContains(w.col, ph))
				continue
			}
			colExpr := d.QuoteIdent(w.col)
			if w.jsonPath != "" {
				colExpr = d.JsonPath(w.col, w.jsonPath)
			}
			switch w.op {
			case "IN", "NOT IN":
				phs := make([]string, 0, len(w.vals))
				for _, v := range w.vals {
					phs = append(phs, d.Placeholder(add(v)))
				}
				parts = append(parts, fmt.Sprintf("%s %s (%s)", colExpr, w.op, strings.Join(phs, ", ")))
			case "BETWEEN":
				lo := add(w.vals[0])
				hi := add(w.vals[1])
				parts = append(parts, fmt.Sprintf("%s BETWEEN %s AND %s", colExpr, d.Placeholder(lo), d.Placeholder(hi)))
			default:
				n := add(w.vals[0])
				parts = append(parts, fmt.Sprintf("%s %s %s", colExpr, w.op, d.Placeholder(n)))
			}
		}
		if len(parts) == 1 {
			groupStrs = append(groupStrs, parts[0])
		} else {
			groupStrs = append(groupStrs, "("+strings.Join(parts, " OR ")+")")
		}
	}
	return strings.Join(groupStrs, " AND ")
}

func ascDesc(asc bool) string {
	if asc {
		return "ASC"
	}
	return "DESC"
}

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// quoteTable 校验并引用表名（支持 schema.table 两段，各自校验/转义）。
// 表名无法用占位符绑定，故用白名单 + 引号兜底；非法表名直接 panic（属编程期错误）。
func quoteTable(name string, d Dialect) string {
	parts := strings.SplitN(name, ".", 2)
	for i, p := range parts {
		if !identRe.MatchString(p) {
			panic(fmt.Sprintf("orm: 非法表名 %q：表名只能由字母/数字/下划线组成，且不能以数字开头", name))
		}
		parts[i] = d.QuoteIdent(p)
	}
	return strings.Join(parts, ".")
}
