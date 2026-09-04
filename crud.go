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
		if meta.pk != nil && c == meta.pk.colName {
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
	if cfg.omitZero {
		cols = filterOmitZeroCols(meta, ev, cols)
	}
	if len(cols) == 0 {
		return fmt.Errorf("orm: %s 无可写字段（OmitZero 跳过全部零值列）", meta.table)
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
	if cfg.omitZero {
		cols = filterOmitZeroCols(meta, reflect.ValueOf(&entities[0]).Elem(), cols)
	}
	if len(cols) == 0 {
		return fmt.Errorf("orm: %s 无可写字段（OmitZero 跳过全部零值列）", meta.table)
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
	if cfg.omitZero {
		cols = filterOmitZeroCols(meta, ev, cols)
	}
	if len(cols) == 0 {
		return fmt.Errorf("orm: %s 无可写字段（OmitZero 跳过全部零值列）", meta.table)
	}
	cc := conflictCols
	if len(cc) == 0 {
		if meta.pk == nil {
			return fmt.Errorf("orm: %s 无主键且未指定冲突键，无法 Upsert", meta.table)
		}
		cc = []string{meta.pk.colName}
	}
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
	sqlStr := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) %s",
		quoteTable(meta.finalTable(db.prefix), db.dialect), quoteCols(cols, db.dialect),
		strings.Join(phs, ", "), db.dialect.UpsertSuffix(cc, updateCols))
	_, err := db.execContext(ctx, sqlStr, args...)
	return err
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
	if cfg.omitZero {
		cols = filterOmitZeroCols(meta, reflect.ValueOf(&entities[0]).Elem(), cols)
	}
	if len(cols) == 0 {
		return fmt.Errorf("orm: %s 无可写字段（OmitZero 跳过全部零值列）", meta.table)
	}
	cc := conflictCols
	if len(cc) == 0 {
		if meta.pk == nil {
			return fmt.Errorf("orm: %s 无主键且未指定冲突键，无法 BatchUpsert", meta.table)
		}
		cc = []string{meta.pk.colName}
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
	_, err := db.execContext(ctx, sqlStr, args...)
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
	var out []T
	for rows.Next() {
		var t T
		if err := scanStruct(rows, &t); err != nil {
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
func Count[T any](ctx context.Context, db *DB, q *Query[T]) (int64, error) {
	meta := getMeta[T]()
	args := []any{}
	idx := 0
	add := func(v any) int { idx++; args = append(args, v); return idx }
	w := whereSQL(q.groups, db.dialect, add)
	if s := logicSuffix(resolveLogic(meta, db), db.dialect, q.unscoped); s != "" {
		if w == "" {
			w = s
		} else {
			w += " AND " + s
		}
	}
	sqlStr := fmt.Sprintf("SELECT COUNT(*) FROM %s", quoteTable(meta.finalTable(db.prefix), db.dialect))
	if w != "" {
		sqlStr += " WHERE " + w
	}
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
	List    []T  `json:"list"`    // 本页数据
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
	list, err := SelectList(ctx, db, q.Limit(size).Offset((page-1)*size))
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
	if cfg.omitZero {
		cols = filterOmitZeroCols(meta, ev, cols)
	}
	if len(cols) == 0 {
		return fmt.Errorf("orm: %s 无可更新字段（OmitZero 跳过全部零值列）", meta.table)
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
	if cfg.omitZero {
		cols = filterOmitZeroCols(meta, ev, cols)
	}
	if len(cols) == 0 {
		return fmt.Errorf("orm: %s 无可更新字段（OmitZero 跳过全部零值列）", meta.table)
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

// UpdateSets 按查询条件更新 q.sets 中的字段（与 Query.Set 链式配合）。
// 自动跳过已逻辑删除的行（Unscoped 例外）；必须有 WHERE 条件，禁止全表更新；
// q.sets 为空则报错。返回受影响行数。
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
// 时返回 ErrOptimisticLock。返回受影响行数。
func UpdateByIdSets[T any](ctx context.Context, db *DB, id any, sets map[string]any) (int64, error) {
	meta := getMeta[T]()
	if meta.pk == nil {
		return 0, fmt.Errorf("orm: %s 无主键，无法 UpdateByIdSets", meta.table)
	}
	if len(sets) == 0 {
		return 0, fmt.Errorf("orm: UpdateByIdSets 至少需要一个字段")
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
	w := whereSQL(q.groups, d, add)
	if w == "" {
		return fmt.Errorf("orm: Delete 必须有条件，禁止全表删除")
	}
	if li := resolveLogic(meta, db); li != nil && !q.unscoped {
		setPart := fmt.Sprintf("%s = %s", d.QuoteIdent(li.col), d.Placeholder(add(li.deletedValue())))
		sqlStr := fmt.Sprintf("UPDATE %s SET %s WHERE %s", quoteTable(meta.finalTable(db.prefix), d), setPart, w)
		if s := logicSuffix(li, d, false); s != "" {
			sqlStr += " AND " + s
		}
		_, err := db.execContext(ctx, sqlStr, args...)
		return err
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
