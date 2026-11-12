package orm

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"strings"
)

// ---- Remove：带行级审计的物理删除 ----
//
// Remove 家族与 ForceDelete 家族的分工：
//   - ForceDelete* 是「盲删」：一条 DELETE 直接下发，框架与调用方都不知道删掉了哪些行，
//     只有 RowsAffected 一个数字；
//   - Remove 家族是「先查后删」：**先 SELECT 把将被删除的行完整捞出来（含已逻辑删除的
//     行，穿透软删），再按主键物理删除**，删除前把行快照交给 RemoveListener 钩子，
//     满足「硬删之前先留档 / 打日志 / 同步到下游」的审计需求。
//
// 代价是每批多一次 SELECT —— 换来的确定性是：**钩子看到的行 = 实际下发的 DELETE
// 主键集合**，不是「猜出来的条件命中行」（条件命中与 DELETE 下发之间行可能变动，
// 按捕获到的主键删除消灭了这个窗口）。

// RemoveEvent 是一次物理删除批次的事件快照，发给所有 RemoveListener。
type RemoveEvent struct {
	Table string // DELETE 的物理表名（含前缀）
	SQL   string // 本批实际下发的 DELETE 语句
	Args  []any  // DELETE 语句的绑定参数
	Count int64  // 本批受影响行数（来自 RowsAffected）
	// Rows 是被删除行的快照，动态类型为 []*T（T 为调用处的实体类型）。
	// 空结果（无行可删）时整个批次不会执行、也不会发事件，因此 Rows 恒非 nil。
	// 断言回具体类型：rows := ev.Rows.([]*orm.User)
	Rows any
	// PKs 是各行主键的归一化字符串（keyOf 口径：数值统一十进制、[]byte 按数字/文本
	// 归一），顺序与 Rows 一致 —— 想打日志不必反射回具体类型，直接用 PKs。
	PKs []string
}

// RemoveListener 在每批物理 DELETE 下发**之后**收到该批的事件快照。
// 多个 listener 按注册顺序依次同步调用；listener 内 panic 会直接向上传播
// （与 Hook 的口径一致，不做静默 recover）。
type RemoveListener func(RemoveEvent)

// removeInChunk 单条 DELETE 携带的主键数量上限。对齐 preloadInChunk 的口径：
// 超过时自动分片执行，避免撞上 SQLite 999 / PG 65535 的绑定参数上限。
// 每片独立发一条 DELETE 与一个 RemoveEvent —— 钩子按批收到行快照，Count 为该批行数。
// 做成 var 是为了测试能在不造 501 行数据的前提下验证分片边界。
var removeInChunk = 500

// WithRemoveListener 返回带额外 Remove 监听器的 DB 副本；仅在显式注册后才生效。
// 也可通过 Config.RemoveListeners 在 Open 时统一配置。
func (db *DB) WithRemoveListener(fns ...RemoveListener) *DB {
	cloned := &DB{
		exec:            db.exec,
		dialect:         db.dialect,
		prefix:          db.prefix,
		logger:          db.logger,
		logLevel:        db.logLevel,
		slowThreshold:   db.slowThreshold,
		hooks:           append([]Hook{}, db.hooks...),
		readWrite:       db.readWrite,
		softDeleteField: db.softDeleteField,
		optimisticField: db.optimisticField,
		removeListeners: append(append([]RemoveListener{}, db.removeListeners...), fns...),
	}
	return cloned
}

// emitRemove 依次同步通知所有 RemoveListener。
func (db *DB) emitRemove(ev RemoveEvent) {
	for _, fn := range db.removeListeners {
		fn(ev)
	}
}

// Remove 按实体主键**物理删除**（穿透逻辑删除列），支持单条与多条：
//
//	n, err := orm.Remove(ctx, db, &u)          // 单个
//	n, err := orm.Remove(ctx, db, &u1, &u2)    // 多个
//
// 流程：先按主键集合 SELECT 把行**完整捞出来**（含已逻辑删除的行）—— 这是钩子能
// 看到「实际删了哪些数据」的来源 —— 再按捕获到的主键分片 DELETE。钩子（RemoveListener，
// 经 db.WithRemoveListener / Config.RemoveListeners 注册）在每批 DELETE 后收到行快照。
//
// 语义要点：
//   - 实体主键为零值直接报错（实体组装错误的典型信号，静默跳过比报错危险）；
//   - 空实体列表返回 ErrInvalidArgument；
//   - 主键非零但记录不存在的实体只是不参与删除（捕获阶段查不到），不算错误；
//   - 先查后删不是原子的：需要「查出来的行一定还在」的强一致请包在
//     db.Transaction 里（两个语句会走同一个事务连接）；
//   - DELETE 语句同时会经过既有的 SQL 日志与 Hook（HookKindExec），行级快照则只走
//     RemoveListener。
func Remove[T any](ctx context.Context, db *DB, entities ...*T) (int64, error) {
	meta := getMeta[T]()
	if meta.pk == nil {
		return 0, fmt.Errorf("orm: %s 无主键，无法 Remove（按主键物理删除需要主键）", meta.table)
	}
	if len(entities) == 0 {
		return 0, fmt.Errorf("orm: Remove: %w：空实体列表（单个实体直接传 &entity，不是 entity）", ErrInvalidArgument)
	}
	pks := make([]any, len(entities))
	for i, e := range entities {
		if e == nil {
			return 0, fmt.Errorf("orm: Remove: 第 %d 个实体为 nil，无法提取主键", i)
		}
		v, err := argFor(meta, reflect.ValueOf(e).Elem(), meta.pk.colName)
		if err != nil {
			return 0, fmt.Errorf("orm: Remove: 第 %d 个实体的主键列 %q 读取失败：%w", i, meta.pk.colName, err)
		}
		if _, ok := keyOf(v); !ok {
			return 0, fmt.Errorf("orm: Remove: 第 %d 个实体的主键为零值（列 %q）—— 通常是实体组装出错（漏扫描 / 拷贝丢主键），按主键删除不静默跳过", i, meta.pk.colName)
		}
		pks[i] = v
	}
	// 捕获阶段不带逻辑删除条件：穿透软删的语义就是「这些行（哪怕已被软删）连同
	// 它们的数据一起物理消失」，钩子看到的快照必须覆盖所有将被 DELETE 的行。
	rows, err := captureRowsByPKs[T](ctx, db, meta, pks)
	if err != nil {
		return 0, err
	}
	return removeByPKs(ctx, db, meta, rows)
}

// RemoveQuery 按查询条件物理删除（穿透逻辑删除列），返回被删除行的快照与行数：
//
//	rows, n, err := orm.RemoveQuery(ctx, db,
//	    orm.NewQuery[Order]().Lt(Col[Order](func(o *Order) *time.Time { return &o.CreatedAt }), cutoff))
//
// 流程与 Remove 一致：先用传入条件 SELECT 捞出行快照（Unscoped 口径，已软删的行
// 一并纳入），再按捕获到的主键分片 DELETE。返回的 rows 即钩子收到的同一批数据。
//
// 语义要点：
//   - 必须有条件（Eq/In/...），禁止无条件全表删除 —— 与 Delete / ForceDelete 同一口径；
//   - 不支持 Limit / Offset：「删前 N 条」这种语义一旦失效就是全表物理删除，必须
//     显式失败而不是静默删多；先圈定范围请把主键查出来再调 Remove；
//   - 捕获 SELECT 需要主键列参与删除：显式 Select(...) 投影时请包含主键列，
//     否则主键为零值时报错；
//   - SELECT 与 DELETE 之间不是原子的，强一致请包 db.Transaction。
func RemoveQuery[T any](ctx context.Context, db *DB, q *Query[T]) ([]*T, int64, error) {
	meta := getMeta[T]()
	if q == nil {
		return nil, 0, fmt.Errorf("orm: RemoveQuery: %w：q 为 nil", ErrInvalidArgument)
	}
	if q.limit > 0 || q.offset > 0 {
		return nil, 0, fmt.Errorf("orm: RemoveQuery 不支持 Limit / Offset：物理删除不支持「只删前 N 条」，" +
			"该意图一旦静默失效就是全表删除。先圈定范围请把主键查出来再调 Remove")
	}
	// 在副本上关掉逻辑删除过滤（穿透软删），不动调用方的 Query。
	c := *q
	c.unscoped = true
	c.dialect = db.dialect
	c.prefix = db.prefix
	if whereSQL(c.groups, db.dialect, func(v any) int { return 0 }) == "" {
		return nil, 0, fmt.Errorf("orm: RemoveQuery 必须有条件，禁止全表物理删除")
	}
	sqlStr, args := c.Build()
	rws, err := db.queryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rws.Close()
	list, err := scanRows[T](rws)
	if err != nil {
		return nil, 0, err
	}
	ptrs := make([]*T, len(list))
	for i := range list {
		ptrs[i] = &list[i]
	}
	n, err := removeByPKs(ctx, db, meta, ptrs)
	return ptrs, n, err
}

// RemoveSQL 按自定义 SELECT 语句物理删除：SELECT 捞出的行会被从 T 的表里按主键删除，
// 并把行快照交给 RemoveListener。返回快照与受影响行数：
//
//	rows, n, err := orm.RemoveSQL[User](ctx, db,
//	    "SELECT * FROM users WHERE last_login < ? AND deleted_at IS NOT NULL", cutoff)
//
// 语义要点：
//   - 只接受 SELECT（或 WITH ... SELECT）：任意 DELETE 请直接用 RawExec —— 那是盲删，
//     拿不到行级快照；RemoveSQL 的价值正是「删之前先查出来」；
//   - SELECT 必须来自 T 的表（首表校验，忽略 schema 限定与引号形态）；CTE / 子查询
//     投影的复杂形态请用 RawQuery 自己取主键再调 Remove；
//   - SELECT 投影必须包含主键列（结果行主键为零值时报错）；
//   - 表名不匹配、主键缺失都会在**任何 DELETE 下发之前**失败。
func RemoveSQL[T any](ctx context.Context, db *DB, query string, args ...any) ([]*T, int64, error) {
	meta := getMeta[T]()
	if meta.pk == nil {
		return nil, 0, fmt.Errorf("orm: %s 无主键，无法 RemoveSQL（按主键物理删除需要主键）", meta.table)
	}
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return nil, 0, fmt.Errorf("orm: RemoveSQL: %w：SQL 为空", ErrInvalidArgument)
	}
	up := strings.ToUpper(trimmed)
	if !strings.HasPrefix(up, "SELECT") && !strings.HasPrefix(up, "WITH") {
		return nil, 0, fmt.Errorf("orm: RemoveSQL 只接受 SELECT 语句（收到 %q 开头）：任意 DELETE 请用 RawExec（盲删，无行级快照）", firstKeyword(trimmed))
	}
	if tbl, ok := tableAfterFrom(trimmed); ok && !tableIs(tbl, meta, db.prefix) {
		return nil, 0, fmt.Errorf("orm: RemoveSQL 的 SELECT 来自 %q，与实体 %q 的表（%q）不一致："+
			"捞出来的主键会被删到 T 的表上，表不匹配时删错表比报错危险得多；CTE / 子查询形态请用 RawQuery 自行取主键再调 Remove", tbl, meta.table, meta.finalTable(db.prefix))
	}
	rws, err := db.queryContext(ctx, trimmed, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rws.Close()
	list, err := scanRows[T](rws)
	if err != nil {
		return nil, 0, err
	}
	ptrs := make([]*T, len(list))
	for i := range list {
		ptrs[i] = &list[i]
	}
	n, err := removeByPKs(ctx, db, meta, ptrs)
	return ptrs, n, err
}

// removeByPKs 把捕获到的行按主键分片物理删除，并在每批 DELETE 后发 RemoveEvent。
// rows 为空时零语句、零事件、返回 0。
func removeByPKs[T any](ctx context.Context, db *DB, meta *modelMeta, rows []*T) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	d := db.dialect
	table := meta.finalTable(db.prefix)
	keys := make([]string, len(rows))
	vals := make([]any, len(rows))
	for i, e := range rows {
		v, err := argFor(meta, reflect.ValueOf(e).Elem(), meta.pk.colName)
		if err != nil {
			return 0, fmt.Errorf("orm: Remove: 第 %d 行的主键列 %q 读取失败：%w（SELECT 投影需包含主键列）", i, meta.pk.colName, err)
		}
		k, ok := keyOf(v)
		if !ok {
			return 0, fmt.Errorf("orm: Remove: 第 %d 行的主键为零值（列 %q）—— SELECT 投影必须包含主键列，按主键删除不静默跳过", i, meta.pk.colName)
		}
		keys[i] = k
		vals[i] = v
	}
	var total int64
	for start := 0; start < len(rows); start += removeInChunk {
		end := start + removeInChunk
		if end > len(rows) {
			end = len(rows)
		}
		inPh, args := idsIN(d, 1, vals[start:end])
		sqlStr := fmt.Sprintf("DELETE FROM %s WHERE %s IN (%s)",
			quoteTable(table, d), d.QuoteIdent(meta.pk.colName), inPh)
		res, err := db.execContext(ctx, sqlStr, args...)
		if err != nil {
			return total, fmt.Errorf("orm: Remove 第 %d~%d 行删除失败：%w", start, end-1, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, fmt.Errorf("orm: Remove 第 %d~%d 行读取受影响行数失败：%w", start, end-1, err)
		}
		total += n
		chunkPKs := make([]string, end-start)
		copy(chunkPKs, keys[start:end])
		db.emitRemove(RemoveEvent{
			Table: table,
			SQL:   sqlStr,
			Args:  append([]any{}, args...),
			Count: n,
			Rows:  rows[start:end],
			PKs:   chunkPKs,
		})
	}
	return total, nil
}

// captureRowsByPKs 按主键集合捞行快照（SELECT *，不带逻辑删除条件 —— 穿透软删）。
func captureRowsByPKs[T any](ctx context.Context, db *DB, meta *modelMeta, pks []any) ([]*T, error) {
	d := db.dialect
	inPh, args := idsIN(d, 1, pks)
	sqlStr := fmt.Sprintf("SELECT * FROM %s WHERE %s IN (%s)",
		quoteTable(meta.finalTable(db.prefix), d), d.QuoteIdent(meta.pk.colName), inPh)
	rws, err := db.queryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rws.Close()
	list, err := scanRows[T](rws)
	if err != nil {
		return nil, err
	}
	ptrs := make([]*T, len(list))
	for i := range list {
		ptrs[i] = &list[i]
	}
	return ptrs, nil
}

// firstKeyword 返回 SQL 首个单词（用于错误文案）。
func firstKeyword(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' || s[i] == '\r' || s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}

// tableAfterFrom 尽力提取 SELECT 的首表名（引号/schema 限定/别名都能剥掉）。
// 首表是子查询括号、或解析不了时返回 ok=false —— 校验是防线不是解析器，剥不出来
// 就不拦（拦错了正常用法比漏拦 exotic 用法代价大）。
func tableAfterFrom(sqlStr string) (string, bool) {
	up := strings.ToUpper(sqlStr)
	fromIdx := -1
	for i := 0; i+4 <= len(up); i++ {
		if up[i:i+4] != "FROM" {
			continue
		}
		if (i == 0 || !isIdentChar(sqlStr[i-1])) && (i+4 >= len(sqlStr) || !isIdentChar(sqlStr[i+4])) {
			fromIdx = i + 4
			break
		}
	}
	if fromIdx < 0 {
		return "", false
	}
	rest := strings.TrimLeft(sqlStr[fromIdx:], " \t\r\n")
	if rest == "" {
		return "", false
	}
	// 逐段读标识符（引号段 / 裸段都行），允许 . 连接的 schema 限定；
	// 返回**最后一段**（真正的表名）。首段是括号（子查询）时剥不出，放弃校验。
	if rest[0] == '(' {
		return "", false
	}
	var parts []string
	i := 0
	for {
		if i >= len(rest) {
			break
		}
		switch rest[i] {
		case '"', '`', '[':
			qch := byte('"')
			if rest[i] == '`' {
				qch = '`'
			} else if rest[i] == '[' {
				qch = ']'
			}
			end := strings.IndexByte(rest[i+1:], qch)
			if end < 0 {
				return "", false
			}
			parts = append(parts, rest[i+1:i+1+end])
			i = i + 1 + end + 1
		default:
			j := i
			for j < len(rest) && isIdentChar(rest[j]) {
				j++
			}
			if j == i {
				return "", false
			}
			parts = append(parts, rest[i:j])
			i = j
		}
		if i >= len(rest) || rest[i] != '.' {
			break
		}
		i++ // 吃掉 '.'，继续读下一段
	}
	if len(parts) == 0 {
		return "", false
	}
	return parts[len(parts)-1], true
}

// tableIs 判断提取出的表名是否指向 T 的表（剥 schema 限定、大小写不敏感、
// 同时接受带前缀与不带前缀两种写法）。
func tableIs(name string, meta *modelMeta, prefix string) bool {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	lower := strings.ToLower(name)
	return lower == strings.ToLower(meta.table) || lower == strings.ToLower(meta.finalTable(prefix))
}

// RemoveAuditLogger 是开箱即用的 RemoveListener：把每批删除的表、行数、主键与
// DELETE 语句打到 w。接自己的日志体系请直接写 RemoveListener（事件里有完整快照）。
func RemoveAuditLogger(w io.Writer) RemoveListener {
	if w == nil {
		w = io.Discard
	}
	return func(ev RemoveEvent) {
		fmt.Fprintf(w, "[orm] remove %s: %d rows, pks=%v, sql=%s\n", ev.Table, ev.Count, ev.PKs, ev.SQL)
	}
}
