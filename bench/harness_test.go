package bench

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"

	// glebarez/sqlite 是 GORM 的纯 Go SQLite 方言，内部即 SQLite 的纯 Go 实现，
	// 顺带把 "sqlite" 驱动注册进 database/sql —— 本模块的 raw 层与 gobreath 层
	// 也复用这一个注册，于是三方跑在**同一个驱动实现**上。
	// 这也是刻意不引入第二个 SQLite 驱动包的原因：同名驱动重复注册会直接 panic。
	"github.com/glebarez/sqlite"
	orm "github.com/wusenshan/gobreath-orm"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// sqliteDriverName 三方共用的 database/sql 驱动名。
const sqliteDriverName = "sqlite"

// dsnSeq 给每次 harness 构造分配一个唯一编号。
//
// 不能只用固定库名：SQLite 的 cache=shared 内存库是**按名字全局共享**的，
// 两个同时存活的 harness 用同一个 DSN 就会互相看到对方的表，
// 第二家建表直接报 "table already exists"。加序号后每个 harness 独占一份内存库。
var dsnSeq atomic.Int64

// sqliteDSN 每个后端一份独立的共享内存库。
//
// 用不同库名而非同一个库：三个后端的建表、种子、软删除标记互不干扰，
// 一个后端的残留数据不会污染另一个后端的测量结果。
// cache=shared 让连接池里的连接看到同一份数据；配合 MaxOpenConns(1)，
// 内存库在最后一个连接关闭前一直存活。
func sqliteDSN(name string) string {
	return fmt.Sprintf("file:%s_%d?mode=memory&cache=shared", name, dsnSeq.Add(1))
}

// ============================================================
// 第 1 层：database/sql 手写 SQL（下界）
// ============================================================

type rawHarness struct {
	db   *sql.DB
	rows []benchRow
}

func newRawHarness(tb testing.TB) *rawHarness {
	tb.Helper()
	db, err := sql.Open(sqliteDriverName, sqliteDSN("bench_raw"))
	if err != nil {
		tb.Fatalf("raw: 打开 SQLite 失败：%v", err)
	}
	db.SetMaxOpenConns(1)
	tb.Cleanup(func() { _ = db.Close() })

	setupRawSchema(tb, db)
	rows := makeRows(seedRowCount)
	seedRaw(tb, db, rows)
	return &rawHarness{db: db, rows: rows}
}

func setupRawSchema(tb testing.TB, db *sql.DB) {
	tb.Helper()
	for _, stmt := range []string{createTableSQL, createIndexSQL} {
		if _, err := db.Exec(stmt); err != nil {
			tb.Fatalf("raw: 建表失败：%v\nSQL: %s", err, stmt)
		}
	}
}

// seedRaw 事务内用预编译语句灌注种子数据。
// 种子不计时，所以这里怎么快怎么来。
func seedRaw(tb testing.TB, db *sql.DB, rows []benchRow) {
	tb.Helper()
	tx, err := db.Begin()
	if err != nil {
		tb.Fatalf("raw: 开启事务失败：%v", err)
	}
	stmt, err := tx.Prepare(rawInsertSQL)
	if err != nil {
		tb.Fatalf("raw: 预编译插入失败：%v", err)
	}
	for i := range rows {
		r := &rows[i]
		if _, err := stmt.Exec(r.Name, r.Age, r.City, r.Email, r.Score, r.CreatedAt, r.UpdatedAt); err != nil {
			tb.Fatalf("raw: 种子第 %d 行失败：%v", i, err)
		}
	}
	if err := stmt.Close(); err != nil {
		tb.Fatalf("raw: 关闭预编译语句失败：%v", err)
	}
	if err := tx.Commit(); err != nil {
		tb.Fatalf("raw: 提交种子事务失败：%v", err)
	}
}

func (h *rawHarness) selectByID(ctx context.Context, id int64) (rawRow, error) {
	var r rawRow
	err := h.db.QueryRowContext(ctx, rawSelectByIDSQL, id).Scan(
		&r.ID, &r.Name, &r.Age, &r.City, &r.Email, &r.Score,
		&r.CreatedAt, &r.UpdatedAt, &r.DeletedAt)
	return r, err
}

func (h *rawHarness) listPage(ctx context.Context, city string, minAge, limit, offset int) ([]rawRow, error) {
	rows, err := h.db.QueryContext(ctx, rawListPageSQL, city, minAge, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]rawRow, 0, limit)
	for rows.Next() {
		var r rawRow
		if err := rows.Scan(&r.ID, &r.Name, &r.Age, &r.City, &r.Email, &r.Score,
			&r.CreatedAt, &r.UpdatedAt, &r.DeletedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (h *rawHarness) count(ctx context.Context, city string) (int64, error) {
	var n int64
	err := h.db.QueryRowContext(ctx, rawCountSQL, city).Scan(&n)
	return n, err
}

func (h *rawHarness) insertOne(ctx context.Context, r benchRow) error {
	_, err := h.db.ExecContext(ctx, rawInsertSQL,
		r.Name, r.Age, r.City, r.Email, r.Score, r.CreatedAt, r.UpdatedAt)
	return err
}

func (h *rawHarness) updateByID(ctx context.Context, r rawRow) error {
	_, err := h.db.ExecContext(ctx, rawUpdateByIDSQL,
		r.Name, r.Age, r.City, r.Email, r.Score, r.UpdatedAt, r.ID)
	return err
}

// ============================================================
// 第 2 层：GORM（对照）
// ============================================================

type gormHarness struct {
	db   *gorm.DB
	rows []benchRow
}

func newGormHarness(tb testing.TB) *gormHarness {
	tb.Helper()
	db, err := gorm.Open(sqlite.Open(sqliteDSN("bench_gorm")), &gorm.Config{
		// 关掉 SQL 日志：写日志是 I/O，会淹没被测代码的开销。
		Logger: logger.Default.LogMode(logger.Silent),
		// 关掉「单条写操作自动包事务」：raw 与 gobreath 都不带事务，
		// 不关的话比的是「GORM 带事务 vs 别人不带事务」，不是同一件事。
		// 想看默认行为的影响，把这行删掉重跑即可。
		SkipDefaultTransaction: true,
	})
	if err != nil {
		tb.Fatalf("gorm: 打开 SQLite 失败：%v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		tb.Fatalf("gorm: 取底层 *sql.DB 失败：%v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	tb.Cleanup(func() { _ = sqlDB.Close() })

	for _, stmt := range []string{createTableSQL, createIndexSQL} {
		if err := db.Exec(stmt).Error; err != nil {
			tb.Fatalf("gorm: 建表失败：%v\nSQL: %s", err, stmt)
		}
	}
	rows := makeRows(seedRowCount)
	seedGorm(tb, db, rows)
	return &gormHarness{db: db, rows: rows}
}

func seedGorm(tb testing.TB, db *gorm.DB, rows []benchRow) {
	tb.Helper()
	users := make([]GormUser, len(rows))
	for i, r := range rows {
		users[i] = GormUser{
			ID: r.ID, Name: r.Name, Age: r.Age, City: r.City,
			Email: r.Email, Score: r.Score, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		}
	}
	// 分批写入，避免逐行 10000 次往返；种子不计时。
	if err := db.CreateInBatches(users, 500).Error; err != nil {
		tb.Fatalf("gorm: 种子数据失败：%v", err)
	}
}

func (h *gormHarness) selectByID(ctx context.Context, id int64) (GormUser, error) {
	var u GormUser
	err := h.db.WithContext(ctx).First(&u, id).Error
	return u, err
}

func (h *gormHarness) listPage(ctx context.Context, city string, minAge, limit, offset int) ([]GormUser, error) {
	var out []GormUser
	err := h.db.WithContext(ctx).
		Where("city = ? AND age >= ?", city, minAge).
		Order("id").Limit(limit).Offset(offset).
		Find(&out).Error
	return out, err
}

func (h *gormHarness) count(ctx context.Context, city string) (int64, error) {
	var n int64
	err := h.db.WithContext(ctx).Model(&GormUser{}).Where("city = ?", city).Count(&n).Error
	return n, err
}

func (h *gormHarness) insertOne(ctx context.Context, r benchRow) error {
	u := GormUser{
		Name: r.Name, Age: r.Age, City: r.City, Email: r.Email,
		Score: r.Score, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	return h.db.WithContext(ctx).Create(&u).Error
}

func (h *gormHarness) updateByID(ctx context.Context, r rawRow) error {
	return h.db.WithContext(ctx).Model(&GormUser{}).Where("id = ?", r.ID).
		Updates(map[string]any{
			"name": r.Name, "age": r.Age, "city": r.City,
			"email": r.Email, "score": r.Score, "updated_at": r.UpdatedAt,
		}).Error
}

// ============================================================
// 第 3 层：gobreath-orm（被测）
// ============================================================

type ormHarness struct {
	db   *orm.DB
	rows []benchRow
}

func newOrmHarness(tb testing.TB) *ormHarness {
	tb.Helper()
	return newOrmHarnessWith(tb, orm.Silent, nil)
}

// newOrmHarnessWith 允许注入日志钩子，仅供 parity 测试打印「真实执行的 SQL」。
//
// 基准测试一律走 newOrmHarness（Silent）：日志是 I/O，开着会淹没被测代码的开销。
func newOrmHarnessWith(tb testing.TB, level orm.LogLevel, lf orm.LogFunc) *ormHarness {
	tb.Helper()
	ctx := context.Background()
	cfg := orm.Config{
		Driver:       sqliteDriverName,
		DSN:          sqliteDSN("bench_orm"),
		MaxOpenConns: 1,
		LogLevel:     level,
	}
	if lf != nil {
		cfg.Logger = lf
	}
	db, err := orm.Open(cfg)
	if err != nil {
		tb.Fatalf("gobreath: 打开 SQLite 失败：%v", err)
	}
	tb.Cleanup(func() { _ = db.SQL().Close() })

	for _, stmt := range []string{createTableSQL, createIndexSQL} {
		if _, err := orm.RawExec(ctx, db, stmt); err != nil {
			tb.Fatalf("gobreath: 建表失败：%v\nSQL: %s", err, stmt)
		}
	}
	rows := makeRows(seedRowCount)
	seedOrm(tb, ctx, db, rows)
	return &ormHarness{db: db, rows: rows}
}

func seedOrm(tb testing.TB, ctx context.Context, db *orm.DB, rows []benchRow) {
	tb.Helper()
	users := make([]User, len(rows))
	for i, r := range rows {
		users[i] = User{
			ID: r.ID, Name: r.Name, Age: r.Age, City: r.City,
			Email: r.Email, Score: r.Score, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		}
	}
	// 分批：一次全塞会撞 SQLite 的变量数上限（9 列 × 10000 行远超 32766）。
	const chunk = 500
	for i := 0; i < len(users); i += chunk {
		end := i + chunk
		if end > len(users) {
			end = len(users)
		}
		if err := orm.BatchInsert(ctx, db, users[i:end]); err != nil {
			tb.Fatalf("gobreath: 种子数据第 %d 批失败：%v", i/chunk, err)
		}
	}
}

func (h *ormHarness) selectByID(ctx context.Context, id int64) (*User, error) {
	return orm.SelectById[User](ctx, h.db, id)
}

func (h *ormHarness) listPage(ctx context.Context, city string, minAge, limit, offset int) ([]User, error) {
	q := orm.NewQuery[User]().
		Eq(orm.Col[User](func(u *User) *string { return &u.City }), city).
		Ge(orm.Col[User](func(u *User) *int { return &u.Age }), minAge).
		OrderBy(orm.Col[User](func(u *User) *int64 { return &u.ID }), true).
		Limit(limit).Offset(offset)
	return orm.SelectList(ctx, h.db, q)
}

func (h *ormHarness) count(ctx context.Context, city string) (int64, error) {
	q := orm.NewQuery[User]().
		Eq(orm.Col[User](func(u *User) *string { return &u.City }), city)
	return orm.Count(ctx, h.db, q)
}

func (h *ormHarness) insertOne(ctx context.Context, r benchRow) error {
	u := User{
		Name: r.Name, Age: r.Age, City: r.City, Email: r.Email,
		Score: r.Score, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	return orm.Insert(ctx, h.db, &u)
}

func (h *ormHarness) updateByID(ctx context.Context, r rawRow) error {
	u := User{
		ID: r.ID, Name: r.Name, Age: r.Age, City: r.City,
		Email: r.Email, Score: r.Score, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	return orm.UpdateById(ctx, h.db, &u)
}
