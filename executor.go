package orm

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Executor 执行 SQL 的最小抽象。*sql.DB 与 *sql.Tx 都实现了该接口，
// 因此同一套 CRUD 既能跑在普通连接上，也能跑在事务里。
type Executor interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// DB 是框架的入口，持有底层执行器、方言、表前缀与日志配置。
type DB struct {
	exec    Executor
	dialect Dialect
	prefix  string // 表前缀（如 "t_"）；仅作用于自动推导的表名，显式指定表名不叠加

	logger        LogFunc       // SQL 日志回调；nil 表示不打印
	logLevel      LogLevel      // 日志等级阈值，默认 Silent
	slowThreshold time.Duration // 慢查询阈值；>0 且超过则按 Warn 输出
	hooks         []Hook        // 可选 SQL 生命周期钩子；未显式配置则为 nil
	readWrite     *readWriteRouter
}

// DataSource 表示一个独立数据库连接源；只有显式配置后才创建并挂载。
type DataSource struct {
	Driver string
	DSN    string
}

// ReadWriteConfig 表示启用读写分离：写入走 Primary，查询走 Replicas（若配置了)；
// 未配置时 DB 仍然保持单库模式，不会自动启用多数据源.
type ReadWriteConfig struct {
	Primary  DataSource
	Replicas []DataSource
}

// MultiSourceConfig 表示一组多个数据源；默认不启用，只有配置了才会建立路由。
type MultiSourceConfig struct {
	Primary  DataSource
	Replicas []DataSource
}

// HookKind 表示当前事件类别。
type HookKind string

const (
	HookKindExec  HookKind = "exec"
	HookKindQuery HookKind = "query"
)

// HookPhase 表示事件发生阶段。
type HookPhase string

const (
	HookPhaseBefore HookPhase = "before"
	HookPhaseAfter  HookPhase = "after"
)

// HookEvent 是框架发给挂钩的事件对象；默认未启用，只有配置了 Hooks 时才会触发。
type HookEvent struct {
	Kind     HookKind
	Phase    HookPhase
	Query    string
	Args     []any
	Duration time.Duration
	Err      error
}

// Hook 是可选的 SQL 生命周期钩子；默认未启用，只有用户在 Config.Hooks 或 db.WithHooks() 中显式注册才生效。
type Hook interface {
	On(event HookEvent)
}

// Config 用于配置数据库连接。推荐方式是按结构体传入，避免参数顺序写错。
type Config struct {
	Driver        string
	DSN           string
	Prefix        string
	Logger        LogFunc
	LogLevel      LogLevel
	SlowThreshold time.Duration
	Hooks         []Hook
	ReadWrite     *ReadWriteConfig
	MultiSource   *MultiSourceConfig
}

// Open 用标准 database/sql 打开连接，并按驱动名选择方言。
// 兼容两种调用方式：
//   - orm.Open("mysql", dsn)
//   - orm.Open(orm.Config{Driver: "mysql", DSN: dsn})
// driver 支持：postgres/pgx → PG；mysql → MySQL；sqlite/sqlite3 → SQLite。
func Open(args ...any) (*DB, error) {
	cfg, err := parseOpenConfig(args...)
	if err != nil {
		return nil, err
	}
	if cfg.Driver == "" {
		return nil, fmt.Errorf("orm: Open() 缺少 Driver 配置")
	}
	if cfg.ReadWrite != nil && cfg.ReadWrite.Primary.Driver == "" && cfg.ReadWrite.Primary.DSN == "" {
		cfg.ReadWrite.Primary = DataSource{Driver: cfg.Driver, DSN: cfg.DSN}
	}
	if cfg.MultiSource != nil && cfg.MultiSource.Primary.Driver == "" && cfg.MultiSource.Primary.DSN == "" {
		cfg.MultiSource.Primary = DataSource{Driver: cfg.Driver, DSN: cfg.DSN}
	}

	primary, err := openDataSource(cfg.Driver, cfg.DSN)
	if err != nil {
		return nil, err
	}

	db := NewDB(primary, dialectForDriver(cfg.Driver))
	if cfg.ReadWrite != nil || cfg.MultiSource != nil {
		routerCfg := cfg.ReadWrite
		if routerCfg == nil {
			routerCfg = &ReadWriteConfig{Primary: cfg.MultiSource.Primary, Replicas: cfg.MultiSource.Replicas}
		}
		router, err := newReadWriteRouter(routerCfg)
		if err != nil {
			return nil, err
		}
		db.readWrite = router
	}
	if cfg.Prefix != "" {
		db = db.WithPrefix(cfg.Prefix)
	}
	if cfg.Logger != nil {
		db = db.WithLogger(cfg.Logger)
	}
	if cfg.LogLevel != 0 {
		db = db.WithLogLevel(cfg.LogLevel)
	}
	if cfg.SlowThreshold != 0 {
		db = db.WithSlowThreshold(cfg.SlowThreshold)
	}
	if len(cfg.Hooks) > 0 {
		db = db.WithHooks(cfg.Hooks...)
	}
	return db, nil
}

func openDataSource(driver, dsn string) (Executor, error) {
	if driver == "" {
		return nil, fmt.Errorf("orm: Open() 缺少 Driver 配置")
	}
	return sql.Open(driver, dsn)
}

func newReadWriteRouter(cfg *ReadWriteConfig) (*readWriteRouter, error) {
	if cfg == nil {
		return nil, nil
	}
	primary := cfg.Primary
	if primary.Driver == "" && primary.DSN == "" {
		return nil, fmt.Errorf("orm: ReadWriteConfig.Primary 不能为空")
	}
	primaryExec, err := openDataSource(primary.Driver, primary.DSN)
	if err != nil {
		return nil, err
	}
	var reads []Executor
	for _, r := range cfg.Replicas {
		exec, err := openDataSource(r.Driver, r.DSN)
		if err != nil {
			return nil, err
		}
		reads = append(reads, exec)
	}
	return &readWriteRouter{primary: primaryExec, replicas: reads}, nil
}

type readWriteRouter struct {
	primary  Executor
	replicas []Executor
	index    int
}

func (r *readWriteRouter) choose(query string) Executor {
	if r == nil {
		return nil
	}
	if len(r.replicas) == 0 || isWriteQuery(query) {
		return r.primary
	}
	if len(r.replicas) == 1 {
		return r.replicas[0]
	}
	r.index = (r.index + 1) % len(r.replicas)
	return r.replicas[r.index]
}

func isWriteQuery(query string) bool {
	trimmed := strings.TrimSpace(strings.ToUpper(query))
	if trimmed == "" {
		return false
	}
	for _, prefix := range []string{"INSERT", "UPDATE", "DELETE", "REPLACE", "CREATE", "ALTER", "DROP", "TRUNCATE", "MERGE", "CALL"} {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

func parseOpenConfig(args ...any) (Config, error) {
	switch len(args) {
	case 1:
		switch c := args[0].(type) {
		case Config:
			return c, nil
		case *Config:
			if c == nil {
				return Config{}, fmt.Errorf("orm: Open() 收到 nil *Config")
			}
			return *c, nil
		default:
			return Config{}, fmt.Errorf("orm: Open() 参数类型不支持 %T，期望 Config 或 (driver string, dsn string)", args[0])
		}
	case 2:
		driver, ok1 := args[0].(string)
		dsn, ok2 := args[1].(string)
		if !ok1 || !ok2 {
			return Config{}, fmt.Errorf("orm: Open() 需要传入 (driver string, dsn string) 或 Config")
		}
		return Config{Driver: driver, DSN: dsn}, nil
	default:
		return Config{}, fmt.Errorf("orm: Open() 期望传入 Config 或 (driver string, dsn string)")
	}
}

func dialectForDriver(d string) Dialect {
	switch d {
	case "mysql":
		return MySQL
	case "sqlite", "sqlite3":
		return SQLite
	default:
		return PG
	}
}

// NewDB 用自定义执行器与方言构造 DB（例如注入 *sql.Tx 做事务）。
func NewDB(exec Executor, d Dialect) *DB {
	return &DB{exec: exec, dialect: d}
}

// WithHooks 返回带额外 SQL 生命周期钩子的 DB 副本；仅在显式注册后才生效。
func (db *DB) WithHooks(hooks ...Hook) *DB {
	cloned := &DB{
		exec:          db.exec,
		dialect:       db.dialect,
		prefix:        db.prefix,
		logger:        db.logger,
		logLevel:      db.logLevel,
		slowThreshold: db.slowThreshold,
		hooks:         append(append([]Hook{}, db.hooks...), hooks...),
	}
	return cloned
}

// WithExecutor 返回使用同一方言但不同底层执行器的 DB（事务里把 *sql.Tx 注入）。
// 日志配置、前缀与钩子一并继承。
func (db *DB) WithExecutor(e Executor) *DB {
	return &DB{
		exec:          e,
		dialect:       db.dialect,
		prefix:        db.prefix,
		logger:        db.logger,
		logLevel:      db.logLevel,
		slowThreshold: db.slowThreshold,
		hooks:         append([]Hook{}, db.hooks...),
		readWrite:     nil,
	}
}

// WithPrefix 返回设置了表前缀的 DB 副本（链式调用，不修改原实例）。
// 例：db := orm.Open(...).WithPrefix("t_")，此后所有 CRUD 自动推导的表名都会带 t_。
func (db *DB) WithPrefix(prefix string) *DB {
	return &DB{
		exec:          db.exec,
		dialect:       db.dialect,
		prefix:        prefix,
		logger:        db.logger,
		logLevel:      db.logLevel,
		slowThreshold: db.slowThreshold,
		hooks:         append([]Hook{}, db.hooks...),
	}
}

// WithLogger 设置 SQL 日志回调（详见 LogFunc 文档）。
func (db *DB) WithLogger(f LogFunc) *DB {
	return &DB{
		exec:          db.exec,
		dialect:       db.dialect,
		prefix:        db.prefix,
		logger:        f,
		logLevel:      db.logLevel,
		slowThreshold: db.slowThreshold,
		hooks:         append([]Hook{}, db.hooks...),
	}
}

// WithLogLevel 设置日志等级阈值（Silent / Info / Warn / Error），默认 Silent（不打印）。
func (db *DB) WithLogLevel(l LogLevel) *DB {
	return &DB{
		exec:          db.exec,
		dialect:       db.dialect,
		prefix:        db.prefix,
		logger:        db.logger,
		logLevel:      l,
		slowThreshold: db.slowThreshold,
		hooks:         append([]Hook{}, db.hooks...),
	}
}

// WithSlowThreshold 设置慢查询阈值；执行耗时超过它（且 >0）时按 Warn 级别输出。
func (db *DB) WithSlowThreshold(d time.Duration) *DB {
	return &DB{
		exec:          db.exec,
		dialect:       db.dialect,
		prefix:        db.prefix,
		logger:        db.logger,
		logLevel:      db.logLevel,
		slowThreshold: d,
		hooks:         append([]Hook{}, db.hooks...),
	}
}

// Transaction 在事务中执行 fn；fn 内使用传入的 tx *DB（已绑定 *sql.Tx，且继承日志配置与前缀）。
// fn 返回 error 时自动回滚，否则提交。
func (db *DB) Transaction(ctx context.Context, fn func(tx *DB) error) error {
	beginner, ok := db.exec.(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	})
	if !ok {
		return fmt.Errorf("orm: 底层执行器 %T 不支持事务", db.exec)
	}
	tx, err := beginner.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(db.WithExecutor(tx)); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ---- SQL 日志 ----

// logSlowOrErr 根据执行结果决定日志事件严重度并交给 logger 输出。
// 严重度：普通成功 = Info；慢查询（超过 slowThreshold）= Warn；执行出错 = Error。
func (db *DB) logSlowOrErr(query string, args []any, dur time.Duration, err error) {
	if db.logLevel == Silent || db.logger == nil {
		return
	}
	severity := Info
	if err != nil {
		severity = Error
	} else if db.slowThreshold > 0 && dur > db.slowThreshold {
		severity = Warn
	}
	if severity < db.logLevel {
		return
	}
	db.logger(severity, query, args, dur, err)
}

func (db *DB) notifyHooks(event HookEvent) {
	for _, h := range db.hooks {
		if h != nil {
			h.On(event)
		}
	}
}

// execContext 执行写操作并记录日志（包装底层 Executor.ExecContext）。
func (db *DB) execContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if len(db.hooks) > 0 {
		db.notifyHooks(HookEvent{Kind: HookKindExec, Phase: HookPhaseBefore, Query: query, Args: append([]any(nil), args...)})
	}
	start := time.Now()
	res, err := db.exec.ExecContext(ctx, query, args...)
	duration := time.Since(start)
	db.logSlowOrErr(query, args, duration, err)
	if len(db.hooks) > 0 {
		db.notifyHooks(HookEvent{Kind: HookKindExec, Phase: HookPhaseAfter, Query: query, Args: append([]any(nil), args...), Duration: duration, Err: err})
	}
	return res, err
}

// queryContext 执行查询并记录日志（包装底层 Executor.QueryContext）。
func (db *DB) queryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if len(db.hooks) > 0 {
		db.notifyHooks(HookEvent{Kind: HookKindQuery, Phase: HookPhaseBefore, Query: query, Args: append([]any(nil), args...)})
	}
	start := time.Now()
	rows, err := db.exec.QueryContext(ctx, query, args...)
	duration := time.Since(start)
	db.logSlowOrErr(query, args, duration, err)
	if len(db.hooks) > 0 {
		db.notifyHooks(HookEvent{Kind: HookKindQuery, Phase: HookPhaseAfter, Query: query, Args: append([]any(nil), args...), Duration: duration, Err: err})
	}
	return rows, err
}
