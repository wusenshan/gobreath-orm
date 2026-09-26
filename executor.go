package orm

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Executor 执行 SQL 的最小抽象。*sql.DB 与 *sql.Tx 都实现了该接口，
// 因此同一套 CRUD 既能跑在普通连接上，也能跑在事务里。
type Executor interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// DB 是框架的入口，持有底层执行器、方言、表前缀与日志配置。
type DB struct {
	exec    Executor
	dialect Dialect
	prefix  string // 表前缀（如 "t_"）；仅作用于自动推导的表名，显式指定表名不叠加

	logger          LogFunc       // SQL 日志回调；nil 表示不打印
	logLevel        LogLevel      // 日志等级阈值，默认 Silent
	slowThreshold   time.Duration // 慢查询阈值；>0 且超过则按 Warn 输出
	hooks           []Hook        // 可选 SQL 生命周期钩子；未显式配置则为 nil
	readWrite       *readWriteRouter
	softDeleteField string // 约定软删除字段名；实体未用 ,logic tag 声明时按此字段名匹配（列名或 Go 字段名）
	optimisticField string // 约定乐观锁字段名；实体未用 ,version tag 声明时按此字段名匹配（列名或 Go 字段名）
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
// 连接池参数为零值时不设置，保持 database/sql 默认行为
// （MaxIdleConns 想显式设为 0 时请通过 db.SQL().SetMaxIdleConns(0) 设置）。
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

	MaxOpenConns    int           // 最大打开连接数（0 = 不限制，默认）
	MaxIdleConns    int           // 最大空闲连接数（0 = 保持默认值 2）
	ConnMaxLifetime time.Duration // 连接最长存活时间（0 = 永不回收，默认；连 MySQL 建议小于 wait_timeout）
	ConnMaxIdleTime time.Duration // 连接最长空闲时间（0 = 永不回收，默认）

	// SoftDeleteField 约定软删除字段名（如 "deleted_at" 或 "deleted"）。
	// 实体未用 db:"...,logic" tag 显式声明时，只要存在列名或 Go 字段名
	// 等于该值的字段、且类型为 time/int/bool，即自动启用软删除。
	// 单表优先级：,logic tag 显式声明 > 本约定；不匹配或类型不支持则物理删除；
	// 可用 ,nologic tag 显式退出约定匹配。
	SoftDeleteField string

	// OptimisticField 约定乐观锁字段名（如 "version" 或 "revision"）。
	// 实体未用 db:"...,version" tag 显式声明时，只要存在列名或 Go 字段名
	// 等于该值的字段即自动启用乐观锁：UpdateById / UpdateByIdSets 会追加
	// "WHERE version = ?" 并在 SET 中 "version = version + 1"，
	// 受影响行数为 0 时返回 ErrOptimisticLock。
	OptimisticField string

	// StrictTagCheck 严格校验 struct tag 格式（默认 false）。
	// 开启后，模型解析阶段会校验 db tag 是否使用标准引号格式 `db:"col,pk"`；
	// 若写成无引号的 `db:col,pk`，reflect 读不到 db key 会导致 pk/autoincrement
	// 等修饰符静默丢失（自增主键被写入 0 值且不报错），此时直接 panic 暴露问题。
	// 默认关闭以兼容旧行为；一旦任一 Open 开启，全局进入严格模式（越严越安全）。
	StrictTagCheck bool
}

// Open 用标准 database/sql 打开连接，并按驱动名选择方言。
// 兼容两种调用方式：
//   - orm.Open("mysql", dsn)
//   - orm.Open(orm.Config{Driver: "mysql", DSN: dsn})
//
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
	if sqlDB, ok := primary.(*sql.DB); ok {
		if cfg.MaxOpenConns != 0 {
			sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
		}
		if cfg.MaxIdleConns != 0 {
			sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
		}
		if cfg.ConnMaxLifetime != 0 {
			sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
		}
		if cfg.ConnMaxIdleTime != 0 {
			sqlDB.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
		}
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
	} else if cfg.LogLevel != Silent {
		db = db.WithLogger(DefaultLogger(nil))
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
	if cfg.SoftDeleteField != "" {
		db = db.WithSoftDeleteField(cfg.SoftDeleteField)
	}
	if cfg.OptimisticField != "" {
		db = db.WithOptimisticField(cfg.OptimisticField)
	}
	if cfg.StrictTagCheck {
		strictTagCheck.Store(true)
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
	mu       sync.Mutex
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
	r.mu.Lock()
	r.index = (r.index + 1) % len(r.replicas)
	next := r.replicas[r.index]
	r.mu.Unlock()
	return next
}

// isWriteQuery 判断一条 SQL 是否必须走主库（写语句或悲观锁读）。
//
// 判定前会先剥掉前导空白与注释：此前直接对原文 TrimSpace + ToUpper 取前缀，
// `/* trace */ UPDATE ...`、`-- x\nDELETE ...`、`# comment\nINSERT ...` 这类带注释的
// 写法全部被判成「读」并路由到只读副本，写操作在副本上直接报只读错误。
// CTE（WITH ... AS (...) INSERT/UPDATE/DELETE）无法靠前缀廉价区分，
// 一律保守判为写：宁可多走主库，也不能把写语句发到只读副本（与上方 FOR UPDATE 同口径）。
func isWriteQuery(query string) bool {
	trimmed := strings.TrimSpace(strings.ToUpper(stripLeadingComments(query)))
	if trimmed == "" {
		return false
	}
	// 悲观锁读（SELECT ... FOR UPDATE / FOR SHARE 等）必须在主库执行，
	// 否则会被路由到只读副本导致锁失效或报错。框架仅生成 FOR UPDATE，
	// 这里按子串兜底覆盖 FOR UPDATE / FOR SHARE / FOR KEY SHARE / FOR NO KEY UPDATE。
	if strings.Contains(trimmed, " FOR UPDATE") ||
		strings.Contains(trimmed, " FOR SHARE") ||
		strings.Contains(trimmed, " FOR KEY SHARE") ||
		strings.Contains(trimmed, " FOR NO KEY UPDATE") {
		return true
	}
	// WITH ... 既可能是只读 CTE（`WITH x AS (SELECT ...) SELECT ...`），
	// 也可能是 CTE + 写（`WITH x AS (SELECT ...) UPDATE ...`），前缀无法区分。
	// 做一次词法扫描：CTE 内部或主句任一处出现写关键字即判写 —— 只看主句会漏掉
	// PG 的 data-modifying CTE（`WITH m AS (DELETE ... RETURNING *) INSERT ...`，
	// 写在 CTE 里、主句仍是 SELECT）。扫描本身不可靠时（字符串 / 注释未闭合）
	// 同样保守判写：宁可多走主库，也不能把写语句发到只读副本。
	if strings.HasPrefix(trimmed, "WITH") {
		hasWrite, reliable := withStatementHasWrite(trimmed)
		return !reliable || hasWrite
	}
	for _, prefix := range []string{"INSERT", "UPDATE", "DELETE", "REPLACE", "CREATE", "ALTER", "DROP", "TRUNCATE", "MERGE", "CALL"} {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

// stripLeadingComments 去掉 SQL 开头连续的空白与注释（/* ... */、-- ... 行注释、
// MySQL 的 # 行注释），返回第一条真实语句的起始内容（大小写保持原样）。
//
// 只处理**开头**：读写路由只需要看清语句的第一个关键字，不做全文扫描。
// 注释未闭合时返回空串（这类语句一定执行失败，交由数据库报错，此处不猜语义）。
func stripLeadingComments(query string) string {
	s := query
	for {
		s = strings.TrimLeft(s, " \t\r\n\f\v")
		switch {
		case strings.HasPrefix(s, "/*"):
			end := strings.Index(s[2:], "*/")
			if end < 0 {
				return ""
			}
			s = s[2+end+2:]
		case strings.HasPrefix(s, "--"), strings.HasPrefix(s, "#"):
			if nl := strings.IndexAny(s, "\r\n"); nl >= 0 {
				s = s[nl+1:]
			} else {
				return ""
			}
		default:
			return s
		}
	}
}

// writeKeywords 是「出现在语句里即说明该语句有写副作用」的关键字（大写形式）。
// 仅用于 CTE（WITH ...）语句的全文检索：普通语句靠前缀判定即可，
// 不必承担全文检索的误判代价（误判的代价是只读查询被送到主库）。
var writeKeywords = []string{
	"INSERT", "UPDATE", "DELETE", "REPLACE", "MERGE",
	"CREATE", "ALTER", "DROP", "TRUNCATE", "CALL",
}

// withStatementHasWrite 判断一条 WITH（CTE）语句是否含有写操作。
//
// 返回 (是否含写, 扫描是否可靠)。可靠 = 语句里的字符串字面量、引号标识符与注释
// 全部正常闭合；遇到未闭合的情况返回 reliable=false，调用方必须保守按写处理
// （这类语句多半本来就执行失败，此处不猜语义）。
//
// 检索以「词」为单位而非子串，因此两类经典误判都不成立：
//   - `SELECT insert_count FROM t`：insert_count 是一个完整的标识符词，与 INSERT 不相等；
//   - `WHERE name = 'UPDATE'`：UPDATE 在字符串字面量内，扫描时整段跳过。
//
// 同理，PG 的 "INSERT_LOG"、MySQL 的反引号形式这类引号标识符也整段跳过。
func withStatementHasWrite(s string) (hasWrite, reliable bool) {
	n := len(s)
	for i := 0; i < n; {
		c := s[i]
		switch {
		case c == '\'':
			// 字符串字面量；'' 表示字面量内部的一个引号，不是结束。
			i++
			closed := false
			for i < n {
				if s[i] == '\'' {
					if i+1 < n && s[i+1] == '\'' {
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				i++
			}
			if !closed {
				return false, false
			}
		case c == '"' || c == '`':
			// 引号标识符（PG "…"、MySQL `…`）：内部可能含写关键字，整段跳过。
			i++
			closed := false
			for i < n {
				if s[i] == c {
					if i+1 < n && s[i+1] == c { // ANSI 的双写转义
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				i++
			}
			if !closed {
				return false, false
			}
		case c == '-' && i+1 < n && s[i+1] == '-', c == '#':
			// 行注释（-- 与 MySQL 的 #）：跳到行尾。
			for i < n && s[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && s[i+1] == '*':
			// 块注释；未闭合即不可靠。
			i += 2
			closed := false
			for i+1 < n {
				if s[i] == '*' && s[i+1] == '/' {
					i += 2
					closed = true
					break
				}
				i++
			}
			if !closed {
				return false, false
			}
		default:
			if !isSQLWordChar(c) {
				i++
				continue
			}
			start := i
			for i < n && isSQLWordChar(s[i]) {
				i++
			}
			word := s[start:i]
			for _, kw := range writeKeywords {
				if word == kw {
					return true, true
				}
			}
		}
	}
	return false, true
}

// isSQLWordChar 判断字节是否属于 SQL 标识符 / 关键字的组成部分。
// 调用方传入的语句已经 ToUpper，故只需覆盖大写字母、数字、下划线与 $。
func isSQLWordChar(c byte) bool {
	return c == '_' || c == '$' ||
		(c >= 'A' && c <= 'Z') ||
		(c >= 'a' && c <= 'z') ||
		(c >= '0' && c <= '9')
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
		exec:            db.exec,
		dialect:         db.dialect,
		prefix:          db.prefix,
		logger:          db.logger,
		logLevel:        db.logLevel,
		slowThreshold:   db.slowThreshold,
		hooks:           append(append([]Hook{}, db.hooks...), hooks...),
		readWrite:       db.readWrite,
		softDeleteField: db.softDeleteField,
		optimisticField: db.optimisticField,
	}
	return cloned
}

// WithExecutor 返回使用同一方言但不同底层执行器的 DB（事务里把 *sql.Tx 注入）。
// 日志配置、前缀与钩子一并继承。
func (db *DB) WithExecutor(e Executor) *DB {
	return &DB{
		exec:            e,
		dialect:         db.dialect,
		prefix:          db.prefix,
		logger:          db.logger,
		logLevel:        db.logLevel,
		slowThreshold:   db.slowThreshold,
		hooks:           append([]Hook{}, db.hooks...),
		readWrite:       nil,
		softDeleteField: db.softDeleteField,
		optimisticField: db.optimisticField,
	}
}

// WithPrefix 返回设置了表前缀的 DB 副本（链式调用，不修改原实例）。
// 例：db := orm.Open(...).WithPrefix("t_")，此后所有 CRUD 自动推导的表名都会带 t_。
func (db *DB) WithPrefix(prefix string) *DB {
	return &DB{
		exec:            db.exec,
		dialect:         db.dialect,
		prefix:          prefix,
		logger:          db.logger,
		logLevel:        db.logLevel,
		slowThreshold:   db.slowThreshold,
		hooks:           append([]Hook{}, db.hooks...),
		readWrite:       db.readWrite,
		softDeleteField: db.softDeleteField,
		optimisticField: db.optimisticField,
	}
}

// WithSoftDeleteField 设置约定软删除字段名（链式调用，不修改原实例）。
// 实体未用 ,logic tag 声明时，列名或 Go 字段名等于该值的 time/int/bool 字段
// 自动启用软删除；详见 Config.SoftDeleteField 文档。
func (db *DB) WithSoftDeleteField(name string) *DB {
	return &DB{
		exec:            db.exec,
		dialect:         db.dialect,
		prefix:          db.prefix,
		logger:          db.logger,
		logLevel:        db.logLevel,
		slowThreshold:   db.slowThreshold,
		hooks:           append([]Hook{}, db.hooks...),
		readWrite:       db.readWrite,
		softDeleteField: name,
		optimisticField: db.optimisticField,
	}
}

// WithOptimisticField 设置约定乐观锁字段名（链式调用，不修改原实例）。
// 实体未用 ,version tag 声明时，列名或 Go 字段名等于该值的字段
// 自动启用乐观锁；详见 Config.OptimisticField 文档。
func (db *DB) WithOptimisticField(name string) *DB {
	return &DB{
		exec:            db.exec,
		dialect:         db.dialect,
		prefix:          db.prefix,
		logger:          db.logger,
		logLevel:        db.logLevel,
		slowThreshold:   db.slowThreshold,
		hooks:           append([]Hook{}, db.hooks...),
		readWrite:       db.readWrite,
		softDeleteField: db.softDeleteField,
		optimisticField: name,
	}
}

// WithLogger 设置 SQL 日志回调（详见 LogFunc 文档）。
func (db *DB) WithLogger(f LogFunc) *DB {
	return &DB{
		exec:            db.exec,
		dialect:         db.dialect,
		prefix:          db.prefix,
		logger:          f,
		logLevel:        db.logLevel,
		slowThreshold:   db.slowThreshold,
		hooks:           append([]Hook{}, db.hooks...),
		readWrite:       db.readWrite,
		softDeleteField: db.softDeleteField,
		optimisticField: db.optimisticField,
	}
}

// WithLogLevel 设置日志等级阈值（Silent / Info / Warn / Error），默认 Silent（不打印）。
func (db *DB) WithLogLevel(l LogLevel) *DB {
	return &DB{
		exec:            db.exec,
		dialect:         db.dialect,
		prefix:          db.prefix,
		logger:          db.logger,
		logLevel:        l,
		slowThreshold:   db.slowThreshold,
		hooks:           append([]Hook{}, db.hooks...),
		readWrite:       db.readWrite,
		softDeleteField: db.softDeleteField,
		optimisticField: db.optimisticField,
	}
}

// WithSlowThreshold 设置慢查询阈值；执行耗时超过它（且 >0）时按 Warn 级别输出。
func (db *DB) WithSlowThreshold(d time.Duration) *DB {
	return &DB{
		exec:            db.exec,
		dialect:         db.dialect,
		prefix:          db.prefix,
		logger:          db.logger,
		logLevel:        db.logLevel,
		slowThreshold:   d,
		hooks:           append([]Hook{}, db.hooks...),
		readWrite:       db.readWrite,
		softDeleteField: db.softDeleteField,
		optimisticField: db.optimisticField,
	}
}

// SQL 返回底层的 *sql.DB，用于设置连接池参数（SetMaxOpenConns 等）、
// 获取统计信息（Stats()）或 Ping 验活等逃逸操作。
// 底层不是 *sql.DB（如事务副本绑定 *sql.Tx）时返回 nil。
func (db *DB) SQL() *sql.DB {
	if sqlDB, ok := db.exec.(*sql.DB); ok {
		return sqlDB
	}
	return nil
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
	// fn panic 时必须回滚：此前没有 defer 兜底，panic 会让已开启的事务既不提交也不回滚，
	// 行锁与未提交写入滞留在连接上（只能等 *sql.Tx 被 GC 回收才释放），
	// 连接池较小的服务会因此逐步耗尽连接、甚至拖住整张表的写。
	// 这里 recover 后回滚，再把原 panic 抛出去，调用方的 panic 语义（含各自的 recover）保持可见。
	defer func() {
		if r := recover(); r != nil {
			_ = tx.Rollback()
			panic(r)
		}
	}()
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
	exec := db.exec
	if db.readWrite != nil {
		if e := db.readWrite.choose(query); e != nil {
			exec = e
		}
	}
	if len(db.hooks) > 0 {
		db.notifyHooks(HookEvent{Kind: HookKindExec, Phase: HookPhaseBefore, Query: query, Args: append([]any(nil), args...)})
	}
	start := time.Now()
	res, err := exec.ExecContext(ctx, query, args...)
	duration := time.Since(start)
	db.logSlowOrErr(query, args, duration, err)
	if len(db.hooks) > 0 {
		db.notifyHooks(HookEvent{Kind: HookKindExec, Phase: HookPhaseAfter, Query: query, Args: append([]any(nil), args...), Duration: duration, Err: err})
	}
	return res, err
}

// queryContext 执行查询并记录日志（包装底层 Executor.QueryContext）。
func (db *DB) queryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	exec := db.exec
	if db.readWrite != nil {
		if e := db.readWrite.choose(query); e != nil {
			exec = e
		}
	}
	if len(db.hooks) > 0 {
		db.notifyHooks(HookEvent{Kind: HookKindQuery, Phase: HookPhaseBefore, Query: query, Args: append([]any(nil), args...)})
	}
	start := time.Now()
	rows, err := exec.QueryContext(ctx, query, args...)
	duration := time.Since(start)
	db.logSlowOrErr(query, args, duration, err)
	if len(db.hooks) > 0 {
		db.notifyHooks(HookEvent{Kind: HookKindQuery, Phase: HookPhaseAfter, Query: query, Args: append([]any(nil), args...), Duration: duration, Err: err})
	}
	return rows, err
}

// queryRowContext 执行仅返回单行的查询（如 INSERT ... RETURNING）并记录日志。
// 语义上仍按写操作（HookKindExec）上报，因为调用方用它做主键回填而非读取数据。
// QueryRowContext 不在调用点返回 error，错误延迟到 Scan；此处按 nil 记录，由调用方捕获 Scan 错误。
func (db *DB) queryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	exec := db.exec
	if db.readWrite != nil {
		if e := db.readWrite.choose(query); e != nil {
			exec = e
		}
	}
	if len(db.hooks) > 0 {
		db.notifyHooks(HookEvent{Kind: HookKindExec, Phase: HookPhaseBefore, Query: query, Args: append([]any(nil), args...)})
	}
	start := time.Now()
	row := exec.QueryRowContext(ctx, query, args...)
	duration := time.Since(start)
	db.logSlowOrErr(query, args, duration, nil)
	if len(db.hooks) > 0 {
		db.notifyHooks(HookEvent{Kind: HookKindExec, Phase: HookPhaseAfter, Query: query, Args: append([]any(nil), args...), Duration: duration, Err: nil})
	}
	return row
}
