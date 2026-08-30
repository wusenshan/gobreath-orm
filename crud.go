package orm

import (
	"context"
	"fmt"
	"reflect"
	"strings"
)

// ---- 插入 ----

// Insert 插入单条记录，并回填自增主键（若字段标记为 autoincrement）。
func Insert[T any](ctx context.Context, db *DB, entity *T) error {
	meta := getMeta[T]()
	cols := writableCols(meta)
	if len(cols) == 0 {
		return fmt.Errorf("orm: %s 无可写字段", meta.table)
	}
	phIdx := 0
	nextPh := func() string { phIdx++; return db.dialect.Placeholder(phIdx) }

	ev := reflect.ValueOf(entity).Elem()
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
func BatchInsert[T any](ctx context.Context, db *DB, entities []T) error {
	n := len(entities)
	if n == 0 {
		return nil
	}
	meta := getMeta[T]()
	cols := writableCols(meta)
	if len(cols) == 0 {
		return fmt.Errorf("orm: %s 无可写字段", meta.table)
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
func UpdateById[T any](ctx context.Context, db *DB, entity *T) error {
	meta := getMeta[T]()
	if meta.pk == nil {
		return fmt.Errorf("orm: %s 无主键，无法 UpdateById", meta.table)
	}
	cols := updateCols(meta)
	if len(cols) == 0 {
		return fmt.Errorf("orm: %s 无可更新字段", meta.table)
	}
	ev := reflect.ValueOf(entity).Elem()
	phIdx := 0
	nextPh := func() string { phIdx++; return db.dialect.Placeholder(phIdx) }
	setParts := make([]string, 0, len(cols))
	args := make([]any, 0, len(cols)+1)
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
	pkVal, err := argFor(meta, ev, meta.pk.colName)
	if err != nil {
		return err
	}
	args = append(args, pkVal)
	sqlStr := fmt.Sprintf("UPDATE %s SET %s WHERE %s = %s",
		quoteTable(meta.finalTable(db.prefix), db.dialect), strings.Join(setParts, ", "),
		db.dialect.QuoteIdent(meta.pk.colName), nextPh())
	if s := logicSuffix(resolveLogic(meta, db), db.dialect, false); s != "" {
		sqlStr += " AND " + s
	}
	_, err = db.execContext(ctx, sqlStr, args...)
	return err
}

// Update 按查询条件更新，使用 entity 的非主键字段作为新值。自动跳过已逻辑删除的行。
func Update[T any](ctx context.Context, db *DB, q *Query[T], entity *T) error {
	meta := getMeta[T]()
	cols := updateCols(meta)
	if len(cols) == 0 {
		return fmt.Errorf("orm: %s 无可更新字段", meta.table)
	}
	ev := reflect.ValueOf(entity).Elem()
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
