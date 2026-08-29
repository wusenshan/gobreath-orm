# gobreath-orm

> 用了这个 ORM，查询数据就像呼吸一样简单。

`gobreath-orm` 是一个极简、类型安全、零魔法字符串的 Go ORM。它借鉴 MyBatis-Plus 的 `LambdaQueryWrapper` 风格：字段选择靠闭包 + 反射，调用点不出现手写列名 / 关键字 / 占位符，从根本上避免拼 SQL 时写错字段、写错 `AND` 位置、出现 SQL 注入等常见问题。

支持 PostgreSQL / MySQL / SQLite 三种方言，开箱即用。

---

## 1. 连接配置：推荐用结构体参数

`orm.Open()` 现在推荐这么写：

```go
import "github.com/wusenshan/gobreath-orm"

// 推荐：按结构体传参，避免 Driver / DSN 顺序写错。
db, err := orm.Open(orm.Config{
    Driver: "postgres",
    DSN:    "postgres://user:pass@localhost:5432/demo?sslmode=disable",
})
if err != nil {
    panic(err)
}
```

配置结构体如下：

```go
type Config struct {
    Driver        string
    DSN           string
    Prefix        string
    Logger        orm.LogFunc
    LogLevel      orm.LogLevel
    SlowThreshold time.Duration
}
```

也兼容旧写法：

```go
db, err := orm.Open("mysql", "user:pass@tcp(localhost:3306)/demo?charset=utf8mb4&parseTime=True&loc=Local")
```

但从可维护性上看，结构体参数更安全。尤其在 MySQL / PostgreSQL 的连接串格式完全不同的情况下，顺序混写会非常容易出问题。

---

## 2. 驱动与方言对照

Go 的 `database/sql` 是驱动无关的：本框架只负责生成 SQL 与方言处理，不内置数据库驱动。具体驱动需要你在自己的 `main` / 应用包里做一次空导入（blank import）。

### 空导入示例

```go
import (
    // PostgreSQL（二选一）
    _ "github.com/lib/pq"                 // 驱动名: "postgres"
    // _ "github.com/jackc/pgx/v5/stdlib" // 驱动名: "pgx"

    // MySQL
    _ "github.com/go-sql-driver/mysql"    // 驱动名: "mysql"

    // SQLite（二选一）
    // _ "github.com/mattn/go-sqlite3"    // 驱动名: "sqlite3"（需 CGO）
    // _ "modernc.org/sqlite"             // 驱动名: "sqlite"（纯 Go，无 CGO）
)
```

### Driver 与方言对应关系

| 驱动包 | Driver | 方言 |
|---|---|---|
| `github.com/lib/pq` | `postgres` | Postgres |
| `github.com/jackc/pgx/v5/stdlib` | `pgx` | Postgres |
| `github.com/go-sql-driver/mysql` | `mysql` | MySQL |
| `github.com/mattn/go-sqlite3` | `sqlite3` | SQLite |
| `modernc.org/sqlite` | `sqlite` | SQLite |

> 占位符规则由方言自动处理：Postgres 用 `$1/$2…`，MySQL / SQLite 用 `?`，业务代码无需关心。

---

## 3. 不同数据库的配置示例

### PostgreSQL

```go
db, err := orm.Open(orm.Config{
    Driver: "postgres", // 或 "pgx"
    DSN:    "postgres://user:pass@localhost:5432/demo?sslmode=disable",
})
if err != nil {
    panic(err)
}
```

Postgres 的 DSN 一般是 URL 形式，常见于 `postgres://user:pass@host:port/db?sslmode=disable`.

### MySQL

```go
db, err := orm.Open(orm.Config{
    Driver: "mysql",
    DSN:    "user:pass@tcp(localhost:3306)/demo?charset=utf8mb4&parseTime=True&loc=Local",
})
if err != nil {
    panic(err)
}
```

MySQL 的 DSN 语法和 PostgreSQL 完全不同，`tcp(host:port)` + `database` + 参数通常是常见写法；结构体参数能明显减少“顺序写错/字段写错”的问题。

### SQLite

```go
db, err := orm.Open(orm.Config{
    Driver: "sqlite",
    DSN:    ":memory:",
})
if err != nil {
    panic(err)
}
```

SQLite 通常直接用文件路径，或者 `:memory:` 作为内存数据库。

---

## 4. 额外配置：前缀 / 日志

配置项里还支持前缀、日志、慢查询阈值等：

```go
db, err := orm.Open(orm.Config{
    Driver:        "mysql",
    DSN:           "user:pass@tcp(localhost:3306)/demo?charset=utf8mb4&parseTime=True&loc=Local",
    Prefix:        "t_",
    LogLevel:      orm.Info,
    SlowThreshold: 200 * time.Millisecond,
})
if err != nil {
    panic(err)
}
```

也可以链式设置：

```go
db := orm.Open(orm.Config{
    Driver: "postgres",
    DSN:    "postgres://user:pass@localhost:5432/demo?sslmode=disable",
}).WithPrefix("t_").WithLogger(orm.DefaultLogger(os.Stdout))
```

---

## 5. 快速开始：定义模型

```go
package main

type User struct {
    ID      int64  `db:"id,pk,autoincrement"`
    Name    string `db:"name"`
    Age     int    `db:"age"`
    Status  int    `db:"status"`
}

func (User) TableName() string { return "users" }
```

---

## 6. 一个最小可运行示例

```go
func main() {
    ctx := context.Background()

    db, err := orm.Open(orm.Config{
        Driver: "sqlite",
        DSN:    ":memory:",
    })
    if err != nil {
        panic(err)
    }

    u := &User{Name: "Alice", Age: 18, Status: 1}
    if err := orm.Insert(ctx, db, u); err != nil {
        panic(err)
    }

    got, err := orm.SelectById[User](ctx, db, u.ID)
    if err != nil {
        panic(err)
    }

    got.Status = 0
    if err := orm.UpdateById(ctx, db, got); err != nil {
        panic(err)
    }
}
```

---

## 7. 结论

`orm.Config` 是推荐用法：

- 不再担心 `Driver` / `DSN` 参数位置写错
- MySQL 和 PostgreSQL 的 DSN 规则差异明显时更容易维护
- 额外配置（`Prefix`、`Logger`、`LogLevel`、`SlowThreshold`）放进结构体里，读起来更清晰

如果你希望，我也可以继续把 README 里自动推导表名、事务和 JSON 字段这些章节也统一成相同风格的结构体配置写法。 
