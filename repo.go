package orm

import (
	"context"
	"database/sql"
	"iter"
)

// Repo 把 *DB 与实体类型 T 绑定，提供免类型参数的 CRUD 方法（类似 DAO / Repository 模式）。
//
// 通过 NewRepo[T](db) 或 db.Repo[T]() 创建。所有方法在内部转发给包级泛型函数
// （Insert[T] / SelectById[T] / Page[T] 等），调用处无需再重复写 [T]，可读性更好：
//
//	users := orm.NewRepo[User](db)
//	u, err := users.SelectById(ctx, 1)
//
// 事务回调中也绑定到同一 T，因此回调内同样免类型参数：
//
//	users.Transaction(ctx, func(tx *orm.Repo[User]) error { ... })
type Repo[T any] struct {
	db *DB
}

// NewRepo 创建针对实体 T 的仓储句柄。
// （Go 不支持泛型方法，故用函数式而非 db.Repo[T]()；NewRepo[User](db) 即等价于绑定。）
func NewRepo[T any](db *DB) *Repo[T] {
	return &Repo[T]{db: db}
}

// DB 返回底层 *DB（需要执行跨实体或原生 SQL 时使用）。
func (r *Repo[T]) DB() *DB { return r.db }

// ---- 增删改查（免类型参数）----

func (r *Repo[T]) Insert(ctx context.Context, entity *T, opts ...WriteOption) error {
	return Insert[T](ctx, r.db, entity, opts...)
}

func (r *Repo[T]) BatchInsert(ctx context.Context, entities []T, opts ...WriteOption) error {
	return BatchInsert[T](ctx, r.db, entities, opts...)
}

func (r *Repo[T]) SelectById(ctx context.Context, id any) (*T, error) {
	return SelectById[T](ctx, r.db, id)
}

func (r *Repo[T]) SelectList(ctx context.Context, q *Query[T]) ([]T, error) {
	return SelectList[T](ctx, r.db, q)
}

func (r *Repo[T]) SelectOne(ctx context.Context, q *Query[T]) (*T, error) {
	return SelectOne[T](ctx, r.db, q)
}

func (r *Repo[T]) Page(ctx context.Context, q *Query[T], page, size int) (*PageResult[T], error) {
	return Page[T](ctx, r.db, q, page, size)
}

func (r *Repo[T]) Count(ctx context.Context, q *Query[T]) (int64, error) {
	return Count[T](ctx, r.db, q)
}

func (r *Repo[T]) Exists(ctx context.Context, q *Query[T]) (bool, error) {
	return Exists[T](ctx, r.db, q)
}

func (r *Repo[T]) UpdateById(ctx context.Context, entity *T, opts ...WriteOption) error {
	return UpdateById[T](ctx, r.db, entity, opts...)
}

func (r *Repo[T]) Update(ctx context.Context, q *Query[T], entity *T, opts ...WriteOption) error {
	return Update[T](ctx, r.db, q, entity, opts...)
}

// Upsert 插入或按冲突键更新单条记录（方言分发：PG/SQLite ON CONFLICT、MySQL ON DUPLICATE KEY）。
func (r *Repo[T]) Upsert(ctx context.Context, entity *T, conflictCols []string, opts ...WriteOption) error {
	return Upsert[T](ctx, r.db, entity, conflictCols, opts...)
}

// BatchUpsert 批量 upsert 切片实体。
func (r *Repo[T]) BatchUpsert(ctx context.Context, entities []T, conflictCols []string, opts ...WriteOption) error {
	return BatchUpsert[T](ctx, r.db, entities, conflictCols, opts...)
}

// UpdateSets 按查询条件更新 q 中 Set 的字段（链式部分更新）。
func (r *Repo[T]) UpdateSets(ctx context.Context, q *Query[T]) (int64, error) {
	return UpdateSets[T](ctx, r.db, q)
}

// UpdatePartial 按查询条件更新 map 指定的字段。
func (r *Repo[T]) UpdatePartial(ctx context.Context, q *Query[T], sets map[string]any) (int64, error) {
	return UpdatePartial[T](ctx, r.db, q, sets)
}

// UpdateByIdSets 按主键更新 map 指定的字段（支持乐观锁版本列）。
func (r *Repo[T]) UpdateByIdSets(ctx context.Context, id any, sets map[string]any) (int64, error) {
	return UpdateByIdSets[T](ctx, r.db, id, sets)
}

func (r *Repo[T]) DeleteById(ctx context.Context, id any) error {
	return DeleteById[T](ctx, r.db, id)
}

func (r *Repo[T]) Delete(ctx context.Context, q *Query[T]) error {
	return Delete[T](ctx, r.db, q)
}

// ForceDeleteById 无视逻辑删除列，按主键物理删除。
func (r *Repo[T]) ForceDeleteById(ctx context.Context, id any) error {
	return ForceDeleteById[T](ctx, r.db, id)
}

// ForceDelete 无视逻辑删除列，按查询条件物理删除。
func (r *Repo[T]) ForceDelete(ctx context.Context, q *Query[T]) error {
	return ForceDelete[T](ctx, r.db, q)
}

// Save 按主键判断插入或更新：主键为零值 → Insert（自增主键照常回填），
// 非零 → 全列更新。命中 0 行时：无乐观锁版本列报 ErrNotFound，
// 有版本列报 ErrOptimisticLock（0 行可能是不存在，也可能是版本冲突，单看影响行数无法区分）。
// 需要真正的 upsert 语义请用 Upsert。
func (r *Repo[T]) Save(ctx context.Context, entity *T, opts ...WriteOption) error {
	return Save[T](ctx, r.db, entity, opts...)
}

// ---- 批量按主键（IN 列表）----

// SelectByIds 按主键列表批量查询，自动过滤已逻辑删除的行。
// 返回顺序与 ids 传入顺序无关；空 ID 列表返回 ErrInvalidArgument（防止折叠成全表查询）。
func (r *Repo[T]) SelectByIds(ctx context.Context, ids ...any) ([]T, error) {
	return SelectByIds[T](ctx, r.db, ids...)
}

// DeleteByIds 按主键列表批量删除（逻辑删除列生效时走逻辑删除），返回受影响行数。
// 空 ID 列表返回 ErrInvalidArgument。
func (r *Repo[T]) DeleteByIds(ctx context.Context, ids ...any) (int64, error) {
	return DeleteByIds[T](ctx, r.db, ids...)
}

// ForceDeleteByIds 无视逻辑删除列，按主键列表物理删除，返回受影响行数。
// 空 ID 列表返回 ErrInvalidArgument。
func (r *Repo[T]) ForceDeleteByIds(ctx context.Context, ids ...any) (int64, error) {
	return ForceDeleteByIds[T](ctx, r.db, ids...)
}

// UpdateByIds 按主键逐条更新一批实体（逐条执行、不隐式开事务、遇错即停）。
// 主键为零值直接报错；空切片是空操作。需要整体原子请套 Transaction。
func (r *Repo[T]) UpdateByIds(ctx context.Context, entities []T, opts ...WriteOption) error {
	return UpdateByIds[T](ctx, r.db, entities, opts...)
}

// UpdateByIdsSets 按主键列表更新 map 指定的字段，返回受影响行数。
// 批量形态不支持乐观锁：sets 含版本列时报错。空 ID 列表 / 空 sets 均报错。
func (r *Repo[T]) UpdateByIdsSets(ctx context.Context, ids []any, sets map[string]any) (int64, error) {
	return UpdateByIdsSets[T](ctx, r.db, ids, sets)
}

// QueryIter 流式遍历本 Repo 的查询结果（单游标逐行产出，大表扫描用），
// 语义与代价说明见包级 QueryIter。
func (r *Repo[T]) QueryIter(ctx context.Context, q *Query[T]) iter.Seq2[*T, error] {
	return QueryIter[T](ctx, r.db, q)
}

// Transaction 在本 Repo 的事务中执行 fn；回调收到的是同一 T 绑定的事务仓储句柄，
// 因此回调内同样免类型参数。fn 返回 error 时自动回滚，否则提交。
func (r *Repo[T]) Transaction(ctx context.Context, fn func(tx *Repo[T]) error) error {
	return r.db.Transaction(ctx, func(txDB *DB) error {
		return fn(&Repo[T]{db: txDB})
	})
}

// RawQuery / RawOne / RawExec 透传原生 SQL 能力（绑定到本 Repo 的 T 与 db）。
// 结果类型不是 T 时（JOIN 出来的 DTO），请改用包级函数：
//
//	vos, err := orm.RawQuery[UserDeptVO](ctx, repo.DB(), "SELECT ... JOIN ...")
func (r *Repo[T]) RawQuery(ctx context.Context, query string, args ...any) ([]T, error) {
	return RawQuery[T](ctx, r.db, query, args...)
}

func (r *Repo[T]) RawOne(ctx context.Context, query string, args ...any) (T, error) {
	return RawOne[T](ctx, r.db, query, args...)
}

func (r *Repo[T]) RawExec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return RawExec(ctx, r.db, query, args...)
}
