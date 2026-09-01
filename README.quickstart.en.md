# gobreath-orm Quick Start for Beginners

This guide is designed to make the ORM easy to use from day one.

The goal is simple:

- get you running in 10 minutes
- show the same example for SQLite / MySQL / PostgreSQL
- let you copy the example and only change the DSN
- avoid heavy API learning before the first successful run

If your goal is to get started quickly, just use the examples below. Replace the DSN and driver, and the rest of the code can stay the same.

---

## 1. Recommended connection pattern

Use the struct-based config form:

```go
db, err := orm.Open(orm.Config{
    Driver: "mysql",
    DSN:    "root:root@tcp(127.0.0.1:3306)/demo?charset=utf8mb4&parseTime=true&loc=Local",
})
```

Why this is recommended:

- avoids argument-order mistakes
- makes the setup easier to read
- scales better when you add logging, prefix, pool settings, and lifecycle hooks

> The older form `orm.Open("mysql", dsn)` still works, but the struct form is clearer and safer.

---

## 2. Minimal model

```go
package main

type User struct {
    ID   int64  `db:"id,pk,autoincrement"`
    Name string `db:"name"`
    Age  int    `db:"age"`
}
```

This is enough to start with.

- the ORM can infer table/column names
- `db` tags are used when you need custom names or primary keys
- you do not need to write a lot of SQL by hand

---

## 3. The three database examples you can copy

The examples below are almost identical. You only need to change:

- the database driver import
- the driver name
- the DSN

Everything else can be reused.

### 3.1 SQLite (best for beginners)

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

Only change the DSN:

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

Only change the DSN:

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

Only change the DSN:

```go
DSN: "host=127.0.0.1 port=5432 user=postgres password=postgres dbname=demo sslmode=disable"
```

---

## 4. DSN cheat sheet

### SQLite

```go
DSN: "file:demo.db?cache=shared&_fk=1"
```

### MySQL

```go
DSN: "root:root@tcp(127.0.0.1:3306)/demo?charset=utf8mb4&parseTime=true&loc=Local"
```

### PostgreSQL

```go
DSN: "host=127.0.0.1 port=5432 user=postgres password=postgres dbname=demo sslmode=disable"
```

---

## 5. The APIs you need first

At the beginning, you only need a few APIs:

```go
db, err := orm.Open(orm.Config{Driver: "sqlite", DSN: "file:demo.db?cache=shared&_fk=1"})

u := &User{Name: "Alice", Age: 28}
err = orm.Insert(ctx, db, u)

users, err := orm.SelectList(ctx, db, orm.NewQuery[User]())
```

Common operations:

```go
err = orm.UpdateById(ctx, db, u)
err = orm.DeleteById(ctx, db, 1)
user, err := orm.SelectOne(ctx, db, orm.NewQuery[User]())
```

---

## 6. The recommended learning order

Do not try to learn everything at once.

Start in this order:

1. Connect to a database
2. Define a model
3. Insert data
4. Query data
5. Update / delete
6. Learn conditions and `Col[T]`
7. Learn joins / preload / vector later

This makes the learning curve much gentler.

---

## 7. Minimal working example

If you want the shortest possible example, use this:

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

This is the easiest way to begin.

---

## 8. Final takeaway

For a new user, the main goal is not to understand every advanced feature. The first goal is:

- connect successfully
- insert one row
- query one row
- repeat for your database

Once that works, everything else becomes much easier to learn.
