# gobreath-orm

A Go ORM designed for AI-native applications and type-safe SQL workflows.

This project is built around the idea that data access should be simple, safe, and explicit:

- zero magic string column names when using typed selectors
- no hidden driver dependency in the core package
- SQL generation remains predictable and inspectable
- vector retrieval is built in for RAG and semantic search use cases

English version of the project overview. The original Chinese README remains at [README.md](README.md).

## Why gobreath-orm

`gobreath-orm` is an AI-ready, type-safe ORM with zero magic column-name strings. It supports Postgres (pgvector), MySQL 9+ (native VECTOR), and SQLite.

The same vector API works across databases without adding a vendor-specific dependency like `pgvector-go`.

This makes it a natural fit for:

- knowledge-base search
- semantic retrieval and RAG pipelines
- duplicate detection and recommendation
- structured CRUD plus analytics queries

## Highlights

- AI-native vector search with `Nearest` and `WithinDistance`
- zero magic field names via `orm.Col[T](...)`
- query builder with chainable conditions and `OR` groups
- MyBatis-Plus style condition blocks via `Or()` and `If(cond, func(...))`
- table name inference and prefix support
- full CRUD support including inserts, updates, deletes, pagination, and aggregates
- Dry-run SQL preview via `Query.ToSQL()` and `orm.DryRun(...)`
- native transactions via `db.Transaction(...)`
- JSON column support with automatic marshal/unmarshal
- SQL injection mitigation through parameterized values and structured identifiers
- raw SQL escape hatches for complex queries
- join support, upsert support, partial updates, and optimistic locking
- soft delete, read/write split, and migration helpers

## Installation

```bash
go get github.com/wusenshan/gobreath-orm
```

Requires Go 1.23+.

## Database drivers

The framework does not include database drivers by design. You import the one matching your runtime.

```go
import (
    // PostgreSQL
    _ "github.com/lib/pq"                 // driver name: postgres
    // _ "github.com/jackc/pgx/v5/stdlib" // driver name: pgx

    // MySQL
    _ "github.com/go-sql-driver/mysql"    // driver name: mysql

    // SQLite
    // _ "github.com/mattn/go-sqlite3" // driver name: sqlite3
    // _ "modernc.org/sqlite"          // driver name: sqlite
)
```

The relation between the driver and dialect is:

- `postgres` / `pgx` -> Postgres
- `mysql` -> MySQL
- `sqlite` / `sqlite3` -> SQLite

## Quick start

```go
package main

import (
    "context"
    "fmt"

    "github.com/wusenshan/gobreath-orm"
)

type User struct {
    ID    int64  `db:"id,pk,autoincrement"`
    Name  string `db:"name"`
    Age   int    `db:"age"`
    Email string `db:"email"`
    Status int  `db:"status"`
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

## Typed selectors

Instead of referring to columns by raw strings, use typed selectors:

```go
nameCol := orm.Col[User](func(u *User) *string { return &u.Name })
statusCol := orm.Col[User](func(u *User) *int { return &u.Status })

users, err := orm.SelectList(ctx, db,
    orm.NewQuery[User]().
        Eq(statusCol, 1).
        Gt(orm.Col[User](func(u *User) *int { return &u.Age }), 18).
        OrderBy(orm.Col[User](func(u *User) *int { return &u.Age }), false).
        Limit(10),
)
```

## Config and database connection

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

The ergonomic configuration avoids repeating parameter order and keeps DB, prefix, logger, and pool settings in one place.

## CRUD basics

```go
// insert
u := &User{Name: "Bob", Age: 30}
_ = orm.Insert(ctx, db, u)

// select by primary key
user, _ := orm.SelectById[User](ctx, db, u.ID)

// list
list, _ := orm.SelectList(ctx, db, orm.NewQuery[User]().Eq(nameCol, "Bob"))

// page
page, _ := orm.Page(ctx, db, orm.NewQuery[User](), 1, 10)

// count
count, _ := orm.Count(ctx, db, orm.NewQuery[User]().Gt(ageCol, 20))

// update
u.Name = "Bobby"
_ = orm.UpdateById(ctx, db, u)

// delete
_ = orm.DeleteById[User](ctx, db, u.ID)
```

## Repository mode

A repository wrapper is available to reduce repeated generic parameters:

```go
users := orm.NewRepo[User](db)
user, err := users.SelectById(ctx, 1)
if err != nil {
    panic(err)
}
```

## Vector search

Vector columns and nearest-neighbor search are first-class features.

```go
type Article struct {
    ID       int64    `db:"id,pk,autoincrement"`
    Title    string   `db:"title"`
    Embedding []float32 `db:"embedding,vector(1536)"`
}

items, err := orm.SelectList(ctx, db,
    orm.NewQuery[Article]().
        Nearest(orm.Col[Article](func(a *Article) *[]float32 { return &a.Embedding }), queryVec, 10),
)
```

This is useful for semantic search, recommendation, and retrieval pipelines.

## Transactions and hooks

```go	err := db.Transaction(ctx, func(tx *orm.DB) error {
    // write inside transaction
    return nil
})
```

Hooks and lifecycle extensions are also supported through the database config, making it easier to instrument SQL execution or add tracing.

## Testing and benchmarking

The repository includes benchmark and integration arrangements for real database validation.

Examples:

```bash
cd bench
go test -run "^$" -bench . -benchmem -benchtime=2s -count=3
```

For integration testing against PostgreSQL / MySQL / SQLite:

```bash
cd integration
go test ./...
```

## License

MIT

## Notes

This English version reflects the current repository behavior and features. For the original Chinese project documentation, see [README.md](README.md).
