package integration

import (
	"context"
	"os"
	"testing"
	"time"

	orm "github.com/wusenshan/gobreath-orm"

	// 驱动由本模块（而非框架）导入，主模块保持零依赖。
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	// lib/pq 与 pgx 都是 README 里公开列出的 PG 驱动，且同为 database/sql 路径
	// （driver 名 "postgres"）。两条必须都真库验证：两者交给 database/sql 的
	// **值类型并不一致**（见 TestMain 里的说明）。
	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------- 后端

// backend 描述一个可真跑的数据库。
type backend struct {
	name    string
	driver  string
	dsn     string
	dialect orm.Dialect
	// dsnEnv 是提供本后端 DSN 的环境变量名，仅用于报错提示。
	// 同一个环境变量可以支撑多个后端（PG 的 pgx / lib/pq 共用一个 DSN），
	// 所以不能用 name 去拼，否则提示会指向一个不存在的变量名。
	dsnEnv string
	// maxOpen > 0 时同时限制最大连接数（SQLite 内存库必须为 1，
	// 否则每条连接看到的是各自独立的库，建的表互相不可见）。
	maxOpen int
}

var backends []backend

func TestMain(m *testing.M) {
	// SQLite 恒可用：纯 Go 驱动 + 共享内存库，零安装、零残留。
	backends = []backend{{
		name:    "sqlite",
		driver:  "sqlite",
		dsn:     "file:gobreath_itest?mode=memory&cache=shared",
		dialect: orm.SQLite,
		maxOpen: 1,
	}}
	// PG / MySQL 需显式提供 DSN：没设就跳过，避免 CI 上误报。
	if dsn := os.Getenv("ORM_IT_PG_DSN"); dsn != "" {
		// 同一个 DSN 挂两条后端，把 README 里公开承诺的**两条 PG 驱动路径**都真库走一遍。
		// 必要性：两者虽同为 database/sql，交给上层 scan 的**值类型并不一致** ——
		//   · 文本列：pgx 给 string，lib/pq 给 []byte
		//   · numeric / uuid / 数组等：lib/pq 一律给 []byte（或 string），不转成数值
		//   · pgvector 的 vector 列：pgx 给 string，lib/pq 给 []byte
		// 框架的 scan 分派（model.go 的 setterFor / setVectorField）必须同时吃下这两种形态。
		// 只挂 pgx 等于只验了一半，而 README 主示例给的恰恰是 lib/pq。
		// 两者共用同一台库、顺序执行（无 t.Parallel），resetSchema 会互相清场，不冲突。
		backends = append(backends,
			backend{name: "postgres", driver: "pgx", dsn: dsn, dialect: orm.PG, dsnEnv: "ORM_IT_PG_DSN"},
			backend{name: "postgres-libpq", driver: "postgres", dsn: dsn, dialect: orm.PG, dsnEnv: "ORM_IT_PG_DSN"},
		)
	}
	if dsn := os.Getenv("ORM_IT_MYSQL_DSN"); dsn != "" {
		backends = append(backends, backend{
			name: "mysql", driver: "mysql", dsn: dsn, dialect: orm.MySQL, dsnEnv: "ORM_IT_MYSQL_DSN",
		})
	}
	os.Exit(m.Run())
}

// openDB 打开连接。注意：DSN 由环境变量提供，连不上属于**配置错误**，
// 因此直接 Fail 而不是 Skip —— 静默跳过会掩盖真实问题。
func openDB(t *testing.T, b backend) *orm.DB {
	t.Helper()
	cfg := orm.Config{Driver: b.driver, DSN: b.dsn}
	if b.maxOpen > 0 {
		cfg.MaxOpenConns = b.maxOpen
		cfg.MaxIdleConns = b.maxOpen
	}
	db, err := orm.Open(cfg)
	if err != nil {
		t.Fatalf("[%s] Open 失败：%v", b.name, err)
	}
	sqlDB := db.SQL()
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := sqlDB.Ping(); err != nil {
		hint := b.dsnEnv
		if hint == "" {
			hint = "DSN 环境变量"
		}
		t.Fatalf("[%s] Ping 失败（检查 %s）：%v", b.name, hint, err)
	}
	return db
}

// ensureVectorExtension 尽力为 PostgreSQL 后端启用 pgvector 扩展。
//
// 为什么放在测试里而不是靠 initdb 脚本：CI 用 `services:` 起容器时**没有**挂载
// `initdb/*.sql` 的机会（工作区在容器启动时还不存在），把这一步收进测试自身，
// 「DSN 指向一个 pgvector 容器」就足以跑完整套用例，不依赖任何外部初始化。
// 已建过是秒过的 no-op；权限不足只记录不中断（当前用例集不依赖它，
// 将来加向量用例时会自然在那里明确报错）。
func ensureVectorExtension(t *testing.T, db *orm.DB, b backend) {
	t.Helper()
	if b.dialect != orm.PG {
		return
	}
	if _, err := orm.RawExec(context.Background(), db, "CREATE EXTENSION IF NOT EXISTS vector"); err != nil {
		t.Logf("[%s] pgvector 扩展未能启用（当前用例集不依赖它）：%v", b.name, err)
	}
}

// forEachBackend 在每个可用后端下各跑一遍 fn。
func forEachBackend(t *testing.T, fn func(t *testing.T, db *orm.DB, b backend)) {
	t.Helper()
	for i := range backends {
		b := backends[i]
		t.Run(b.name, func(t *testing.T) {
			db := openDB(t, b)
			ensureVectorExtension(t, db, b)
			resetSchema(t, db)
			fn(t, db, b)
		})
	}
}

// ---------------------------------------------------------------- 模型

// User 一次覆盖五类列：自增主键、字符串、整数、浮点、JSON、时间、软删除。
type User struct {
	ID        int64          `db:"id,pk,autoincrement"`
	Name      string         `db:"name"`
	Age       int            `db:"age"`
	Score     float64        `db:"score"`
	City      string         `db:"city"`
	Meta      map[string]any `db:"meta,json"`
	CreatedAt time.Time      `db:"created_at"`
	DeletedAt *time.Time     `db:"deleted_at,logic"`
}

func (User) TableName() string { return "it_users" }

// Order 用于验证 JOIN 计数与聚合（Count 漏 JOIN 的回归正是靠它暴露的）。
type Order struct {
	ID     int64  `db:"id,pk,autoincrement"`
	UserID int64  `db:"user_id"`
	Amount int64  `db:"amount"`
	Status string `db:"status"`
}

func (Order) TableName() string { return "it_orders" }

// RelUser 与 User 同一张表，仅多一条 has_many 关系声明，供 Preload 使用。
type RelUser struct {
	ID    int64  `db:"id,pk,autoincrement"`
	Name  string `db:"name"`
	Posts []Post `db:"-" orm:"has_many;fk:user_id"`
}

func (RelUser) TableName() string { return "it_users" }

type Post struct {
	ID     int64  `db:"id,pk,autoincrement"`
	UserID int64  `db:"user_id"`
	Title  string `db:"title"`
}

func (Post) TableName() string { return "it_posts" }

// Comment / Account 验证 belongs_to（子表持外键指向父表）。
type Comment struct {
	ID     int64    `db:"id,pk,autoincrement"`
	Body   string   `db:"body"`
	UserID int64    `db:"user_id"`
	Author *Account `db:"-" orm:"belongs_to;fk:user_id"`
}

func (Comment) TableName() string { return "it_comments" }

type Account struct {
	ID   int64  `db:"id,pk,autoincrement"`
	Name string `db:"name"`
}

func (Account) TableName() string { return "it_accounts" }

// Doc 验证乐观锁：version 列由 ,version tag 声明。
type Doc struct {
	ID      int64  `db:"id,pk,autoincrement"`
	Title   string `db:"title"`
	Version int    `db:"version,version"`
}

func (Doc) TableName() string { return "it_docs" }

// PrefUser 故意**不**实现 TableName()，用于验证表前缀只作用于自动推导的表名。
// 推导链路：PrefUser → pref_user → 复数 pref_users → 加前缀 t_pref_users。
type PrefUser struct {
	ID   int64  `db:"id,pk,autoincrement"`
	Name string `db:"name"`
}

// IdxUser 验证 db tag 的 ,index 修饰符能否被 AutoMigrate 正确建索引。
type IdxUser struct {
	ID    int64  `db:"id,pk,autoincrement"`
	Email string `db:"email,index"`
}

func (IdxUser) TableName() string { return "it_idx_users" }

// ZeroUser 供「批量写入 + OmitZero」的真库取证：主键非自增（显式指定，
// 便于逐行比对），name / age 都允许零值 —— 「首行全零、后续行非零」
// 正是旧实现静默丢数据的触发条件。
type ZeroUser struct {
	ID   int64  `db:"id,pk"`
	Name string `db:"name"`
	Age  int    `db:"age"`
}

func (ZeroUser) TableName() string { return "it_zero_users" }

// TypedUser / TypedNote 验证 Preload 的键比较：父表主键（int64）与子表外键（int）
// 在 Go 侧是**不同的整数类型**，数据库里的值却完全相同。旧实现用 reflect.DeepEqual
// 比较键，int(1) != int64(1)，关联被整片漏掉且不报任何错。
type TypedUser struct {
	ID    int64       `db:"id,pk"`
	Name  string      `db:"name"`
	Notes []TypedNote `db:"-" orm:"has_many;fk:user_id"`
}

func (TypedUser) TableName() string { return "it_typed_users" }

type TypedNote struct {
	ID     int64  `db:"id,pk"`
	UserID int    `db:"user_id"`
	Body   string `db:"body"`
}

func (TypedNote) TableName() string { return "it_typed_notes" }

// ---------------------------------------------------------------- 列表达式

var (
	uID        = orm.Col[User](func(u *User) *int64 { return &u.ID })
	uName      = orm.Col[User](func(u *User) *string { return &u.Name })
	uAge       = orm.Col[User](func(u *User) *int { return &u.Age })
	uScore     = orm.Col[User](func(u *User) *float64 { return &u.Score })
	uCity      = orm.Col[User](func(u *User) *string { return &u.City })
	uMeta      = orm.Col[User](func(u *User) *map[string]any { return &u.Meta })
	uCreatedAt = orm.Col[User](func(u *User) *time.Time { return &u.CreatedAt })

	// 强类型列（带字段类型 F），供 Pluck / SumOf / MaxOf 直接推导返回类型。
	uNameT    = orm.TCol(func(u *User) *string { return &u.Name })
	uAgeT     = orm.TCol(func(u *User) *int { return &u.Age })
	uScoreT   = orm.TCol(func(u *User) *float64 { return &u.Score })
	uCreatedT = orm.TCol(func(u *User) *time.Time { return &u.CreatedAt })

	oUserID = orm.Col[Order](func(o *Order) *int64 { return &o.UserID })
	oAmount = orm.Col[Order](func(o *Order) *int64 { return &o.Amount })
	oStatus = orm.Col[Order](func(o *Order) *string { return &o.Status })

	ruName = orm.Col[RelUser](func(u *RelUser) *string { return &u.Name })
	ruID   = orm.Col[RelUser](func(u *RelUser) *int64 { return &u.ID })

	cUserID = orm.Col[Comment](func(c *Comment) *int64 { return &c.UserID })
	pUserID = orm.Col[Post](func(p *Post) *int64 { return &p.UserID })
	pTitle  = orm.Col[Post](func(p *Post) *string { return &p.Title })
)

// ---------------------------------------------------------------- 建表 / 造数

var itTables = []string{
	"it_users", "it_orders", "it_posts", "it_comments", "it_accounts",
	"it_docs", "it_idx_users", "it_uq_users", "t_pref_users",
	"it_zero_users", "it_typed_users", "it_typed_notes",
}

func resetSchema(t *testing.T, db *orm.DB) {
	t.Helper()
	ctx := context.Background()
	for _, tb := range itTables {
		if _, err := orm.RawExec(ctx, db, "DROP TABLE IF EXISTS "+tb); err != nil {
			t.Fatalf("DROP %s 失败：%v", tb, err)
		}
	}
	if err := db.AutoMigrate(ctx, &User{}, &Order{}, &Post{}, &Comment{}, &Account{}, &Doc{}, &UqUser{}, &ZeroUser{}, &TypedUser{}, &TypedNote{}); err != nil {
		t.Fatalf("AutoMigrate 失败：%v", err)
	}
	// 前缀只作用于「自动推导」的表名，所以 PrefUser 要挂在前缀 DB 上建。
	if err := db.WithPrefix("t_").AutoMigrate(ctx, &PrefUser{}); err != nil {
		t.Fatalf("AutoMigrate(带前缀) 失败：%v", err)
	}
}

// aggregateSeed 是聚合断言的基准数据（4 行，取值刻意选成二进制可精确表示，
// 避免浮点累加误差让断言变脆）：
//
//	age   : 30 25 35 25 → SUM=115 AVG=28.75 MAX=35 MIN=25
//	score : 90.5 80 70.25 60 → SUM=300.75 AVG=75.1875 MAX=90.5 MIN=60
//	city  : bj sh bj gz（BJ 两行，供分组/去重类断言）
var baseTime = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func seedUsers(t *testing.T, db *orm.DB) []User {
	t.Helper()
	ctx := context.Background()
	rows := []User{
		{Name: "alice", Age: 30, Score: 90.5, City: "bj",
			Meta: map[string]any{"level": 3.0, "tag": "hot"}, CreatedAt: baseTime.Add(1 * time.Hour)},
		{Name: "bob", Age: 25, Score: 80, City: "sh",
			Meta: map[string]any{"level": 1.0, "tag": "cold"}, CreatedAt: baseTime.Add(2 * time.Hour)},
		{Name: "carol", Age: 35, Score: 70.25, City: "bj",
			Meta: map[string]any{"level": 2.0, "tag": "hot"}, CreatedAt: baseTime.Add(3 * time.Hour)},
		{Name: "dave", Age: 25, Score: 60, City: "gz",
			Meta: map[string]any{}, CreatedAt: baseTime.Add(4 * time.Hour)},
	}
	for i := range rows {
		if err := orm.Insert(ctx, db, &rows[i]); err != nil {
			t.Fatalf("插入基准数据第 %d 行失败：%v", i, err)
		}
	}
	return rows
}

// seedOrders 按 users 的下标挂单：alice 2 单、bob 1 单、carol/dave 无单。
// INNER JOIN 后应为 3 行 —— 这个数字就是 Count 漏 JOIN 的判据。
func seedOrders(t *testing.T, db *orm.DB, users []User) {
	t.Helper()
	ctx := context.Background()
	rows := []Order{
		{UserID: users[0].ID, Amount: 100, Status: "paid"},
		{UserID: users[0].ID, Amount: 200, Status: "paid"},
		{UserID: users[1].ID, Amount: 50, Status: "pending"},
	}
	for i := range rows {
		if err := orm.Insert(ctx, db, &rows[i]); err != nil {
			t.Fatalf("插入订单失败：%v", err)
		}
	}
}
