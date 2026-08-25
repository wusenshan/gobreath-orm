# gobreath-orm

> 用了这个 ORM，查询数据就像呼吸一样简单。

`gobreath-orm` 是一个极简、类型安全、零魔法字符串的 Go ORM。它借鉴 MyBatis-Plus 的 `LambdaQueryWrapper` 风格：
**字段选择靠闭包 + 反射，调用点绝不出现手写列名 / 关键字 / 占位符**，从根本上避免拼 SQL 时写错字段、写错 `AND` 位置、出现 SQL 注入等常见问题。

支持 Postgres / MySQL / SQLite 三种方言，开箱即用。

---

## 特性

- 🦭 **零字段名字符串**：用 `orm.Col[T](func(u *T) *F { return &u.Name })` 选字段，列名从结构体 `db` tag 自动推导，编译期就能发现字段用错。
- 🧩 **泛型 + 链式条件**：`Eq / Ne / Gt / Ge / Lt / Le / Like / In / NotIn / Between / IsNull / IsNotNull`，自动处理 `AND/OR` 拼接与占位符。
- 🪄 **MyBatis-Plus 式条件块**：`Or()` 与 `If(cond, func(q))` 对标 MP 的 `.or()` 与三参数条件（Go 不支持重载，用条件块统一实现）。
- 🗂 **自动表名推导**：`User` → `users`（蛇形 + 复数），也可实现 `TableName()` 显式指定；支持 DB 级表前缀（`t_users`）。
- 🧱 **完整 CRUD**：`Insert / BatchInsert / SelectById / SelectList / SelectOne / Count / Exists / Page / UpdateById / Update / DeleteById / Delete`，自增主键自动回填。
- 🔒 **原生事务**：`db.Transaction(ctx, func(tx *orm.DB) error)`。
- 📦 **JSON 字段**：`db:"meta,json"` 即可把结构体字段（map / struct）自动与 JSON 列互转；支持按路径查询与 `JSON_CONTAINS / @>` 包含查询，三方言全适配。
- 🔍 **向量检索**：`Nearest / WithinDistance` 生成 `<embedding> <-> $1` 距离排序（Postgres 语法，需数据库支持向量类型）。
- 🛡 **三层防注入**：值参数化绑定 + 列名仅取自结构体 tag 白名单 + 表名白名单校验与引号转义。

---

## 安装

```bash
go get github.com/wusenshan/gobreath-orm
```


要求 Go 1.23+。

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

驱动名与方言的对应关系（`orm.Open(driver, dsn)` 的第一个参数）：

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

### 2. 打开连接

```go
ctx := context.Background()

// driver 支持：postgres/pgx → Postgres（默认）；mysql → MySQL；sqlite/sqlite3 → SQLite
db, err := orm.Open("postgres", "postgres://user:pass@localhost:5432/demo?sslmode=disable")
if err != nil {
    panic(err)
}
// 若数据库统一加了表前缀，可链式指定（仅对自动推导的表名生效）：
// db = db.WithPrefix("t_")
```

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

## SQL 执行日志

内建轻量级 SQL 日志：输出每条 SQL、绑定参数、耗时与执行错误，并按日志等级过滤。默认 **Silent**（不打印），按需开启。

### 快速开启

```go
db := orm.Open("postgres", dsn).
    WithLogger(orm.DefaultLogger(os.Stdout)). // 输出到 stdout（默认 os.Stderr）
    WithLogLevel(orm.Info)                    // Info=全部, Warn=慢查询+错误, Error=仅错误
```

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

## 向量检索（进阶）

需数据库支持向量类型（如 Postgres + pgvector）。`Nearest` / `WithinDistance` 会生成 `<embedding> <-> $1` 距离排序与阈值过滤，用 `SelectList` 执行即可：

```go
type Doc struct {
    ID        int64     `db:"id,pk,autoincrement"`
    Title     string    `db:"title"`
    Embedding []float32 `db:"embedding"`
}

embCol := orm.Col[Doc](func(d *Doc) *[]float32 { return &d.Embedding })

// 取与 vec 最相似的 5 条
list, err := orm.SelectList(ctx, db,
    orm.NewQuery[Doc]().Nearest(embCol, vec, 5),
)

// 再叠加距离阈值：距离 < 0.3 才返回
q := orm.NewQuery[Doc]().Nearest(embCol, vec, 10).WithinDistance(embCol, vec, 0.3)
```

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
| Postgres | `postgres` / `pgx` | Postgres | 默认；支持 jsonb、向量 `<->` |
| MySQL | `mysql` | MySQL | 支持 `JSON_CONTAINS` |
| SQLite | `sqlite` / `sqlite3` | SQLite | 支持 `json_extract` / `json_contains` |

新增方言只需实现 `Dialect` 接口（`QuoteIdent` / `Placeholder` / `JsonPath` / `JsonContains`）并在 `dialectForDriver` 注册。

---

## 路线图（Phase 2+）

- `Join`（联表查询）
- 软删除（`deleted_at` 自动过滤）
- 钩子（`BeforeCreate / AfterUpdate` 等）
- 乐观锁（`version` 字段）
- 投影 `Select` 强类型化与聚合结果映射

---

## License

MIT
