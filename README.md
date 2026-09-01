# gobreath-orm

[![Go Reference](https://pkg.go.dev/badge/github.com/wusenshan/gobreath-orm.svg)](https://pkg.go.dev/github.com/wusenshan/gobreath-orm)
[![Go Report Card](https://goreportcard.com/badge/github.com/wusenshan/gobreath-orm)](https://goreportcard.com/report/github.com/wusenshan/gobreath-orm)
[![CI](https://github.com/wusenshan/gobreath-orm/actions/workflows/ci.yml/badge.svg)](https://github.com/wusenshan/gobreath-orm/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
![Go](https://img.shields.io/badge/go-1.23%2B-00ADD8)

> 用了这个 ORM，查询数据就像呼吸一样简单。

`gobreath-orm` 是一个 **AI 原生（AI-ready）、类型安全、零魔法字符串** 的 Go ORM——**内置向量检索，让 RAG / 语义搜索开箱即用**，同时保留 MyBatis-Plus 式 `LambdaQueryWrapper` 的零字段名查询体验：调用点绝不出现手写列名 / 关键字 / 占位符，从根本上避免拼 SQL 写错字段、写错 `AND` 位置、出现 SQL 注入等常见问题。

支持 Postgres（pgvector）/ MySQL 9+（原生 VECTOR）/ SQLite 三种方言；**向量一套 API 跨库通用、零额外依赖**（不引入 pgvector-go）。想直接用上向量能力，看 [向量检索专文档 →](VECTOR.md)。

> 🤖 **为 AI / RAG 而生**：只要你的业务要做「知识库问答 / 语义搜索 / 相似推荐 / 文本去重」，第一步就是把文本变成向量存进数据库再近邻检索。`gobreath-orm` 把这一步做成一行 `NearestBy(...)`，Postgres 与 MySQL 同一套代码——向量逻辑、检索效果与接入 AI 向量模型的布置，见 [VECTOR.md](VECTOR.md)。

---

## 特性

- 🌟 **AI 原生 · 向量检索（核心卖点）**：内置 `Nearest / WithinDistance`，**一套 API 同时适配 Postgres(pgvector) 与 MySQL 9+**，支持 Cosine/L2/InnerProduct/L1 四种度量；零依赖、无需 `pgvector-go`，`[]float32` 自动序列化为 `[..]`。让 RAG 检索、语义搜索、相似推荐、文本去重直接落地——完整逻辑、效果演示与接入 AI 向量模型的布置见 [VECTOR.md](VECTOR.md)。
- 🦭 **零字段名字符串**：用 `orm.Col[T](func(u *T) *F { return &u.Name })` 选字段，列名从结构体 `db` tag 自动推导；配合 `ormgen` 代码生成器可进一步缩成 `UserCols.Age`，编译期就能发现字段用错。
- 🧩 **泛型 + 链式条件**：`Eq / Ne / Gt / Ge / Lt / Le / Like / In / NotIn / Between / IsNull / IsNotNull`，自动处理 `AND/OR` 拼接与占位符。
- 🪄 **MyBatis-Plus 式条件块**：`Or()` 与 `If(cond, func(q))` 对标 MP 的 `.or()` 与三参数条件（Go 不支持重载，用条件块统一实现）。
- 🗂 **自动表名推导**：`User` → `users`（蛇形 + 复数），也可实现 `TableName()` 显式指定；支持 DB 级表前缀（`t_users`）。
- 🧱 **完整 CRUD**：`Insert / BatchInsert / SelectById / SelectList / SelectOne / Count / Exists / Page / UpdateById / Update / DeleteById / Delete`，自增主键自动回填。
- 🔒 **原生事务**：`db.Transaction(ctx, func(tx *orm.DB) error)`。
- 📦 **JSON 字段**：`db:"meta,json"` 即可把结构体字段（map / struct）自动与 JSON 列互转；支持按路径查询与 `JSON_CONTAINS / @>` 包含查询，三方言全适配。
- 🛡 **三层防注入**：值参数化绑定 + 列名仅取自结构体 tag 白名单 + 表名白名单校验与引号转义。
- ⚡ **原生 SQL 出口**：`RawQuery / RawOne / RawExec` 执行任意 SQL 并自动扫描结果，支持字段别名与非表 DTO 接收（对标 MP 交给 XML 的复杂查询）。
- ⚙️ **结构体配置**：`orm.Open(orm.Config{...})` 具名传参一次配齐驱动 / 前缀 / 日志 / 连接池，也兼容 `Open(driver, dsn)` 旧写法。
- 🔗 **联表查询（JOIN）**：`Join / LeftJoin / RightJoin`（+ `As` 别名变体）+ 主表 `Alias()`，表名白名单校验，ON 条件原文拼接；`Select` 支持 `u.name` 带别名列。
- 🔁 **Upsert（插入或更新）**：`Upsert / BatchUpsert`，方言分发——Postgres / SQLite 走 `ON CONFLICT ... DO UPDATE`，MySQL 走 `ON DUPLICATE KEY UPDATE`；冲突键默认主键，可覆盖。
- 🎯 **部分更新（多字段 / map）**：`Query.Set(col, val)` 链式 + `UpdateSets`，或 `UpdatePartial / UpdateByIdSets` 以 `map[string]any` 指定字段；强制带 WHERE，禁止全表更新。
- 🔐 **乐观锁**：`db:"version,version"`（或 `Config.OptimisticField` 约定）标记版本列；`UpdateById / UpdateByIdSets` 自动 `WHERE version = ?` 并 `SET version = version + 1`，冲突时返回 `ErrOptimisticLock`。
- 🪝 **SQL 生命周期钩子（Hook）**：`Config.Hooks` 或 `db.WithHooks(...)` 注册实现 `Hook` 接口的对象；每次 `exec` / `query` 的 before / after 阶段都会触发 `On(HookEvent)`，可零侵入地做审计、限流、链路追踪。未配置则不触发、零开销。
- 🌐 **读写分离 / 多数据源**：`Config.ReadWrite`（或等价的 `Config.MultiSource`）声明主库 + 只读副本；框架按 SQL 前缀自动把写操作路由主库、读操作 round-robin 到副本，事务内自动回落主库。`*readWriteRouter` 内部加锁，并发安全。
- 🗃 **AutoMigrate（数据库迁移）**：`db.AutoMigrate(ctx, &User{}, &Order{})` 幂等建表（`CREATE TABLE IF NOT EXISTS`）+ 二级索引（`CREATE INDEX IF NOT EXISTS`），三方言自动生成 DDL；自动识别 `,vector(N)`（PG `vector(N)` / MySQL `VECTOR(N)` / SQLite `TEXT`）与 `,json`（PG `JSONB` / MySQL `JSON` / SQLite `TEXT`）、`,unique` / `,index`；无需引入迁移工具即可让表结构与结构体对齐。
- 🔗 **关联预加载（Preload）**：`orm.Preload(ctx, db, &users, "Articles")` 一次性批量加载 **has_many / has_one / belongs_to** 关联，避免 N+1 查询；默认外键约定 `<类型名>_id`（如 `User` → `user_id`），可用 `orm:"has_many;fk:user_id"` / `orm:"belongs_to;fk:xxx"` 覆盖；软删除过滤对子查询同样生效。
- ⋇ **Distinct 去重查询**：`orm.NewQuery[T]().Distinct()` 生成 `SELECT DISTINCT`，可搭配 `Select` / 条件 / 排序 / 分页照常使用。

---

## 安装

```bash
go get github.com/wusenshan/gobreath-orm
```

要求 Go 1.23+。

**30 秒体验**：仓库自带一个零依赖（纯 Go SQLite 驱动）的可运行示例，不需要装任何数据库：

```bash
git clone https://github.com/wusenshan/gobreath-orm.git
cd gobreath-orm/examples/quickstart
go run .
```

示例覆盖了建表、插入（主键回填 + JSON 列）、闭包条件查询、原生 SQL + DTO 聚合、事务更新，看一遍就能上手。

---

## 数据库驱动（使用前必读）

Go 的 `database/sql` 是**驱动无关**的：本框架只负责生成 SQL 与方言处理，**不内置任何数据库驱动**。具体驱动需要你在**自己的 `main`/应用包**里做一次「空导入」（blank import），驱动包会在其 `init()` 里调用 `sql.Register` 把自己注册给标准库。漏掉这一步会在首次查询时报：

```
sql: unknown driver "mysql" (forgotten import?)
```

（框架刻意不替你导入驱动——否则会把 pgx / mysql / sqlite3 全变成硬依赖，而 sqlite3 还依赖 CGO，反而污染你的工程。）

按你实际用的数据库，导入对应的一个即可：

```go
import (
    // PostgreSQL（二选一）
    _ "github.com/lib/pq"                 // 驱动名 "postgres"
    // _ "github.com/jackc/pgx/v5/stdlib" // 驱动名 "pgx"（更现代，推荐）

    // MySQL
    _ "github.com/go-sql-driver/mysql"    // 驱动名 "mysql"

    // SQLite（二选一）
    // _ "github.com/mattn/go-sqlite3"    // 驱动名 "sqlite3"（需 CGO）
    // _ "modernc.org/sqlite"             // 驱动名 "sqlite"（纯 Go，无需 CGO）
)
```

驱动名与方言的对应关系（`orm.Open` 的 `Driver` 参数 / `orm.Config` 的 `Driver` 字段）：

| 驱动包 | 驱动名（driver） | 本框架方言 |
|---|---|---|
| `github.com/lib/pq` | `postgres` | Postgres |
| `github.com/jackc/pgx/v5/stdlib` | `pgx` | Postgres |
| `github.com/go-sql-driver/mysql` | `mysql` | MySQL |
| `github.com/mattn/go-sqlite3` | `sqlite3` | SQLite |
| `modernc.org/sqlite` | `sqlite` | SQLite |

> 占位符规则由方言自动处理：Postgres 用 `$1/$2…`，MySQL / SQLite 用 `?`，业务代码无需关心。

---

## 快速开始

### 1. 定义模型

```go
package main

import "github.com/yourname/gobreath-orm"

// 注意：所有需要映射的字段必须导出（首字母大写）。
// 非导出字段会被直接忽略。
type User struct {
    ID      int64           `db:"id,pk,autoincrement"` // 主键 + 自增
    Name    string          `db:"name"`
    Age     int             `db:"age"`
    Email   string          `db:"email"`
    Status  int             `db:"status"` // 0 禁用 1 启用
    Profile map[string]any  `db:"profile,json"` // JSON 列，自动 marshal/unmarshal
}

// 可选：显式指定表名（推荐）。不写则按结构体名自动推导为 users。
// 注意：只要实现了 TableName()，DB 前缀便不会再叠加到该名字上。
func (User) TableName() string { return "users" }
```

`db` tag 支持的修饰符：

| 写法 | 含义 |
|---|---|
| `db:"id,pk"` | 该列为（复合前的）主键 |
| `db:"id,pk,autoincrement"` | 主键且自增，插入后自动回填 |
| `db:"profile,json"` | 以 JSON 文本读写该列 |
| `db:"-"` | 忽略该字段（不参与任何 SQL） |
| `db:"user_name"` | 自定义列名（不写则按蛇形自动推导，如 `UserName` → `user_name`） |

> 若字段名为 `ID` 或 `Id` 且未声明 `pk`，框架会自动把它当作主键。

> ⚠️ **最常见的坑：`db` tag 必须用双引号包裹。**
> 正确：`db:"id,pk,autoincrement"`（引号包裹）
> 错误：`db:id,pk,autoincrement`（无引号）
>
> Go 的 struct tag 标准格式是 `key:"value"`。写成无引号的 `db:col,pk` 时，
> `reflect` 读不到 `db` 这个 key，`Tag.Get("db")` 返回空字符串 —— 字段会退化成
> 「只按字段名（蛇形）映射」，而你写的 `pk` / `autoincrement` 全部丢失。
> 典型症状：**自增主键列被当成普通列、插入时写入 `0`**，SQL 形如
> `INSERT INTO t (id, name) VALUES (0, 'x')`，且不会报任何错，极难排查。
>
> **防呆开关（默认关闭）**：从 v0.1.8 起，可在 `orm.Open(orm.Config{StrictTagCheck: true})`
> 中开启严格校验。开启后，解析模型时遇到无引号 `db` tag 会**主动 panic** 并提示：
> `orm: 结构体 User 字段 Id 的 db tag 格式错误：缺少引号，正确写法是 db:"col,pk,autoincrement"`。
> 默认关闭是为了兼容旧行为（不校验）；一旦任一 `Open` 开启，全局进入严格模式（越严越安全）。
> `go vet` 也能静态拦截同类笔误，此开关作为运行时兜底，建议在 CI / 启动阶段开启以便第一时间暴露。

### 2. 打开连接

推荐用结构体配置（`orm.Config`），字段具名、顺序无关，前缀 / 日志等一次性配齐：

```go
ctx := context.Background()

db, err := orm.Open(orm.Config{
    Driver: "postgres", // postgres/pgx → Postgres；mysql → MySQL；sqlite/sqlite3 → SQLite
    DSN:    "postgres://user:pass@localhost:5432/demo?sslmode=disable",
    // Prefix:        "t_",                          // 表前缀（仅对自动推导的表名生效）
    // Logger:        orm.DefaultLogger(os.Stdout),  // SQL 日志输出
    // LogLevel:      orm.Info,                      // 日志等级
    // SlowThreshold: 200 * time.Millisecond,        // 慢查询阈值

    // 连接池参数（不填保持 database/sql 默认行为）
    // MaxOpenConns:    20,                // 最大打开连接数（默认不限制）
    // MaxIdleConns:    20,                // 最大空闲连接数（默认 2）
    // ConnMaxLifetime: time.Hour,         // 连接最长存活时间（默认永不回收）
    // ConnMaxIdleTime: 10 * time.Minute,  // 连接最长空闲时间（默认永不回收）
})
if err != nil {
    panic(err)
}
```

也兼容旧的两参数写法（`Driver` / `DSN` 位置传参）：

```go
db, err := orm.Open("postgres", "postgres://user:pass@localhost:5432/demo?sslmode=disable")
// 需要前缀 / 日志时再链式补：
// db = db.WithPrefix("t_")
```

> `Config` 里没填的可选项（`Prefix` / `Logger` / `LogLevel` / `SlowThreshold` / 连接池四项）保持默认值，也可事后用 `WithXxx` 链式方法补配（连接池参数除外，见下）。

**连接池调优提示**：`database/sql` 默认 `ConnMaxLifetime` 永不回收，连 MySQL 时若超过服务端 `wait_timeout`（默认 8h），会随机报 `invalid connection` / `driver: bad connection`——建议把 `ConnMaxLifetime` 设成略小于 `wait_timeout` 的值（如 `time.Hour`）。

更冷门的池参数或统计信息可通过逃生舱拿到底层连接再调：

```go
sqlDB := db.SQL()               // 返回底层 *sql.DB（事务副本/非 *sql.DB 底层时为 nil）
sqlDB.SetMaxIdleConns(0)        // 例如显式关闭空闲连接
_ = sqlDB.Stats()               // 连接池统计
```

> 连接池参数属于 `database/sql` 原生能力，框架在 `Open` 后自动透传设置；`orm.Open` 之外的构建方式（如 `NewDB`）可自行通过 `db.SQL()` 配置。

### 3. 一个最小可运行示例

把上面的模型与连接串起来，一个最基础的「增 → 查 → 改 → 删」流程（包级 API 版）：

```go
func main() {
    ctx := context.Background()

    // 实际使用前需在本文件的 import 里空导入对应驱动（详见上文「数据库驱动」一节）。
    // 本例用 "sqlite" 驱动名，对应纯 Go 的 _ "modernc.org/sqlite"（无需 CGO）。
    // 生产换成真实 DSN 即可，例如 _ "github.com/go-sql-driver/mysql" + orm.Open("mysql", dsn)。
    db, err := orm.Open("sqlite", ":memory:")
    if err != nil {
        panic(err)
    }

    // 增：插入后自增主键自动回填到 u.ID
    u := &User{Name: "Alice", Age: 18, Status: 1}
    if err := orm.Insert(ctx, db, u); err != nil {
        panic(err)
    }

    // 查：按主键
    got, err := orm.SelectById[User](ctx, db, u.ID)
    if err != nil {
        panic(err)
    }

    // 改：用查出来的实体更新
    got.Status = 0
    if err := orm.UpdateById(ctx, db, got); err != nil {
        panic(err)
    }

    // 查：分页（返回通用分页结构）
    pr, err := orm.Page(ctx, db, orm.NewQuery[User](), 1, 10)
    if err != nil {
        panic(err)
    }
    fmt.Printf("共 %d 条，本页 %d 条\n", pr.Total, len(pr.List))

    // 删：按主键
    if err := orm.DeleteById[User](ctx, db, u.ID); err != nil {
        panic(err)
    }
}
```

> 若更偏好「免写 `[User]`」的仓储风格，把上面的 `orm.Xxx[User](...)` 换成
> `users := orm.NewRepo[User](db); users.Insert(ctx, u)` 即可，详见下方「仓储模式」一节。

---

## 增删改查完整 Demo

下面用一套连贯的示例覆盖所有常用操作。先抽取列选择器，避免重复书写：

```go
nameCol    := orm.Col[User](func(u *User) *string       { return &u.Name })
ageCol     := orm.Col[User](func(u *User) *int          { return &u.Age })
statusCol  := orm.Col[User](func(u *User) *int          { return &u.Status })
profileCol := orm.Col[User](func(u *User) *map[string]any { return &u.Profile })
```

### 新增（Insert）

```go
u := &User{
    Name:    "Alice",
    Age:     18,
    Email:   "alice@example.com",
    Status:  1,
    Profile: map[string]any{"city": "Shanghai", "tags": []string{"vip"}},
}
if err := orm.Insert(ctx, db, u); err != nil {
    panic(err)
}
// 插入成功后，自增主键已自动回填到 u.ID
fmt.Println("new id =", u.ID)
```

> 💡 **自增主键回填的方言差异**：MySQL / SQLite 走标准 `sql.Result.LastInsertId()` 回填；
> **PostgreSQL（pgx 经 `database/sql`）不支持 `LastInsertId()`**，框架会自动改用
> `INSERT ... RETURNING "id"` + 扫描单行回填，对调用方透明——无需任何额外代码。
> 注意：`BatchInsert` 因签名为值切片（无法回写元素），不回填自增主键；单条 `Insert` 才回填。

### 批量新增（BatchInsert）

```go
users := []User{
    {Name: "Bob",   Age: 20, Status: 1},
    {Name: "Carol", Age: 25, Status: 1},
}
if err := orm.BatchInsert(ctx, db, users); err != nil {
    panic(err)
}
```

### 按主键查询（SelectById）

```go
u, err := orm.SelectById[User](ctx, db, 1)
if errors.Is(err, orm.ErrNotFound) {
    // 记录不存在
}
```

### 条件查询列表（SelectList）

```go
list, err := orm.SelectList(ctx, db,
    orm.NewQuery[User]().
        Eq(statusCol, 1).              // status = 1
        Gt(ageCol, 18).                // AND age > 18
        OrderBy(ageCol, false).        // ORDER BY age DESC
        Limit(10).                     // LIMIT 10
        Offset(0),
)
```

### 查首条（SelectOne）

```go
u, err := orm.SelectOne(ctx, db,
    orm.NewQuery[User]().Eq(nameCol, "Alice"),
)
if errors.Is(err, orm.ErrNotFound) {
    // 没有叫 Alice 的人
}
```

### 分页（Page）

`Page` 返回通用分页结构 `PageResult[T]`，同时携带本页数据与分页元信息：

```go
// 返回：*PageResult[User]、错误
pr, err := orm.Page(ctx, db,
    orm.NewQuery[User]().Eq(statusCol, 1),
    1,   // 第几页，从 1 开始（非法值自动归正为 1）
    10,  // 每页大小（非法值自动归正为 10）
)
// pr.List    []User   本页数据
// pr.Total   int64    符合条件的总条数
// pr.Pages   int      总页数
// pr.Page    int      当前页
// pr.Size    int      每页大小
// pr.HasNext bool     是否有下一页
// pr.HasPrev bool     是否有上一页
fmt.Printf("total=%d pages=%d hasNext=%v\n", pr.Total, pr.Pages, pr.HasNext)
```

> `Page` 内部会先 `Count` 一次拿到 `Total`，再用 `LIMIT/OFFSET` 取本页；`Total` 与 `Pages`/`HasNext`/`HasPrev` 一次性算好，无需前端自行计算。

### 仓储模式（把类型绑定到 DB）

若嫌每次写 `orm.SelectById[User](...)` 里的 `[User]` 啰嗦，可先把 `*DB` 与实体类型 `T` 绑成一个 `Repo[T]` 句柄（类似 DAO / Repository）。之后方法调用**免写类型参数**：

```go
// 创建针对 User 的仓储句柄（Go 不支持泛型方法，故用函数式 NewRepo[T]）
users := orm.NewRepo[User](db)

u, err := users.SelectById(ctx, 1)                              // 免写 [User]
list, err := users.SelectList(ctx, orm.NewQuery[User]().Eq(statusCol, 1))
pr, err := users.Page(ctx, orm.NewQuery[User](), 1, 10)
n, err := users.Count(ctx, orm.NewQuery[User]().Gt(ageCol, 18))

// 事务回调同样绑定到 T，回调内也免写类型参数
err := users.Transaction(ctx, func(tx *orm.Repo[User]) error {
    if _, e := tx.SelectById(ctx, 1); e != nil {
        return e
    }
    return tx.UpdateById(ctx, u)
})
```

`Repo[T]` 完整转发：`Insert / BatchInsert / SelectById / SelectList / SelectOne / Page / Count / Exists / UpdateById / Update / DeleteById / Delete / Transaction`，底层与包级泛型函数完全一致，只是把 `T` 提前固定了。

### 按主键更新（UpdateById）

```go
u, _ := orm.SelectById[User](ctx, db, 1)
u.Status = 0
if err := orm.UpdateById(ctx, db, u); err != nil {
    panic(err)
}
```

### 条件更新（Update）

> 第二个参数是「新值来源」，用它的非主键字段作为 `SET`；**必须带 WHERE 条件**，否则返回错误（禁止全表更新）。

```go
// 把所有叫 Alice 的用户状态改为 0
update := &User{Status: 0}
err := orm.Update(ctx, db,
    orm.NewQuery[User]().Eq(nameCol, "Alice"),
    update,
)
```

### 按主键删除（DeleteById）

```go
if err := orm.DeleteById[User](ctx, db, 1); err != nil {
    panic(err)
}
```

### 条件删除（Delete）

> **必须带 WHERE 条件**，禁止无条件全表删除。

```go
err := orm.Delete(ctx, db, orm.NewQuery[User]().Eq(nameCol, "Bob"))
```

### 计数 / 存在性（Count / Exists）

```go
n, err := orm.Count(ctx, db, orm.NewQuery[User]().Gt(ageCol, 18))
hasVip, err := orm.Exists(ctx, db,
    orm.NewQuery[User]().JsonContains(profileCol, map[string]any{"tags": []string{"vip"}}),
)
```

### 事务（Transaction）

```go
err := db.Transaction(ctx, func(tx *orm.DB) error {
    if err := orm.Insert(ctx, tx, &User{Name: "Dave", Age: 30, Status: 1}); err != nil {
        return err // 返回 error 自动回滚
    }
    if err := orm.UpdateById(ctx, tx, &modifiedUser); err != nil {
        return err
    }
    return nil // 全部成功才提交
})
```

---

## 查询构造器详解

### 条件方法一览

| 方法 | 生成 SQL（示意） |
|---|---|
| `Eq(col, v)` | `col = ?` |
| `Ne(col, v)` | `col != ?` |
| `Gt / Ge / Lt / Le` | `col > / >= / < / <= ?` |
| `Like(col, "x")` | `col LIKE '%x%'`（**内部自动加 %**，无需手写） |
| `LikeRight(col, "x")` | `col LIKE 'x%'`（前缀匹配） |
| `LikeLeft(col, "x")` | `col LIKE '%x'`（后缀匹配） |
| `NotLike(col, "x")` | `col NOT LIKE '%x%'` |
| `NotLikeRight / NotLikeLeft` | 反向前缀 / 后缀匹配 |
| `In(col, []any{...})` | `col IN (?, ?)` |
| `NotIn(col, []any{...})` | `col NOT IN (?, ?)` |
| `Between(col, lo, hi)` | `col BETWEEN ? AND ?` |
| `IsNull(col)` | `col IS NULL` |
| `IsNotNull(col)` | `col IS NOT NULL` |

### 列名代码生成（ormgen）：从手写闭包到 `UserCols.Age`

`orm.Col[T](func(u *T) *F { return &u.Age })` 写法零风险，但链式条件里同一个字段反复出现会很啰嗦。框架提供 `ormgen` 生成器，给每个模型生成一个列名集合结构体，调用点变成真正的字段访问：

```go
//go:generate go run github.com/wusenshan/gobreath-orm/cmd/ormgen -type User -out user_cols.go -dir .

type User struct { ... }
```

执行后生成 `user_cols.go`：

```go
type UserColumnSet struct {
    Id   orm.ColExpr
    Name orm.ColExpr
    Age  orm.ColExpr
}

var UserCols = UserColumnSet{
    Id:   orm.ColOf[User]("Id"),
    Name: orm.ColOf[User]("Name"),
    Age:  orm.ColOf[User]("Age"),
}
```

然后查询条件可以写成：

```go
q := orm.NewQuery[User]().
    Ge(UserCols.Age, 10).
    Le(UserCols.Age, 20)
```

比手写闭包短很多，而且 `UserCols.Age` 拼错会在编译期报错。`ColOf[T]` 按 Go 字段名匹配，也回退支持 db tag 列名；不存在的字段会在运行时 panic，和 `Col` 一样。

> 推荐：每个模型文件顶上加一行 `//go:generate`，保存或 CI 时跑 `go generate ./...`，列名集合会自动保持同步。

### 从 DDL 生成（ormgen -ddl）与 Web 生成器（ormgen serve）

除了给已有结构体补列闭包，`ormgen` 还能**直接从建表语句生成模型 + 列闭包**（适合已有库、不想手搓 struct 的新手）：

```bash
# 从建表语句生成（自动嗅探 PG/MySQL/SQLite 方言，不依赖扩展名）
go run github.com/wusenshan/gobreath-orm/cmd/ormgen -ddl schema.sql -pkg model -mode perType -dir ./generated
```

- 支持单文件内多张表、`serial`/`bigserial`/`AUTO_INCREMENT`/`AUTOINCREMENT` 自增、`vector(N)` → `[]float32` + `,vector(N)`、引号标识符与 `schema.表` 限定名。
- `-mode`：`perType`（每表 `xxx.go`+`xxx_cols.go`，默认）/`twoFiles`（合并 `models.go`+`columns.go`）/`singleFile`（合并 `models_gen.go`，结构体与闭包同文件）。

也可以启动**本地 Web 生成器**：粘贴 Go 结构体或 DDL、左右分屏预览、一键复制 / 下载、还能复制等价 CLI 命令：

```bash
go run github.com/wusenshan/gobreath-orm/cmd/ormgen -serve   # 默认 http://:8080
```

> 生成器仅做文本解析、零依赖、绝不执行上传内容；输出不保证可直接编译，命名 / 包冲突由开发自行处理。

### OR 与条件块

```go
// status = 1 AND (name = 'Alice' OR age > 30)
q := orm.NewQuery[User]().
    Eq(statusCol, 1).
    Or().
    Gt(ageCol, 30)
// 注意：先 Eq 再 Or()，表示下一条条件与前一条用 OR 连接（组内 OR，组间 AND）
```

### If 条件块（对标 MyBatis-Plus 三参数）

Go 不支持方法重载，因此用「条件块」统一实现。当 `cond` 为 `false` 时整段条件被忽略：

```go
// 仅当 keyword 非空时才按名字模糊匹配
keyword := ""
q := orm.NewQuery[User]().
    Eq(statusCol, 1).
    If(keyword != "", func(q *orm.Query[User]) {
        q.Like(nameCol, keyword) // 内部自动包成 %keyword%
    })
```

也可以直接用普通的 `if` 语句（方法都返回 `*Query[T]` 且原地修改）：

```go
q := orm.NewQuery[User]().Eq(statusCol, 1)
if keyword != "" {
    q.Like(nameCol, keyword) // 包含匹配，等价于 LIKE '%keyword%'
}
if prefix != "" {
    q.LikeRight(nameCol, prefix) // 前缀匹配，等价于 LIKE 'prefix%'
}
```

### 分组聚合（GroupBy / Having）

```go
// SELECT status, COUNT(*) ... GROUP BY status HAVING COUNT(*) > 1
rows, err := orm.SelectList(ctx, db,
    orm.NewQuery[User]().
        Select("status").
        GroupBy(statusCol).
        Having(statusCol, ">", 1),
)
```

---

### 悲观锁与自定义结尾（ForUpdate / Last）

**`ForUpdate`** 在 `SELECT` 末尾追加悲观行锁 `FOR UPDATE`，用于「先查后改」防并发覆盖：

```go
// PG / MySQL 生成：SELECT * FROM users WHERE id = $1 FOR UPDATE
// SQLite 无行级锁，自动降级为空串（不生成任何锁子句）
u, err := orm.SelectOne(ctx, db,
    orm.NewQuery[User]().Eq(idCol, 1).ForUpdate(),
)
```

- 方言感知：Postgres / MySQL 生成 `" FOR UPDATE"`；**SQLite 自动降级为空**（避免老版本直接报语法错）。
- 必须在**事务内**才真正生效，建议配合 `db.Transaction` 使用。

**`Last`** 在生成的 SQL **最末尾**原样拼接一段自定义片段（对标 MyBatis-Plus 的 `last()`），典型用于方言特有语法：

```go
// 跳过已被其他事务锁住的行（Postgres / Oracle 支持）
// SELECT * FROM users WHERE status = $1 FOR UPDATE SKIP LOCKED
orm.NewQuery[User]().
    Eq(statusCol, 1).
    ForUpdate().
    Last("SKIP LOCKED")

// 其他方言特有尾语法：OFFSET ... FETCH ...、窗口函数提示、数据库 hint 等
orm.NewQuery[User]().Limit(10).Last("FETCH NEXT 10 ROWS ONLY")
```

> ⚠️ **安全提示**：`Last` 的内容**不经占位符参数化、直接拼接进 SQL**，只允许放可信 / 静态片段，**切勿拼接任何用户输入**，否则会造成 SQL 注入。

`ForUpdate` 与 `Last` 的拼接顺序固定为：`... LIMIT/OFFSET → FOR UPDATE → Last`，即 `Last` 永远在最末尾。

---

## 原生 SQL（Raw SQL）

单表构造器覆盖不到的场景（JOIN、子查询、报表、方言专属语法）交给 `RawQuery / RawOne / RawExec`——对标 MyBatis-Plus 把复杂 SQL 交给 XML 的那部分。参数只走占位符绑定，SQL 日志 / 慢查询与普通 CRUD 走同一管线；传入事务内的 `*DB` 即在事务中执行。

```go
// 多行：JOIN / 子查询随便写，结果自动扫描进 []T
users, err := orm.RawQuery[User](ctx, db,
    "SELECT * FROM users WHERE age > ? AND status = ?", 18, 1)

// 增删改 / DDL
res, err := orm.RawExec(ctx, db, "UPDATE users SET status = ? WHERE id IN (?, ?)", 1, 7, 9)

// 命名参数：database/sql 原生支持，直接用
u, err := orm.RawOne[User](ctx, db, "SELECT * FROM users WHERE id = :id", sql.Named("id", 7))
```

### 字段别名与 DTO（非表结构体）接收结果

`T` 不要求是表结构体——任何普通 struct 都能当"视图对象"用，**不需要 `db` tag、不需要 `TableName()`**：

```go
type UserDeptVO struct {   // 纯 DTO，只声明关心的列
    UserName string        // 无 tag → 按字段名 snake_case 匹配列 user_name
    DeptName string        // ← 匹配列 dept_name
}

vos, err := orm.RawQuery[UserDeptVO](ctx, db, `
    SELECT u.name AS user_name, d.name AS dept_name
    FROM users u JOIN dept d ON d.id = u.dept_id
    WHERE u.age > ?`, 18)
```

映射规则：

- **列名精确匹配**：优先 `db` tag，无 tag 时用字段名的 snake_case；SQL 里 `AS` 别名即返回列名，按 snake_case 起名即可命中字段。
- **未匹配的列直接忽略**：`SELECT u.*` 带出的多余列不会报错，DTO 只放需要的字段。
- **标量类型直接查**：`RawOne[int64](ctx, db, "SELECT COUNT(*) ...")`、`RawQuery[string](...)` 取单列。

两个约定（写进团队规范即可避免）：

1. **别名一律用 snake_case**：`AS deptName` 在 PostgreSQL 会折叠成 `deptname`，与 `dept_name` 匹配不上；MySQL 虽保留大小写但 `deptName` ≠ `dept_name`。
2. **JOIN 出现同名列时必须用别名消歧**：`SELECT u.id, d.id` 两列都会落进 `Id` 字段，后者覆盖前者。

`Repo[T]` 提供同名透传（`repo.RawQuery / RawOne / RawExec`，绑定到 T）；结果类型不是 T 时用包级函数 `orm.RawQuery[DTO](ctx, repo.DB(), ...)`。

> 防注入底线不变：SQL 文本由你负责（它是 raw 的意义所在），**参数永远走 `?` / `$1` 绑定**，不要字符串拼接。

---

## JSON 字段支持

### 存储映射

结构体里把某个字段标成 `db:"...,json"`，读写时框架自动 `json.Marshal / Unmarshal`：

```go
type Doc struct {
    ID      int64           `db:"id,pk,autoincrement"`
    Title   string          `db:"title"`
    Meta    map[string]any  `db:"meta,json"` // 自动与 JSON 列互转
}
```

- **插入 / 更新**：`Meta` 被 `json.Marshal` 成文本写入 `meta` 列。
- **查询**：数据库返回的 JSON 文本自动 `Unmarshal` 回 `map[string]any`（指针字段会自动分配底层对象）。

### 按路径查询 JSON 内部

路径串用 `"a.b.c"` 形式（嵌套 JSON 键无法用 Go 字段 picker 选取，故传字符串）：

```go
// PG:    "profile"->'city'->>'city' = $1
// MySQL: JSON_EXTRACT(`profile`, '$.city') = ?
// SQLite: json_extract("profile", '$.city') = ?
list, err := orm.SelectList(ctx, db,
    orm.NewQuery[User]().Json(profileCol, "city", "=", "Shanghai"),
)
```

### 包含查询（最常用）

```go
// PG:    "profile" @> $1::jsonb
// MySQL: JSON_CONTAINS(`profile`, ?)
// SQLite: json_contains("profile", ?)
list, err := orm.SelectList(ctx, db,
    orm.NewQuery[User]().JsonContains(profileCol, map[string]any{"status": "active"}),
)
```

> 参数可为 `map` / `struct` / `slice`，或已序列化的 `[]byte` / `string` / `json.RawMessage`。

---

## 表前缀

前缀挂在 **DB 实例**上（而非全局变量），多库 / 多租户各自前缀互不干扰，且只在最终生成 SQL 时拼接：

```go
db := orm.Open("mysql", dsn).WithPrefix("t_")
```

规则（与主流框架约定一致）：

| 场景 | 结果 |
|---|---|
| 模型无 `TableName()`，`db` 前缀 `t_` | `User` → `t_users` |
| 模型实现 `TableName() string { return "users" }` | `users`（**不加**前缀，尊重显式命名） |
| 查询里 `.Table("my_orders")` | `my_orders`（**不加**前缀，显式即物理全名） |

事务内前缀自动继承：`db.WithExecutor(tx)` 会拷贝前缀。

---

## 逻辑删除（Soft Delete）

给模型字段加 `,logic` 标记即可启用逻辑删除（对标 MyBatis-Plus 的 `@TableLogic`）。

- **time 类型**（`time.Time` / `*time.Time`）列：未删除判定为 `IS NULL`，删除时写入当前时间。
- **int 类型**列：未删除判定为 `= 0`，删除时写入 `1`。
- **bool 类型**列：未删除判定为 `= false`，删除时写入 `true`。

启用后，**所有读操作自动过滤已删除数据**（`SelectById` / `SelectList` / `SelectOne` / `Count` / `Exists` / `Page` / `Update` / `UpdateById` 都会追加 `未删除条件`）；**`Delete` / `DeleteById` 自动改为 `UPDATE ... SET 逻辑列 = ...` 软删除**，不再物理删行。

```go
type User struct {
    Id        int64      `db:"id,pk,autoincrement"`
    Name      string     `db:"name"`
    DeletedAt *time.Time `db:"deleted_at,logic"` // 逻辑删除列
}
```

```go
// 自动：SELECT * FROM "users" WHERE "id" = ? AND "deleted_at" IS NULL
u, _ := orm.SelectById[User](ctx, db, 1)

// 自动软删：UPDATE "users" SET "deleted_at" = ? WHERE "id" = ? AND "deleted_at" IS NULL
orm.DeleteById[User](ctx, db, 1)

// 查询已删除数据 / 物理删除：用 Unscoped 或 ForceDelete
orm.SelectList(ctx, db, orm.NewQuery[User]().Unscoped())      // 不过滤已删除
orm.Delete(ctx, db, orm.NewQuery[User]().Unscoped().Eq(...))  // 物理删（无视逻辑列）
orm.ForceDeleteById[User](ctx, db, 1)                         // 物理删（Repo 同样有 ForceDelete/ForceDeleteById）
```

### 约定软删除字段名（免 tag）

如果项目里每张表都用同一个软删除列名（如 `deleted_at` / `deleted` / `is_del`），不想给每个模型都写 `,logic`，可在 `orm.Open` 时配置 `SoftDeleteField`：**只要实体存在列名或 Go 字段名等于该值、且类型为 time/int/bool 的字段，就自动启用软删除**：

```go
db := orm.Open(orm.Config{
    Driver: "postgres", DSN: dsn,
    SoftDeleteField: "deleted_at", // 约定软删除列名
})

type Order struct {
    Id        int64      `db:"id,pk,autoincrement"`
    Title     string     `db:"title"`
    DeletedAt *time.Time `db:"deleted_at"` // 没有 ,logic，但列名命中约定 → 自动软删
}
```

- **优先级**：`db:"...,logic"` 显式声明 > `SoftDeleteField` 约定匹配 > 都没有则**物理删除**。
- **匹配规则**：列名（`db` tag 第一项）或 Go 字段名等于 `SoftDeleteField` 即命中；类型必须是 `time` / `int` / `bool`，不支持的类型（如 string）不启用（保守处理，避免误软删）。
- **显式退出**：实体不想要软删除时，加 `,nologic` 即可退出约定匹配，退化为物理删除：

```go
type Log struct {
    Id        int64      `db:"id,pk,autoincrement"`
    Body      string     `db:"body"`
    DeletedAt *time.Time `db:"deleted_at,nologic"` // 命中约定名但显式退出 → 物理删除
}
```

> 也可用链式 `db.WithSoftDeleteField("deleted_at")` 设置；该配置会随 `WithPrefix` / `WithLogger` 等一并继承。

**注意**：逻辑列不参与 `Insert` / `Update` 的实体赋值（交由数据库默认值 `NULL` / `0` / `false`），避免零值写入破坏「未删除」判定；请确保该列在表结构上有对应默认值。

---

## SQL 执行日志

内建轻量级 SQL 日志：输出每条 SQL、绑定参数、耗时与执行错误，并按日志等级过滤。默认 **Silent**（不打印），按需开启。

### 快速开启

```go
db := orm.Open("postgres", dsn).
    WithLogger(orm.DefaultLogger(os.Stdout)). // 输出到 stdout（默认 os.Stderr）
    WithLogLevel(orm.Info)                    // Info=全部, Warn=慢查询+错误, Error=仅错误
```

> 这三项也可以在 `orm.Open(orm.Config{...})` 时直接以 `Logger` / `LogLevel` / `SlowThreshold` 字段传入，等价且更集中。

输出示例：

```
2026-08-24 17:20:00 INFO  (   1.2ms) SELECT * FROM users WHERE id = $1 args=[1]
2026-08-24 17:20:01 ERROR (   3.4ms) SELECT * FROM x: err=ERROR: relation "x" does not exist
```

### 日志等级

| 等级 | 输出范围 |
|---|---|
| `Silent`（默认） | 不输出 |
| `Info` | 全部 SQL |
| `Warn` | 仅慢查询与执行错误 |
| `Error` | 仅执行错误 |

事件严重度规则：

- 普通执行成功 → `Info` 级；
- 执行出错 → `Error` 级；
- 耗时超过 `WithSlowThreshold(d)` 阈值（且 >0）→ 提升为 `Warn` 级。

### 接到自己的日志库

`LogFunc` 是回调，可无缝接到 zap / logrus / slog 等：

```go
db := orm.Open("mysql", dsn).WithLogger(func(level orm.LogLevel, query string, args []any, dur time.Duration, err error) {
    // 例如用 zap：
    // logger.Debug("sql", zap.String("query", query), zap.Any("args", args), zap.Duration("dur", dur), zap.Error(err))
})
```

> 事务（`db.Transaction`）与前缀副本（`WithPrefix` / `WithExecutor`）都会自动继承日志配置，无需重复设置。

---

## 向量检索（AI / RAG 场景的核心卖点）

> 完整的能力说明、距离度量数学直觉、检索效果演示，以及「如何接入 OpenAI / Ollama 等 AI 向量模型」的端到端布置，单独写在 [VECTOR.md](VECTOR.md)——建议先读它再回来对照下面这段速览。

gobreath-orm 内置向量近邻检索，**一套 API 同时适配 Postgres（pgvector）与 MySQL 9+（原生 VECTOR 类型）**——SQL 由方言自动分发，业务代码不用关心底层差异：

| 数据库 | 启用方式 | 框架生成的检索语法 |
|---|---|---|
| **Postgres + pgvector** | `CREATE EXTENSION vector;` + `vector(N)` 列 | `"embedding" <=> $1`（运算符 `<=>`/`<->`/`<#>`/`<+>`） |
| **MySQL 9+** | `VECTOR(N)` 列 | `VECTOR_DISTANCE(\`embedding\`, STRING_TO_VECTOR(?), 'COSINE')` |

向量字段用 `[]float32` / `[]float64` 即可，框架自动序列化为 `[..]` 文本参数化绑定——**无需引入 `pgvector-go` 之类的第三方包**，零额外依赖。

```go
type Doc struct {
    Id        int64     `db:"id,pk,autoincrement"`
    Title     string    `db:"title"`
    Embedding []float32 `db:"embedding,vector"` // ,vector 标记向量列（Insert/Update 自动序列化）
}

embCol := orm.Col[Doc](func(d *Doc) *[]float32 { return &d.Embedding })

// 余弦近邻（文本语义相似度首选，RAG 检索默认就用它）
list, err := orm.SelectList(ctx, db,
    orm.NewQuery[Doc]().NearestBy(embCol, vec, 5, orm.Cosine),
)

// 欧几里得（默认度量，等价于旧版 Nearest）+ 距离阈值：距离 < 0.3 才返回
q := orm.NewQuery[Doc]().Nearest(embCol, vec, 10).WithinDistance(embCol, vec, 0.3)
```

**距离度量**（`orm.VectorMetric`，默认 `L2`）：

| 度量 | 常量 | Postgres 运算符 | MySQL 度量名 | 典型用途 |
|---|---|---|---|---|
| 欧几里得 | `orm.L2` | `<->` | `EUCLIDEAN` | 图像/音频等已归一化前的几何距离 |
| 余弦 | `orm.Cosine` | `<=>` | `COSINE` | **文本嵌入相似度（推荐）** |
| 内积 | `orm.InnerProduct` | `<#>`（负内积） | `DOT` | 向量已归一化时最快 |
| 曼哈顿 | `orm.L1` | `<+>` | `MANHATTAN`（MySQL 9.7+） | 稀疏向量 |

> **注意**：`Nearest` / `WithinDistance` 不指定度量时沿用旧版默认 `L2`；做语义检索请显式 `NearestBy(..., orm.Cosine)`。
> SQLite 无原生向量类型，仅能生成语法（用于离线拼 SQL），真正检索请换 PG / MySQL。

**完整可运行示例**：见 [`examples/vector-search`](examples/vector-search)，无需装数据库即可看到 PG / MySQL 两种方言生成的 SQL。

**性能提示**：百万级向量请用向量索引——PG `CREATE INDEX ON docs USING hnsw (embedding vector_cosine_ops);`，MySQL `CREATE VECTOR INDEX idx ON docs(embedding);`。框架生成的 `ORDER BY 距离 ASC LIMIT k` 能直接命中这些索引。

---

## 联表 / Upsert / 部分更新 / 乐观锁

### 联表查询（JOIN）

`Join / LeftJoin / RightJoin` 各对应 SQL 的 `INNER/LEFT/RIGHT JOIN`；带 `As` 的变体（如 `LeftJoinAs(table, alias, on)`）可给被联接表起别名。主表用 `Alias()` 起别名后，即可在 ON 与 `Select` 里用 `u.name` 形式引用。

```go
// 左联部门表，取用户名与部门名（ON 为原文拼接，跨表列无法用 Col[T] 表示）
list, err := orm.SelectList(ctx, db, orm.NewQuery[User]().
    Alias("u").
    LeftJoin("departments", `"u"."dept_id" = "departments"."id"`).
    Select("u.name", "departments.dept_name").
    Eq(orm.Col[User](func(u *User) *int { return &u.Age }), 18),
)
```

> ⚠️ **ON 条件为原文拼接**，框架只校验表名（白名单 + 引号），不解析 ON 内的列引用。请按当前方言使用正确的引号（PG `"col"`、MySQL `` `col` ``），勿拼接任何用户输入，否则有注入风险。结果集出现同名列时务必用别名消歧（如 `SELECT u.id, d.id AS dept_id`）。

### Upsert（插入或更新）

`Upsert / BatchUpsert` 一套 API 适配三种方言；冲突键**默认主键**，也可经可变参数 `conflictCols ...string` 覆盖（需对应唯一索引）。更新列 = 全部可写列减去冲突键；无可更新列时退化为 `DO NOTHING`（PG/SQLite）或等价无操作（MySQL）。

```go
// PG: INSERT ... ON CONFLICT ("id") DO UPDATE SET "name" = EXCLUDED."name", ...
// MySQL: INSERT ... ON DUPLICATE KEY UPDATE `name` = VALUES(`name`), ...
err := orm.Upsert(ctx, db, &User{Id: 1, Name: "neo", Age: 30})
```

### 部分更新（多字段 / map）

不想整行更新时，用 `Query.Set(col, val)` 链式 + `UpdateSets`，或以 `map[string]any` 传 `UpdatePartial` / `UpdateByIdSets`。**强制带 WHERE 条件，禁止全表更新**；向量列同样自动序列化绑定。

```go
// 链式：只改 name / age
_, _ = orm.UpdateSets(ctx, db, orm.NewQuery[User]().
    Eq(orm.Col[User](func(u *User) *int64 { return &u.Id }), 1).
    Set(orm.Col[User](func(u *User) *string { return &u.Name }), "bob").
    Set(orm.Col[User](func(u *User) *int { return &u.Age }), 30))

// map：按主键改部分字段
_, _ = orm.UpdateByIdSets[User](ctx, db, int64(1), map[string]any{"name": "bob"})
```

### 乐观锁

实体加 `db:"version,version"`（或 `orm.Open` 配 `OptimisticField: "version"` 约定）标记版本列。`UpdateById / UpdateByIdSets` 自动追加 `WHERE version = ?` 并在 `SET` 里 `version = version + 1`；若期望版本与数据库当前值不一致（被别的事务改过），受影响行数为 0，返回 `ErrOptimisticLock`，调用方据此重试或提示。

```go
type Order struct {
    Id      int64  `db:"id,pk,autoincrement"`
    Status  string `db:"status"`
    Version int    `db:"version,version"` // 乐观锁版本列
}

// 并发修改：只有 version 仍为 5 时才更新成功
err := orm.UpdateById(ctx, db, &Order{Id: 1, Status: "paid", Version: 5})
if err == orm.ErrOptimisticLock {
    // 版本冲突，记录已被他人修改，需重试
}
```

---

## SQL 生命周期钩子（Hook）

`Hook` 是一个接口，实现 `On(HookEvent)` 即可在每次 SQL 执行的前/后拿到查询、参数、耗时与错误：

```go
type Hook interface {
    On(event HookEvent)
}
// HookEvent.Kind  : HookKindExec | HookKindQuery
// HookEvent.Phase : HookPhaseBefore | HookPhaseAfter
// HookEvent 字段   : Query / Args / Duration / Err
```

注册方式二选一：

```go
// 1) 打开时配置
db, _ := orm.Open(orm.Config{Driver: "sqlite", DSN: "file.db", Hooks: []orm.Hook{&MyAuditHook{}}})

// 2) 运行中追加（返回新 DB 副本，不修改原实例）
db = db.WithHooks(&MyAuditHook{})
```

`Args` 在触发时已被框架拷贝，钩子内读取安全；未注册任何 Hook 时完全不触发、不分配，零运行时开销。

## 读写分离 / 多数据源

声明主库与只读副本，框架自动按 SQL 类型路由：

```go
db, _ := orm.Open(orm.Config{
    Driver: "mysql", DSN: "user:pass@tcp(primary:3306)/app",
    ReadWrite: &orm.ReadWriteConfig{
        Replicas: []orm.DataSource{
            {Driver: "mysql", DSN: "user:pass@tcp(replica1:3306)/app"},
            {Driver: "mysql", DSN: "user:pass@tcp(replica2:3306)/app"},
        },
    },
})
```

- 写操作（`INSERT/UPDATE/DELETE/...`）走 `Primary`；读操作（`SELECT`）round-robin 走 `Replicas`。
- 副本为空时所有请求回落主库；`MultiSourceConfig` 与本配置**完全等价**（仅别名），当前统一走同一套路由。
- 事务内（`db.Transaction`）自动回落主库，避免读副本造成的不一致。
- 路由选择内部加锁（`sync.Mutex`），并发安全。

---

## AutoMigrate（数据库迁移）

`AutoMigrate` 让表结构与 Go 结构体保持一致，**幂等、可重复执行**：先 `CREATE TABLE IF NOT EXISTS` 建表，再按需补 `CREATE INDEX IF NOT EXISTS` 二级索引。它直接读结构体的 `db` tag，**不扩展 `Dialect` 接口**（用方言类型 switch 生成 DDL），三方言（PG / MySQL / SQLite）都能正确产出对应语法。

```go
type Product struct {
    Id        int64           `db:"id,pk,autoincrement"`
    SKU       string          `db:"sku,unique"`              // 唯一索引
    Name      string          `db:"name,index"`              // 普通二级索引
    Meta      map[string]any  `db:"meta,json"`               // JSON 列
    Embedding []float32       `db:"embedding,vector(1536)"` // 向量列（带维度）
}

ctx := context.Background()
// 幂等：表/索引用 IF NOT EXISTS，重复执行安全（不删列、不重建）
if err := db.AutoMigrate(ctx, &Product{}, &User{}, &Order{}); err != nil {
    panic(err)
}
```

- **向量列**：`db:"embedding,vector(1536)"` → PG 生成 `embedding vector(1536)`；MySQL 生成 `embedding VECTOR(1536)`；SQLite 无原生向量类型，退化为 `TEXT`（仍保留数据，检索请换 PG / MySQL）。
- **JSON 列**：PG → `JSONB`，MySQL → `JSON`，SQLite → `TEXT`。
- **主键 + 自增**：PG `BIGSERIAL PRIMARY KEY`；MySQL `BIGINT AUTO_INCREMENT PRIMARY KEY`；SQLite `INTEGER PRIMARY KEY AUTOINCREMENT`。
- **索引**：`,unique` 生成唯一索引，`,index` 生成普通二级索引；列类型未显式声明维度时向量默认 1536。

> 当前 AutoMigrate 负责「建表 + 建索引」，**不会删除结构体里已移除的列**（保守策略，避免误删数据）；需要改列类型 / 删列请走原生 SQL（`RawExec`）或专业迁移工具。

---

## 关联预加载（Preload）

`Preload` 一次性批量加载父子 / 主从关联，**避免逐对象查询造成的 N+1 问题**。支持三类关系：

| 关系 | 父字段形态 | 默认外键列 | 含义 |
|---|---|---|---|
| `has_many` | 切片 `[]Child` | `<父类型名>_id`（如 `User` → `user_id`） | 子表持有指向父主键的外键 |
| `has_one` | 结构体 / 指针 | 同上 | 同 has_many，每个父最多一个 |
| `belongs_to` | 结构体 / 指针 | `<子类型名>_id`（如 `Account` → `account_id`） | 父表上持有指向子表主键的外键 |

```go
type User struct {
    Id       int64      `db:"id,pk,autoincrement"`
    Name     string     `db:"name"`
    Articles []Article  `db:"-" orm:"has_many"`          // 子表含 user_id 列
    Profile  *Profile   `db:"-" orm:"has_one;fk:user_id"` // 显式指定外键列
}
func (User) TableName() string { return "users" }

type Article struct {
    Id     int64  `db:"id,pk,autoincrement"`
    Title  string `db:"title"`
    UserId int64  `db:"user_id"`
}
func (Article) TableName() string { return "articles" }

// 批量预加载：一次性把 users 的 Articles / Profile 都挂好
users := []User{{Id: 1}, {Id: 2}}
if err := orm.Preload(ctx, db, &users, "Articles", "Profile"); err != nil {
    panic(err)
}

// 单对象版本
u := &User{Id: 1}
if err := orm.PreloadOne(ctx, db, u, "Articles"); err != nil {
    panic(err)
}
```

`belongs_to` 示例（父表持有外键，指向子表主键）：

```go
type Comment struct {
    Id     int64  `db:"id,pk,autoincrement"`
    Body   string `db:"body"`
    UserId int64  `db:"user_id"`
    Author *User  `db:"-" orm:"belongs_to;fk:user_id"` // 指向 User 主键
}
func (Comment) TableName() string { return "comments" }

comments := []Comment{{Id: 10, UserId: 1}, {Id: 11, UserId: 2}}
orm.Preload(ctx, db, &comments, "Author") // 每个 Comment.Author 被填充
```

约定与注意：

- ⚠️ **关联字段务必用 `db:"-"` 标记**，否则会同时参与 CRUD 而报错。
- 外键列默认 `<类型名>_id`（如 `User` → `user_id`、`Account` → `account_id`）；与默认不符时用 `orm:"has_many;fk:user_id"` / `orm:"belongs_to;fk:xxx"` 覆盖。
- 子查询**同样应用软删除过滤**，不会加载已逻辑删除的子对象。
- `Preload` / `PreloadOne` 在 `Repo[T]` 上同名透传（`repo.Preload(ctx, &list, "Articles")`）。

---

## Distinct 去重查询

`Distinct()` 在 `SELECT` 后追加 `DISTINCT`，常用于「按某列去重后统计 / 列举取值」：

```go
cityCol := orm.Col[User](func(u *User) *string { return &u.City })

// SELECT DISTINCT city FROM users WHERE age > ?
cities, err := orm.SelectList(ctx, db,
    orm.NewQuery[User]().Distinct().Select("city").Gt(ageCol, 18),
)
```

`Distinct()` 与 `Select` / `Eq` / `OrderBy` / `Limit` / `Page` 等链式条件完全兼容，顺序随意。

---

## 安全与防注入

`gobreath-orm` 在三层都做了处理：

1. **值永远参数化**：所有条件值走占位符（`$N` / `?`），绝不字符串拼接。
2. **列名白名单**：列名只从结构体 `db` tag 解析得到，无法构造出 tag 里不存在的列，旁路注入无效。
3. **表名白名单 + 引号转义**：表名无法用占位符绑定，框架用正则校验「字母/数字/下划线、不以数字开头」，再做方言引号（如 `"` / `` ` ``）转义，非法表名直接 panic（属编程期错误，不会生成恶意 SQL）。

---

## 支持数据库

| 数据库 | `Open` 驱动名 | 默认方言 | 备注 |
|---|---|---|---|
| Postgres | `postgres` / `pgx` | Postgres | 默认；支持 jsonb、向量 `<->`（pgvector） |
| MySQL | `mysql` | MySQL | 支持 `JSON_CONTAINS`、向量 `VECTOR_DISTANCE`（MySQL 9+） |
| SQLite | `sqlite` / `sqlite3` | SQLite | 支持 `json_extract` / `json_contains`；无原生向量类型 |

新增方言只需实现 `Dialect` 接口（`QuoteIdent` / `Placeholder` / `JsonPath` / `JsonContains` / `VectorDistance` / `VectorBind` / `UpsertSuffix` / `SupportsLastInsertID` / `InsertReturning`）并在 `dialectForDriver` 注册。

---

## 路线图（Phase 2+）

- `RawQuery` 体验增强：`IN` 占位符批量展开（slice → `(?, ?, ...)`）、`map[string]any` 结果集、流式游标（大结果集分批读取）
- 钩子（`BeforeCreate / AfterUpdate` 等，实体级回调——当前 `Hook` 是 SQL 生命周期级，二者定位不同，后续可扩展）
- 投影 `Select` 强类型化与聚合结果映射
- ✅ 关联预加载 / 关联映射（relation preload / association mapping）—— 已在 v0.1.7 通过 `Preload` / `PreloadOne` 落地
- ✅ 数据库迁移（migration）—— 已在 v0.1.7 通过 `AutoMigrate` 落地（建表 + 二级索引，暂不含删列 / 改列类型）

---

## License

MIT
