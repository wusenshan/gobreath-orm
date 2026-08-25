package orm

import "context"

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

func (r *Repo[T]) Insert(ctx context.Context, entity *T) error {
	return Insert[T](ctx, r.db, entity)
}

func (r *Repo[T]) BatchInsert(ctx context.Context, entities []T) error {
	return BatchInsert[T](ctx, r.db, entities)
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

func (r *Repo[T]) UpdateById(ctx context.Context, entity *T) error {
	return UpdateById[T](ctx, r.db, entity)
}

func (r *Repo[T]) Update(ctx context.Context, q *Query[T], entity *T) error {
	return Update[T](ctx, r.db, q, entity)
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

// Transaction 在本 Repo 的事务中执行 fn；回调收到的是同一 T 绑定的事务仓储句柄，
// 因此回调内同样免类型参数。fn 返回 error 时自动回滚，否则提交。
func (r *Repo[T]) Transaction(ctx context.Context, fn func(tx *Repo[T]) error) error {
	return r.db.Transaction(ctx, func(txDB *DB) error {
		return fn(&Repo[T]{db: txDB})
	})
}
