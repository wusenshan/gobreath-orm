// Package bench 是 gobreath-orm 的横向性能对照基准。
//
// 口径：**同一张表 + 同一份种子数据 + 语义相同的 SQL**，对比三层
//
//	raw      —— database/sql 手写 SQL，作为下界，告诉你「ORM 之外」花了多少
//	gorm     —— GORM，生态里最常被拿来对照的参照物
//	gobreath —— 被测对象
//
// 三个后端用同一个 SQLite 驱动实现（modernc.org/sqlite），跑在内存库上、无网络，
// 所以 ns/op 反映的就是纯 ORM 层开销，而不是连接池或网络往返。
//
// 设计与取舍见 README.md。
package bench

import (
	"database/sql"
	"fmt"
	"math/rand"
	"time"

	"gorm.io/gorm"
)

const (
	// tableName 三方共用的表名。
	tableName = "bench_users"

	// seedRowCount 种子数据行数：足以让索引与分页有区分度，又不至于让建库成本喧宾夺主。
	seedRowCount = 10000

	// seedValue 固定随机种子 —— 三个后端必须拿到逐字节相同的 10000 行，
	// 否则比较的是不同数据。
	seedValue = 42
)

// createTableSQL 三方共用的建表语句。
//
// 刻意不用 orm.AutoMigrate / gorm.AutoMigrate 建表：两个 ORM 产出的 DDL
// （列类型、索引命名、是否内联约束）并不相同，用各自的 DDL 就等于在
// 不同的表结构上比性能。这里手动建表，把变量锁死。
const createTableSQL = `
CREATE TABLE bench_users (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	name       TEXT     NOT NULL,
	age        INTEGER  NOT NULL,
	city       TEXT     NOT NULL,
	email      TEXT     NOT NULL,
	score      REAL     NOT NULL,
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL,
	deleted_at DATETIME
)`

// createIndexSQL 给列表/计数场景一个真实的索引条件。
// 不带索引的话三个后端都在全表扫，比的只是行映射开销，区分度会失真。
const createIndexSQL = `CREATE INDEX idx_bench_users_city_age ON bench_users (city, age)`

// queryCities 种子数据里城市取值的全集。
// 查询参数固定用 cities[0]（Beijing），与 GORM / raw 侧保持一致。
var queryCities = []string{"Beijing", "Shanghai", "Guangzhou", "Shenzhen", "Hangzhou", "Chengdu"}

// 列表场景的固定参数：WHERE city = queryCity AND age >= queryMinAge
//                        ORDER BY id LIMIT queryLimit OFFSET queryOffset
const (
	queryLimit  = 50
	queryOffset = 200
	queryMinAge = 30
)

// queryCity 所有列表/计数场景共用的过滤值。
var queryCity = queryCities[0]

// baseTime 种子时间的基准点，固定值以保证可复现。
var baseTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// benchRow 一行的中立表示。三方各自把它转成自己的实体或扫描目标再落库。
type benchRow struct {
	ID        int64
	Name      string
	Age       int
	City      string
	Email     string
	Score     float64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// makeRows 生成 n 行可复现的种子数据。
// 只依赖固定种子，与前一次运行、与哪个后端都无关。
func makeRows(n int) []benchRow {
	rnd := rand.New(rand.NewSource(seedValue))
	out := make([]benchRow, n)
	for i := range out {
		ts := baseTime.Add(time.Duration(i) * time.Minute)
		out[i] = benchRow{
			ID:        int64(i + 1),
			Name:      fmt.Sprintf("user-%06d", i),
			Age:       queryMinAge - 12 + rnd.Intn(53), // 18..70
			City:      queryCities[rnd.Intn(len(queryCities))],
			Email:     fmt.Sprintf("user-%06d@example.com", i),
			Score:     float64(rnd.Intn(10000)) / 100,
			CreatedAt: ts,
			UpdatedAt: ts,
		}
	}
	return out
}

// ---- 被测方模型 ----

// User 是 gobreath-orm 的实体。
//
// deleted_at 用 `,logic` 显式声明软删除（不依赖 Config.SoftDeleteField 全局约定），
// 与 GORM 侧 gorm.DeletedAt 的行为对齐：查询自动追加 deleted_at IS NULL。
type User struct {
	ID        int64      `db:"id,pk,autoincrement"`
	Name      string     `db:"name"`
	Age       int        `db:"age"`
	City      string     `db:"city"`
	Email     string     `db:"email"`
	Score     float64    `db:"score"`
	CreatedAt time.Time  `db:"created_at"`
	UpdatedAt time.Time  `db:"updated_at"`
	DeletedAt *time.Time `db:"deleted_at,logic"`
}

// TableName 显式指定表名，与 GORM 侧的 TableName 保持同一张表。
func (User) TableName() string { return tableName }

// ---- 对照组模型 ----

// GormUser 是 GORM 的实体，列与 gobreath 侧一一对应。
//
// CreatedAt / UpdatedAt 显式关掉 GORM 的自动时间填充（autoCreateTime /
// autoUpdateTime）：gobreath-orm 目前没有自动填充能力，开着这项就等于
// 一方写值、另一方不写，两边做的不是同一件事。等 gobreath 的
// autocreate / autoupdate 落地后，这里应当改回默认值并重测。
type GormUser struct {
	ID        int64          `gorm:"column:id;primaryKey;autoIncrement"`
	Name      string         `gorm:"column:name"`
	Age       int            `gorm:"column:age"`
	City      string         `gorm:"column:city"`
	Email     string         `gorm:"column:email"`
	Score     float64        `gorm:"column:score"`
	CreatedAt time.Time      `gorm:"column:created_at;autoCreateTime:false;autoUpdateTime:false"`
	UpdatedAt time.Time      `gorm:"column:updated_at;autoCreateTime:false;autoUpdateTime:false"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at"`
}

// TableName 与 gobreath 侧同名，确保是同一张表。
func (GormUser) TableName() string { return tableName }

// ---- 手写 SQL 层 ----

// rawRow 是 database/sql 层的扫描目标，列顺序与 rawSelectCols 一致。
type rawRow struct {
	ID        int64
	Name      string
	Age       int
	City      string
	Email     string
	Score     float64
	CreatedAt time.Time
	UpdatedAt time.Time
	// 用 sql.NullTime 而非 *time.Time：少一次堆分配，给 raw 层它应得的下界。
	DeletedAt sql.NullTime
}

// rawSelectCols 手写查询的列清单。
// 与 ORM 侧的 SELECT * 在列集合上等价（表就这 9 列），也带了软删除过滤。
const rawSelectCols = `id, name, age, city, email, score, created_at, updated_at, deleted_at`

// rawSelectByIDSQL 单行主键查询。
const rawSelectByIDSQL = `SELECT ` + rawSelectCols + `
FROM bench_users WHERE id = ? AND deleted_at IS NULL`

// rawListPageSQL 条件列表 + 分页。
const rawListPageSQL = `SELECT ` + rawSelectCols + `
FROM bench_users WHERE city = ? AND age >= ? AND deleted_at IS NULL
ORDER BY id LIMIT ? OFFSET ?`

// rawCountSQL 条件计数。
const rawCountSQL = `SELECT COUNT(*) FROM bench_users WHERE city = ? AND deleted_at IS NULL`

// rawInsertSQL 单行插入（主键交给自增）。
const rawInsertSQL = `INSERT INTO bench_users
(name, age, city, email, score, created_at, updated_at, deleted_at)
VALUES (?, ?, ?, ?, ?, ?, ?, NULL)`

// rawUpdateByIDSQL 按主键整行更新。
const rawUpdateByIDSQL = `UPDATE bench_users
SET name = ?, age = ?, city = ?, email = ?, score = ?, updated_at = ?
WHERE id = ? AND deleted_at IS NULL`
