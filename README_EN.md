# gobreath-orm

[![Go Reference](https://pkg.go.dev/badge/github.com/wusenshan/gobreath-orm.svg)](https://pkg.go.dev/github.com/wusenshan/gobreath-orm)
[![Go Report Card](https://goreportcard.com/badge/github.com/wusenshan/gobreath-orm)](https://goreportcard.com/report/github.com/wusenshan/gobreath-orm)
[![CI](https://github.com/wusenshan/gobreath-orm/actions/workflows/ci.yml/badge.svg)](https://github.com/wusenshan/gobreath-orm/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
![Go](https://img.shields.io/badge/go-1.23%2B-00ADD8)

A Go ORM designed for AI-native applications, semantic search, and type-safe SQL workflows.

`gobreath-orm` is a modern ORM built around three ideas:

- type-safe column access instead of raw string magic
- predictable SQL generation and real dialect handling
- built-in vector search for RAG, semantic retrieval, recommendation, and similarity workloads

It supports PostgreSQL, MySQL, and SQLite, while keeping the core package free of hard database-driver coupling.

Original Chinese documentation: [README.md](README.md)

---

## Why gobreath-orm

This project is designed for real application code, not just toy CRUD examples.

It tries to solve the common problems that appear in production:

- too many `"name"` / `"user_id"` string literals
- SQL generation that is hard to debug
- hidden type mismatches between app code and database schemas
- vector retrieval being bolted on as a separate tool instead of being first-class
- dialect-specific SQL issues leaking into business logic

Compared with a typical ORM, `gobreath-orm` keeps the important parts explicit:

- type-safe column selectors via `Col[T](...)`
- conditional query builders with `Eq` / `In` / `Like` / `Json` / `Nearest`
- raw SQL escape hatches for advanced queries
- repository style wrappers
- transaction support
- soft delete and optimistic locking
- JSON field support
- vector search across dialects

---

## Features

- AI-native vector queries with `Nearest`, `NearestBy`, and `WithinDistance`
- zero-magic field access via `orm.Col[T](...)`
- chainable query builder with `Eq`, `Like`, `In`, `Between`, `Json`, `Having`, etc.
- MyBatis-Plus style condition blocks via `Or()` and `If(cond, func(...))`
- table name inference and prefix support
- full CRUD: insert, batch insert, select by id, list, one, update, delete, count, exists, page
- aggregate queries with stable result mapping
- dry-run SQL preview via `Query.ToSQL()` and `orm.DryRun(...)`
- transaction support via `db.Transaction(...)`
- JSON column marshal/unmarshal with path-based queries
- partial updates via `Set`, `UpdateSets`, `UpdatePartial`, `UpdateByIdSets`
- soft delete and optimistic lock semantics
- join support and repository wrappers
- raw SQL access for complex or dialect-specific SQL
- migration helpers and schema generation support

---

## Installation

```bash
go get github.com/wusenshan/gobreath-orm
```

Requires Go 1.23+.

---

## Database drivers

The framework intentionally does not import database drivers for you.

You choose the driver that matches your runtime and keep the dependency closer to the application boundary.

```go
import (
    // PostgreSQL
    _ "github.com/lib/pq"                 // driver name: postgres
    // _ "github.com/jackc/pgx/v5/stdlib" // driver name: pgx

    // MySQL
    _ "github.com/go-sql-driver/mysql" // driver name: mysql

    // SQLite
    // _ "github.com/mattn/go-sqlite3" // driver name: sqlite3
    // _ "modernc.org/sqlite"         // driver name: sqlite
)
```

Driver-to-dialect mappings:

- `postgres` / `pgx` -> PostgreSQL
- `mysql` -> MySQL
- `sqlite` / `sqlite3` -> SQLite

This keeps the core ORM independent from CGO-heavy or vendor-specific dependencies.

---

## Quick start

```go
package main

import (
    "context"
    "fmt"

    "github.com/wusenshan/gobreath-orm"
)

type User struct {
    ID     int64  `db:"id,pk,autoincrement"`
    Name   string `db:"name"`
    Age    int    `db:"age"`
    Email  string `db:"email"`
    Status int    `db:"status"`
}

func (User) TableName() string { return "users" }

func main() {
    ctx := context.Background()

    db, err := orm.Open("sqlite", ":memory:")
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

    fmt.Println("user id:", u.ID)
    fmt.Println("status:", got.Status)
}
```

---

## Typed selectors

Instead of writing raw strings like `"name"`, `"age"`, or `"status"`, use a typed selector.

```go
nameCol := orm.Col[User](func(u *User) *string { return &u.Name })
ageCol := orm.Col[User](func(u *User) *int { return &u.Age })
statusCol := orm.Col[User](func(u *User) *int { return &u.Status })

users, err := orm.SelectList(ctx, db,
    orm.NewQuery[User]().
        Eq(statusCol, 1).
        Gt(ageCol, 18).
        OrderBy(ageCol, false).
        Limit(10),
)
if err != nil {
    panic(err)
}
```

This keeps the query builder strongly aligned with the model fields and reduces typos.

---

## Database config

Use `orm.Config` to set the driver, DSN, table prefix, logger, and pool settings in a single structured object.

```go
db, err := orm.Open(orm.Config{
    Driver: "postgres",
    DSN:    "postgres://user:pass@localhost:5432/demo?sslmode=disable",
    // Prefix: "t_",
    // Logger: orm.DefaultLogger(os.Stdout),
    // LogLevel: orm.Info,
})
if err != nil {
    panic(err)
}
```

The older convenience form is also supported:

```go
db, err := orm.Open("mysql", "user:pass@tcp(localhost:3306)/demo")
```

---

## CRUD basics

```go
// insert
u := &User{Name: "Bob", Age: 30, Status: 1}
_ = orm.Insert(ctx, db, u)

// get by primary key
user, _ := orm.SelectById[User](ctx, db, u.ID)

// list with conditions
list, _ := orm.SelectList(ctx, db,
    orm.NewQuery[User]().Eq(nameCol, "Bob"),
)

// pagination
page, _ := orm.Page(ctx, db, orm.NewQuery[User](), 1, 10)

// count
count, _ := orm.Count(ctx, db, orm.NewQuery[User]().Gt(ageCol, 20))

// update
u.Name = "Bobby"
_ = orm.UpdateById(ctx, db, u)

// delete
_ = orm.DeleteById[User](ctx, db, u.ID)
```

---

## Repository layer

The repository wrapper binds a model type `T` to a `*DB` so you do not repeat the type parameter on every call.

```go
users := orm.NewRepo[User](db)
user, err := users.SelectById(ctx, 1)
if err != nil {
    panic(err)
}

err = users.Transaction(ctx, func(tx *orm.Repo[User]) error {
    // do DB work in transaction with same bound type
    return nil
})
```

This is especially useful in service layers and DAOs where the model is known in advance.

---

## Query builder

### Conditions

```go
q := orm.NewQuery[User]().
    Eq(statusCol, 1).
    Gt(ageCol, 18).
    Like(nameCol, "ali").
    In(statusCol, []any{1, 2, 3}).
    OrderBy(ageCol, false).
    Limit(10)
```

Supported conditions include:

- `Eq`, `Ne`, `Gt`, `Ge`, `Lt`, `Le`
- `Like`, `LikeRight`, `LikeLeft`
- `NotLike`, `NotLikeRight`, `NotLikeLeft`
- `In`, `NotIn`
- `Between`
- `IsNull`, `IsNotNull`
- `Json`, `JsonContains`

### OR groups and conditional blocks

```go
q := orm.NewQuery[User]().
    Eq(statusCol, 1).
    Or().
    Gt(ageCol, 30)
```

This means: `status = 1 OR age > 30` within the same logical group.

`If(cond, func(q *Query[T]))` is also supported:

```go
keyword := "ali"
q := orm.NewQuery[User]().Eq(statusCol, 1)
q.If(keyword != "", func(q *orm.Query[User]) {
    q.Like(nameCol, keyword)
})
```

---

## JSON support

A field can be treated as a JSON column with `db:"...,json"`.

```go
type Doc struct {
    ID   int64         `db:"id,pk,autoincrement"`
    Meta map[string]any `db:"meta,json"`
}
```

Then inserts and updates marshal/unmarshal automatically.

You can also query by JSON path:

```go
rows, err := orm.SelectList(ctx, db,
    orm.NewQuery[Doc]().Json(orm.Col[Doc](func(d *Doc) *map[string]any { return &d.Meta }), "user.city", "=", "Shanghai"),
)
```

And query JSON containment:

```go
rows, err := orm.SelectList(ctx, db,
    orm.NewQuery[Doc]().JsonContains(orm.Col[Doc](func(d *Doc) *map[string]any { return &d.Meta }), map[string]any{"role": "admin"}),
)
```

---

## Vector search

Vector columns are first-class and can be used for nearest-neighbor retrieval, semantic search, and recommendation workloads.

```go
type Article struct {
    ID       int64    `db:"id,pk,autoincrement"`
    Title    string   `db:"title"`
    Embedding []float32 `db:"embedding,vector(1536)"`
}

queryVec := []float32{0.1, 0.2, 0.3}
items, err := orm.SelectList(ctx, db,
    orm.NewQuery[Article]().
        Nearest(orm.Col[Article](func(a *Article) *[]float32 { return &a.Embedding }), queryVec, 10),
)
```

This is intended for:

- knowledge-base search
- semantic retrieval and RAG pipelines
- duplicate detection
- approximate nearest-neighbor ranking

---

## Transactions

```go	err := db.Transaction(ctx, func(tx *orm.DB) error {
    if err := orm.Insert(ctx, tx, &User{Name: "Alice"}); err != nil {
        return err
    }
    return nil
})
```

If the callback returns an error, the transaction rolls back automatically.

---

## Soft delete and optimistic locking

### Soft delete

A model field can be marked as a logical delete field:

```go
type User struct {
    ID        int64     `db:"id,pk,autoincrement"`
    Name      string    `db:"name"`
    DeletedAt time.Time `db:"deleted_at,logic"`
}
```

`DeleteById` and `Delete` then update the logical-delete column instead of deleting the row immediately.

### Optimistic locking

```go
type User struct {
    ID      int64 `db:"id,pk,autoincrement"`
    Name    string `db:"name"`
    Version int64 `db:"version,version"`
}
```

Update operations automatically add the version check and increment the version counter.

---

## Aggregrates and paging

### Pagination

```go
res, err := orm.Page(ctx, db, orm.NewQuery[User](), 1, 10)
if err != nil {
    panic(err)
}

fmt.Println("total:", res.Total)
fmt.Println("pages:", res.Pages)
fmt.Println("has next:", res.HasNext)
```

### Aggregates

```go
total, err := orm.Sum(ctx, db,
    orm.NewQuery[Order]().Eq(statusCol, "paid"),
    orm.Col[Order](func(o *Order) *int64 { return &o.Amount }),
)
```

`Count`, `Exists`, `Sum`, `Avg`, `Max`, and `Min` are supported.

---

## Raw SQL

When a query is too complex for the builder, use raw SQL access:

```go
users, err := orm.RawQuery[User](ctx, db,
    "SELECT * FROM users WHERE age > ? AND status = ?", 18, 1,
)
if err != nil {
    panic(err)
}

res, err := orm.RawExec(ctx, db, "UPDATE users SET status = ? WHERE id = ?", 2, 7)
```

This still keeps parameter binding safe.

---

## Dry run and SQL inspection

You can inspect the SQL that would be generated without executing it:

```go
q := orm.NewQuery[User]().Eq(nameCol, "Alice")
sql, args := q.ToSQL()
fmt.Println(sql, args)
```

You can also get the final executable SQL with db context applied:

```go
sql, args := orm.DryRun(db, q)
fmt.Println(sql)
fmt.Println(args)
```

This is especially useful in tests, debugging, and DB review.

---

## Tags and model metadata

The ORM reads metadata from the Go struct tags.

Common tags:

- `db:"id,pk"`
- `db:"id,pk,autoincrement"`
- `db:"meta,json"`
- `db:"embedding,vector(1536)"`
- `db:"deleted_at,logic"`
- `db:"version,version"`
- `db:"-"` for ignored fields

If a field name is `ID` or `Id`, it is treated as a primary key automatically when appropriate.

---

## Examples and benchmarks

The project includes examples and benchmark code for validating behavior and performance across realistic workloads.

Run the benchmark suite:

```bash
cd bench
go test -run '^$' -bench . -benchmem -benchtime=2s -count=3
```

For integration testing against real databases:

```bash
cd integration
go test ./...
```

---

## License

MIT

---

## Notes

This English README reflects the current repository behavior and examples. For the original Chinese project documentation, see [README.md](README.md).

If you want, the next step is to add a dedicated `docs/` section with a full API reference and a more granular examples index.
