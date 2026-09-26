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

// join 一条联表子句。kind 为 INNER/LEFT/RIGHT；table 为被联接表名（白名单校验）；
// alias 为表别名（可选）；on 为 ON 条件原文——因跨表列无法用 T 的 Col[T] 表示，
// 故 ON 为可信列引用的原文拼接，调用方需自行使用正确的引号（PG 用 "col"，MySQL 用 `col`）。
type join struct {
	kind  string
	table string
	alias string
	on    string
}

// Query[T] 泛型查询构造器，对标 MyBatis-Plus 的 LambdaQueryWrapper。
// 字段通过 Col[T](picker) 选择，条件方法链调用，自动处理占位符与 AND/OR 拼接。
type Query[T any] struct {
	dialect       Dialect
	table         string
	tableExplicit bool   // true 表示 .Table() 显式指定，前缀不再叠加
	prefix        string // 来自 DB 的表前缀（仅在自动推导名上生效）
	selects       []string
	aggFn         string    // 聚合函数名（SUM / AVG / MAX / MIN / COUNT），非空时投影整体替换为 FN(aggCol)
	aggCol        string    // 聚合列名；aggFn 为 COUNT 且此处为空时渲染 FN(*)
	noVecCol      bool      // true 表示投影已完全确定，不要再追加向量距离列（聚合 / Pluck 使用）
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
	distinct      bool   // true 时 SELECT 改为 SELECT DISTINCT
	hasVector     bool
	vecCol        string
	vector        any
	vecFilterOn   bool
	vecFilter     float64
	vectorMetric  VectorMetric   // 向量距离度量，默认 L2
	alias         string         // 主表别名（FROM "users" u），便于在 ON / 条件里引用
	joins         []join         // 联表子句（JOIN ... ON ...）
	sets          map[string]any // 部分更新字段（UpdateSets / UpdatePartial 使用）
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

// Alias 给主表设置别名（FROM "users" u），便于在 JOIN 的 ON 或条件里引用。
// 别名只能由字母/数字/下划线组成，非法值直接 panic（属编程期错误）。
func (q *Query[T]) Alias(alias string) *Query[T] {
	if !identRe.MatchString(alias) {
		panic(fmt.Sprintf("orm: 非法表别名 %q：只能由字母/数字/下划线组成", alias))
	}
	q.alias = alias
	return q
}

// Join 系列：联表查询（对标 SQL 的 JOIN ... ON ...）。表名经白名单校验并引用；
// ON 条件为原文拼接（跨表列无法用 T 的 Col[T] 表示），调用方需自行使用正确引号。
//   - Join / LeftJoin / RightJoin(table, on)：被联接表无别名；
//   - JoinAs / LeftJoinAs / RightJoinAs(table, alias, on)：被联接表带别名（如 "d"）。
//
// 例：
//
//	orm.NewQuery[User]().
//	  LeftJoin("departments", `"users"."dept_id" = "departments"."id"`).
//	  Select("users.name", "departments.dept_name")
func (q *Query[T]) Join(table, on string) *Query[T]      { return q.join("INNER", table, "", on) }
func (q *Query[T]) LeftJoin(table, on string) *Query[T]  { return q.join("LEFT", table, "", on) }
func (q *Query[T]) RightJoin(table, on string) *Query[T] { return q.join("RIGHT", table, "", on) }
func (q *Query[T]) JoinAs(table, alias, on string) *Query[T] {
	return q.join("INNER", table, alias, on)
}
func (q *Query[T]) LeftJoinAs(table, alias, on string) *Query[T] {
	return q.join("LEFT", table, alias, on)
}
func (q *Query[T]) RightJoinAs(table, alias, on string) *Query[T] {
	return q.join("RIGHT", table, alias, on)
}

func (q *Query[T]) join(kind, table, alias, on string) *Query[T] {
	if !identRe.MatchString(table) {
		panic(fmt.Sprintf("orm: 非法表名 %q：表名只能由字母/数字/下划线组成，且不能以数字开头", table))
	}
	if alias != "" && !identRe.MatchString(alias) {
		panic(fmt.Sprintf("orm: 非法表别名 %q：只能由字母/数字/下划线组成", alias))
	}
	if strings.TrimSpace(on) == "" {
		panic("orm: Join 的 ON 条件不能为空")
	}
	q.joins = append(q.joins, join{kind: kind, table: table, alias: alias, on: on})
	return q
}

// Set 为「部分更新」设置单个字段值（与 UpdateSets / UpdatePartial 配合）。
// 同一字段多次 Set 后者覆盖前者；条件（WHERE）仍由 Eq/In 等链式方法提供。
//
// 例：
//
//	orm.UpdateSets(ctx, db, orm.NewQuery[User]().Eq(orm.Col[User](func(u *User) *int64 { return &u.Id }), 1).Set("name", "bob"))
func (q *Query[T]) Set(col ColExpr, val any) *Query[T] {
	if q.sets == nil {
		q.sets = make(map[string]any)
	}
	q.sets[col.name] = val
	return q
}

// Select 指定返回列；不调用则默认 *。向量检索时会自动追加距离列。
//
// 每个参数必须是**单个**列名，可带表别名前缀（如 "u.name"）。不支持把多个列名写在
// 同一个字符串里：每个参数都会作为整体标识符加引号，Select("name, age") 会拼出
// `name, age` 这样一段非法列名 —— 框架自己不报错，只在数据库侧以 unknown column
// 暴露，错误信息也指不回调用处。
// 多列请逐个传入：Select("name", "age")；表达式列请改用原生 SQL（RawQuery）。
func (q *Query[T]) Select(cols ...string) *Query[T] {
	for _, c := range cols {
		if c == "" || strings.ContainsAny(c, ", \t\r\n()") {
			panic(fmt.Sprintf("orm: Select 的每个参数必须是单个列名（可带前缀，如 \"u.name\"），"+
				"不能为空、也不能把多列写在同一个字符串里：收到 %q。"+
				"请改为逐个传入，例如 Select(\"name\", \"age\")", c))
		}
	}
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

func (q *Query[T]) Eq(col ColExpr, val any) *Query[T] { return q.addWhere(col.name, "=", []any{val}) }
func (q *Query[T]) Ne(col ColExpr, val any) *Query[T] { return q.addWhere(col.name, "!=", []any{val}) }
func (q *Query[T]) Gt(col ColExpr, val any) *Query[T] { return q.addWhere(col.name, ">", []any{val}) }
func (q *Query[T]) Ge(col ColExpr, val any) *Query[T] { return q.addWhere(col.name, ">=", []any{val}) }
func (q *Query[T]) Lt(col ColExpr, val any) *Query[T] { return q.addWhere(col.name, "<", []any{val}) }
func (q *Query[T]) Le(col ColExpr, val any) *Query[T] { return q.addWhere(col.name, "<=", []any{val}) }

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

// In 集合匹配。**空切片（或 nil）不是错误输入**：按「空集合的成员判定恒为假」的集合
// 语义折叠为恒假条件（1 = 0），即「没有任何行匹配」—— 这正是动态多选条件「一个都没勾」
// 时期望的结果，且不会生成非法的 `IN ()`（MySQL 1064 / PG 42601 / SQLite 1）。
//
// 注意：折叠后不绑定任何参数。若不带条件地用在 Update / Delete 上，会因「无条件」
// 被直接拒绝（禁止全表更新/删除），而不是把整表数据当成命中集合。
func (q *Query[T]) In(col ColExpr, vals []any) *Query[T] {
	return q.addWhere(col.name, "IN", vals)
}

// NotIn 反向集合匹配。空切片（或 nil）同样不是错误输入：`NOT IN ()` 是非法 SQL，
// 按集合语义折叠为恒真条件（「所有行都匹配」），即整条条件被省略。
func (q *Query[T]) NotIn(col ColExpr, vals []any) *Query[T] {
	return q.addWhere(col.name, "NOT IN", vals)
}
func (q *Query[T]) Between(col ColExpr, lo, hi any) *Query[T] {
	return q.addWhere(col.name, "BETWEEN", []any{lo, hi})
}
func (q *Query[T]) IsNull(col ColExpr) *Query[T]    { return q.addRaw(col.name, "IS NULL") }
func (q *Query[T]) IsNotNull(col ColExpr) *Query[T] { return q.addRaw(col.name, "IS NOT NULL") }

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

// Nearest 向量近邻检索：按默认度量（L2 欧几里得）生成距离排序并 LIMIT k。
// 文本语义相似度检索（如 RAG）建议改用 NearestBy(..., Cosine)。
//
// k 必须为正整数：k <= 0 会被跳过 LIMIT 生成，退化成「全表逐行算距离 + 全量排序」，
// 大表上直接把 CPU 与临时空间打满（而且查询「成功」返回，没有任何提示）。
// 确需无上限的全量距离排序，请写明一个明确上限，或用原生 SQL（RawQuery）表达。
func (q *Query[T]) Nearest(col ColExpr, vec any, k int) *Query[T] {
	if k <= 0 {
		panic(fmt.Sprintf("orm: Nearest/NearestBy 的 k 必须为正整数（收到 %d）：k <= 0 会让查询退化为「无 LIMIT 的全表距离排序」，"+
			"大表上会打满 CPU 与临时空间；请传入明确的上限（如 k = 100）", k))
	}
	q.guardLastPaging("Nearest 的 k")
	q.hasVector = true
	q.vecCol = col.name
	q.vector = vec
	q.limit = k
	q.orders = append(q.orders, order{vec: true, asc: true})
	return q
}

// NearestBy 与 Nearest 相同，但显式指定距离度量（Cosine / L2 / InnerProduct / L1）。
func (q *Query[T]) NearestBy(col ColExpr, vec any, k int, m VectorMetric) *Query[T] {
	q.vectorMetric = m
	return q.Nearest(col, vec, k)
}

// WithVectorMetric 设置后续向量检索（Nearest / WithinDistance）使用的距离度量，默认 L2。
func (q *Query[T]) WithVectorMetric(m VectorMetric) *Query[T] {
	q.vectorMetric = m
	return q
}

// WithinDistance 增加向量距离阈值过滤（默认 L2）：仅返回距离小于 threshold 的行。
// 常与 Nearest 连用，既限制召回范围又按距离排序。
func (q *Query[T]) WithinDistance(col ColExpr, vec any, threshold float64) *Query[T] {
	q.hasVector = true
	q.vecCol = col.name
	q.vector = vec
	q.vecFilterOn = true
	q.vecFilter = threshold
	return q
}

// WithinDistanceBy 与 WithinDistance 相同，但显式指定距离度量。
func (q *Query[T]) WithinDistanceBy(col ColExpr, vec any, threshold float64, m VectorMetric) *Query[T] {
	q.vectorMetric = m
	return q.WithinDistance(col, vec, threshold)
}

// Limit 设置返回行数上限；与 Last 互斥（见 Last 的说明）。
func (q *Query[T]) Limit(n int) *Query[T] {
	q.guardLastPaging("Limit")
	q.limit = n
	return q
}

// Offset 设置跳过行数；与 Last 互斥（见 Last 的说明）。
func (q *Query[T]) Offset(n int) *Query[T] {
	q.guardLastPaging("Offset")
	q.offset = n
	return q
}

// Distinct 让本次查询使用 SELECT DISTINCT 去重（对标 SQL 的 SELECT DISTINCT）。
// 常与 GroupBy / 聚合场景配合；向量检索（hasVector）时仅作用于普通列，距离列 dist 不受影响。
func (q *Query[T]) Distinct() *Query[T] { q.distinct = true; return q }

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
//
// Last 与 Limit / Offset（含 Nearest 的 k）互斥：两者都会生成分页子句，同时使用会得到
// 重复/错序的 SQL（如 "... LIMIT 10 ORDER BY id DESC LIMIT 1"），框架此前照拼不误，
// 只有数据库执行时才报语法错误。需要自定义分页时，把整段分页写进 Last 并不要调用
// Limit / Offset。
func (q *Query[T]) Last(sql string) *Query[T] {
	q.guardLastPaging("Last")
	q.last = sql
	return q
}

// guardLastPaging 校验「自定义尾片段（Last）」不与「框架分页（Limit / Offset / Nearest 的 k）」
// 拼出重复分页子句。参数 other 是触发方名称，用于报错定位。
//
// 只在**尾片段自带分页子句**（LIMIT / OFFSET / FETCH）时才判为冲突：
// 那样会拼出 "SELECT ... LIMIT 10 ORDER BY id DESC LIMIT 1" 这类非法 SQL
// （数据库只会报语法错误，错误信息指向不了调用处）。
// 而「SKIP LOCKED」「NOWAIT」「FOR SHARE」这类与分页无关的尾片段和 Limit 同用是合法且常见的
// 写法（队列式抢锁：`FOR UPDATE SKIP LOCKED LIMIT 1`），不做限制。
// 两种书写顺序都能拦到：先 Last 再 Limit、先 Limit 再 Last。
func (q *Query[T]) guardLastPaging(other string) {
	if q.last == "" || (q.limit <= 0 && q.offset <= 0) {
		return
	}
	if kw := lastPagingClause(q.last); kw != "" {
		panic(fmt.Sprintf("orm: Last 与 %s 不能同时使用：Last 片段里已有 %s 分页子句，"+
			"框架还会再拼一段 LIMIT/OFFSET，最终 SQL 会出现两个分页子句（形如 "+
			"\"... LIMIT 10 ORDER BY id DESC LIMIT 1\"），数据库只会报语法错误、难以定位到调用处。"+
			"请二选一：自定义分页时把整段（含 ORDER BY 与 LIMIT/OFFSET）写进 Last 并不要再调 Limit/Offset；"+
			"或改用框架分页、把 Last 留给 SKIP LOCKED 这类非分页尾子句", other, kw))
	}
}

// lastPagingClause 返回尾片段中出现的第一处分页关键字（LIMIT / OFFSET / FETCH），无则返回空串。
// 按整词匹配（两侧需为非标识符字符），避免把 SKIP LOCKED 之类的片段误判为分页。
func lastPagingClause(sql string) string {
	up := " " + strings.ToUpper(stripLeadingComments(sql)) + " "
	for _, kw := range []string{"LIMIT", "OFFSET", "FETCH"} {
		idx := 0
		for {
			p := strings.Index(up[idx:], kw)
			if p < 0 {
				break
			}
			p += idx
			before, after := up[p-1], up[p+len(kw)]
			if !isIdentChar(before) && !isIdentChar(after) {
				return kw
			}
			idx = p + len(kw)
		}
	}
	return ""
}

// isIdentChar 判断字节是否可出现在 SQL 标识符/数字里（用于整词匹配）。
func isIdentChar(c byte) bool {
	return c == '_' || c == '"' || c == '`' || c == '[' || c == ']' ||
		(c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

// Unscoped 关闭本次查询/删除的逻辑删除自动过滤，用于查询已删除数据或物理删除。
func (q *Query[T]) Unscoped() *Query[T] {
	q.unscoped = true
	return q
}

// applyLogic 返回一个可能追加了「未删除」过滤条件的新查询：模型存在生效的软删除列
// 且未显式 Unscoped 时追加条件，否则原样复制一份。
// 条件作为独立的 AND 组追加，与原条件正确衔接；新查询标记 unscoped 以防重复叠加。
//
// ⚠️ 无论是否真的加了条件，都**必须返回副本**，不能把 q 原样还回去：
// 调用方（SelectList / SelectOne / Count / 聚合 / DryRun）紧接着链的是**就地修改**的
// WithDialect / WithPrefix / Limit。短路径上返回原对象，这些写入就会落到调用方的 Query 上，
// 于是调用方被上一次查询的方言、表前缀乃至 LIMIT 粘住 —— 例如 SelectOne 之后
// 复用同一个 q 查列表，会静默只返回 1 行（不报错）。
// 这个坑只在「无软删除列 / 已 Unscoped」时出现，带软删除列的模型因为本来就返回副本而免疫。
func (q *Query[T]) applyLogic(meta *modelMeta, db *DB) *Query[T] {
	c := *q
	li := resolveLogic(meta, db)
	if li == nil || c.unscoped {
		return &c
	}
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

// resolveVersion 解析模型实际生效的乐观锁版本列，优先级：
//  1. db:"...,version" tag 显式声明（单表级别，不依赖全局配置，总是生效）；
//  2. DB 的约定字段名（Config.OptimisticField）：列名或 Go 字段名与其相等的字段，
//     类型不限（通常为 int），自动启用乐观锁。
//  3. 都不满足 → 返回 nil，即不启用乐观锁。
func resolveVersion(meta *modelMeta, db *DB) *fieldInfo {
	if meta.versionCol != nil {
		return meta.versionCol
	}
	name := db.optimisticField
	if name == "" {
		return nil
	}
	for i := range meta.fields {
		f := &meta.fields[i]
		if f.ignore {
			continue
		}
		if f.colName == name || f.goName == name {
			return f
		}
	}
	return nil
}

// contains 判断字符串切片是否包含目标串。
func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// Build 生成最终 SQL 与参数。向量（若有）通常落在第一个占位符上。
func (q *Query[T]) Build() (string, []any) {
	d := q.dialect
	// 兜底再校验一次 Last 与分页互斥：setter（Limit/Offset/Last/Nearest）已经拦过，
	// 这里覆盖直接改字段的路径（如分页辅助函数），保证任何一条构造路径都不会产出
	// 两段分页子句的非法 SQL。
	q.guardLastPaging("Limit/Offset（含 Nearest 的 k）")
	// 投影已被完全指定时（聚合 SUM/AVG/... 或 Pluck 的单列投影），不要再追加向量距离列：
	// 结果集会从 1 列变成 2 列，Scan 单值直接报 "expected 1 destination arguments in Scan"。
	// 另外聚合时的向量排序也要跳过（见下方 ORDER BY），否则是非法 SQL。
	noVecCol := q.noVecCol
	args := []any{}
	idx := 0
	add := func(v any) int {
		idx++
		args = append(args, v)
		return idx
	}

	// 向量参数「按需」分配 —— 分配参数与写出占位符必须是同一个动作，不能在开头无条件注入。
	//
	// 渲染距离表达式的地方共三处：投影里的 dist 列（受 noVecCol 控制）、距离阈值过滤
	// （受 vecFilterOn 控制）、按距离排序（聚合时整段跳过）。三者都可能同时是关的，
	// 于是 SQL 里一个占位符都没有、args 里却多出一个向量：lib/pq 会直接报
	// "bind message supplies 1 parameters, but prepared statement requires 0"，
	// MySQL / SQLite 的 prepared 路径同样失败。
	//
	// 真实触发场景是 Nearest(...) + Count / Sum —— RAG 里「统计某向量邻域内有多少条」：
	// agg 关掉距离列与距离排序，投影只剩 COUNT(*)，这个向量参数本就不该存在。
	//
	// 同一处还要区分占位符是不是「位置型」：PG 的 $n 可以让 dist 列与按距离排序引用同一个
	// $1，MySQL / SQLite 的 ? 则必须按出现次数各传一个（位置型逐个消耗参数），
	// 否则 database/sql 报 "sql: expected 2 arguments, got 1"。
	reuseVec := !positionalPlaceholder(d)
	vecIdx := 0
	vecPH := func() int {
		if vecIdx != 0 && reuseVec {
			return vecIdx
		}
		vecIdx = add(serializeVector(q.vector))
		return vecIdx
	}

	sel := "*"
	switch {
	case q.aggFn != "":
		// 聚合投影：SUM/AVG/MAX/MIN/COUNT。列名同样走 quoteIdentPath，
		// 与普通 Select 保持一致（支持 "u.name" 这种带别名的列路径）。
		if q.aggCol == "" {
			sel = q.aggFn + "(*)"
		} else {
			sel = q.aggFn + "(" + quoteIdentPath(d, q.aggCol) + ")"
		}
	case len(q.selects) > 0:
		quoted := make([]string, len(q.selects))
		for i, c := range q.selects {
			quoted[i] = quoteIdentPath(d, c)
		}
		sel = strings.Join(quoted, ", ")
	}
	if q.hasVector && !noVecCol {
		dist := fmt.Sprintf("%s AS dist", d.VectorDistance(q.vecCol, d.Placeholder(vecPH()), q.vectorMetric))
		if sel == "*" {
			sel = "*" + ", " + dist
		} else {
			sel = sel + ", " + dist
		}
	}
	from := q.fromClause(d)
	kw := "SELECT"
	if q.distinct {
		kw = "SELECT DISTINCT"
	}
	sql := fmt.Sprintf("%s %s FROM %s", kw, sel, from)

	if w := whereSQL(q.groups, d, add); w != "" {
		sql += " WHERE " + w
	}

	if q.hasVector && q.vecFilterOn {
		clause := fmt.Sprintf("%s < %s", d.VectorDistance(q.vecCol, d.Placeholder(vecPH()), q.vectorMetric), d.Placeholder(add(q.vecFilter)))
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
				// 聚合查询里「按距离排序」是非法的：距离表达式既不在投影里也不在
				// GROUP BY 里，PG 会直接报 "must appear in the GROUP BY clause or
				// be used in an aggregate function"。聚合场景整段跳过。
				if q.aggFn != "" {
					continue
				}
				ords = append(ords, fmt.Sprintf("%s %s", d.VectorDistance(q.vecCol, d.Placeholder(vecPH()), q.vectorMetric), ascDesc(o.asc)))
			} else {
				ords = append(ords, fmt.Sprintf("%s %s", d.QuoteIdent(o.col), ascDesc(o.asc)))
			}
		}
		if len(ords) > 0 {
			sql += " ORDER BY " + strings.Join(ords, ", ")
		}
	} else if q.hasVector && q.aggFn == "" {
		sql += fmt.Sprintf(" ORDER BY %s", d.VectorDistance(q.vecCol, d.Placeholder(vecPH()), q.vectorMetric))
	}

	// 分页：LIMIT 必须写在 OFFSET 之前。「只有 OFFSET 没有 LIMIT」的合法性按方言区分：
	//   - MySQL / SQLite：语法上不允许，必须补一个「不设上限」的 LIMIT
	//     （MySQL 用官方推荐的 18446744073709551615，SQLite 用负数即无上限）
	//   - PostgreSQL：OFFSET 可独立出现，且 PG 拒绝负数 LIMIT（会报
	//     "LIMIT must not be negative"，见 nodeLimit.c recompute_limits），故不补
	// 未知方言按 SQLite 兜底（与 migrate.go 的 dialectKind 策略一致）。
	// Limit(0) 不会被当作「取 0 行」，未设 Limit/Offset 时仍不生成 LIMIT。
	if q.limit > 0 {
		sql += fmt.Sprintf(" LIMIT %d", q.limit)
	} else if q.offset > 0 {
		// 同时覆盖值/指针两种持有方式，避免直传 `*mysqlDialect` 这类指针时漏判。
		switch d.(type) {
		case mysqlDialect, *mysqlDialect:
			sql += " LIMIT 18446744073709551615"
		case postgresDialect, *postgresDialect:
			// PG 原生支持独立 OFFSET，无需补 LIMIT
		default:
			sql += " LIMIT -1"
		}
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

// ToSQL 返回当前查询编译出的 SQL 与参数，不访问数据库，对标 GORM 的 DryRun。
//
// 它只反映查询构造器自身的状态：不会补上 db 级表前缀、方言与软删除条件。
// 要看「这条查询在某个 db 上真正会执行什么」，用 orm.DryRun(db, q)。
func (q *Query[T]) ToSQL() (string, []any) { return q.Build() }

// fromClause 渲染 "表 [别名] [JOIN ...]"，供 Build / Count / 聚合共用。
//
// 历史上 Count 自己手搓了 FROM，把 JOIN 整个漏掉了 —— 带 JOIN 的查询算总数时
// 与 SelectList 的行数口径不一致（分页页码因此对不上）。统一到这里是修那个 bug 的一部分。
func (q *Query[T]) fromClause(d Dialect) string {
	s := quoteTable(q.finalTable(), d)
	if q.alias != "" {
		s += " " + d.QuoteIdent(q.alias)
	}
	for _, j := range q.joins {
		s += fmt.Sprintf(" %s JOIN %s", j.kind, quoteTable(j.table, d))
		if j.alias != "" {
			s += " " + d.QuoteIdent(j.alias)
		}
		s += " ON " + j.on
	}
	return s
}

// agg 返回一个「投影被替换成聚合函数」的新查询（原查询不被修改）。
// LIMIT / OFFSET 在聚合场景下会改变语义（只取部分行再聚合），故一并清空。
func (q *Query[T]) agg(fn, col string) *Query[T] {
	c := *q
	c.aggFn = fn
	c.aggCol = col
	c.noVecCol = true
	c.limit = 0
	c.offset = 0
	return &c
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
				// 空集合是动态条件里最常见的边界（多选条件一个都没勾）：
				// `IN ()` / `NOT IN ()` 是非法 SQL（MySQL 1064 / PG 42601 / SQLite 1），
				// 这里按集合语义折叠成常量条件，且不绑定任何参数：
				//   IN(空)     → 恒假：1 = 0（没有任何行匹配）
				//   NOT IN(空) → 恒真：整条条件省略（其所在 OR 组退化为 TRUE，参与 AND 无影响）
				// 与 Go / SQL 里「空集合的成员判定恒为假」一致，也避免「忘记传条件」
				// 直接抛出一条数据库语法错误。折叠不引用该列，故 jsonPath 分支也无副作用。
				if len(w.vals) == 0 {
					if w.op == "NOT IN" {
						continue // 恒真：不产生任何条件
					}
					parts = append(parts, "1 = 0")
					continue
				}
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
		if len(parts) == 0 {
			// 组内条件全部被折叠掉（如 NotIn(空) 恒真）：整组省略。
			// 语义上等价于 TRUE，参与外层 AND 不改变结果；
			// 必须在这里 continue —— 否则会拼出一个空的 "()"，那才是真正的非法 SQL。
			continue
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

// quoteIdentPath 引用可能带表/别名前缀的列名（如 "u.name" / "d.dept_name"），
// 逐段加引号后用 "." 连接；无前缀时退化为单段 QuoteIdent。用于 JOIN 场景的 SELECT 列。
func quoteIdentPath(d Dialect, name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return d.QuoteIdent(name[:i]) + "." + d.QuoteIdent(name[i+1:])
	}
	return d.QuoteIdent(name)
}
