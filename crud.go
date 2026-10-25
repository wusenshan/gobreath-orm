package orm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// ---- 写入选项 ----

// WriteOption 控制写入（Insert / Update 系列）行为，按需以变参形式传入。
type WriteOption func(*writeConfig)

// writeConfig 是写入选项的累加结果。
type writeConfig struct {
	omitZero bool
	only     []string // OnlyColumns 指定的列白名单
	onlySet  bool     // 是否调用过 OnlyColumns（用于区分「未指定」与「显式指定为空」）
}

// OmitZero 使 Insert / Update 跳过「值为类型零值」的可写列（主键列除外），
// 让数据库列的默认值或 NULL 生效。适用于「未显式设置的字段不覆盖已有值」的场景。
//
// 行为要点：
//   - 字面 0 / "" / false / 零时间 在启用 OmitZero 时会被跳过；
//   - 若需显式写入这些零值，请用指针字段（nil 即 NULL，非 nil 即值），或不要启用本选项；
//   - 指针 / 接口 / 切片 / 映射 字段永不被 OmitZero 跳过——它们的 NULL 语义由 driver 依据指针是否为 nil 决定。
func OmitZero() WriteOption {
	return func(c *writeConfig) { c.omitZero = true }
}

// OnlyColumns 把本次写入限定在给定列上，其余列不进入 SQL。
//
// 主要用途是 UpdateById / Update：它们默认写入**全部**可写列，实体上未赋值的字段
// （典型是 time.Time 的零值）会被一并写回，把库里已有的值抹成零值。用 OnlyColumns
// 显式声明本次要更新的列即可避免。
//
// 与 OmitZero 的区别：OmitZero 按「值是否为零值」动态跳过，OnlyColumns 是静态白名单；
// 两者可叠加（先取白名单，再按零值过滤）。列名必须是模型的真实列名 ——
// 拼错或指向主键 / 自增 / 逻辑删除列时直接报错，不做静默忽略（见 filterOnlyCols）。
func OnlyColumns(cols ...string) WriteOption {
	return func(c *writeConfig) {
		c.only = cols
		c.onlySet = true
	}
}

func applyWriteOptions(opts ...WriteOption) writeConfig {
	var c writeConfig
	for _, o := range opts {
		if o != nil {
			o(&c)
		}
	}
	return c
}

// shouldOmitZero 判断某字段在 OmitZero 下是否应被跳过。
// 指针/接口/切片/映射类型永不被跳过（nil 由 driver 写入 NULL，非 nil 正常写入）；
// 其余值类型在其为零值时跳过。
func shouldOmitZero(fv reflect.Value) bool {
	if !fv.IsValid() {
		return true
	}
	switch fv.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice:
		return false
	}
	return fv.IsZero()
}

// filterOmitZeroCols 应用 OmitZero：从可写列中剔除零值非指针列（主键列永不被剔除）。
func filterOmitZeroCols(meta *modelMeta, ev reflect.Value, cols []string) []string {
	out := make([]string, 0, len(cols))
	for _, c := range cols {
		if fi := fieldInfoForCol(meta, c); fi != nil && fi.pk {
			out = append(out, c)
			continue
		}
		if shouldOmitZero(fieldByCol(ev, meta, c)) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// filterOmitZeroColsBatch 是 OmitZero 在**批量写入**下的正确语义：取各行「保留列」的并集，
// 等价于「只有当所有行该列都是零值时才跳过该列」。
//
// 单行写入按本行零值动态剔除列是合理的；但批量写入的多行 VALUES 要求每行列数一致，
// 此前实现用 entities[0] 一行的零值情况决定整批的列集合 —— 只要后续行有任何非零字段
// 恰好落在「首行的零值列」里，该字段就被静默丢弃：SQL 里根本没有这一列、不报任何错，
// 调用方以为已经写进去了。
//
// 这里按并集取列：任何一行非零的列都保留（不丢数据），只有**所有行**都为零值的列
// 才跳过（保留 OmitZero「跳过没用的列」的本意）。注意不能用交集 —— 那会让首行零值
// 的列被整批剔除，正是要修的那个 bug。
//
// 主键列由 filterOmitZeroCols 保证永不被剔除，故并集必然保留主键列。
func filterOmitZeroColsBatch[T any](meta *modelMeta, entities []T, cols []string) []string {
	keep := make(map[string]bool, len(cols))
	for i := range entities {
		ev := reflect.ValueOf(&entities[i]).Elem()
		for _, c := range filterOmitZeroCols(meta, ev, cols) {
			keep[c] = true
		}
	}
	out := make([]string, 0, len(cols))
	for _, c := range cols {
		if keep[c] {
			out = append(out, c)
		}
	}
	return out
}

// filterOnlyCols 应用 OnlyColumns 白名单；未指定该选项时原样返回 cols。
//
// 白名单里出现「存在于模型但不属于本次可写列」的列（主键 / 自增 / 逻辑删除 / 被忽略）
// 时报错，而不是静默丢弃：静默丢弃会让「我以为更新了 name，其实 SQL 里根本没有它」
// 这类误解一直藏着。这与 OmitZero 的取舍相反 —— 那里的零值跳过是调用方明确预期的行为。
func filterOnlyCols(meta *modelMeta, cols, only []string) ([]string, error) {
	writable := make(map[string]bool, len(cols))
	for _, c := range cols {
		writable[c] = true
	}
	out := make([]string, 0, len(only))
	seen := make(map[string]bool, len(only))
	for _, c := range only {
		if seen[c] {
			continue // 重复项去重（无害，不报错）
		}
		seen[c] = true
		if !writable[c] {
			return nil, fmt.Errorf("orm: %s 的列 %q 不能用于 OnlyColumns%s", meta.table, c, whyNotWritable(meta, c))
		}
		out = append(out, c)
	}
	return out, nil
}

// whyNotWritable 为「列不该出现在写入白名单里」给出具体原因，便于调用方定位。
func whyNotWritable(meta *modelMeta, col string) string {
	switch fi := fieldInfoForCol(meta, col); {
	case fi == nil:
		return "（模型中不存在这一列，请检查拼写）"
	case fi.ignore:
		return "（该字段的 db tag 为 \"-\"，不映射任何列）"
	case fi.pk && fi.autoInc:
		return "（自增主键由数据库发号，不参与写入）"
	case fi.pk:
		return "（主键列是 WHERE 条件，不参与写入）"
	case fi.autoInc:
		return "（自增列由数据库赋值，不由调用方指定）"
	case fi.logic:
		return "（逻辑删除列请用 DeleteById / Delete）"
	}
	return ""
}

// emptyColsHint 解释「筛完之后一列都不剩」的原因。
//
// 不能一律归咎于 OmitZero：OnlyColumns() 传空、或白名单与模型不匹配时同样会走到这里，
// 此时提示「OmitZero 跳过全部零值列」会把人引向完全无关的排查方向。
func emptyColsHint(cfg writeConfig) string {
	switch {
	case cfg.onlySet && cfg.omitZero:
		return "（OnlyColumns 白名单为空，或 OmitZero 跳过了其中全部零值列）"
	case cfg.onlySet:
		return "（OnlyColumns 白名单为空）"
	case cfg.omitZero:
		return "（OmitZero 跳过了全部零值列）"
	}
	return ""
}

// Ptr 返回 v 的指针，便于把可空列声明为指针类型并安全赋值：
//
//	Score *int `db:"score"`
//	e := Product{ Score: orm.Ptr(0) } // 显式存 0；不赋值则为 nil → NULL
func Ptr[T any](v T) *T { return &v }

// ---- 插入 ----

// Insert 插入单条记录，并回填自增主键（若字段标记为 autoincrement）。
func Insert[T any](ctx context.Context, db *DB, entity *T, opts ...WriteOption) error {
	meta := getMeta[T]()
	cfg := applyWriteOptions(opts...)
	ev := reflect.ValueOf(entity).Elem()
	cols := writableCols(meta)
	if cfg.onlySet {
		filtered, err := filterOnlyCols(meta, cols, cfg.only)
		if err != nil {
			return err
		}
		cols = filtered
	}
	if cfg.omitZero {
		cols = filterOmitZeroCols(meta, ev, cols)
	}
	if len(cols) == 0 {
		return fmt.Errorf("orm: %s 无可写字段%s", meta.table, emptyColsHint(cfg))
	}
	phIdx := 0
	nextPh := func() string { phIdx++; return db.dialect.Placeholder(phIdx) }
	phs := make([]string, 0, len(cols))
	args := make([]any, 0, len(cols))
	for _, c := range cols {
		v, err := argFor(meta, ev, c)
		if err != nil {
			return err
		}
		args = append(args, v)
		ph := nextPh()
		if fi := fieldInfoForCol(meta, c); fi != nil && fi.vector {
			ph = db.dialect.VectorBind(ph)
		}
		phs = append(phs, ph)
	}
	sqlStr := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		quoteTable(meta.finalTable(db.prefix), db.dialect), quoteCols(cols, db.dialect), strings.Join(phs, ", "))

	// 自增主键回填：PostgreSQL（pgx 经 database/sql）不支持 sql.Result.LastInsertId()，
	// 会返回 error，故改用 INSERT ... RETURNING "id" + 扫描单行回填；MySQL / SQLite
	// 走标准 LastInsertId() 路径。
	if meta.pk != nil && meta.pk.autoInc && !db.dialect.SupportsLastInsertID() {
		sqlStr += db.dialect.InsertReturning(meta.pk.colName)
		var id int64
		if err := db.queryRowContext(ctx, sqlStr, args...).Scan(&id); err != nil {
			return err
		}
		return setFieldValue(fieldByCol(ev, meta, meta.pk.colName), id)
	}

	res, err := db.execContext(ctx, sqlStr, args...)
	if err != nil {
		return err
	}
	if meta.pk != nil && meta.pk.autoInc {
		if id, err := res.LastInsertId(); err == nil {
			if err := setFieldValue(fieldByCol(ev, meta, meta.pk.colName), id); err != nil {
				return err
			}
		}
	}
	return nil
}

// BatchInsert 批量插入切片实体。
//
// 行数 × 列数超过方言的参数上限时会自动分批执行（每批一条 INSERT）。
// 分批只在单条语句层面保证原子性，**跨批不保证**：需要「要么全成功要么全不写入」
// 时请自己开事务（db.Transaction）—— 在事务里调用，多批就都在同一事务内。
func BatchInsert[T any](ctx context.Context, db *DB, entities []T, opts ...WriteOption) error {
	n := len(entities)
	if n == 0 {
		return nil
	}
	meta := getMeta[T]()
	cfg := applyWriteOptions(opts...)
	cols := writableCols(meta)
	if cfg.onlySet {
		filtered, err := filterOnlyCols(meta, cols, cfg.only)
		if err != nil {
			return err
		}
		cols = filtered
	}
	if cfg.omitZero {
		cols = filterOmitZeroColsBatch(meta, entities, cols)
	}
	if len(cols) == 0 {
		return fmt.Errorf("orm: %s 无可写字段%s", meta.table, emptyColsHint(cfg))
	}
	buildBatch := func(start, end int) (string, []any, error) {
		phIdx := 0
		nextPh := func() string { phIdx++; return db.dialect.Placeholder(phIdx) }
		args := make([]any, 0, (end-start)*len(cols))
		valueRows := make([]string, 0, end-start)
		for i := start; i < end; i++ {
			ev := reflect.ValueOf(&entities[i]).Elem()
			phs := make([]string, 0, len(cols))
			for _, c := range cols {
				v, err := argFor(meta, ev, c)
				if err != nil {
					return "", nil, err
				}
				args = append(args, v)
				ph := nextPh()
				if fi := fieldInfoForCol(meta, c); fi != nil && fi.vector {
					ph = db.dialect.VectorBind(ph)
				}
				phs = append(phs, ph)
			}
			valueRows = append(valueRows, "("+strings.Join(phs, ", ")+")")
		}
		sqlStr := fmt.Sprintf("INSERT INTO %s (%s) VALUES %s",
			quoteTable(meta.finalTable(db.prefix), db.dialect), quoteCols(cols, db.dialect), strings.Join(valueRows, ", "))
		return sqlStr, args, nil
	}
	// 一条语句塞下全部行会撞方言的参数上限（SQLite 32766 变量 / PG·MySQL 65535），
	// 行数一多就是一条硬报错（too many SQL variables 等）。按列数自动分批。
	per := batchRowsPerStmt(db.dialect, len(cols))
	for start := 0; start < n; start += per {
		end := start + per
		if end > n {
			end = n
		}
		sqlStr, args, err := buildBatch(start, end)
		if err != nil {
			return err
		}
		if _, err := db.execContext(ctx, sqlStr, args...); err != nil {
			return err
		}
	}
	return nil
}

// batchRowsPerStmt 按「方言参数上限 ÷ 每行列数」算出单条语句最多能带多少行
// （少于 1 行时按 1 行一批，保证至少能推进）。
//
// 上限是三方实测值（见 integration 的批量用例）：SQLite 32766 个变量、
// PostgreSQL 65535 个参数、MySQL 65535 个占位符。留 8 个余量，避免与驱动
// 自己追加的参数相撞。SQL 文本长度（MySQL 的 max_allowed_packet）不在考虑范围内 ——
// 那取决于每行数据的大小，只能由调用方按 chunk 控制。
func batchRowsPerStmt(d Dialect, cols int) int {
	if cols <= 0 {
		return 1
	}
	limit := 32766
	switch dialectKind(d) {
	case "postgres", "mysql":
		limit = 65535
	}
	n := (limit - 8) / cols
	if n < 1 {
		n = 1
	}
	return n
}

// bindVal 返回部分更新 / map 更新时字段应绑定的参数值；向量列序列化为文本 [..]。
// bindVal 返回部分更新 / map 更新时字段应绑定的参数值：
// JSON 列先 marshal 成 []byte（与实体路径的 argFor 同口径，否则 map[string]any
// 原样交给驱动会报 unsupported type），向量列序列化为文本 [..]。
func bindVal(meta *modelMeta, col string, val any) (any, error) {
	fi := fieldInfoForCol(meta, col)
	if fi != nil && fi.json {
		return json.Marshal(val)
	}
	if fi != nil && fi.vector {
		return serializeVector(val), nil
	}
	return val, nil
}

// ---- Upsert（插入或更新，方言分发）----

// Upsert 插入单条记录；若发生冲突键（默认主键，可经 conflictCols 覆盖）已存在，则更新其余列。
// 方言差异由 Dialect.UpsertSuffix 处理：
//   - Postgres / SQLite：INSERT ... ON CONFLICT (key) DO UPDATE SET col = EXCLUDED.col
//   - MySQL：           INSERT ... ON DUPLICATE KEY UPDATE col = VALUES(col)
//
// 冲突键需对应表中的主键或唯一索引，否则数据库会报约束错误。更新列 = 全部可写列减去冲突键；
// 若无可更新列（仅冲突键一列），退化为 DO NOTHING（PG/SQLite）或等价无操作（MySQL）。
//
// conflictCols 必须是模型中的真实列名（空串 / 重复 / 未映射列一律返回
// ErrInvalidArgument）。这条校验是必要的跨方言兜底：**MySQL 的
// ON DUPLICATE KEY UPDATE 完全忽略冲突键**，只按表上存在的唯一索引判定 ——
// 在 MySQL 上把冲突键拼错不会报任何错，只会静默按另一个唯一键插入或更新；
// PG/SQLite 则会原样拼进 ON CONFLICT，交给数据库报错。前置校验让两种方言的行为一致。
// 反过来说：**MySQL 上「冲突键写对了」也不代表生效**，真正的判定依据是表上的唯一索引。
func Upsert[T any](ctx context.Context, db *DB, entity *T, conflictCols []string, opts ...WriteOption) error {
	meta := getMeta[T]()
	cfg := applyWriteOptions(opts...)
	ev := reflect.ValueOf(entity).Elem()
	cols := writableCols(meta)
	if cfg.onlySet {
		filtered, err := filterOnlyCols(meta, cols, cfg.only)
		if err != nil {
			return err
		}
		cols = filtered
	}
	if cfg.omitZero {
		cols = filterOmitZeroCols(meta, ev, cols)
	}
	if len(cols) == 0 {
		return fmt.Errorf("orm: %s 无可写字段%s", meta.table, emptyColsHint(cfg))
	}
	cc := conflictCols
	if len(cc) == 0 {
		if meta.pk == nil {
			return fmt.Errorf("orm: %s 无主键且未指定冲突键，无法 Upsert", meta.table)
		}
		cc = []string{meta.pk.colName}
	}
	if err := checkConflictCols(meta, cc, "Upsert"); err != nil {
		return err
	}
	cols = upsertConflictCols(meta, ev, cols, cc)
	updateCols := make([]string, 0, len(cols))
	for _, c := range cols {
		if !contains(cc, c) {
			updateCols = append(updateCols, c)
		}
	}
	phIdx := 0
	nextPh := func() string { phIdx++; return db.dialect.Placeholder(phIdx) }
	phs := make([]string, 0, len(cols))
	args := make([]any, 0, len(cols))
	for _, c := range cols {
		v, err := argFor(meta, ev, c)
		if err != nil {
			return err
		}
		args = append(args, v)
		ph := nextPh()
		if fi := fieldInfoForCol(meta, c); fi != nil && fi.vector {
			ph = db.dialect.VectorBind(ph)
		}
		phs = append(phs, ph)
	}
	needPK := upsertNeedsPKBackfill(meta, ev)
	suffix := db.dialect.UpsertSuffix(cc, updateCols)
	if needPK {
		// 方言变体：让 LAST_INSERT_ID() 带回命中行的主键（MySQL）。
		if d, ok := db.dialect.(upsertPKCapturingDialect); ok {
			suffix = d.UpsertSuffixCapturePK(cc, updateCols, meta.pk.colName)
		}
	}
	sqlStr := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) %s",
		quoteTable(meta.finalTable(db.prefix), db.dialect), quoteCols(cols, db.dialect),
		strings.Join(phs, ", "), suffix)
	if needPK {
		// RETURNING 路径：PG 与 SQLite 3.35+ 在 DO UPDATE 命中时返回的也是目标行，可靠。
		if d, ok := db.dialect.(upsertReturningDialect); ok {
			var id int64
			if err := db.queryRowContext(ctx, sqlStr+d.UpsertReturning(meta.pk.colName), args...).Scan(&id); err != nil {
				return err
			}
			if err := setFieldValue(fieldByCol(ev, meta, meta.pk.colName), id); err != nil {
				return err
			}
			return nil
		}
	}
	res, err := db.execContext(ctx, sqlStr, args...)
	if err != nil {
		return err
	}
	// 自增主键回填，与 Insert 对齐：只回填「数据库真正发号」的情形（id > 0 才算数）。
	if needPK {
		if id, err := res.LastInsertId(); err == nil && id > 0 {
			if err := setFieldValue(fieldByCol(ev, meta, meta.pk.colName), id); err != nil {
				return err
			}
		}
	}
	return nil
}

// upsertConflictCols 返回 upsert 语句的 INSERT 列清单：在 writableCols 的基础上，
// 把「冲突键里被排除、但实体已赋非零值」的自增主键补回来。
//
// 为什么必须补：writableCols（model.go）跳过 autoInc 列，于是
// Upsert(&User{Id: 7, ...}, []string{"id"}) 生成的 INSERT 里根本没有 "id" 列 ——
// PG/SQLite 的 ON CONFLICT ("id") 与 MySQL 的 ON DUPLICATE KEY 都无从命中，
// 数据库另发一个新主键后照常插入：「存在则更新」静默退化成「新增一行」，且不报任何错。
// 三方言真库实测全中，而 mock 测试恰好把这条错误 SQL 断言成了期望值，所以长期未暴露。
//
// 主键为类型零值时**不补**：那时的语义本来就是「插入新行，主键交给数据库」。
// checkConflictCols 校验 upsert 的冲突键列表（Upsert / BatchUpsert 共用）。
//
// 为什么必须前置校验 —— 跨方言口径在这件事上完全相反：
//   - PG / SQLite 把冲突键原样拼进 ON CONFLICT (...)，列名拼错由**数据库**报
//     `column "emial" does not exist`，指不回调用处；重复列则是数据库层的
//     "conflict target specified more than once"。
//   - MySQL 的 ON DUPLICATE KEY UPDATE **根本不看冲突键**，一律按表上的唯一索引判定。
//     于是同一个拼错的列名，在 PG 上炸、在 MySQL 上静默按「别的唯一键」插入或更新 ——
//     调用方以为自己指定了冲突键，实际完全没生效，而且没有任何信号。
//
// 这与 checkSetCols 对 map 入口做的是同一类校验（列必须是模型里的真实列），
// 只是这里还要额外挡空串与重复：空串会渲染成 ON CONFLICT ("")，重复会让
// ON CONFLICT 的列数与 updateCols 的排除逻辑对不上。
//
// 只拒绝「模型里根本没有的列」（fi == nil）。标了 `,ignore` 的列仍然放行：
// 这类列在表里真实存在、只是 ORM 不负责写它，把它当冲突键（例如外部维护的唯一索引）
// 是合法用法。
func checkConflictCols(meta *modelMeta, cc []string, api string) error {
	if len(cc) == 0 {
		return fmt.Errorf("orm: %s %s: %w：未指定冲突键", meta.table, api, ErrInvalidArgument)
	}
	seen := make(map[string]struct{}, len(cc))
	for _, c := range cc {
		if c == "" {
			return fmt.Errorf("orm: %s %s 的冲突键含空列名：%w", meta.table, api, ErrInvalidArgument)
		}
		if _, dup := seen[c]; dup {
			return fmt.Errorf("orm: %s %s 的冲突键 %q 重复：%w", meta.table, api, c, ErrInvalidArgument)
		}
		seen[c] = struct{}{}
		if fieldInfoForCol(meta, c) == nil {
			return fmt.Errorf("orm: %s %s 的冲突键 %q 不是模型中的列：%w（PG/SQLite 会把它原样拼进 "+
				"ON CONFLICT，MySQL 则完全忽略冲突键、只按表上唯一索引判定 —— 拼错在 MySQL 上不会报错，"+
				"只会静默按另一个唯一键执行；确需对未映射列做冲突判定请用原生 SQL）",
				meta.table, api, c, ErrInvalidArgument)
		}
	}
	return nil
}

func upsertConflictCols(meta *modelMeta, ev reflect.Value, cols, conflict []string) []string {
	var extra []string
	for _, c := range conflict {
		if contains(cols, c) || contains(extra, c) {
			continue
		}
		fi := fieldInfoForCol(meta, c)
		// 只补自增主键：ignore 列是用户明确要求忽略的，logic 列由 ORM 接管，都不该被写回。
		if fi == nil || !fi.autoInc {
			continue
		}
		if fv := fieldByCol(ev, meta, c); fv.IsValid() && !fv.IsZero() {
			extra = append(extra, c)
		}
	}
	if len(extra) == 0 {
		return cols
	}
	// 冲突键前置：贴近手写 SQL 的阅读习惯（主键在最前），也便于日志排查。
	return append(extra, cols...)
}

// upsertNeedsPKBackfill 判断 upsert 后是否需要回填自增主键：
// 模型有自增主键、且实体里该主键仍是零值（未指定 → 由数据库发号）时为 true。
func upsertNeedsPKBackfill(meta *modelMeta, ev reflect.Value) bool {
	if meta.pk == nil || !meta.pk.autoInc {
		return false
	}
	fv := fieldByCol(ev, meta, meta.pk.colName)
	return fv.IsValid() && fv.IsZero()
}

// upsertBatchConflictCols 是 upsertConflictCols 的批量版本。
//
// 多行 VALUES 要求每行列数一致，所以无法逐行决定补不补，只能整批统一：
//   - 全部行都赋了非零冲突主键 → 补上该列，各行的「存在则更新」才可能命中；
//   - 全部行都留零值           → 不补（主键交由数据库发号，等价批量插入）；
//   - 只有部分行赋值           → 两种解释都错（补列会让未赋值的行走主键 0），直接报错。
func upsertBatchConflictCols(meta *modelMeta, ents reflect.Value, cols, conflict []string) ([]string, error) {
	var candidates []string
	for _, c := range conflict {
		if contains(cols, c) {
			continue
		}
		if fi := fieldInfoForCol(meta, c); fi != nil && fi.autoInc {
			candidates = append(candidates, c)
		}
	}
	if len(candidates) == 0 {
		return cols, nil
	}
	n := ents.Len()
	for _, c := range candidates {
		assigned := 0
		for i := 0; i < n; i++ {
			if fv := fieldByCol(ents.Index(i), meta, c); fv.IsValid() && !fv.IsZero() {
				assigned++
			}
		}
		switch assigned {
		case 0:
			// 整批都交给数据库发号
		case n:
			cols = append([]string{c}, cols...) // 同 Upsert：冲突键前置
		default:
			return nil, fmt.Errorf("orm: %s 的 BatchUpsert 冲突键 %q 是自增主键，"+
				"但本批 %d 行里只有 %d 行赋了值 —— 多行 VALUES 列数必须一致，"+
				"无法只给部分行指定主键。请统一赋值，或把已赋值的行单独成批",
				meta.table, c, n, assigned)
		}
	}
	return cols, nil
}

// BatchUpsert 批量 upsert 切片实体，复用 Upsert 的冲突键与方言策略（多行 VALUES）。
// 与 BatchInsert 一样按方言参数上限自动分批，跨批不保证原子性（需要原子性请自己开事务）。
func BatchUpsert[T any](ctx context.Context, db *DB, entities []T, conflictCols []string, opts ...WriteOption) error {
	n := len(entities)
	if n == 0 {
		return nil
	}
	meta := getMeta[T]()
	cfg := applyWriteOptions(opts...)
	cols := writableCols(meta)
	if cfg.onlySet {
		filtered, err := filterOnlyCols(meta, cols, cfg.only)
		if err != nil {
			return err
		}
		cols = filtered
	}
	if cfg.omitZero {
		cols = filterOmitZeroColsBatch(meta, entities, cols)
	}
	if len(cols) == 0 {
		return fmt.Errorf("orm: %s 无可写字段%s", meta.table, emptyColsHint(cfg))
	}
	cc := conflictCols
	if len(cc) == 0 {
		if meta.pk == nil {
			return fmt.Errorf("orm: %s 无主键且未指定冲突键，无法 BatchUpsert", meta.table)
		}
		cc = []string{meta.pk.colName}
	}
	if err := checkConflictCols(meta, cc, "BatchUpsert"); err != nil {
		return err
	}
	// 逐行补冲突键列 —— 同一批里各行的自增主键可能有的已赋值、有的没赋值，
	// 但多行 VALUES 必须列数一致，故这里统一决策（见 upsertBatchConflictCols）。
	cols, err := upsertBatchConflictCols(meta, reflect.ValueOf(entities), cols, cc)
	if err != nil {
		return err
	}
	updateCols := make([]string, 0, len(cols))
	for _, c := range cols {
		if !contains(cc, c) {
			updateCols = append(updateCols, c)
		}
	}
	buildBatch := func(start, end int) (string, []any, error) {
		phIdx := 0
		nextPh := func() string { phIdx++; return db.dialect.Placeholder(phIdx) }
		args := make([]any, 0, (end-start)*len(cols))
		valueRows := make([]string, 0, end-start)
		for i := start; i < end; i++ {
			ev := reflect.ValueOf(&entities[i]).Elem()
			phs := make([]string, 0, len(cols))
			for _, c := range cols {
				v, err := argFor(meta, ev, c)
				if err != nil {
					return "", nil, err
				}
				args = append(args, v)
				ph := nextPh()
				if fi := fieldInfoForCol(meta, c); fi != nil && fi.vector {
					ph = db.dialect.VectorBind(ph)
				}
				phs = append(phs, ph)
			}
			valueRows = append(valueRows, "("+strings.Join(phs, ", ")+")")
		}
		sqlStr := fmt.Sprintf("INSERT INTO %s (%s) VALUES %s %s",
			quoteTable(meta.finalTable(db.prefix), db.dialect), quoteCols(cols, db.dialect),
			strings.Join(valueRows, ", "), db.dialect.UpsertSuffix(cc, updateCols))
		return sqlStr, args, nil
	}
	// 批量不回填自增主键：多行 RETURNING 的返回顺序与行序、以及 MySQL 的
	// LAST_INSERT_ID + auto_increment_increment 都依赖较多前提，与 BatchInsert 保持一致。
	//
	// 与 BatchInsert 同口径按参数上限分批（见 batchRowsPerStmt）。
	per := batchRowsPerStmt(db.dialect, len(cols))
	for start := 0; start < n; start += per {
		end := start + per
		if end > n {
			end = n
		}
		sqlStr, args, err := buildBatch(start, end)
		if err != nil {
			return err
		}
		if _, err := db.execContext(ctx, sqlStr, args...); err != nil {
			return err
		}
	}
	return nil
}

// ---- 查询 ----

// SelectById 按主键查询单条；未命中返回 ErrNotFound。自动过滤已逻辑删除的行。
func SelectById[T any](ctx context.Context, db *DB, id any) (*T, error) {
	meta := getMeta[T]()
	if meta.pk == nil {
		return nil, fmt.Errorf("orm: %s 无主键，无法 SelectById", meta.table)
	}
	sqlStr := fmt.Sprintf("SELECT * FROM %s WHERE %s = %s",
		quoteTable(meta.finalTable(db.prefix), db.dialect),
		db.dialect.QuoteIdent(meta.pk.colName), db.dialect.Placeholder(1))
	if s := logicSuffix(resolveLogic(meta, db), db.dialect, false); s != "" {
		sqlStr += " AND " + s
	}
	rows, err := db.queryContext(ctx, sqlStr, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var t T
	if !rows.Next() {
		if e := rows.Err(); e != nil {
			return nil, e
		}
		return nil, ErrNotFound
	}
	if err := scanStruct(rows, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// SelectList 按查询构造器返回列表。自动过滤已逻辑删除的行（Unscoped 例外）。
func SelectList[T any](ctx context.Context, db *DB, q *Query[T]) ([]T, error) {
	sqlStr, args := q.applyLogic(getMeta[T](), db).WithDialect(db.dialect).WithPrefix(db.prefix).Build()
	rows, err := db.queryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRows[T](rows)
}

// scanRows 把整个结果集扫进 R，是所有「多行结果」入口的共用实现。
//
// 存在的理由：凡是列表查询都得走同一套「扫描器构造 → 列映射 → 逐行 Scan」流程，
// 一旦有人按自己方便再抄一份（历史上 Count 就抄丢过 JOIN），就会在某条路径上
// 悄悄少掉一个环节。SelectList 与 Aggregate 是指定的两个使用者，新入口应当复用它。
//
// R 必须是结构体：行映射按 结果集列名 == 字段 db tag 列名 匹配，别的形态没有字段可落。
func scanRows[R any](rows *sql.Rows) ([]R, error) {
	if rt := reflect.TypeOf((*R)(nil)).Elem(); rt.Kind() != reflect.Struct {
		return nil, fmt.Errorf("orm: 结果类型必须是结构体（收到 %s）：行映射按 db tag 把列匹配到字段上", rt)
	}
	// 扫描器按查询构造一次：列映射计划与扫描缓冲都与具体某一行无关，
	// 逐行重建是列表路径上最大的一笔浪费。
	sc, err := newRowScanner(rows, (*R)(nil))
	if err != nil {
		return nil, err
	}
	var out []R
	for rows.Next() {
		var r R
		if err := sc.scanInto(rows, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SelectOne 返回列表首条；空结果返回 ErrNotFound。自动过滤已逻辑删除的行。
func SelectOne[T any](ctx context.Context, db *DB, q *Query[T]) (*T, error) {
	list, err := SelectList(ctx, db, q.applyLogic(getMeta[T](), db).Limit(1))
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	return &list[0], nil
}

// Count 返回符合条件的记录数。自动过滤已逻辑删除的行（Unscoped 例外）。
//
// 与 SelectList 的口径一致性：JOIN 与主表别名会计入 —— INNER JOIN 会改变行数，
// 此前 Count 手搓 FROM 把 JOIN 整个漏掉，带 JOIN 的查询算总数会与列表行数不符，
// 分页页码因此对不上。ORDER BY / LIMIT / OFFSET 不参与计数。
//
// 语义始终是「列表那条 SQL 会返回多少行」：带 DISTINCT 或 GROUP BY（含 HAVING）时，
// 计数改为对原查询加一层子查询（见 Query.countBuild），于是 Distinct 会去重、
// GroupBy 数的是分组数 —— 两者都与列表行数一致，而不是退化为全表行数。
func Count[T any](ctx context.Context, db *DB, q *Query[T]) (int64, error) {
	qq := q.applyLogic(getMeta[T](), db).WithDialect(db.dialect).WithPrefix(db.prefix)
	sqlStr, args := qq.countBuild()
	rows, err := db.queryContext(ctx, sqlStr, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var n int64
	if rows.Next() {
		if err := rows.Scan(&n); err != nil {
			return 0, err
		}
	}
	return n, rows.Err()
}

// Exists 是否存在符合条件的记录。
func Exists[T any](ctx context.Context, db *DB, q *Query[T]) (bool, error) {
	n, err := Count(ctx, db, q)
	return n > 0, err
}

// PageResult 通用分页结果：既携带本页数据，也携带分页元数据，方便前端直接渲染分页器。
type PageResult[T any] struct {
	List    []T   `json:"list"`    // 本页数据
	Page    int   `json:"page"`    // 当前页（1-based，非法值自动归正为 1）
	Size    int   `json:"size"`    // 每页条数（非法值自动归正为 10）
	Total   int64 `json:"total"`   // 符合条件的总条数
	Pages   int   `json:"pages"`   // 总页数
	HasNext bool  `json:"hasNext"` // 是否存在下一页
	HasPrev bool  `json:"hasPrev"` // 是否存在上一页
}

// computePageMeta 由总条数与页码/页大小推导总页数及前后页标记。
func computePageMeta(page, size int, total int64) (pages int, hasNext, hasPrev bool) {
	if size > 0 {
		pages = int((total + int64(size) - 1) / int64(size))
	}
	hasNext = pages > 0 && int64(page) < int64(pages)
	hasPrev = pages > 0 && page > 1
	return
}

// Page 分页：返回通用分页结果（数据 + 分页元数据）。
// page/size 非法（<1）时自动归正为 1/10；同时计算总条数与总页数。
func Page[T any](ctx context.Context, db *DB, q *Query[T], page, size int) (*PageResult[T], error) {
	total, err := Count(ctx, db, q)
	if err != nil {
		return nil, err
	}
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 10
	}
	// 分页参数只能落在内部副本上：q 属于调用方，直接 q.Limit(size).Offset(...)
	// 会把这些值写回它的 Query，之后复用它做列表查询就会静默只取 size 行。
	qq := *q
	qq.limit, qq.offset = size, (page-1)*size
	list, err := SelectList(ctx, db, &qq)
	if err != nil {
		return nil, err
	}
	pages, hasNext, hasPrev := computePageMeta(page, size, total)
	return &PageResult[T]{
		List:    list,
		Page:    page,
		Size:    size,
		Total:   total,
		Pages:   pages,
		HasNext: hasNext,
		HasPrev: hasPrev,
	}, nil
}

// ---- 更新 ----

// updateByIdImpl 是 UpdateById / Save 的共用实现，额外返回受影响行数：
// Save 需要「命中 0 行 = 记录不存在」的信号（返回 ErrNotFound），
// 而 UpdateById 在无乐观锁列时维持旧行为（不判行数）。
//
// 注意此处 RowsAffected 报错时直接返回错误（旧行为是静默跳过行数判断继续成功）：
// 对 Save 来说「拿不到行数」与「记录不存在」一样不能装作成功。
// 三个主方言（PG / MySQL / SQLite）的 RowsAffected 均可靠，不影响既有调用方。
func updateByIdImpl[T any](ctx context.Context, db *DB, entity *T, opts ...WriteOption) (int64, error) {
	meta := getMeta[T]()
	if meta.pk == nil {
		return 0, fmt.Errorf("orm: %s 无主键，无法 UpdateById", meta.table)
	}
	cfg := applyWriteOptions(opts...)
	ev := reflect.ValueOf(entity).Elem()
	cols := updateCols(meta)
	if cfg.onlySet {
		filtered, err := filterOnlyCols(meta, cols, cfg.only)
		if err != nil {
			return 0, err
		}
		cols = filtered
	}
	if cfg.omitZero {
		cols = filterOmitZeroCols(meta, ev, cols)
	}
	if len(cols) == 0 {
		return 0, fmt.Errorf("orm: %s 无可更新字段%s", meta.table, emptyColsHint(cfg))
	}
	vi := resolveVersion(meta, db)
	phIdx := 0
	nextPh := func() string { phIdx++; return db.dialect.Placeholder(phIdx) }
	setParts := make([]string, 0, len(cols)+1)
	args := make([]any, 0, len(cols)+2)
	for _, c := range cols {
		if vi != nil && c == vi.colName {
			continue // 版本列由「自增 + WHERE 旧值」处理，不按实体值赋值
		}
		v, err := argFor(meta, ev, c)
		if err != nil {
			return 0, err
		}
		args = append(args, v)
		ph := nextPh()
		if fi := fieldInfoForCol(meta, c); fi != nil && fi.vector {
			ph = db.dialect.VectorBind(ph)
		}
		setParts = append(setParts, fmt.Sprintf("%s = %s", db.dialect.QuoteIdent(c), ph))
	}
	if vi != nil {
		// 乐观锁：SET version = version + 1
		setParts = append(setParts, fmt.Sprintf("%s = %s + 1", db.dialect.QuoteIdent(vi.colName), db.dialect.QuoteIdent(vi.colName)))
	}
	pkVal, err := argFor(meta, ev, meta.pk.colName)
	if err != nil {
		return 0, err
	}
	args = append(args, pkVal)
	sqlStr := fmt.Sprintf("UPDATE %s SET %s WHERE %s = %s",
		quoteTable(meta.finalTable(db.prefix), db.dialect), strings.Join(setParts, ", "),
		db.dialect.QuoteIdent(meta.pk.colName), nextPh())
	if vi != nil {
		// 乐观锁：WHERE version = 期望的旧值
		oldVer, err := argFor(meta, ev, vi.colName)
		if err != nil {
			return 0, err
		}
		sqlStr += fmt.Sprintf(" AND %s = %s", db.dialect.QuoteIdent(vi.colName), nextPh())
		args = append(args, oldVer)
	}
	if s := logicSuffix(resolveLogic(meta, db), db.dialect, false); s != "" {
		sqlStr += " AND " + s
	}
	res, err := db.execContext(ctx, sqlStr, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if vi != nil {
		if n == 0 {
			return 0, ErrOptimisticLock
		}
		// 回写自增后的版本号：库里已经是 version+1，实体却还停在旧值 ——
		// 同一个实体连续两次 UpdateById，第二次会拿旧版本去比对，于是收到
		// 一个根本没有并发写者的 ErrOptimisticLock（假冲突）。
		// 自增主键就是回填的（见 Insert），版本没有理由例外。
		if err := bumpVersionField(ev, vi); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// UpdateById 按实体主键更新其非主键字段。
//
// 会写入「全部可写列」：未赋值的字段被写回零值（含 created_at 等审计列），
// 需要部分更新请用 UpdateByIdSets / UpdatePartial，或 OnlyColumns(...) 限定列。
// 带乐观锁版本列时自动比对并自增版本，命中 0 行返回 ErrOptimisticLock；
// 无版本列时命中 0 行不报错（历史行为，调用方如需「记录必须存在」的更新
// 请用 Save，它把 0 行显式报告为 ErrNotFound）。
func UpdateById[T any](ctx context.Context, db *DB, entity *T, opts ...WriteOption) error {
	_, err := updateByIdImpl[T](ctx, db, entity, opts...)
	return err
}

// bumpVersionField 把实体的乐观锁版本字段 +1，与 SQL 里的 "version = version + 1" 对齐。
//
// 版本列必须是整型：`version + 1` 在 PostgreSQL 上对 text 列直接报错，但 MySQL 会
// 静默把字符串转成 0 再 +1（版本从 "abc" 变成 1，冲突检测形同虚设），
// 所以这里不猜语义、直接报错（调用方换成整型字段即可）。
func bumpVersionField(ev reflect.Value, vi *fieldInfo) error {
	f := ev.FieldByIndex(vi.idx)
	if !f.IsValid() {
		return nil
	}
	if f.Kind() == reflect.Ptr {
		if f.IsNil() {
			return fmt.Errorf("orm: 乐观锁版本列 %q 的字段是指针且为 nil，无法自增（请先赋值，或改用整型值字段）", vi.colName)
		}
		f = f.Elem()
	}
	switch f.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		f.SetInt(f.Int() + 1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		f.SetUint(f.Uint() + 1)
	default:
		return fmt.Errorf("orm: 乐观锁版本列 %q 的字段类型是 %s，无法自增：必须是整型（int / int64 / uint 等）。"+
			"非整型版本列在 MySQL 上会被静默转成 0 再 +1，冲突检测形同虚设（PG 则直接报错），"+
			"故在这里显式失败而不是继续写入", vi.colName, f.Type())
	}
	return nil
}

// Update 按查询条件更新，使用 entity 的非主键字段作为新值。自动跳过已逻辑删除的行。
func Update[T any](ctx context.Context, db *DB, q *Query[T], entity *T, opts ...WriteOption) error {
	meta := getMeta[T]()
	cfg := applyWriteOptions(opts...)
	ev := reflect.ValueOf(entity).Elem()
	cols := updateCols(meta)
	if cfg.onlySet {
		filtered, err := filterOnlyCols(meta, cols, cfg.only)
		if err != nil {
			return err
		}
		cols = filtered
	}
	if cfg.omitZero {
		cols = filterOmitZeroCols(meta, ev, cols)
	}
	if len(cols) == 0 {
		return fmt.Errorf("orm: %s 无可更新字段%s", meta.table, emptyColsHint(cfg))
	}
	phIdx := 0
	nextPh := func() string { phIdx++; return db.dialect.Placeholder(phIdx) }
	// 版本列不按实体值赋值：Update 是**按条件批量**更新，把实体的版本号原样写回
	// 所有匹配行，等于把一批行的版本统一改成调用方手里的那个值（既丢自增、
	// 又可能把别人的版本改小）。这里与 UpdateById 同口径 —— SET version = version + 1，
	// 由数据库按各行当前值自增。（批量更新的是多行，实体上回填版本号没有意义。）
	vi := resolveVersion(meta, db)
	setParts := make([]string, 0, len(cols))
	args := make([]any, 0, len(cols))
	for _, c := range cols {
		if vi != nil && c == vi.colName {
			continue
		}
		v, err := argFor(meta, ev, c)
		if err != nil {
			return err
		}
		args = append(args, v)
		ph := nextPh()
		if fi := fieldInfoForCol(meta, c); fi != nil && fi.vector {
			ph = db.dialect.VectorBind(ph)
		}
		setParts = append(setParts, fmt.Sprintf("%s = %s", db.dialect.QuoteIdent(c), ph))
	}
	if vi != nil {
		setParts = append(setParts, fmt.Sprintf("%s = %s + 1",
			db.dialect.QuoteIdent(vi.colName), db.dialect.QuoteIdent(vi.colName)))
	}
	idx := phIdx
	add := func(v any) int { idx++; args = append(args, v); return idx }
	w := whereSQL(q.groups, db.dialect, add)
	if w == "" {
		return fmt.Errorf("orm: Update 必须有条件，禁止全表更新")
	}
	sqlStr := fmt.Sprintf("UPDATE %s SET %s WHERE %s",
		quoteTable(queryTable(q, meta, db), db.dialect), strings.Join(setParts, ", "), w)
	if s := logicSuffix(resolveLogic(meta, db), db.dialect, q.unscoped); s != "" {
		sqlStr += " AND " + s
	}
	_, err := db.execContext(ctx, sqlStr, args...)
	return err
}

// ---- 部分更新（多字段 / map）----

// checkSetCols 校验部分更新（map 形式）使用的列名都真实存在于模型元数据中。
// map 的 key 是纯字符串，若来自外部输入（HTTP 参数、配置）会被原样加引号拼进 SET 子句，
// 而不像 Col/ColOf 那样经过 picker 反查校验；同时拼错的列名（如 "nmae"）也只有数据库
// 才会报错。这里在拼 SQL 前直接拒绝，兼顾注入防护与拼写防呆。
func checkSetCols(meta *modelMeta, sets map[string]any) error {
	for col := range sets {
		fi := fieldInfoForCol(meta, col)
		if fi == nil || fi.ignore {
			return fmt.Errorf("orm: %s 不存在列 %q（UpdateSets/UpdateByIdSets 的字段名必须是模型中的真实列名）", meta.table, col)
		}
		if fi.pk {
			// 主键通常被别的表当外键引用：改名会让引用它的行静默指向不存在的目标（或指向另一行），
			// 关联数据当场失联；框架的 DeleteById / UpdateById 也都是按「旧主键」定位行的。
			// 此前 UpdateSets / UpdatePartial 允许 `{"id": 99}` 静默改主键，属数据损坏级用法。
			// 确需调整主键，请用原生 SQL（RawExec）并在业务侧自行处理关联一致性。
			return fmt.Errorf("orm: %s 的 %q 是主键列，UpdateSets/UpdateByIdSets/UpdatePartial 不允许修改主键；"+
				"确需调整主键请用原生 SQL（RawExec）并自行处理关联数据", meta.table, col)
		}
	}
	return nil
}

// UpdateSets 按查询条件更新 q.sets 中的字段（与 Query.Set 链式配合）。
// 自动跳过已逻辑删除的行（Unscoped 例外）；必须有 WHERE 条件，禁止全表更新；
// q.sets 为空则报错。字段名必须是模型中的真实列名（含 db:"..." 的列名），
// 否则报错且不下发 SQL。返回受影响行数。
//
// **不能写乐观锁版本列**（模型带 version 列时，sets 里出现它直接返回
// ErrInvalidArgument）：这里命中的是多行、各行版本互不相同，写死一个值会让
// 并发冲突检测失效。需要单行乐观锁请用 UpdateByIdSets。同理，本入口**不**自增
// 版本列（与 UpdateByIdSets 的「sets 不带版本列就不碰版本列」同口径）——若要
// 按条件批量更新并让版本自增，请用实体路径的 Update（它会发 version = version + 1）。
//
// 例：
//
//	orm.UpdateSets(ctx, db, orm.NewQuery[User]().
//	  Eq(orm.Col[User](func(u *User) *int64 { return &u.Id }), 1).
//	  Set("name", "bob").Set("age", 30))
func UpdateSets[T any](ctx context.Context, db *DB, q *Query[T]) (int64, error) {
	return updateSetsImpl[T](ctx, db, q, q.sets)
}

// updateSetsImpl 是 UpdateSets / UpdatePartial 的共用实现。sets 由调用方显式传入：
// UpdatePartial 此前直接写 q.sets = sets，把调用方的 Query 改掉 —— 之后在同一 q 上
// 链 .Set() 会追加到这份旧 map（Set 只在 map 为 nil 时新建），残留字段混进下一次
// UPDATE，静默多更新一列。凡框架入口都不得修改调用方传入的 Query。
func updateSetsImpl[T any](ctx context.Context, db *DB, q *Query[T], sets map[string]any) (int64, error) {
	meta := getMeta[T]()
	if len(sets) == 0 {
		return 0, fmt.Errorf("orm: UpdateSets 至少需要 Set 一个字段")
	}
	if err := checkSetCols(meta, sets); err != nil {
		return 0, err
	}
	// 乐观锁版本列不能由 map 直接写。UpdateSets / UpdatePartial 是**按条件**更新，
	// 命中的是多行、各行版本号互不相同：把调用方手里的单个值写进所有行，等于
	// 一次性抹平版本差异 —— 之后任何带 `WHERE version = ?` 的更新都匹配不上
	// （或者反过来，被一个过期值命中），并发冲突检测彻底失效，而且不报错。
	// Update（实体路径）早已是同一套理由：那里跳过实体的版本值、改发
	// `version = version + 1`（见上方 Update 内注释）。这里没有「正确值」可写，
	// 因为多行版本各异，所以只能显式失败。
	//
	// 这条校验**不能**下沉到 checkSetCols：UpdateByIdSets 正是靠 sets 里带版本值
	// 来触发单行乐观锁（那里合法），一刀切会把那条路径打死。
	if vi := resolveVersion(meta, db); vi != nil {
		if _, ok := sets[vi.colName]; ok {
			return 0, fmt.Errorf("orm: %s 的 %q 是乐观锁版本列，UpdateSets / UpdatePartial 不允许直接写：%w："+
				"按条件更新命中的是多行、各行版本互不相同，写死一个值会让并发冲突检测形同虚设；"+
				"单行请用 UpdateByIdSets（那里 sets 带版本值即触发 WHERE version 校验）", meta.table, vi.colName, ErrInvalidArgument)
		}
	}
	d := db.dialect
	phIdx := 0
	nextPh := func() string { phIdx++; return d.Placeholder(phIdx) }
	// sets 是 map，直接遍历会得到**随机顺序**的 SET 列。后果不只是「SQL 不好看」：
	//   - 依赖 prepared statement 的驱动（MySQL / PG）每次都要重新 Prepare，
	//     同一份代码的语句缓存命中率归零；
	//   - 慢查询日志、SLA 采样、逐字比对 SQL 的断言全部变得不稳定。
	// 按列名排序后遍历，SQL 文本对同一份输入恒定。
	cols := make([]string, 0, len(sets))
	for col := range sets {
		cols = append(cols, col)
	}
	sort.Strings(cols)
	setParts := make([]string, 0, len(cols))
	args := make([]any, 0, len(cols))
	for _, col := range cols {
		bv, err := bindVal(meta, col, sets[col])
		if err != nil {
			return 0, fmt.Errorf("orm: 列 %q 绑定失败：%w", col, err)
		}
		args = append(args, bv)
		ph := nextPh()
		if fi := fieldInfoForCol(meta, col); fi != nil && fi.vector {
			ph = d.VectorBind(ph)
		}
		setParts = append(setParts, fmt.Sprintf("%s = %s", d.QuoteIdent(col), ph))
	}
	idx := phIdx
	add := func(v any) int { idx++; args = append(args, v); return idx }
	w := whereSQL(q.groups, d, add)
	if w == "" {
		return 0, fmt.Errorf("orm: UpdateSets 必须有条件，禁止全表更新")
	}
	sqlStr := fmt.Sprintf("UPDATE %s SET %s WHERE %s",
		quoteTable(queryTable(q, meta, db), d), strings.Join(setParts, ", "), w)
	if s := logicSuffix(resolveLogic(meta, db), d, q.unscoped); s != "" {
		sqlStr += " AND " + s
	}
	res, err := db.execContext(ctx, sqlStr, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// UpdatePartial 是 UpdateSets 的 map 入口：直接以 sets map 指定待更新字段，
// 条件仍来自 q（Eq/In 等链式方法）。不修改调用方的 Query。返回受影响行数。
func UpdatePartial[T any](ctx context.Context, db *DB, q *Query[T], sets map[string]any) (int64, error) {
	return updateSetsImpl[T](ctx, db, q, sets)
}

// queryTable 返回「按条件写入」入口（Update / UpdateSets / Delete / ForceDelete）
// 应使用的物理表名：q 显式指定的表（q.Table，如分表 / 视图）优先，否则回退到
// 模型元数据推导的表名。此前这些入口一律用 meta.finalTable(db.prefix)，把
// q.Table 静默忽略掉 —— 读路径（走 Build()）尊重 q.table，写路径却写到默认表。
// 前缀口径与读路径一致：db.prefix 为准（读路径也是 WithPrefix(db.prefix) 覆盖）。
func queryTable[T any](q *Query[T], meta *modelMeta, db *DB) string {
	if q.table != "" {
		return applyPrefix(q.table, db.prefix, q.tableExplicit)
	}
	return meta.finalTable(db.prefix)
}

// UpdateByIdSets 按主键更新 sets 中的字段（map 形式的部分更新）。
// 自动跳过已逻辑删除的行（Unscoped 例外）。若 sets 含乐观锁版本列，则
// 自动追加 "WHERE version = ?" 并 "SET version = version + 1"，受影响行数为 0
// 时返回 ErrOptimisticLock；**sets 不含版本列时不碰版本列**（不判冲突、也不自增）。
// field 名必须是模型中的真实列名，否则报错。返回受影响行数。
func UpdateByIdSets[T any](ctx context.Context, db *DB, id any, sets map[string]any) (int64, error) {
	meta := getMeta[T]()
	if meta.pk == nil {
		return 0, fmt.Errorf("orm: %s 无主键，无法 UpdateByIdSets", meta.table)
	}
	if len(sets) == 0 {
		return 0, fmt.Errorf("orm: UpdateByIdSets 至少需要一个字段")
	}
	if err := checkSetCols(meta, sets); err != nil {
		return 0, err
	}
	d := db.dialect
	vi := resolveVersion(meta, db)
	hasVersion := vi != nil
	if hasVersion {
		if _, ok := sets[vi.colName]; !ok {
			hasVersion = false // sets 未带版本值时不强行乐观锁
		}
	}
	phIdx := 0
	nextPh := func() string { phIdx++; return d.Placeholder(phIdx) }
	// 列名排序后再拼 SET：map 的遍历顺序在 Go 里是随机的，同一份 sets 每次生成的
	// SQL 文本都不同 —— prepared statement 缓存命中率归零、慢查询日志与逐字断言不稳定。
	// updateSetsImpl / UpdateByIdsSets 早已排序，此处（单行版）此前漏了，三个入口
	// 口径必须一致。
	cols := make([]string, 0, len(sets))
	for col := range sets {
		cols = append(cols, col)
	}
	sort.Strings(cols)
	setParts := make([]string, 0, len(sets)+1)
	args := make([]any, 0, len(sets)+2)
	for _, col := range cols {
		val := sets[col]
		if hasVersion && col == vi.colName {
			continue // 版本列由「自增 + WHERE 旧值」处理
		}
		bv, err := bindVal(meta, col, val)
		if err != nil {
			return 0, fmt.Errorf("orm: 列 %q 绑定失败：%w", col, err)
		}
		args = append(args, bv)
		ph := nextPh()
		if fi := fieldInfoForCol(meta, col); fi != nil && fi.vector {
			ph = d.VectorBind(ph)
		}
		setParts = append(setParts, fmt.Sprintf("%s = %s", d.QuoteIdent(col), ph))
	}
	if hasVersion {
		// 只在 sets 真的带了版本值（即走上乐观锁语义）时才自增。
		// 此前判的是 vi != nil：sets 未带版本列时 hasVersion 已被置 false，
		// 这里却仍然 `SET version = version + 1` 且**不加** `WHERE version = ?`，
		// 于是每次 map 更新都静默抬版本 —— 其它会话拿着刚读到的版本再写
		// 会收到假的 ErrOptimisticLock（并没有并发写者）。
		setParts = append(setParts, fmt.Sprintf("%s = %s + 1", d.QuoteIdent(vi.colName), d.QuoteIdent(vi.colName)))
	}
	args = append(args, id)
	sqlStr := fmt.Sprintf("UPDATE %s SET %s WHERE %s = %s",
		quoteTable(meta.finalTable(db.prefix), d), strings.Join(setParts, ", "),
		d.QuoteIdent(meta.pk.colName), nextPh())
	if hasVersion {
		oldVer := sets[vi.colName]
		sqlStr += fmt.Sprintf(" AND %s = %s", d.QuoteIdent(vi.colName), nextPh())
		args = append(args, oldVer)
	}
	if s := logicSuffix(resolveLogic(meta, db), d, false); s != "" {
		sqlStr += " AND " + s
	}
	res, err := db.execContext(ctx, sqlStr, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if hasVersion && n == 0 {
		return 0, ErrOptimisticLock
	}
	return n, nil
}

// ---- 删除 ----

// DeleteById 按主键「逻辑删除」（若模型存在生效的逻辑删除列），否则物理删除。
// 软删时只更新逻辑列（不触碰已删除行），如需物理删除请使用 ForceDeleteById。
func DeleteById[T any](ctx context.Context, db *DB, id any) error {
	meta := getMeta[T]()
	if meta.pk == nil {
		return fmt.Errorf("orm: %s 无主键，无法 DeleteById", meta.table)
	}
	d := db.dialect
	if li := resolveLogic(meta, db); li != nil {
		sqlStr := fmt.Sprintf("UPDATE %s SET %s = %s WHERE %s = %s",
			quoteTable(meta.finalTable(db.prefix), d),
			d.QuoteIdent(li.col), d.Placeholder(1),
			d.QuoteIdent(meta.pk.colName), d.Placeholder(2))
		args := []any{li.deletedValue(), id}
		if s := logicSuffix(li, d, false); s != "" {
			sqlStr += " AND " + s
		}
		_, err := db.execContext(ctx, sqlStr, args...)
		return err
	}
	sqlStr := fmt.Sprintf("DELETE FROM %s WHERE %s = %s",
		quoteTable(meta.finalTable(db.prefix), d),
		d.QuoteIdent(meta.pk.colName), d.Placeholder(1))
	_, err := db.execContext(ctx, sqlStr, id)
	return err
}

// Delete 按查询条件「逻辑删除」（若模型存在生效的逻辑删除列且未 Unscoped），否则物理删除；禁止无条件全表删除。
func Delete[T any](ctx context.Context, db *DB, q *Query[T]) error {
	meta := getMeta[T]()
	d := db.dialect
	args := []any{}
	idx := 0
	add := func(v any) int { idx++; args = append(args, v); return idx }

	if li := resolveLogic(meta, db); li != nil && !q.unscoped {
		// 参数分配顺序必须与占位符在 SQL 里的出现顺序一致：SET 在 WHERE 之前，
		// 所以「被删除的值」要先分配，条件值后分配。
		//
		// 这里曾经把 whereSQL 放在前面（先给 WHERE 分配、再给 SET 分配），在 PG 上因为
		// $n 自带序号而完全正确，在 MySQL / SQLite 上却生成
		// `UPDATE t SET deleted_at = ? WHERE col = ?` + args=[条件值, 时间值] ——
		// 两个值互换：WHERE 拿时间值去比字符串列必然命中 0 行，于是**删除静默不生效**
		// （不报错、不删、返回值也是 nil）；而 SET 侧那个非法值因为没有任何行被匹配到，
		// 连 MySQL 严格模式的类型检查都不会触发，所以两边都不留痕迹。
		setPart := fmt.Sprintf("%s = %s", d.QuoteIdent(li.col), d.Placeholder(add(li.deletedValue())))
		w := whereSQL(q.groups, d, add)
		if w == "" {
			return fmt.Errorf("orm: Delete 必须有条件，禁止全表删除")
		}
		sqlStr := fmt.Sprintf("UPDATE %s SET %s WHERE %s",
			quoteTable(queryTable(q, meta, db), d), setPart, w)
		if s := logicSuffix(li, d, false); s != "" {
			sqlStr += " AND " + s
		}
		_, err := db.execContext(ctx, sqlStr, args...)
		return err
	}
	w := whereSQL(q.groups, d, add)
	if w == "" {
		return fmt.Errorf("orm: Delete 必须有条件，禁止全表删除")
	}
	sqlStr := fmt.Sprintf("DELETE FROM %s WHERE %s", quoteTable(queryTable(q, meta, db), d), w)
	_, err := db.execContext(ctx, sqlStr, args...)
	return err
}

// ForceDeleteById 无视逻辑删除列，按主键物理删除。
func ForceDeleteById[T any](ctx context.Context, db *DB, id any) error {
	meta := getMeta[T]()
	if meta.pk == nil {
		return fmt.Errorf("orm: %s 无主键，无法 ForceDeleteById", meta.table)
	}
	sqlStr := fmt.Sprintf("DELETE FROM %s WHERE %s = %s",
		quoteTable(meta.finalTable(db.prefix), db.dialect),
		db.dialect.QuoteIdent(meta.pk.colName), db.dialect.Placeholder(1))
	_, err := db.execContext(ctx, sqlStr, id)
	return err
}

// ForceDelete 无视逻辑删除列，按查询条件物理删除；禁止无条件全表删除。
func ForceDelete[T any](ctx context.Context, db *DB, q *Query[T]) error {
	meta := getMeta[T]()
	args := []any{}
	idx := 0
	add := func(v any) int { idx++; args = append(args, v); return idx }
	w := whereSQL(q.groups, db.dialect, add)
	if w == "" {
		return fmt.Errorf("orm: ForceDelete 必须有条件，禁止全表删除")
	}
	sqlStr := fmt.Sprintf("DELETE FROM %s WHERE %s", quoteTable(queryTable(q, meta, db), db.dialect), w)
	_, err := db.execContext(ctx, sqlStr, args...)
	return err
}

// ---- 批量按主键（IN 列表）----

// idsIN 生成 IN (...) 的占位符列表与对应参数：占位符从 startIdx 起连续编号，
// 参数按传入顺序返回。位置型方言（MySQL / SQLite 的 ?）按出现次序逐个消耗实参，
// 调用方必须把返回的 args 原样排在「SQL 文本中先于 IN 出现」的参数之后
// （见 DeleteByIds 的软删分支 —— SET 值占 Placeholder(1)，IN 从 2 起）。
func idsIN(d Dialect, startIdx int, ids []any) (string, []any) {
	phs := make([]string, len(ids))
	for i := range ids {
		phs[i] = d.Placeholder(startIdx + i)
	}
	return strings.Join(phs, ", "), ids
}

// SelectByIds 按主键列表批量查询。自动过滤已逻辑删除的行。
// 返回顺序与 ids 的传入顺序无关（SQL 不保证 IN 的输出次序），需要按序请自行排序。
//
// 空 ID 列表返回 ErrInvalidArgument（包装 errors.Is 可判别）：`IN ()` 不是合法 SQL，
// 若把空列表折叠成恒真条件则等价于全表查询 —— 这类调用几乎总是漏传参数的笔误，
// 必须显式失败而不是静默查全表。ID 数量受各方言占位符上限约束
// （PG / MySQL 65535、SQLite 32766），超限由数据库报错，框架不做分片。
func SelectByIds[T any](ctx context.Context, db *DB, ids ...any) ([]T, error) {
	meta := getMeta[T]()
	if meta.pk == nil {
		return nil, fmt.Errorf("orm: %s 无主键，无法 SelectByIds", meta.table)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("orm: SelectByIds: %w：空 ID 列表", ErrInvalidArgument)
	}
	d := db.dialect
	inPh, args := idsIN(d, 1, ids)
	sqlStr := fmt.Sprintf("SELECT * FROM %s WHERE %s IN (%s)",
		quoteTable(meta.finalTable(db.prefix), d), d.QuoteIdent(meta.pk.colName), inPh)
	if s := logicSuffix(resolveLogic(meta, db), d, false); s != "" {
		sqlStr += " AND " + s
	}
	rows, err := db.queryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRows[T](rows)
}

// DeleteByIds 按主键列表批量删除：模型存在生效的逻辑删除列时走逻辑删除
// （只更新逻辑列，已删除行不受影响），否则物理删除。返回受影响行数。
// 空 ID 列表返回 ErrInvalidArgument（理由同 SelectByIds）。
func DeleteByIds[T any](ctx context.Context, db *DB, ids ...any) (int64, error) {
	meta := getMeta[T]()
	if meta.pk == nil {
		return 0, fmt.Errorf("orm: %s 无主键，无法 DeleteByIds", meta.table)
	}
	if len(ids) == 0 {
		return 0, fmt.Errorf("orm: DeleteByIds: %w：空 ID 列表", ErrInvalidArgument)
	}
	d := db.dialect
	if li := resolveLogic(meta, db); li != nil {
		// 实参顺序必须与占位符在 SQL 文本中的出现顺序一致：SET 在 WHERE 之前，
		// 所以「删除值」占 Placeholder(1)，IN 列表从 2 起。顺序颠倒时 PG 因为 $n
		// 自带序号而侥幸正确，MySQL / SQLite 的 ? 会把删除值绑给条件列 ——
		// 匹配 0 行、不报错、不删除（历史上 Delete 真出过这事故，真库才测得出）。
		inPh, idArgs := idsIN(d, 2, ids)
		sqlStr := fmt.Sprintf("UPDATE %s SET %s = %s WHERE %s IN (%s)",
			quoteTable(meta.finalTable(db.prefix), d),
			d.QuoteIdent(li.col), d.Placeholder(1),
			d.QuoteIdent(meta.pk.colName), inPh)
		args := make([]any, 0, len(ids)+1)
		args = append(args, li.deletedValue())
		args = append(args, idArgs...)
		if s := logicSuffix(li, d, false); s != "" {
			sqlStr += " AND " + s
		}
		res, err := db.execContext(ctx, sqlStr, args...)
		if err != nil {
			return 0, err
		}
		return res.RowsAffected()
	}
	inPh, args := idsIN(d, 1, ids)
	sqlStr := fmt.Sprintf("DELETE FROM %s WHERE %s IN (%s)",
		quoteTable(meta.finalTable(db.prefix), d), d.QuoteIdent(meta.pk.colName), inPh)
	res, err := db.execContext(ctx, sqlStr, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ForceDeleteByIds 无视逻辑删除列，按主键列表物理删除。
// 空 ID 列表返回 ErrInvalidArgument（理由同 SelectByIds）。
func ForceDeleteByIds[T any](ctx context.Context, db *DB, ids ...any) (int64, error) {
	meta := getMeta[T]()
	if meta.pk == nil {
		return 0, fmt.Errorf("orm: %s 无主键，无法 ForceDeleteByIds", meta.table)
	}
	if len(ids) == 0 {
		return 0, fmt.Errorf("orm: ForceDeleteByIds: %w：空 ID 列表", ErrInvalidArgument)
	}
	d := db.dialect
	inPh, args := idsIN(d, 1, ids)
	sqlStr := fmt.Sprintf("DELETE FROM %s WHERE %s IN (%s)",
		quoteTable(meta.finalTable(db.prefix), d), d.QuoteIdent(meta.pk.colName), inPh)
	res, err := db.execContext(ctx, sqlStr, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// UpdateByIds 按主键逐条更新一批实体，是 UpdateById 的多行形态。
//
// 语义刻意保持「朴素」，不要拿它跟 BatchInsert 的吞吐类比：
//   - **逐条执行**：每行的 SET 值都不同，无法合并成一条语句（真要一条语句只能拼
//     CASE WHEN 或按值分组，SQL 会随行数膨胀，还会撞上参数上限）；
//   - **不隐式开事务**：中途失败时前面已更新的行不会回滚。需要整体原子，
//     请自行用 db.Transaction 包一层（见示例）；
//   - **遇错即停**：错误里带下标与主键值，能直接定位是哪一行出的问题；
//   - **主键为零值直接报错**：批量更新不做「没有就插入」。主键为零通常是实体组装
//     出错（漏扫描、拷贝丢了主键），静默跳过比报错危险得多。要 upsert 请用
//     Upsert / BatchUpsert，要插入请用 Insert / BatchInsert。
//
// 带乐观锁版本列时逐行比对并自增版本（与 UpdateById 完全一致）：任一行版本冲突
// 都会中断并返回 ErrOptimisticLock，该错误同样可用 errors.Is 判别。
//
// 空切片返回 nil —— 只是没东西可做，不是错误（与 DeleteByIds 的空 ID 守卫不同：
// 那里折叠成空条件会变成全表操作，这里不会执行任何语句）。
//
//	err := db.Transaction(ctx, func(tx *orm.DB) error {
//	    return orm.UpdateByIds(ctx, tx, users) // 任一行失败 → 整体回滚
//	})
func UpdateByIds[T any](ctx context.Context, db *DB, entities []T, opts ...WriteOption) error {
	meta := getMeta[T]()
	if meta.pk == nil {
		return fmt.Errorf("orm: %s 无主键，无法 UpdateByIds", meta.table)
	}
	for i := range entities {
		pk := reflect.ValueOf(&entities[i]).Elem().FieldByIndex(meta.pk.idx)
		if pk.IsZero() {
			return fmt.Errorf("orm: UpdateByIds 第 %d 行主键为 %q 的零值：批量更新不做插入，"+
				"主键为空通常是实体组装出错；要插入请用 Insert / BatchInsert，要 upsert 请用 Upsert",
				i, meta.pk.colName)
		}
		if err := UpdateById(ctx, db, &entities[i], opts...); err != nil {
			return fmt.Errorf("orm: UpdateByIds 第 %d 行（%s = %v）更新失败：%w",
				i, meta.pk.colName, pk.Interface(), err)
		}
	}
	return nil
}

// UpdateByIdsSets 按主键列表更新 sets 指定的字段（UpdateByIdSets 的批量形态）。
// 自动跳过已逻辑删除的行，返回受影响行数。
//
// 与单行版本不同，批量形态不支持乐观锁：多个目标行的版本各不相同，无法用单个
// 「WHERE version = 旧值」表达；sets 含版本列时直接报错，避免静默写出只对个别行
// 成立的条件（那是「不报错但做错事」）。空 ID 列表 / 空 sets 均报错。
func UpdateByIdsSets[T any](ctx context.Context, db *DB, ids []any, sets map[string]any) (int64, error) {
	meta := getMeta[T]()
	if meta.pk == nil {
		return 0, fmt.Errorf("orm: %s 无主键，无法 UpdateByIdsSets", meta.table)
	}
	if len(ids) == 0 {
		return 0, fmt.Errorf("orm: UpdateByIdsSets: %w：空 ID 列表", ErrInvalidArgument)
	}
	if len(sets) == 0 {
		return 0, fmt.Errorf("orm: UpdateByIdsSets 至少需要一个字段")
	}
	d := db.dialect
	if vi := resolveVersion(meta, db); vi != nil {
		if _, ok := sets[vi.colName]; ok {
			return 0, fmt.Errorf("orm: UpdateByIdsSets 的 sets 不应包含乐观锁版本列 %q：批量行版本各异，无法做单版本冲突检测", vi.colName)
		}
	}
	phIdx := 0
	nextPh := func() string { phIdx++; return d.Placeholder(phIdx) }
	// sets 是 map，直接遍历会得到随机顺序的 SET 列 —— 与 updateSetsImpl 同理，
	// 按列名排序保证同一份输入生成的 SQL 文本恒定（语句缓存与日志比对依赖它）。
	cols := make([]string, 0, len(sets))
	for col := range sets {
		cols = append(cols, col)
	}
	sort.Strings(cols)
	setParts := make([]string, 0, len(cols))
	args := make([]any, 0, len(cols)+len(ids))
	for _, col := range cols {
		bv, err := bindVal(meta, col, sets[col])
		if err != nil {
			return 0, fmt.Errorf("orm: 列 %q 绑定失败：%w", col, err)
		}
		args = append(args, bv)
		ph := nextPh()
		if fi := fieldInfoForCol(meta, col); fi != nil && fi.vector {
			ph = d.VectorBind(ph)
		}
		setParts = append(setParts, fmt.Sprintf("%s = %s", d.QuoteIdent(col), ph))
	}
	inPh, idArgs := idsIN(d, phIdx+1, ids)
	args = append(args, idArgs...)
	sqlStr := fmt.Sprintf("UPDATE %s SET %s WHERE %s IN (%s)",
		quoteTable(meta.finalTable(db.prefix), d), strings.Join(setParts, ", "),
		d.QuoteIdent(meta.pk.colName), inPh)
	if s := logicSuffix(resolveLogic(meta, db), d, false); s != "" {
		sqlStr += " AND " + s
	}
	res, err := db.execContext(ctx, sqlStr, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Save 按主键判断插入或更新的「直觉版」保存入口：
//   - 主键为零值（含 nil 指针）→ Insert，自增主键照常回填；
//   - 主键非零 → 按 UpdateById 的口径全列更新，命中 0 行返回错误 —— 不静默、
//     也不退回 Insert：拿着非零主键却插入新行，通常意味着主键被意外清零或并发删除，
//     应当显式失败；需要真正的 upsert 语义请用 Upsert。
//
// 命中 0 行时报什么错，取决于模型**有没有乐观锁版本列**（这点容易踩）：
//   - 无版本列 → ErrNotFound，语义是「记录不存在或已被逻辑删除」；
//   - 有版本列 → ErrOptimisticLock。updateByIdImpl 的 WHERE 同时带着主键和
//     `version = 旧值`，0 行既可能是记录不存在、也可能被别人改过版本，
//     单看影响行数无法区分，所以统一报成冲突（错误信息里会列出三种可能）。
//     要判「到底是不是不存在」，先 SelectById 查一次再决定是否重试。
//
// 写入选项（OmitZero / OnlyColumns）原样透传给 Insert / UpdateById。
// 注意非零主键的更新会写入全部可写列（未赋值字段写回零值），与 UpdateById 一致。
func Save[T any](ctx context.Context, db *DB, entity *T, opts ...WriteOption) error {
	meta := getMeta[T]()
	if meta.pk == nil {
		return fmt.Errorf("orm: %s 无主键，无法 Save（无法判断插入还是更新）", meta.table)
	}
	if entity == nil {
		return fmt.Errorf("orm: Save: %w：entity 为 nil", ErrInvalidArgument)
	}
	ev := reflect.ValueOf(entity).Elem()
	if ev.FieldByIndex(meta.pk.idx).IsZero() {
		return Insert[T](ctx, db, entity, opts...)
	}
	n, err := updateByIdImpl[T](ctx, db, entity, opts...)
	if err != nil {
		// 带版本列时，0 行被 updateByIdImpl 统一报成 ErrOptimisticLock —— 它无法区分
		// 「记录不存在」与「版本被别人改过」（两者的 WHERE 都命中 0 行）。这里补上
		// 三种可能，避免调用方把它当成纯粹的并发冲突去无脑重试。
		if errors.Is(err, ErrOptimisticLock) {
			return fmt.Errorf("orm: Save 更新命中 0 行（主键 %s = %v）：记录不存在 / 已被逻辑删除 / "+
				"版本冲突三者之一 —— 该模型带乐观锁版本列，单凭影响行数无法区分；"+
				"需要确认是哪种，请先 SelectById 再决定是否重试：%w",
				meta.pk.colName, ev.FieldByIndex(meta.pk.idx).Interface(), err)
		}
		return err
	}
	if n == 0 {
		return fmt.Errorf("orm: Save 更新命中 0 行（主键 %s = %v 的记录不存在或已被删除）: %w",
			meta.pk.colName, ev.FieldByIndex(meta.pk.idx).Interface(), ErrNotFound)
	}
	return nil
}
