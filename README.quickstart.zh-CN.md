# gobreath-orm 快速上手（新手版）

这份文档的目标很简单：

- 让第一次接触这个 ORM 的人，能在 10 分钟内跑通
- 让你只要复制示例代码，改一下 DSN 就能连 SQLite / MySQL / PostgreSQL
- 不要求你一开始就理解全部高级能力

如果你想先跑通，直接看下面三种数据库的最小示例即可。只要把 DSN 改成自己的连接串，代码几乎不需要改。

---

## 1. 最推荐的连接方式

推荐你用结构体配置方式：

```go
db, err := orm.Open(orm.Config{
    Driver: "mysql",
    DSN:    "root:root@tcp(127.0.0.1:3306)/demo?charset=utf8mb4&parseTime=true&loc=Local",
})
```

这样写的好处是：

- 不容易写错参数顺序
- 代码更清晰
- 以后加前缀、日志、连接池、软删除、乐观锁都很方便

> 不推荐一上来写成：`orm.Open("mysql", dsn)`
> 这是兼容写法，但结构体配置更稳、更清楚

---

## 2. 先定义一个最小模型

```go
package main

type User struct {
    ID   int64  `db:"id,pk,autoincrement"`
    Name string `db:"name"`
    Age  int    `db:"age"`
}
```

这里的重点是：

- 字段名和列名可以自动匹配
- `db` tag 只是显式指定列名或主键
- 你不需要手写大量 SQL 字符串

---

## 3. 你真正需要的三种数据库示例

下面这三段代码，几乎一样。你只需要替换：

- import 驱动包
- DSN
- Driver 名称

其余结构都可以直接复用。

### 3.1 SQLite（最适合新手）

```go
package main

import (
    "context"
    "fmt"

    orm "github.com/wusenshan/gobreath-orm"
    _ "modernc.org/sqlite"
)

type User struct {
    ID   int64  `db:"id,pk,autoincrement"`
    Name string `db:"name"`
    Age  int    `db:"age"`
}

func main() {
    ctx := context.Background()

    db, err := orm.Open(orm.Config{
        Driver: "sqlite",
        DSN:    "file:demo.db?cache=shared&_fk=1",
    })
    if err != nil {
        panic(err)
    }

    if _, err := orm.RawExec(ctx, db, `
        CREATE TABLE IF NOT EXISTS users (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            name TEXT NOT NULL,
            age INTEGER NOT NULL
        )`); err != nil {
        panic(err)
    }

    u := &User{Name: "Alice", Age: 28}
    if err := orm.Insert(ctx, db, u); err != nil {
        panic(err)
    }

    list, err := orm.SelectList(ctx, db, orm.NewQuery[User]())
    if err != nil {
        panic(err)
    }

    fmt.Println("users:", list)
}
```

只需要改 DSN：

```go
DSN: "file:demo.db?cache=shared&_fk=1"
```

---

### 3.2 MySQL

```go
package main

import (
    "context"
    "fmt"

    orm "github.com/wusenshan/gobreath-orm"
    _ "github.com/go-sql-driver/mysql"
)

type User struct {
    ID   int64  `db:"id,pk,autoincrement"`
    Name string `db:"name"`
    Age  int    `db:"age"`
}

func main() {
    ctx := context.Background()

    db, err := orm.Open(orm.Config{
        Driver: "mysql",
        DSN:    "root:root@tcp(127.0.0.1:3306)/demo?charset=utf8mb4&parseTime=true&loc=Local",
    })
    if err != nil {
        panic(err)
    }

    if _, err := orm.RawExec(ctx, db, `
        CREATE TABLE IF NOT EXISTS users (
            id BIGINT PRIMARY KEY AUTO_INCREMENT,
            name VARCHAR(255) NOT NULL,
            age INT NOT NULL
        )`); err != nil {
        panic(err)
    }

    u := &User{Name: "Bob", Age: 30}
    if err := orm.Insert(ctx, db, u); err != nil {
        panic(err)
    }

    list, err := orm.SelectList(ctx, db, orm.NewQuery[User]())
    if err != nil {
        panic(err)
    }

    fmt.Println("users:", list)
}
```

只需要改 DSN：

```go
DSN: "root:root@tcp(127.0.0.1:3306)/demo?charset=utf8mb4&parseTime=true&loc=Local"
```

---

### 3.3 PostgreSQL

```go
package main

import (
    "context"
    "fmt"

    orm "github.com/wusenshan/gobreath-orm"
    _ "github.com/lib/pq"
)

type User struct {
    ID   int64  `db:"id,pk,autoincrement"`
    Name string `db:"name"`
    Age  int    `db:"age"`
}

func main() {
    ctx := context.Background()

    db, err := orm.Open(orm.Config{
        Driver: "postgres",
        DSN:    "host=127.0.0.1 port=5432 user=postgres password=postgres dbname=demo sslmode=disable",
    })
    if err != nil {
        panic(err)
    }

    if _, err := orm.RawExec(ctx, db, `
        CREATE TABLE IF NOT EXISTS users (
            id BIGSERIAL PRIMARY KEY,
            name TEXT NOT NULL,
            age INT NOT NULL
        )`); err != nil {
        panic(err)
    }

    u := &User{Name: "Carol", Age: 25}
    if err := orm.Insert(ctx, db, u); err != nil {
        panic(err)
    }

    list, err := orm.SelectList(ctx, db, orm.NewQuery[User]())
    if err != nil {
        panic(err)
    }

    fmt.Println("users:", list)
}
```

只需要改 DSN：

```go
DSN: "host=127.0.0.1 port=5432 user=postgres password=postgres dbname=demo sslmode=disable"
```

---

## 4. 这三种数据库的区别，最简单的理解方式

### SQLite

- 最适合本地开发
- 适合脚本、Demo、单机测试
- 通常用文件路径或内存库
- 不需要起数据库服务

示例：

```go
DSN: "file:demo.db?cache=shared&_fk=1"
```

### MySQL

- 适合中小型 Web 应用
- DSN 常见格式：

```go
root:root@tcp(127.0.0.1:3306)/demo?charset=utf8mb4&parseTime=true&loc=Local
```

### PostgreSQL

- 适合更严谨的生产环境
- DSN 常见格式：

```go
host=127.0.0.1 port=5432 user=postgres password=postgres dbname=demo sslmode=disable
```

---

## 5. 你真正需要记住的几个 API

最开始不需要背太多，先记住这几个就够了：

```go
db, err := orm.Open(orm.Config{Driver: "sqlite", DSN: "file:demo.db?cache=shared&_fk=1"})

u := &User{Name: "Alice", Age: 28}
err = orm.Insert(ctx, db, u)

users, err := orm.SelectList(ctx, db, orm.NewQuery[User]())
```

常见的增删改查：

```go
err = orm.UpdateById(ctx, db, u)
err = orm.DeleteById(ctx, db, 1)
user, err := orm.SelectOne(ctx, db, orm.NewQuery[User]())
```

---

## 6. 什么时候该开始看高级功能

等你已经能跑通下面这条链路后，再看高级能力就容易理解：

1. 连接数据库
2. 定义结构体
3. 插入/查询
4. 处理条件
5. 处理事务
6. 再看 JSON / 关联 / vector / 代码生成

也就是说：

- 新手先跑通最小代码
- 中级再学 `Col[T]` / 条件构造器
- 高级再学 `Join` / `Preload` / `Vector`

这才是平滑上手路线。

---

## 7. 一个最短的“先跑起来”版本

如果你想要最省事的版本，可以直接用这个：

```go
package main

import (
    "context"
    "fmt"

    orm "github.com/wusenshan/gobreath-orm"
    _ "modernc.org/sqlite"
)

type User struct {
    ID   int64  `db:"id,pk,autoincrement"`
    Name string `db:"name"`
    Age  int    `db:"age"`
}

func main() {
    ctx := context.Background()

    db, err := orm.Open(orm.Config{
        Driver: "sqlite",
        DSN:    "file:demo.db?cache=shared&_fk=1",
    })
    if err != nil {
        panic(err)
    }

    _, _ = orm.RawExec(ctx, db, `CREATE TABLE IF NOT EXISTS users (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, age INTEGER NOT NULL)`)

    u := &User{Name: "Tom", Age: 26}
    if err := orm.Insert(ctx, db, u); err != nil {
        panic(err)
    }

    users, err := orm.SelectList(ctx, db, orm.NewQuery[User]())
    if err != nil {
        panic(err)
    }

    fmt.Println(users)
}
```

这份代码可以直接复制，唯一需要替换的，是数据库类型和 DSN。

---

## 8. 结论

如果你是第一次用这个 ORM，最重要的不是一口气看完所有特性，而是：

- 先拿 SQLite 跑通
- 再拿 MySQL 跑通
- 再拿 PostgreSQL 跑通
- 然后再深入看 `Col[T]`、条件查询、联表和 vector

这才是最容易坚持下来的学习路线。

如果你愿意，我下一步可以继续补一版：

- `README.quickstart.zh-CN.md` + `README.quickstart.en.md`
- 简化成“最短代码 + 三个数据库公式 + 复制即用”
- 并且配一个更适合首页展示的简短版本
