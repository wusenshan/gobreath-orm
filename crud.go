package orm

import (
	"context"
	"fmt"
	"reflect"
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
		setFieldValue(fieldByCol(ev, meta, meta.pk.colName), id)
		return nil
	}

	res, err := db.execContext(ctx, sqlStr, args...)
	if err != nil {
		return err
	}
	if meta.pk != nil && meta.pk.autoInc {
		if id, err := res.LastInsertId(); err == nil {
			setFieldValue(fieldByCol(ev, meta, meta.pk.colName), id)
		}
	}
	return nil
}

// BatchInsert 批量插入切片实体。
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
		cols = filterOmitZeroCols(meta, reflect.ValueOf(&entities[0]).Elem(), cols)
	}
	if len(cols) == 0 {
		return fmt.Errorf("orm: %s 无可写字段%s", meta.table, emptyColsHint(cfg))
	}
	phIdx := 0
	nextPh := func() string { phIdx++; return db.dialect.Placeholder(phIdx) }

	args := make([]any, 0, n*len(cols))
	valueRows := make([]string, 0, n)
	for i := 0; i < n; i++ {
		ev := reflect.ValueOf(&entities[i]).Elem()
		phs := make([]string, 0, len(cols))
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
		valueRows = append(valueRows, "("+strings.Join(phs, ", ")+")")
	}
	sqlStr := fmt.Sprintf("INSERT INTO %s (%s) VALUES %s",
		quoteTable(meta.finalTable(db.prefix), db.dialect), quoteCols(cols, db.dialect), strings.Join(valueRows, ", "))
	_, err := db.execContext(ctx, sqlStr, args...)
	return err
}

// bindVal 返回部分更新 / map 更新时字段应绑定的参数值；向量列序列化为文本 [..]。
func bindVal(meta *modelMeta, col string, val any) any {
	if fi := fieldInfoForCol(meta, col); fi != nil && fi.vector {
		return serializeVector(val)
	}
	return val
}

// ---- Upsert（插入或更新，方言分发）----

// Upsert 插入单条记录；若发生冲突键（默认主键，可经 conflictCols 覆盖）已存在，则更新其余列。
// 方言差异由 Dialect.UpsertSuffix 处理：
//   - Postgres / SQLite：INSERT ... ON CONFLICT (key) DO UPDATE SET col = EXCLUDED.col
//   - MySQL：           INSERT ... ON DUPLICATE KEY UPDATE col = VALUES(col)
//
// 冲突键需对应表中的主键或唯一索引，否则数据库会报约束错误。更新列 = 全部可写列减去冲突键；
// 若无可更新列（仅冲突键一列），退化为 DO NOTHING（PG/SQLite）或等价无操作（MySQL）。
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
			setFieldValue(fieldByCol(ev, meta, meta.pk.colName), id)
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
			setFieldValue(fieldByCol(ev, meta, meta.pk.colName), id)
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
		cols = filterOmitZeroCols(meta, reflect.ValueOf(&entities[0]).Elem(), cols)
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
	phIdx := 0
	nextPh := func() string { phIdx++; return db.dialect.Placeholder(phIdx) }
	args := make([]any, 0, n*len(cols))
	valueRows := make([]string, 0, n)
	for i := 0; i < n; i++ {
		ev := reflect.ValueOf(&entities[i]).Elem()
		phs := make([]string, 0, len(cols))
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
		valueRows = append(valueRows, "("+strings.Join(phs, ", ")+")")
	}
	sqlStr := fmt.Sprintf("INSERT INTO %s (%s) VALUES %s %s",
		quoteTable(meta.finalTable(db.prefix), db.dialect), quoteCols(cols, db.dialect),
		strings.Join(valueRows, ", "), db.dialect.UpsertSuffix(cc, updateCols))
	// 批量不回填自增主键：多行 RETURNING 的返回顺序与行序、以及 MySQL 的
	// LAST_INSERT_ID + auto_increment_increment 都依赖较多前提，与 BatchInsert 保持一致。
	_, err = db.execContext(ctx, sqlStr, args...)
	return err
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
	// 扫描器按查询构造一次：列映射计划与扫描缓冲都与具体某一行无关，
	// 逐行重建是列表路径上最大的一笔浪费。
	sc, err := newRowScanner(rows, (*T)(nil))
	if err != nil {
		return nil, err
	}
	var out []T
	for rows.Next() {
		var t T
		if err := sc.scanInto(rows, &t); err != nil {
			return nil, err
		}
		out = append(out, t)
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
// 分页页码因此对不上。ORDER BY / LIMIT / OFFSET、GROUP BY / HAVING 与
// DISTINCT 不参与计数，语义始终是「符合条件的总行数」。
func Count[T any](ctx context.Context, db *DB, q *Query[T]) (int64, error) {
	qq := q.applyLogic(getMeta[T](), db).WithDialect(db.dialect).WithPrefix(db.prefix)
	c := qq.agg("COUNT", "")
	c.groupBy, c.havings, c.orders, c.distinct = nil, nil, nil, false
	c.forUpdate, c.last = false, ""

	sqlStr, args := c.Build()
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

// UpdateById 按实体主键更新其非主键字段。
func UpdateById[T any](ctx context.Context, db *DB, entity *T, opts ...WriteOption) error {
	meta := getMeta[T]()
	if meta.pk == nil {
		return fmt.Errorf("orm: %s 无主键，无法 UpdateById", meta.table)
	}
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
		// 乐观锁：SET version = version + 1
		setParts = append(setParts, fmt.Sprintf("%s = %s + 1", db.dialect.QuoteIdent(vi.colName), db.dialect.QuoteIdent(vi.colName)))
	}
	pkVal, err := argFor(meta, ev, meta.pk.colName)
	if err != nil {
		return err
	}
	args = append(args, pkVal)
	sqlStr := fmt.Sprintf("UPDATE %s SET %s WHERE %s = %s",
		quoteTable(meta.finalTable(db.prefix), db.dialect), strings.Join(setParts, ", "),
		db.dialect.QuoteIdent(meta.pk.colName), nextPh())
	if vi != nil {
		// 乐观锁：WHERE version = 期望的旧值
		oldVer, err := argFor(meta, ev, vi.colName)
		if err != nil {
			return err
		}
		sqlStr += fmt.Sprintf(" AND %s = %s", db.dialect.QuoteIdent(vi.colName), nextPh())
		args = append(args, oldVer)
	}
	if s := logicSuffix(resolveLogic(meta, db), db.dialect, false); s != "" {
		sqlStr += " AND " + s
	}
	res, err := db.execContext(ctx, sqlStr, args...)
	if err != nil {
		return err
	}
	if vi != nil {
		if n, e := res.RowsAffected(); e == nil && n == 0 {
			return ErrOptimisticLock
		}
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
	setParts := make([]string, 0, len(cols))
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
		setParts = append(setParts, fmt.Sprintf("%s = %s", db.dialect.QuoteIdent(c), ph))
	}
	idx := phIdx
	add := func(v any) int { idx++; args = append(args, v); return idx }
	w := whereSQL(q.groups, db.dialect, add)
	if w == "" {
		return fmt.Errorf("orm: Update 必须有条件，禁止全表更新")
	}
	sqlStr := fmt.Sprintf("UPDATE %s SET %s WHERE %s",
		quoteTable(meta.finalTable(db.prefix), db.dialect), strings.Join(setParts, ", "), w)
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
	}
	return nil
}

// UpdateSets 按查询条件更新 q.sets 中的字段（与 Query.Set 链式配合）。
// 自动跳过已逻辑删除的行（Unscoped 例外）；必须有 WHERE 条件，禁止全表更新；
// q.sets 为空则报错。字段名必须是模型中的真实列名（含 db:"..." 的列名），
// 否则报错且不下发 SQL。返回受影响行数。
//
// 例：
//
//	orm.UpdateSets(ctx, db, orm.NewQuery[User]().
//	  Eq(orm.Col[User](func(u *User) *int64 { return &u.Id }), 1).
//	  Set("name", "bob").Set("age", 30))
func UpdateSets[T any](ctx context.Context, db *DB, q *Query[T]) (int64, error) {
	meta := getMeta[T]()
	if len(q.sets) == 0 {
		return 0, fmt.Errorf("orm: UpdateSets 至少需要 Set 一个字段")
	}
	if err := checkSetCols(meta, q.sets); err != nil {
		return 0, err
	}
	d := db.dialect
	phIdx := 0
	nextPh := func() string { phIdx++; return d.Placeholder(phIdx) }
	setParts := make([]string, 0, len(q.sets))
	args := make([]any, 0, len(q.sets))
	for col, val := range q.sets {
		args = append(args, bindVal(meta, col, val))
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
		quoteTable(meta.finalTable(db.prefix), d), strings.Join(setParts, ", "), w)
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
// 条件仍来自 q（Eq/In 等链式方法）。返回受影响行数。
func UpdatePartial[T any](ctx context.Context, db *DB, q *Query[T], sets map[string]any) (int64, error) {
	q.sets = sets
	return UpdateSets(ctx, db, q)
}

// UpdateByIdSets 按主键更新 sets 中的字段（map 形式的部分更新）。
// 自动跳过已逻辑删除的行（Unscoped 例外）。若 sets 含乐观锁版本列，则
// 自动追加 "WHERE version = ?" 并 "SET version = version + 1"，受影响行数为 0
// 时返回 ErrOptimisticLock。field 名必须是模型中的真实列名，否则报错。返回受影响行数。
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
	setParts := make([]string, 0, len(sets)+1)
	args := make([]any, 0, len(sets)+2)
	for col, val := range sets {
		if hasVersion && col == vi.colName {
			continue // 版本列由「自增 + WHERE 旧值」处理
		}
		args = append(args, bindVal(meta, col, val))
		ph := nextPh()
		if fi := fieldInfoForCol(meta, col); fi != nil && fi.vector {
			ph = d.VectorBind(ph)
		}
		setParts = append(setParts, fmt.Sprintf("%s = %s", d.QuoteIdent(col), ph))
	}
	if vi != nil {
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
			quoteTable(meta.finalTable(db.prefix), d), setPart, w)
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
	sqlStr := fmt.Sprintf("DELETE FROM %s WHERE %s", quoteTable(meta.finalTable(db.prefix), d), w)
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
	sqlStr := fmt.Sprintf("DELETE FROM %s WHERE %s", quoteTable(meta.finalTable(db.prefix), db.dialect), w)
	_, err := db.execContext(ctx, sqlStr, args...)
	return err
}
