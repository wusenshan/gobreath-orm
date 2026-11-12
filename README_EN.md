# gobreath-orm

[![Go Reference](https://pkg.go.dev/badge/github.com/wusenshan/gobreath-orm.svg)](https://pkg.go.dev/github.com/wusenshan/gobreath-orm)
[![Go Report Card](https://goreportcard.com/badge/github.com/wusenshan/gobreath-orm)](https://goreportcard.com/report/github.com/wusenshan/gobreath-orm)
[![CI](https://github.com/wusenshan/gobreath-orm/actions/workflows/ci.yml/badge.svg)](https://github.com/wusenshan/gobreath-orm/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
![Go](https://img.shields.io/badge/go-1.23%2B-00ADD8)

**English** | [简体中文](README.md)

A Go ORM built for AI-native applications: type-safe column access, predictable SQL, and vector search as a first-class citizen.

`gobreath-orm` is built around three ideas:

- **type-safe column access** instead of raw string literals — column names come from struct `db` tags
- **predictable SQL generation** with real dialect handling (PostgreSQL / MySQL / SQLite)
- **built-in vector search** for RAG, semantic retrieval, recommendation, and near-duplicate detection

It supports PostgreSQL, MySQL, and SQLite, while keeping the core package free of any database-driver dependency.

Dedicated vector documentation: [VECTOR.md](VECTOR.en.md).

---

## Why gobreath-orm

This project targets production application code, not toy CRUD examples.

It solves the problems that keep showing up in real services:

- too many `"name"` / `"user_id"` string literals scattered through query code
- generated SQL that is hard to reproduce and hard to debug
- silent type mismatches between application code and the database schema
- vector retrieval bolted on as a separate tool instead of being first-class
- dialect-specific SQL leaking into business logic

Compared with a typical ORM, `gobreath-orm` keeps the important parts explicit:

- type-safe column selectors via `Col[T](...)`
- conditional query builder with `Eq` / `In` / `Like` / `Json` / `Nearest`
- raw SQL escape hatches for advanced queries
- repository-style wrappers
- transaction support
- soft delete and optimistic locking
- JSON column support
- vector search across dialects

Its guiding principle is **fail loudly instead of guessing**: invalid column names, zero primary keys, unbounded `UPDATE` / `DELETE`, and forbidden option combinations are rejected before any SQL reaches the database.

---

## Features

- 🌟 **AI-native vector search (headline feature)**: `Nearest` / `WithinDistance` with one API for both PostgreSQL (pgvector) and MySQL 9+; four metrics — Cosine / L2 / InnerProduct / L1. Zero extra dependencies, no `pgvector-go`: `[]float32` is serialized to `[..]` automatically. Full reasoning, worked examples, and how to wire an embedding model: [VECTOR.md](VECTOR.en.md).<br>*Note: MySQL's `VECTOR_DISTANCE` function and vector indexes are only available in HeatWave on OCI and MySQL AI — community/commercial builds can create and store vector columns but cannot query by distance. MySQL distance functions also support only `COSINE` / `DOT` / `EUCLIDEAN`, so **`orm.L1` (Manhattan) is PostgreSQL-only**. Run the self-check in VECTOR.md §7 before going live.*
- 🦭 **Zero field-name strings**: select columns with `orm.Col[T](func(u *T) *F { return &u.Name })`; the column name is derived from the `db` tag. With the `ormgen` code generator this shrinks further to `UserCols.Age`, so a wrong field is a compile error.
- 🧩 **Generics + chainable conditions**: `Eq / Ne / Gt / Ge / Lt / Le / Like / In / NotIn / Between / IsNull / IsNotNull`, with `AND/OR` grouping and placeholder handling done for you.
- 🪄 **MyBatis-Plus style condition blocks**: `Or()` and `If(cond, func(q))` mirror MP's `.or()` and its three-argument conditionals (Go has no method overloading, so a condition block is used instead).
- 🗂 **Automatic table naming**: `User` → `users` (snake_case, pluralized), or implement `TableName()` to be explicit; DB-level table prefixes (`t_users`) are supported.
- 🧱 **Complete CRUD**: `Insert / BatchInsert / SelectById / SelectByIds / SelectList / SelectOne / QueryIter / Count / Exists / Page / UpdateById / UpdateByIds / Save / Update / DeleteById / DeleteByIds / Delete`, with auto-increment primary keys written back. Plus aggregates `Sum / Avg / Max / Min` (and strongly typed `*Of` variants) and single-column extraction via `Pluck / PluckCol`.
- 🔎 **SQL preview**: `Query.ToSQL()` and `orm.DryRun(db, q)` give you the final SQL and arguments without touching the database (comparable to GORM's DryRun) — useful for unit-test assertions and DBA review.
- 🔒 **Native transactions**: `db.Transaction(ctx, func(tx *orm.DB) error)`.
- 📦 **JSON columns**: tag a field with `db:"meta,json"` and map/struct fields are marshalled to and from the JSON column automatically; path queries and `JSON_CONTAINS / @>` containment are supported on all three dialects.
- 🛡 **Three-layer injection defence**: parameterized value binding + column names restricted to the struct-tag whitelist + table-name validation and quoting.
- ⚡ **Raw SQL escape hatch**: `RawQuery / RawOne / RawExec` run arbitrary SQL and scan results automatically; column aliases and non-table DTOs are supported (the equivalent of handing complex SQL to XML in MyBatis-Plus).
- ⚙️ **Struct-based config**: `orm.Open(orm.Config{...})` configures driver / prefix / logger / pool in one named-argument struct; the older `Open(driver, dsn)` form still works.
- 🔗 **Joins**: `Join / LeftJoin / RightJoin` (plus `As` alias variants) with a main-table `Alias()`; table names are whitelisted and the `ON` clause is passed through verbatim; `Select` accepts aliased columns like `u.name`.
- 🔁 **Upsert**: `Upsert / BatchUpsert` dispatched by dialect — `ON CONFLICT ... DO UPDATE` on PostgreSQL / SQLite, `ON DUPLICATE KEY UPDATE` on MySQL. Conflict key defaults to the primary key and can be overridden; auto-increment keys are written back (via `RETURNING` on PG/SQLite, `LAST_INSERT_ID(id)` on MySQL).
- 🎯 **Partial updates (multi-column / map)**: `Query.Set(col, val)` + `UpdateSets`, or `UpdatePartial / UpdateByIdSets` with `map[string]any`. A `WHERE` clause is mandatory — unbounded updates are rejected.
- 🔐 **Optimistic locking**: mark a version column with `db:"version,version"` (or `Config.OptimisticField`); `UpdateById / UpdateByIdSets` add `WHERE version = ?` and `SET version = version + 1`, returning `ErrOptimisticLock` on conflict.
- 🪝 **SQL lifecycle hooks**: register a `Hook` implementation via `Config.Hooks` or `db.WithHooks(...)`; `On(HookEvent)` fires before and after every `exec` / `query`, giving you auditing, rate limiting, and tracing without touching call sites. Zero cost when unregistered.
- 🌐 **Read/write splitting and multiple data sources**: `Config.ReadWrite` (or the equivalent `Config.MultiSource`) declares a primary plus read replicas; writes are routed to the primary, reads round-robin across replicas, and transactions fall back to the primary. **Pessimistic reads such as `SELECT ... FOR UPDATE` / `FOR SHARE` are detected and routed to the primary** so locks are not silently lost on a read replica. The router is mutex-protected and concurrency-safe.
- 🗃 **AutoMigrate**: `db.AutoMigrate(ctx, &User{}, &Order{})` creates tables idempotently (`CREATE TABLE IF NOT EXISTS`), **adds missing columns to existing tables** (`ALTER TABLE ADD COLUMN`, add-only), and creates secondary indexes (PG/SQLite use `CREATE INDEX IF NOT EXISTS`; MySQL has no such clause, so idempotency comes from ignoring "index already exists"). DDL is generated for all three dialects, including `,vector(N)` (PG `vector(N)` / MySQL `VECTOR(N)` / SQLite `TEXT`), `,json` (PG `JSONB` / MySQL `JSON` / SQLite `TEXT`), and `,unique` / `,index`. No external migration tool required to keep schema and structs aligned.
- 🔗 **Preload**: `orm.Preload(ctx, db, &users, "Articles")` batch-loads **has_many / has_one / belongs_to** associations, avoiding N+1 queries. The default foreign-key convention is `<type>_id` (`User` → `user_id`), overridable with `orm:"has_many;fk:user_id"` / `orm:"belongs_to;fk:xxx"`. Soft-delete filtering also applies to the child query.
- ⋇ **Distinct**: `orm.NewQuery[T]().Distinct()` emits `SELECT DISTINCT` and composes with `Select` / conditions / ordering / paging. ⚠️ **It cannot be combined with distance ordering (`Nearest` / `NearestBy`)**: the distance column differs per row, so including it makes `DISTINCT` a no-op, while excluding it makes `ORDER BY` illegal under `SELECT DISTINCT` (PG always rejects, MySQL rejects when the sort column is not projected, SQLite accepts — no cross-dialect consistency). That combination **panics** in `SelectList` / `Count` / `Page` / aggregates rather than letting the database report a syntax error. **A pure threshold filter via `WithinDistance` is unaffected**: it neither sorts nor projects `dist`, so `SELECT DISTINCT * FROM t WHERE distance < ?` is perfectly legal (and "how many distinct rows are in this neighbourhood" remains answerable).
- 🗑 **Audited hard delete (`Remove`)**: `Remove / RemoveQuery / RemoveSQL` capture a row-level snapshot before physically deleting, then hand it to a `RemoveListener` — so you know exactly which rows were destroyed, and deleted (soft-deleted) rows are included.

---

## Installation

```bash
go get github.com/wusenshan/gobreath-orm
```

Requires Go 1.23+.

**30-second tour**: the repo ships a zero-dependency runnable example (pure-Go SQLite driver), no database to install:

```bash
git clone https://github.com/wusenshan/gobreath-orm.git
cd gobreath-orm/examples/quickstart
go run .
```

It covers table creation, insert (primary-key write-back + JSON column), closure-based conditions, raw SQL with a DTO aggregate, and a transactional update — enough to get the shape of the API.

---

## Database drivers

Go's `database/sql` is **driver-agnostic**: this framework only generates SQL and handles dialects, and **ships no database driver**. You must blank-import the driver in **your own `main` / application package**; the driver's `init()` calls `sql.Register` to register itself with the standard library. Skip that step and the first query fails with:

```
sql: unknown driver "mysql" (forgotten import?)
```

(The framework deliberately does not import drivers for you — otherwise pgx / mysql / sqlite3 would all become hard dependencies, and sqlite3 additionally requires CGO, polluting your project.)

Import exactly one driver for the database you actually use:

```go
import (
    // PostgreSQL (pick one)
    _ "github.com/lib/pq"                 // driver name: "postgres"
    // _ "github.com/jackc/pgx/v5/stdlib" // driver name: "pgx" (more modern, recommended)

    // MySQL
    _ "github.com/go-sql-driver/mysql"    // driver name: "mysql"

    // SQLite
    // _ "github.com/mattn/go-sqlite3"    // driver name: "sqlite3" (requires CGO)
    // _ "modernc.org/sqlite"             // driver name: "sqlite"  (pure Go, no CGO)
)
```

Driver-to-dialect mapping:

- `postgres` / `pgx` → PostgreSQL
- `mysql` → MySQL
- `sqlite` / `sqlite3` → SQLite

This keeps the core ORM independent of CGO-heavy or vendor-specific dependencies.

---

## Quick start

### 1. Define the model

```go
type User struct {
    Id     int64  `db:"id,pk,autoincrement"`
    Name   string `db:"name"`
    Age    int    `db:"age"`
    Email  string `db:"email"`
    Status int    `db:"status"`
}

// Optional: without this, the table name is inferred (User -> users).
func (User) TableName() string { return "users" }
```

### 2. Open a connection

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

### 3. Minimal runnable example

```go
package main

import (
    "context"
    "fmt"

    orm "github.com/wusenshan/gobreath-orm"
)

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

    got, err := orm.SelectById[User](ctx, db, u.Id)
    if err != nil {
        panic(err)
    }

    got.Status = 0
    if err := orm.UpdateById(ctx, db, got); err != nil {
        panic(err)
    }

    fmt.Println("user id:", u.Id) // auto-increment value written back
    fmt.Println("status:", got.Status)
}
```

---

## Complete CRUD tour

The examples below use one coherent set of column selectors:

```go
nameCol    := orm.Col[User](func(u *User) *string          { return &u.Name })
ageCol     := orm.Col[User](func(u *User) *int             { return &u.Age })
statusCol  := orm.Col[User](func(u *User) *int             { return &u.Status })
profileCol := orm.Col[User](func(u *User) *map[string]any  { return &u.Profile })
```

### Insert

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
// The auto-increment primary key is written back into u.Id.
fmt.Println("new id =", u.Id)
```

> 💡 **Dialect difference for primary-key write-back**: MySQL / SQLite use the standard `sql.Result.LastInsertId()`. **PostgreSQL (pgx via `database/sql`) does not support `LastInsertId()`**, so the framework automatically switches to `INSERT ... RETURNING "id"` and scans the single row — transparent to the caller, no extra code required.
> Note: `BatchInsert` takes a value slice (elements cannot be written back), so it does not backfill auto-increment keys; only single-row `Insert` does.

### BatchInsert

```go
users := []User{
    {Name: "Bob",   Age: 20, Status: 1},
    {Name: "Carol", Age: 25, Status: 1},
}
if err := orm.BatchInsert(ctx, db, users); err != nil {
    panic(err)
}
```

> **Large batches are chunked automatically**: `BatchInsert` / `BatchUpsert` split entities into batches of "dialect parameter limit ÷ columns per row", one `INSERT` per batch (SQLite allows 32766 variables, PG / MySQL 65535 parameters — without chunking, tens of thousands of rows hit `too many SQL variables` / `Error 1390`). Chunking only guarantees **per-statement** atomicity, **not across batches**: for "all or nothing", wrap the call in `db.Transaction` and every batch joins the same transaction.

### SelectById

```go
u, err := orm.SelectById[User](ctx, db, 1)
if errors.Is(err, orm.ErrNotFound) {
    // no such row
}
```

### Batch operations by primary key (SelectByIds / DeleteByIds / UpdateByIdsSets / UpdateByIds)

The most common service-layer "query / delete / update by ID list" operations have first-class entries, with soft-delete semantics identical to the singular versions (logical delete is used when an active `logic` column exists):

```go
// Batch select (soft-deleted rows filtered out automatically); result order is unrelated to ids order.
users, err := orm.SelectByIds[User](ctx, db, 1, 2, 3)

// Batch delete: returns affected rows (soft delete for logic models; ForceDeleteByIds physically deletes).
n, err := orm.DeleteByIds[User](ctx, db, 4, 5)

// Batch partial update (map form: the same sets applied to every row, same semantics as UpdateByIdSets).
n, err := orm.UpdateByIdsSets[User](ctx, db,
    []any{int64(4), int64(5)},
    map[string]any{"status": 2, "reviewed_by": "admin"})

// Per-row entity update (each row has different values: same contract as UpdateById, all writable columns).
err = orm.UpdateByIds[User](ctx, db, users) // executed row by row, no implicit transaction, stops on first error

// Need all-or-nothing? Wrap it yourself:
err = db.Transaction(ctx, func(tx *orm.DB) error {
    return orm.UpdateByIds(ctx, tx, users)
})
```

> ⚠️ **An empty ID list returns `ErrInvalidArgument`**: `IN ()` is not valid SQL, and folding an empty list into an always-true condition would mean a full-table read or delete — such calls are almost always a missing parameter, so the framework fails loudly instead of silently scanning the table.
> The number of IDs is bounded by each dialect's placeholder limit (PG / MySQL 65535, SQLite 32766); the framework does not chunk automatically. Batch updates do not support optimistic-lock version columns (versions differ per row and cannot be expressed as a single `WHERE version = ?`); a `sets` map containing the version column is rejected.
>
> `UpdateByIds` (taking `[]T`) differs from the three APIs above in ways that matter: it **issues one SQL per row** (each row has different `SET` values; merging would require `CASE WHEN`), so do not compare its throughput with `BatchInsert`; it **does not open an implicit transaction**, so a mid-way failure leaves earlier rows updated; a **zero primary key is a hard error** (batch update never inserts, and a zero key usually means the entity was assembled incorrectly); an empty slice is a no-op returning nil. In exchange it fully supports optimistic locking — per-row comparison, per-row version increment, aborting with `ErrOptimisticLock` on the first conflict.

### SelectList

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

> `Limit` / `Offset` **reject negative values**: negative `LIMIT` semantics are contradictory across databases (PG / MySQL raise an error, SQLite treats it as "no limit"), while previously the framework silently emitted no clause — `Limit(-1)` looked bounded but returned the whole table. To mean "no row limit", simply do not call `Limit`.

### Streaming large tables (QueryIter)

For exports, cleanup, and ETL workloads that process millions of rows, `SelectList` pulls the whole result set into memory. `QueryIter` uses a **single cursor yielding row by row**, with constant memory usage:

```go
for u, err := range orm.QueryIter[User](ctx, db, orm.NewQuery[User]().OrderBy(idCol, false)) {
    if err != nil {
        return err // row-mapping or network error, stop iterating
    }
    if process(u) {
        break // closes the cursor and returns the connection immediately
    }
}

// Same wrapper on Repo
for u, err := range repo.QueryIter(ctx, q) { ... }
```

Semantics and costs:

- **One cursor, not batched OFFSET paging**: OFFSET paging skips or duplicates rows when the sort key is not stable or the table is written during iteration — the framework cannot guarantee a stable sort key for you. A single cursor means exactly "the result set of this one query".
- **The cursor holds a connection** until iteration ends / breaks / returns — with a small pool, many concurrent iterations will wait on each other.
- Soft-delete filtering matches `SelectList` exactly (`Unscoped` excepted); `Limit` / `OrderBy` / `GroupBy` / `Distinct` all come from the supplied `q` (iteration stops at `Limit`).
- Writing to the same table during iteration and continuing to read depends on the dialect's isolation level; wrap in a transaction if you need cross-dialect consistency.

### SelectOne

```go
u, err := orm.SelectOne(ctx, db,
    orm.NewQuery[User]().Eq(nameCol, "Alice"),
)
if errors.Is(err, orm.ErrNotFound) {
    // no one named Alice
}
```

### Page

`Page` returns a generic `PageResult[T]` carrying both the current page and its metadata:

```go
// returns: *PageResult[User], error
pr, err := orm.Page(ctx, db,
    orm.NewQuery[User]().Eq(statusCol, 1),
    1,   // page number, 1-based (invalid values normalise to 1)
    10,  // page size (invalid values normalise to 10)
)
// pr.List    []User   rows on this page
// pr.Total   int64    total matching rows
// pr.Pages   int      total pages
// pr.Page    int      current page
// pr.Size    int      page size
// pr.HasNext bool     whether a next page exists
// pr.HasPrev bool     whether a previous page exists
fmt.Printf("total=%d pages=%d hasNext=%v\n", pr.Total, pr.Pages, pr.HasNext)
```

> `Page` runs `Count` first to obtain `Total`, then fetches the page with `LIMIT/OFFSET`. `Total`, `Pages`, `HasNext`, and `HasPrev` are all computed for you.

### Repository layer

If writing `orm.SelectById[User](...)` everywhere feels noisy, bind `*DB` and the entity type `T` into a `Repo[T]` handle (DAO / repository style). Method calls then **omit the type parameter**:

```go
// Create a repository bound to User (Go has no generic methods, hence the NewRepo[T] function).
users := orm.NewRepo[User](db)

u, err := users.SelectById(ctx, 1)                              // no [User] needed
list, err := users.SelectList(ctx, orm.NewQuery[User]().Eq(statusCol, 1))
pr, err := users.Page(ctx, orm.NewQuery[User](), 1, 10)
n, err := users.Count(ctx, orm.NewQuery[User]().Gt(ageCol, 18))

// Transaction callbacks are bound to T as well.
err := users.Transaction(ctx, func(tx *orm.Repo[User]) error {
    if _, e := tx.SelectById(ctx, 1); e != nil {
        return e
    }
    return tx.UpdateById(ctx, u)
})
```

`Repo[T]` forwards everything: `Insert / BatchInsert / SelectById / SelectByIds / SelectList / SelectOne / QueryIter / Page / Count / Exists / UpdateById / UpdateByIds / Save / Update / DeleteById / DeleteByIds / ForceDeleteByIds / UpdateByIdsSets / Delete / ForceDelete / Remove / RemoveQuery / RemoveSQL / Transaction` — identical to the package-level generic functions, just with `T` fixed up front.

### UpdateById

```go
u, _ := orm.SelectById[User](ctx, db, 1)
u.Status = 0
if err := orm.UpdateById(ctx, db, u); err != nil {
    panic(err)
}
```

### Save (insert-or-update by primary key)

```go
// Zero primary key -> Insert (auto-increment key written back as usual).
u := &User{Name: "erin", Age: 28}
if err := orm.Save[User](ctx, db, u); err != nil { ... }

// Non-zero primary key -> full-column update (same contract as UpdateById).
u.Age = 99
if err := orm.Save[User](ctx, db, u); err != nil { ... }

// Non-zero primary key but the row does not exist (or is soft-deleted) -> ErrNotFound.
if errors.Is(err, orm.ErrNotFound) {
    // No silent fallback to Insert: holding a non-zero key and inserting a new row
    // usually means the key was cleared by accident or the row was deleted concurrently.
    // For a real upsert, use Upsert.
}
```

> `Save`'s update is a **full-column write-back**: unassigned fields are written back as zero values (including audit columns such as `created_at`). To change only some fields, use `UpdateByIdSets` / `UpdatePartial`, or restrict columns with `OnlyColumns(...)`.

### Conditional update (Update)

> The second argument is the "source of new values": its non-primary-key fields become the `SET` clause. **A `WHERE` clause is mandatory** — otherwise an error is returned (no full-table updates).

```go
// Set status to 0 for every user named Alice.
update := &User{Status: 0}
err := orm.Update(ctx, db,
    orm.NewQuery[User]().Eq(nameCol, "Alice"),
    update,
)
```

### DeleteById

```go
if err := orm.DeleteById[User](ctx, db, 1); err != nil {
    panic(err)
}
```

### Conditional delete (Delete)

> **A `WHERE` clause is mandatory** — unconditional full-table deletes are rejected.

```go
err := orm.Delete(ctx, db, orm.NewQuery[User]().Eq(nameCol, "Bob"))
```

### Count / Exists

```go
n, err := orm.Count(ctx, db, orm.NewQuery[User]().Gt(ageCol, 18))
hasVip, err := orm.Exists(ctx, db,
    orm.NewQuery[User]().JsonContains(profileCol, map[string]any{"tags": []string{"vip"}}),
)
```

### Aggregates (Sum / Avg / Max / Min)

```go
total, err := orm.Sum(ctx, db,
    orm.NewQuery[Order]().Eq(statusCol, "paid"),
    orm.Col[Order](func(o *Order) *int64 { return &o.Amount }),
)
avg, err := orm.Avg(ctx, db, q, amountCol)
maxAge, err := orm.Max(ctx, db, orm.NewQuery[User](), ageCol) // returns any (driver-native type)
```

Aggregate columns also come from the struct's `db` tag (`orm.Col` / `orm.TCol` / an ormgen column set) — you cannot write a bare string column. The contract matches `Count`: soft-delete filtering is applied automatically, and joins, table aliases, and DB-level prefixes are respected.

- **Empty result set**: `Sum` / `Avg` return 0 (SQL `NULL` is normalised), no error.
- **Return type**: `Max` / `Min` depend on the column, so they return `any` (numeric → `int64`/`float64`/`string`, time columns → `time.Time`; PostgreSQL `numeric` is normalised from `[]byte` to `string`). For a guaranteed type, use the strongly typed variants:

```go
// Latest value of a time column: MaxOf scans straight into time.Time, no assertion needed.
last, err := orm.MaxOf(ctx, db, q, orm.TCol(func(u *User) *time.Time { return &u.CreatedAt }))
```

- **`GROUP BY` / `HAVING` is rejected**: such queries return multiple rows, and a single-value API would silently return just the first — worse than an error. Use aggregate projections (`SelectSum` / `SelectCount`, …) plus `orm.Aggregate[DTO]` to scan grouped results into your own struct (see "Grouping (GroupBy / Having)").

### Pluck (single-column extraction)

Query one column and get a strongly typed slice directly, instead of selecting whole rows and looping:

```go
// Recommended: every type is inferred, no type parameters needed.
ids, err := orm.Pluck(ctx, db, orm.NewQuery[User]().Eq(statusCol, 1),
    orm.TCol(func(u *User) *int64 { return &u.Id }))

// With an ormgen column set (typed as orm.ColExpr, without the field type): the element type must be given.
ids, err = orm.PluckCol[User, int64](ctx, db, q, UserCols.Id)
```

`F` should be a scalar type. `NULL` values fill the position with `F`'s zero value, keeping indexes aligned with the result set. Unlike aggregates, `Pluck` is an ordinary query — `OrderBy / Limit / Offset / Join` all apply.

> **Why two functions**: Go cannot infer type parameters from an interface-typed argument. `Pluck` takes `orm.TColExpr` (which carries the field type), so everything is inferred; `PluckCol` accepts any column expression (including ormgen's `ColExpr`) at the cost of spelling out the element type.

### Previewing SQL (ToSQL / DryRun)

```go
sql, args := q.ToSQL()          // only the builder's own state, no database access
sql, args = orm.DryRun(db, q)   // adds the DB's dialect / prefix / soft-delete condition — the statement that would really run
```

`ToSQL` is for unit-test assertions and quick debugging; `DryRun` applies DB-level configuration (comparable to GORM's DryRun) and is handy for handing slow queries to a DBA.

### Reusing a Query

`Query[T]` is reusable: every query entry point (`SelectList` / `SelectOne` / `Count` / `Exists` / `Page` / aggregates / `Pluck` / `DryRun`) adds its call-specific context only to an **internal copy** — the DB's dialect, table prefix, soft-delete condition, and any internal `LIMIT`. Your `q` is never mutated.

```go
q := orm.NewQuery[Post]().LikeRight(postTitle, "go ")

first, _ := orm.SelectOne(ctx, db, q)   // LIMIT 1 added internally; q untouched
n, _ := orm.Count(ctx, db, q)           // still counts all matching rows
all, _ := orm.SelectList(ctx, db, q)    // still returns every matching row, not just one
p, _ := orm.Page(ctx, db, q, 1, 10)     // paging LIMIT / OFFSET also applies to this call only
```

So `q.ToSQL()` is **byte-for-byte identical** before and after any call (it reflects only the builder's own state).

---

## Nullable columns and zero values (OmitZero / pointers)

Whether a zero value (`0` / `""` / `false`) should reach the database is the classic ORM ambiguity. `gobreath-orm` offers two complementary mechanisms — pick by scenario.

### 1. Pointer fields: express NULL precisely (recommended, type-safe)

Declare nullable columns as pointer types; "nil or not" then distinguishes "unset (NULL)" from "explicitly zero" with no ambiguity:

```go
type Product struct {
    Id    int    `db:"id,pk,autoincrement"`
    Name  string `db:"name"`
    Score *int   `db:"score"` // nullable: nil -> NULL, Ptr(0) -> 0
}
```

The write path (`argFor`) passes the field value straight through, and `database/sql` converts a **nil pointer to SQL NULL** and dereferences non-nil pointers — no framework magic, zero-value semantics unambiguous. Use `orm.Ptr` for convenience:

```go
e := Product{ Score: orm.Ptr(0) }   // explicitly store 0
e2 := Product{}                     // Score=nil -> INSERT score=NULL
orm.Insert(ctx, db, &e)
```

> Note: `OmitZero` **does not** skip pointer fields (their NULL/value semantics are decided by nil); only value types are skipped.

### 2. OmitZero: skip zero-value columns for value types (convenience)

If you would rather not make everything a pointer, enable `OmitZero()` on Insert / Update and the framework skips writable columns whose value is the type's zero value (primary keys excepted), letting database defaults or NULL take over:

```go
// Only Name is written; Age=0 is skipped, so the column default / NULL applies.
orm.Insert(ctx, db, &User{Name: "alice", Age: 0}, orm.OmitZero())

// On update, only non-zero fields are overwritten — other columns are not flattened to 0 / "".
orm.UpdateById(ctx, db, &User{Id: 1, Name: "bob"}, orm.OmitZero())
```

The repository layer supports it too: `repo.Insert(ctx, &User{Name: "alice"}, orm.OmitZero())`.

Behaviour notes and trade-offs:

- Literal `0` / `""` / `false` / zero time are skipped when `OmitZero` is active; **if you need to write those explicitly, use pointer fields or do not enable the option**.
- Pointer / interface / slice / map fields are never skipped (NULL semantics belong to the driver).
- `OmitZero` is **opt-in**; default behaviour is unchanged (zero values are written as usual). Determinism first.

### 3. OnlyColumns: update exactly the listed columns (precise, no guessing)

`OmitZero` decides by "is the value zero", but sometimes you mean "only touch these columns, leave everything else alone" — the two are not equivalent. `UpdateById` / `Update` write **all** writable columns by default, so unassigned fields are written back too:

```go
type Article struct {
    Id        int64     `db:"id,pk,autoincrement"`
    Title     string    `db:"title"`
    Body      string    `db:"body"`
    CreatedAt time.Time `db:"created_at"`
}

// You only want to change the title, but CreatedAt is zero — this wipes created_at to 0001-01-01.
orm.UpdateById(ctx, db, &Article{Id: 1, Title: "new title"})

// State your intent with a whitelist (columns come from type-safe column expressions, no hand-written strings).
orm.UpdateById(ctx, db, &Article{Id: 1, Title: "new title"},
    orm.OnlyColumns(orm.Col[Article](func(a *Article) *string { return &a.Title })))
// UPDATE "articles" SET "title" = ? WHERE "id" = ?

// With the ormgen generator, use the generated column closures — identical to the query builder.
orm.UpdateById(ctx, db, &Article{Id: 1, Title: "new title"}, orm.OnlyColumns(ArticleCols.Title))

// Column names coming from config / variables (dynamic): the string escape hatch (still validated before execution).
orm.UpdateById(ctx, db, &Article{Id: 1, Title: "new title"}, orm.OnlyColumnsRaw(dynamicCols...))
```

Behaviour notes:

- Columns must be real writable columns of the model. **A typo, or a primary-key / auto-increment / logical-delete column, is a hard error** rather than a silent skip — silent skipping hides "I thought I updated `name`, but it is not in the SQL at all". `OnlyColumns` takes `ColExpr`, so a renamed field is a compile error; the string escape hatch `OnlyColumnsRaw` is validated before execution too.
- Combined with `OmitZero`, the whitelist applies first and zero-filtering second: `OnlyColumns(ArticleCols.Title, ArticleCols.Body)` + `OmitZero()` writes only the non-zero ones among them.
- Duplicate columns inside the whitelist are de-duplicated without error; passing nothing (`OnlyColumns()`) reports "empty whitelist" instead of emitting SQL with no `SET` clause.
- Intended for expressing "which fields this update may touch" as explicit business semantics (e.g. a PATCH endpoint allowing only a few fields), rather than relying on implicit zero-skipping rules.

### 4. ormgen generates pointers from DDL

When generating models with `ormgen -ddl`, **nullable columns without a default** automatically become pointer types (`*int`, `*time.Time`), matching "nullable = may be NULL"; `NOT NULL` or `DEFAULT`-ed columns stay value types:

```bash
ormgen -ddl schema.sql -type Product
```

---

## Query builder in depth

### Condition reference

| Method | Generated SQL (illustrative) |
|---|---|
| `Eq(col, v)` | `col = ?` |
| `Ne(col, v)` | `col != ?` |
| `Gt / Ge / Lt / Le` | `col > / >= / < / <= ?` |
| `Like(col, "x")` | `col LIKE '%x%'` (**`%` added internally**, do not add your own) |
| `LikeRight(col, "x")` | `col LIKE 'x%'` (prefix match) |
| `LikeLeft(col, "x")` | `col LIKE '%x'` (suffix match) |
| `NotLike(col, "x")` | `col NOT LIKE '%x%'` |
| `NotLikeRight / NotLikeLeft` | negated prefix / suffix match |
| `In(col, []any{...})` | `col IN (?, ?)` |
| `NotIn(col, []any{...})` | `col NOT IN (?, ?)` |
| `Between(col, lo, hi)` | `col BETWEEN ? AND ?` |
| `IsNull(col)` | `col IS NULL` |
| `IsNotNull(col)` | `col IS NOT NULL` |

### Column codegen (ormgen): from hand-written closures to `UserCols.Age`

`orm.Col[T](func(u *T) *F { return &u.Age })` is zero-risk but verbose when the same field appears repeatedly in a chain. The `ormgen` generator produces a column-set struct per model, turning call sites into real field access:

```go
//go:generate go run github.com/wusenshan/gobreath-orm/cmd/ormgen -type User -out user_cols.go -dir .

type User struct { ... }
```

Running it produces `user_cols.go`:

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

Conditions then read:

```go
q := orm.NewQuery[User]().
    Ge(UserCols.Age, 10).
    Le(UserCols.Age, 20)
```

Much shorter than closures, and a typo in `UserCols.Age` is a compile error. `ColOf[T]` matches by Go field name and also falls back to the `db` tag column name; a non-existent field panics at runtime, same as `Col`.

> Recommended: put one `//go:generate` line at the top of each model file and run `go generate ./...` on save or in CI so column sets stay in sync.

### DDL generation (ormgen -ddl) and the web generator (ormgen serve)

Besides adding column closures to existing structs, `ormgen` can **generate models + column closures directly from `CREATE TABLE` statements** (ideal for existing databases, or newcomers who would rather not hand-write structs):

```bash
# Generate from DDL (dialect is sniffed from content — PG/MySQL/SQLite — not from the extension).
go run github.com/wusenshan/gobreath-orm/cmd/ormgen -ddl schema.sql -pkg model -mode perType -dir ./generated
```

- Supports multiple tables per file, `serial`/`bigserial`/`AUTO_INCREMENT`/`AUTOINCREMENT`, `vector(N)` → `[]float32` + `,vector(N)`, quoted identifiers, and `schema.table` qualified names.
- `-mode`: `perType` (each table gets `xxx.go` + `xxx_cols.go`, default) / `twoFiles` (merged `models.go` + `columns.go`) / `singleFile` (merged `models_gen.go`, structs and closures in one file).

You can also start a **local web generator**: paste Go structs or DDL, preview side by side, copy / download with one click, and copy the equivalent CLI command:

```bash
go run github.com/wusenshan/gobreath-orm/cmd/ormgen -serve   # default http://:8080
```

> The generator only parses text, has zero dependencies, and never executes uploaded content. Output is not guaranteed to compile as-is; naming and package conflicts are up to you.

### OR groups

```go
// status = 1 AND (name = 'Alice' OR age > 30)
q := orm.NewQuery[User]().
    Eq(statusCol, 1).
    Or().
    Gt(ageCol, 30)
// Note: Eq first, then Or(), means the next condition is joined to the previous one with OR
// (OR within a group, AND between groups).
```

### If blocks (MyBatis-Plus style)

Go has no method overloading, so conditional blocks cover that need. When `cond` is `false`, the whole block is ignored:

```go
// Only filter by name when keyword is non-empty.
keyword := ""
q := orm.NewQuery[User]().
    Eq(statusCol, 1).
    If(keyword != "", func(q *orm.Query[User]) {
        q.Like(nameCol, keyword) // wrapped as %keyword% internally
    })
```

Plain `if` statements work just as well (every method returns `*Query[T]` and mutates in place):

```go
q := orm.NewQuery[User]().Eq(statusCol, 1)
if keyword != "" {
    q.Like(nameCol, keyword) // contains match, equivalent to LIKE '%keyword%'
}
if prefix != "" {
    q.LikeRight(nameCol, prefix) // prefix match, equivalent to LIKE 'prefix%'
}
```

### Grouping (GroupBy / Having)

**When you only need the grouping column values** (no aggregate values), use `Select` + `GroupBy` and scan back into the model:

```go
// SELECT "status" FROM users GROUP BY "status" HAVING "status" > ?
// (HAVING applies to a column; grouping already guarantees one value per group.)
rows, err := orm.SelectList(ctx, db,
    orm.NewQuery[User]().
        SelectCol(statusCol).
        GroupBy(statusCol).
        Having(statusCol, ">", 1),
)
```

**When you need aggregate values** such as `SUM` / `COUNT`, `Select` deliberately rejects expressions (see the security section), so use the dedicated aggregate-projection builders and scan into your own struct:

```go
// Result DTO: field names / db tags matching the aliases given to SelectSum / SelectCount.
type DeptStat struct {
    Dept  string  `db:"dept"`
    Total float64 `db:"total"`
    Cnt   int64   `db:"cnt"`
}

// SELECT "dept", SUM("amount") AS "total", COUNT(*) AS "cnt"
//   FROM sales GROUP BY "dept" HAVING SUM("amount") > ? ORDER BY "dept" ASC
stats, err := orm.Aggregate[DeptStat](ctx, db,
    orm.NewQuery[Sale]().
        SelectCol(deptCol).                  // grouping column (Select("dept") also works)
        SelectSum(amountCol, "total").       // SUM("amount") AS "total"
        SelectCount("cnt").                  // COUNT(*) AS "cnt"
        GroupBy(deptCol).
        HavingSum(amountCol, ">", 100).      // HAVING SUM("amount") > ?
        OrderBy(deptCol, true),
)
```

| API | Purpose |
|---|---|
| `SelectCol(col ColExpr)` | Append a grouping column (type-safe `Select`; the column name comes from the `db` tag whitelist) |
| `SelectSum / SelectAvg / SelectMax / SelectMin(col, alias)` | Append `FN(col) AS alias` |
| `SelectCount(alias)` | Append `COUNT(*) AS alias` |
| `SelectCountDistinct(col, alias)` | Append `COUNT(DISTINCT col) AS alias` (single-column distinct count, **NULLs excluded**; not the same as whole-row `Distinct()`) |
| `HavingCount(op, val)` | `HAVING COUNT(*) op ?` (likewise `HavingSum` / `HavingAvg` / `HavingMax` / `HavingMin`) |
| `Aggregate[DTO](ctx, db, q)` | Execute and scan grouped rows into the DTO (the second type parameter is inferred from `*Query[T]`) |

Conventions:

- **Aliases are mandatory**: without one, dialects name aggregate columns differently (PG says `sum`, MySQL repeats `SUM(...)`), and the DTO cannot bind to a stable field name. Aliases must start with a letter / underscore and contain only alphanumerics and underscores, otherwise construction panics (the framework quotes them per dialect, which is also part of the injection defence).
- **DTOs need no new metadata**: row mapping already matches "result-set column name == the field's `db` column name", and an aliased aggregate column is exactly what `rows.Columns()` reports — so it lands in the same logic. No need to add "read-only column" tags to entity models (`db:"-"` would also exclude it from `INSERT`, making things worse).
- **NULL silently becomes a zero value**: `NULL` in a grouped result (e.g. a group whose `SUM` is all NULL) leaves non-pointer fields at zero — a `float64` field **cannot distinguish** "this group sums to 0" from "this group has no data". Declare the DTO field as `*float64` to distinguish them; the pointer is set to `nil`. Single-value aggregates (`Sum` / `Avg`) share the contract: an empty result set returns 0 without error.
- **You must supply the grouping columns yourself**: MySQL enables `ONLY_FULL_GROUP_BY` by default and PG requires every non-aggregated projected column to appear in `GROUP BY`; missing ones are rejected by the database — the framework does not guess grouping keys.
- Row counts and paging for these queries: `Count` automatically switches to a subquery count for `GROUP BY` / `DISTINCT` queries (``SELECT COUNT(*) FROM (<original query>) "_t"``), so `Page`'s `Total` matches the row count of the list.

### Pessimistic locks and custom tails (ForUpdate / Last)

**`ForUpdate`** appends a pessimistic row lock (`FOR UPDATE`) to the `SELECT`, for "read then write" flows that must not be overwritten concurrently:

```go
// PG / MySQL generate: SELECT * FROM users WHERE id = $1 FOR UPDATE
// SQLite has no row-level locks, so the clause degrades to an empty string.
u, err := orm.SelectOne(ctx, db,
    orm.NewQuery[User]().Eq(idCol, 1).ForUpdate(),
)
```

- Dialect-aware: PostgreSQL / MySQL emit `" FOR UPDATE"`; **SQLite degrades to nothing** (older versions would raise a syntax error).
- Only takes real effect **inside a transaction**; use it with `db.Transaction`.

**`Last`** appends a custom fragment verbatim at the **very end** of the generated SQL (comparable to MyBatis-Plus `last()`), typically for dialect-specific syntax:

```go
// Skip rows locked by other transactions (PostgreSQL / Oracle).
// SELECT * FROM users WHERE status = $1 FOR UPDATE SKIP LOCKED
orm.NewQuery[User]().
    Eq(statusCol, 1).
    ForUpdate().
    Last("SKIP LOCKED")

// Other dialect-specific tails: window-function hints, database hints, etc.
orm.NewQuery[User]().Eq(statusCol, 1).Last("/*+ MAX_EXECUTION_TIME(1000) */")
```

> ⚠️ **Security note**: the `Last` fragment is **concatenated into the SQL, not parameterized**. Only ever pass trusted / static fragments and **never any user input** — otherwise you introduce SQL injection.

The concatenation order is fixed: `... LIMIT/OFFSET → FOR UPDATE → Last`, so `Last` is always last.

> ⚠️ **`Last` and framework paging are mutually exclusive**: if the `Last` fragment contains its own `LIMIT` / `OFFSET` / `FETCH`, you may not also call `Limit` / `Offset` (including `Nearest`'s k) — that would emit two paging clauses (e.g. `... LIMIT 10 ORDER BY id DESC LIMIT 1`), which the database reports as a syntax error with no trace back to the call site. This is now a **construction-time panic**. Pick one: either use `Limit` / `Offset`, or put the whole thing (including `ORDER BY` and `LIMIT/OFFSET`) in `Last` and do not call them.
> Non-paging tails such as `SKIP LOCKED`, `FOR SHARE`, or database hints combine fine with `Limit` (the queue-style `FOR UPDATE SKIP LOCKED LIMIT 1` is the recommended pattern).

---

## Raw SQL

When the single-table builder cannot express the query (joins, subqueries, reports, dialect-specific syntax), hand it to `RawQuery / RawOne / RawExec` — the equivalent of handing complex SQL to XML in MyBatis-Plus. Arguments are still placeholder-bound, SQL logging / slow-query reporting share the same pipeline as normal CRUD, and passing a `*DB` from inside a transaction runs the statement in that transaction.

```go
// Multiple rows: write any JOIN / subquery, results are scanned into []T.
users, err := orm.RawQuery[User](ctx, db,
    "SELECT * FROM users WHERE age > ? AND status = ?", 18, 1)

// Inserts / updates / deletes / DDL
res, err := orm.RawExec(ctx, db, "UPDATE users SET status = ? WHERE id IN (?, ?)", 1, 7, 9)

// Named parameters: natively supported by database/sql, use them directly.
u, err := orm.RawOne[User](ctx, db, "SELECT * FROM users WHERE id = :id", sql.Named("id", 7))
```

### Column aliases and DTOs (non-table structs)

`T` does not have to be a table struct — any plain struct can act as a view object, **no `db` tags and no `TableName()` required**:

```go
type UserDeptVO struct {   // plain DTO, only the columns you care about
    UserName string        // no tag -> matches column user_name by snake_case field name
    DeptName string        //            matches column dept_name
}

vos, err := orm.RawQuery[UserDeptVO](ctx, db, `
    SELECT u.name AS user_name, d.name AS dept_name
    FROM users u JOIN dept d ON d.id = u.dept_id
    WHERE u.age > ?`, 18)
```

Mapping rules:

- **Exact column-name matching**: `db` tag first; without a tag, the snake_case form of the field name. An `AS` alias in the SQL becomes the returned column name, so a snake_case alias is enough to hit the field.
- **Unmatched columns are ignored**: extra columns from `SELECT u.*` do not cause errors; the DTO only carries the fields it declares.
- **Scalars work directly**: `RawOne[int64](ctx, db, "SELECT COUNT(*) ...")`, `RawQuery[string](...)` for a single column.

Two conventions (put them in your team style guide and they stop being issues):

1. **Always use snake_case aliases**: `AS deptName` folds to `deptname` in PostgreSQL and never matches `dept_name`; MySQL preserves the case but `deptName` ≠ `dept_name`.
2. **Disambiguate same-named columns in joins**: `SELECT u.id, d.id` maps both into `Id`, and the latter overwrites the former.

`Repo[T]` forwards these under the same names (`repo.RawQuery / RawOne / RawExec`, bound to `T`); when the result type is not `T`, use the package-level function `orm.RawQuery[DTO](ctx, repo.DB(), ...)`.

> The injection baseline still holds: you own the SQL text (that is the point of "raw"), but **arguments must always go through `?` / `$1` binding** — never string-concatenated.

---

## JSON support

### Storage mapping

Tag a field with `db:"...,json"` and the framework marshals/unmarshals it automatically:

```go
type Doc struct {
    ID      int64           `db:"id,pk,autoincrement"`
    Title   string          `db:"title"`
    Meta    map[string]any  `db:"meta,json"` // marshalled to / from the JSON column
}
```

- **Insert / update**: `Meta` is `json.Marshal`-ed to text and written into the `meta` column.
- **Query**: JSON text returned by the database is `Unmarshal`-ed back into `map[string]any` (pointer fields get their backing object allocated).

### Querying inside JSON by path

Paths use the `"a.b.c"` form (nested JSON keys cannot be selected with a Go field picker, so a string is used):

```go
// PG:     "profile"->'city'->>'city' = $1
// MySQL:  JSON_EXTRACT(`profile`, '$.city') = ?
// SQLite: json_extract("profile", '$.city') = ?
list, err := orm.SelectList(ctx, db,
    orm.NewQuery[User]().Json(profileCol, "city", "=", "Shanghai"),
)
```

> `Json`'s `op` accepts a whitelist only: `= != <> > >= < <= LIKE NOT LIKE IN NOT IN`. The operator is **concatenated into the SQL verbatim** (column names / paths can be parameterized, operators cannot), so arbitrary fragments are rejected — if the operator comes from configuration or user input, map it to a whitelisted value at the call site first. `Having` (including `HavingCount` / `HavingSum`, …) follows the same rule, except it does not support `IN` / `NOT IN` (its left side is an aggregate expression, so only binary comparisons make sense).

### Containment queries (the most common case)

```go
// PG:     "profile" @> $1::jsonb
// MySQL:  JSON_CONTAINS(`profile`, ?)
// SQLite: json_contains("profile", ?)
list, err := orm.SelectList(ctx, db,
    orm.NewQuery[User]().JsonContains(profileCol, map[string]any{"status": "active"}),
)
```

> The argument may be a `map` / `struct` / `slice`, or an already-serialized `[]byte` / `string` / `json.RawMessage`.

---

## Table prefixes

The prefix lives on the **DB instance** (not a global), so multiple databases or tenants do not interfere, and it is applied only when the final SQL is generated:

```go
db := orm.Open("mysql", dsn).WithPrefix("t_")
```

Rules (consistent with mainstream framework conventions):

| Scenario | Result |
|---|---|
| Model has no `TableName()`, DB prefix is `t_` | `User` → `t_users` |
| Model implements `TableName() string { return "users" }` | `users` (**no** prefix — explicit naming is respected) |
| Query uses `.Table("my_orders")` | `my_orders` (**no** prefix — explicit means the physical full name) |

The prefix is inherited inside transactions automatically: `db.WithExecutor(tx)` copies it.

---

## Soft delete

Tag a model field with `,logic` to enable soft delete (comparable to MyBatis-Plus `@TableLogic`).

- **time types** (`time.Time` / `*time.Time`): "not deleted" means `IS NULL`; deleting writes the current time.
- **int types**: "not deleted" means `= 0`; deleting writes `1`.
- **bool types**: "not deleted" means `= false`; deleting writes `true`.

Once enabled, **every read filters deleted rows automatically** (`SelectById` / `SelectList` / `SelectOne` / `Count` / `Exists` / `Page` / `Update` / `UpdateById` all append the "not deleted" condition), and **`Delete` / `DeleteById` become `UPDATE ... SET logic_col = ...`** instead of physically removing rows.

```go
type User struct {
    Id        int64      `db:"id,pk,autoincrement"`
    Name      string     `db:"name"`
    DeletedAt *time.Time `db:"deleted_at,logic"` // logical delete column
}
```

```go
// Automatic: SELECT * FROM "users" WHERE "id" = ? AND "deleted_at" IS NULL
u, _ := orm.SelectById[User](ctx, db, 1)

// Automatic soft delete: UPDATE "users" SET "deleted_at" = ? WHERE "id" = ? AND "deleted_at" IS NULL
orm.DeleteById[User](ctx, db, 1)

// Query deleted rows / hard delete: use Unscoped or ForceDelete.
orm.SelectList(ctx, db, orm.NewQuery[User]().Unscoped())      // do not filter deleted rows
orm.Delete(ctx, db, orm.NewQuery[User]().Unscoped().Eq(...))  // physical delete (ignores the logic column)
orm.ForceDeleteById[User](ctx, db, 1)                         // physical delete (Repo has ForceDelete/ForceDeleteById too)
```

### Audited hard delete: `Remove` (penetrates soft delete + row-level hook)

`ForceDelete*` is a **blind delete**: one `DELETE` goes out and all you get back is `RowsAffected` — nobody knows which rows died. When you need a record before destroying data (audit log, downstream notification, later forensics), use the `Remove` family: **`SELECT` the rows about to be deleted in full (including already soft-deleted rows — it penetrates soft delete), then delete by the captured primary keys, handing each batch's row snapshot to a `RemoveListener`**.

```go
db := orm.Open(orm.Config{
    Driver: "postgres", DSN: dsn,
    RemoveListeners: []orm.RemoveListener{orm.RemoveAuditLogger(os.Stdout)}, // ready to use
})

// Single / multiple entities
n, err := orm.Remove(ctx, db, &u)
n, err = orm.Remove(ctx, db, &u1, &u2)

// By query condition (returns the deleted-row snapshot)
rows, n, err := orm.RemoveQuery(ctx, db, orm.NewQuery[User]().Lt(
    orm.Col[User](func(u *User) *time.Time { return &u.CreatedAt }), cutoff))

// Custom SELECT to define the scope (SELECT only; for arbitrary DELETE use RawExec — that is a blind delete)
rows, n, err := orm.RemoveSQL[User](ctx, db,
    "SELECT * FROM users WHERE last_login < ? AND deleted_at IS NOT NULL", cutoff)

// Your own audit logic: the event carries the full row snapshot, the key list, the actual DELETE, and the row count.
db = db.WithRemoveListener(func(ev orm.RemoveEvent) {
    log.Printf("hard delete %s %d rows: pks=%v sql=%s", ev.Table, ev.Count, ev.PKs, ev.SQL)
    deleted := ev.Rows.([]*User) // assert back to the concrete type when you need row contents
    _ = deleted
})
```

| Point | Detail |
|---|---|
| **Select first, then delete** | The rows a hook sees == the primary-key set actually deleted (not "rows the condition was guessed to hit": rows can change between the condition and the `DELETE`, and deleting by captured keys closes that window) |
| **Penetrates soft delete** | Neither the capture nor the delete applies the "not deleted" condition, so soft-deleted rows disappear too — exactly the semantics "purge / compliance hard delete" needs |
| **Chunking** | At most 500 primary keys per `DELETE` (aligned with the preload parameter limit), **one event per chunk**, `Count` being that chunk's row count |
| **Guards before SQL** | Zero primary key, empty entity list, no condition, presence of `Limit`/`Offset`, `RemoveSQL`'s SELECT table not matching the entity, projection missing the primary key — all reported **before any `DELETE` is issued** |
| **No implicit transaction** | Capture and delete are two statements; if you need "what was read is still there", wrap it in `db.Transaction` (both statements then use the same connection) |
| **Division of labour with hooks** | `Hook` (`HookKindExec`) sees SQL text; row-level snapshots only come from `RemoveListener`. Neither replaces the other |

`Repo[T]` forwards `Remove / RemoveQuery / RemoveSQL`.

### Conventional soft-delete column name (no tag needed)

If every table in your project uses the same soft-delete column name (`deleted_at` / `deleted` / `is_del`), you can configure `SoftDeleteField` at `orm.Open` instead of tagging every model: **any field whose column name or Go field name equals that value and whose type is time/int/bool automatically enables soft delete**:

```go
db := orm.Open(orm.Config{
    Driver: "postgres", DSN: dsn,
    SoftDeleteField: "deleted_at", // conventional soft-delete column name
})

type Order struct {
    Id        int64      `db:"id,pk,autoincrement"`
    Title     string     `db:"title"`
    DeletedAt *time.Time `db:"deleted_at"` // no ,logic — but the column name matches the convention -> soft delete
}
```

- **Precedence**: explicit `db:"...,logic"` > `SoftDeleteField` convention match > neither, which means **physical delete**.
- **Matching rule**: the column name (first item of the `db` tag) or the Go field name equals `SoftDeleteField`; the type must be `time` / `int` / `bool` (pointer forms included).
- **A type mismatch panics at model-parse time** (both paths): when `SoftDeleteField` matches a same-named field with an unsupported type, the previous behaviour was to **silently not enable soft delete** — deleted rows kept showing up in query results with no clue. Likewise `,logic` on a `string` field generated `WHERE "state" = 0`, which MySQL / SQLite accept and return zero rows for, **without an error**. Both are "silently doing the wrong thing", so both now fail at startup. The escape hatch is `,nologic` below.
- **Explicit opt-out**: an entity that does not want soft delete can add `,nologic` to fall back to physical delete:

```go
type Log struct {
    Id        int64      `db:"id,pk,autoincrement"`
    Body      string     `db:"body"`
    DeletedAt *time.Time `db:"deleted_at,nologic"` // matches the convention name but opts out -> physical delete
    State     string     `db:"state,nologic"`      // matches the convention name but type unsupported -> opt out, no panic
}
```

> You can also set it via the chainable `db.WithSoftDeleteField("deleted_at")`; the setting is inherited by `WithPrefix` / `WithLogger` and friends.

**Note**: the logic column is excluded from `Insert` / `Update` entity assignment (left to database defaults `NULL` / `0` / `false`) so that a zero value cannot break the "not deleted" predicate — make sure the column has a corresponding default in the schema.

---

## SQL logging

A lightweight built-in SQL log prints every statement, its bound arguments, duration, and error, filtered by level. The default is **Silent**; enable it as needed.

### Enabling it

```go
db := orm.Open("postgres", dsn).
    WithLogger(orm.DefaultLogger(os.Stdout)). // write to stdout (default is os.Stderr)
    WithLogLevel(orm.Info)                    // Info=all, Warn=slow queries+errors, Error=errors only
```

> The same three settings can be passed directly in `orm.Open(orm.Config{...})` via `Logger` / `LogLevel` / `SlowThreshold` — equivalent and more centralised.

Sample output:

```
2026-08-24 17:20:00 INFO  (   1.2ms) SELECT * FROM users WHERE id = $1 args=[1]
2026-08-24 17:20:01 ERROR (   3.4ms) SELECT * FROM x: err=ERROR: relation "x" does not exist
```

### Log levels

| Level | What is emitted |
|---|---|
| `Silent` (default) | nothing |
| `Info` | all SQL |
| `Warn` | slow queries and execution errors only |
| `Error` | execution errors only |

Event severity rules:

- normal successful execution → `Info`;
- execution error → `Error`;
- duration above the `WithSlowThreshold(d)` threshold (and > 0) → promoted to `Warn`.

### Wiring into your own logger

`LogFunc` is a callback, so it plugs straight into zap / logrus / slog:

```go
db := orm.Open("mysql", dsn).WithLogger(func(level orm.LogLevel, query string, args []any, dur time.Duration, err error) {
    // e.g. with zap:
    // logger.Debug("sql", zap.String("query", query), zap.Any("args", args), zap.Duration("dur", dur), zap.Error(err))
})
```

> Transactions (`db.Transaction`) and prefixed copies (`WithPrefix` / `WithExecutor`) inherit the logging configuration automatically.

---

## Vector search (the headline feature for AI / RAG)

> The full capability description, the mathematical intuition behind each distance metric, worked retrieval examples, and an end-to-end setup for wiring an AI embedding model (OpenAI / Ollama, …) live in [VECTOR.md](VECTOR.en.md) — read that first, then come back to this overview.

`gobreath-orm` ships vector nearest-neighbour search with **one API for both PostgreSQL (pgvector) and MySQL 9+ (native `VECTOR` type)** — the SQL is dispatched per dialect, so business code never branches on the database:

> ⚠️ **MySQL availability caveat**: `VECTOR_DISTANCE()` and `VECTOR INDEX` are provided only by **MySQL HeatWave on OCI** / **MySQL AI**; community and commercial builds do **not** include them (per the official note on dev.mysql.com). Community builds can create `VECTOR(N)` columns and store/read via `STRING_TO_VECTOR()`, but distance queries fail with `FUNCTION VECTOR_DISTANCE does not exist`. Always confirm the target database's capabilities with the self-check in [VECTOR.md §7](VECTOR.en.md).
>
> ⚠️ **Two more MySQL prerequisites (both verified against a real database)**: ① the driver must be **`go-sql-driver/mysql` ≥ v1.9.0** — `VECTOR` is field type code 242 (`MYSQL_TYPE_VECTOR`) added in MySQL 9.0, and v1.8.x does not recognise it, failing with `unknown field type 242` when reading vector columns (the problem is in the driver, so upgrading the server alone does not help); ② a `VECTOR` column is stored as a BLOB, so `SELECT` returns **little-endian raw float32 bytes**, not `"[1,2,3]"` text — the framework dispatches on payload shape (text / binary) automatically, so callers need not care.

| Database | How to enable | Retrieval syntax generated |
|---|---|---|
| **PostgreSQL + pgvector** | `CREATE EXTENSION vector;` + a `vector(N)` column | `"embedding" <=> $1` (operators `<=>` / `<->` / `<#>` / `<+>`) |
| **MySQL 9+ (HeatWave / MySQL AI only)** | a `VECTOR(N)` column | `VECTOR_DISTANCE(\`embedding\`, STRING_TO_VECTOR(?), 'COSINE')` (community builds report the function as missing) |

Vector fields are simply `[]float32` / `[]float64`; the framework serializes them to a parameterized `[..]` text value — **no `pgvector-go` or similar third-party package**, zero extra dependencies.

```go
type Doc struct {
    Id        int64     `db:"id,pk,autoincrement"`
    Title     string    `db:"title"`
    Embedding []float32 `db:"embedding,vector"` // ,vector marks a vector column (auto-serialized on Insert/Update)
}

embCol := orm.Col[Doc](func(d *Doc) *[]float32 { return &d.Embedding })

// Cosine nearest neighbours (first choice for text semantics — the default for RAG retrieval).
list, err := orm.SelectList(ctx, db,
    orm.NewQuery[Doc]().NearestBy(embCol, vec, 5, orm.Cosine),
)

// Euclidean (the default metric, equivalent to the old Nearest) plus a distance threshold: only rows closer than 0.3.
q := orm.NewQuery[Doc]().Nearest(embCol, vec, 10).WithinDistance(embCol, vec, 0.3)

// Threshold filter only: without Nearest it neither sorts nor projects a dist column.
only := orm.NewQuery[Doc]().WithinDistance(embCol, vec, 0.3)
```

> **`WithinDistance` is a pure filter**: used on its own it generates `SELECT * FROM docs WHERE distance < ?`,
> **neither ordering by distance nor projecting a `dist` column** (chain `Nearest` / `NearestBy` if you want ordering).
> The reason is that a pure filter should not pay for a full-table distance computation just to sort; a useful
> side effect is that it composes with `Distinct()` — `SELECT DISTINCT * FROM docs WHERE distance < ?` is valid SQL,
> whereas `DISTINCT` with distance ordering has no consistent cross-dialect behaviour (see the Distinct section).
> If you are upgrading from v0.1.14 or earlier, the old behaviour was "sort automatically and append `dist`"; check
> any call sites that relied on that ordering.

**Distance metrics** (`orm.VectorMetric`, default `L2`):

| Metric | Constant | PostgreSQL operator | MySQL metric name | Typical use |
|---|---|---|---|---|
| Euclidean | `orm.L2` | `<->` | `EUCLIDEAN` | geometric distance (images/audio before normalisation) |
| Cosine | `orm.Cosine` | `<=>` | `COSINE` | **text embedding similarity (recommended)** |
| Inner product | `orm.InnerProduct` | `<#>` (negative inner product) | `DOT` | fastest when vectors are already normalised |
| Manhattan | `orm.L1` | `<+>` | **not supported by MySQL** (MySQL distance functions are `COSINE`/`DOT`/`EUCLIDEAN` only — no `MANHATTAN`) | sparse vectors (PostgreSQL only) |

> **Note**: `Nearest` / `WithinDistance` without an explicit metric keep the legacy default `L2`; for semantic retrieval, use `NearestBy(..., orm.Cosine)` explicitly.
> SQLite has no native vector type — it can only generate the syntax (for offline SQL assembly); use PG / MySQL for real retrieval.

**k versus paging**: k is a **recall ceiling**, not the page size of `Limit` / `Page` — when both are present the stricter one wins (`Nearest(col, vec, 3)` + `Page(..., size = 10)` returns 3 rows). `Count` / `Page`'s `Total` likewise means "how many rows are in the neighbourhood" (`min(k, matching rows)`), not the table row count:

```go
q := orm.NewQuery[Doc]().NearestBy(embCol, vec, 5, orm.Cosine)
total, _ := orm.Count(ctx, db, q) // at most 5 — never the table's total row count
```

**Complete runnable example**: see [`examples/vector-search`](examples/vector-search) — you can see the SQL generated for both PostgreSQL and MySQL dialects without installing a database.

**Performance tip**: for millions of vectors, use a vector index — PG: `CREATE INDEX ON docs USING hnsw (embedding vector_cosine_ops);`, MySQL: `CREATE VECTOR INDEX idx ON docs(embedding);` (vector indexes are likewise HeatWave / MySQL AI only; community builds have none). The `ORDER BY distance ASC LIMIT k` the framework generates hits these indexes directly.

---

## Joins / Upsert / Partial updates / Optimistic locking

### Joins

`Join / LeftJoin / RightJoin` map to SQL's `INNER/LEFT/RIGHT JOIN`; the `As` variants (e.g. `LeftJoinAs(table, alias, on)`) alias the joined table. Alias the main table with `Alias()` to reference it as `u.name` in `ON` and `Select`:

```go
// Left join the departments table and select the user name and department name
// (ON is passed through verbatim — cross-table columns cannot be expressed with Col[T]).
list, err := orm.SelectList(ctx, db, orm.NewQuery[User]().
    Alias("u").
    LeftJoin("departments", `"u"."dept_id" = "departments"."id"`).
    Select("u.name", "departments.dept_name").
    Eq(orm.Col[User](func(u *User) *int { return &u.Age }), 18),
)
```

> ⚠️ **The `ON` condition is raw text**: the framework only validates table names (whitelist + quoting) and does not parse column references inside `ON`. Use the quoting style of the current dialect (PG `"col"`, MySQL `` `col` ``) and never concatenate user input, or you introduce injection risk. When the result set contains same-named columns, always disambiguate with aliases (e.g. `SELECT u.id, d.id AS dept_id`).

### Upsert

`Upsert / BatchUpsert` cover all three dialects with one API. The conflict key **defaults to the primary key** and can be overridden via the variadic `conflictCols ...string` (which requires a matching unique index). Updated columns = all writable columns minus the conflict keys; when there is nothing to update it degrades to `DO NOTHING` (PG/SQLite) or an equivalent no-op (MySQL).

```go
// PG:    INSERT ... ON CONFLICT ("id") DO UPDATE SET "name" = EXCLUDED."name", ...
// MySQL: INSERT ... ON DUPLICATE KEY UPDATE `name` = VALUES(`name`), ...
err := orm.Upsert(ctx, db, &User{Id: 1, Name: "neo", Age: 30}, nil)
```

Two easy-to-miss points the framework handles for you:

- **When the conflict key is the auto-increment primary key, that column is included in the INSERT column list** (as long as the entity carries a non-zero value). Otherwise the INSERT omits `"id"`, `ON CONFLICT ("id")` never matches, and "update if exists" silently degrades into "insert a new row".
- **The auto-increment key is written back**, same as `Insert`: PG / SQLite via `RETURNING`, MySQL via the `LAST_INSERT_ID(id)` trick (when `ON DUPLICATE KEY UPDATE` takes the update branch, a bare `LAST_INSERT_ID()` does not point at that row). `BatchUpsert` does not backfill, consistent with `BatchInsert`.

A zero primary key means "insert a new row, let the database assign the key", and the `id` column is not written. Because a multi-row `VALUES` list must have consistent column counts, `BatchUpsert` decides per batch: the column is included only if every row has a key; if only some do, it **errors out** rather than guessing.

> ⚠️ **`conflictCols` must be real column names of the model** (empty strings / duplicates / unmapped columns all return `ErrInvalidArgument` without issuing SQL).
> This validation mainly guards MySQL: `ON DUPLICATE KEY UPDATE` **has no syntactic slot for the conflict key and ignores `conflictCols` entirely**, deciding solely on the table's existing unique indexes — a typo in the conflict key produces no error on MySQL, it just silently inserts or updates against a different unique key.
> PG / SQLite splice the conflict key into `ON CONFLICT (...)` verbatim; a typo is reported by the database, though the error does not point back to the call site.
> The converse also matters: **on MySQL, a correct conflict key does not mean it takes effect** — the table's unique indexes are what actually decide.

### Partial updates (multi-column / map)

To avoid rewriting the whole row, use the chainable `Query.Set(col, val)` + `UpdateSets`, or pass a `map[string]any` to `UpdatePartial` / `UpdateByIdSets`. **A `WHERE` clause is mandatory — full-table updates are rejected**; vector columns are serialized and bound automatically.

```go
// Chainable: change only name / age.
_, _ = orm.UpdateSets(ctx, db, orm.NewQuery[User]().
    Eq(orm.Col[User](func(u *User) *int64 { return &u.Id }), 1).
    Set(orm.Col[User](func(u *User) *string { return &u.Name }), "bob").
    Set(orm.Col[User](func(u *User) *int { return &u.Age }), 30))

// Map: change some fields by primary key.
_, _ = orm.UpdateByIdSets[User](ctx, db, int64(1), map[string]any{"name": "bob"})
```

> ⚠️ **`UpdateSets` / `UpdatePartial` may not write the optimistic-lock version column**: both are **conditional** updates that hit multiple rows with different versions, so writing one caller-supplied value into all of them flattens the version differences at once — afterwards any update with `WHERE version = ?` fails to match, and concurrency-conflict detection is silently disabled. A `sets` map containing the version column returns `ErrInvalidArgument`.
> For single-row optimistic locking use `UpdateByIdSets` (passing the version in `sets` triggers the check); for a conditional batch update that should bump versions, use the entity path `Update`.
> Conversely, these two entries **will not** bump the version column for you (same rule as `UpdateByIdSets` not touching the version column when `sets` omits it).

### Optimistic locking

Mark a version column with `db:"version,version"` (or configure `OptimisticField: "version"` at `orm.Open`). `UpdateById / UpdateByIdSets` automatically append `WHERE version = ?` and `SET version = version + 1`; if the expected version no longer matches the database (another transaction changed it), zero rows are affected and `ErrOptimisticLock` is returned so the caller can retry or report.

```go
type Order struct {
    Id      int64  `db:"id,pk,autoincrement"`
    Status  string `db:"status"`
    Version int    `db:"version,version"` // optimistic-lock version column
}

// Concurrent modification: succeeds only while version is still 5.
err := orm.UpdateById(ctx, db, &Order{Id: 1, Status: "paid", Version: 5})
if errors.Is(err, orm.ErrOptimisticLock) {
    // version conflict — the row was changed by someone else, retry
}
```

**On success the version is written back to the entity** (`order.Version` becomes 6), so the same entity can be updated repeatedly without hitting a false `ErrOptimisticLock` against itself:

```go
order.Status = "paid"
_ = orm.UpdateById(ctx, db, &order) // Version: 5 -> 6 (entity updated in step)
order.Status = "shipped"
_ = orm.UpdateById(ctx, db, &order) // compares with version = 6, succeeds
```

Four caveats:

- **The version column must be an integer type** (`int` / `int64` / `uint`, …, pointer forms included). `version + 1` on a string column makes MySQL silently coerce the text to 0 and add 1 (PG errors outright), rendering conflict detection useless. This check now happens **at model-parse time**: `db:"version,version"` on a non-integer field, or an `OptimisticField` convention matching a non-integer field, panics immediately (rather than failing at execution time, by which point MySQL has already written bad data). When the convention name collides with an unrelated same-named field (e.g. a semantic version string `Version string`), opt out with `,noversion`.
- **A conditional batch `Update` does not add `WHERE version`** (it updates multiple rows), but `SET version = version + 1` still applies — each row increments from its own current value rather than being set to the entity's value.
- **`UpdateByIdSets` (map form) only takes optimistic-lock semantics when `sets` includes the version column**; otherwise it leaves the version column alone.
- **`SET` columns in `UpdateSets` / `UpdatePartial` / `UpdateByIdSets` are generated in column-name order** — the input is a `map`, and random iteration order would make identical code emit different column orders each time, forcing prepared-statement drivers to re-prepare and making slow-query logs impossible to compare byte for byte.

---

## SQL lifecycle hooks

`Hook` is an interface: implement `On(HookEvent)` and you receive the query, arguments, duration, and error before and after every SQL execution:

```go
type Hook interface {
    On(event HookEvent)
}
// HookEvent.Kind  : HookKindExec | HookKindQuery
// HookEvent.Phase : HookPhaseBefore | HookPhaseAfter
// HookEvent fields: Query / Args / Duration / Err
```

Register it one of two ways:

```go
// 1) At open time
db, _ := orm.Open(orm.Config{Driver: "sqlite", DSN: "file.db", Hooks: []orm.Hook{&MyAuditHook{}}})

// 2) Append at runtime (returns a new DB copy; the original is untouched)
db = db.WithHooks(&MyAuditHook{})
```

`Args` is copied by the framework before dispatch, so reading it inside a hook is safe. With no hooks registered nothing fires and nothing is allocated — zero runtime cost.

---

## Read/write splitting and multiple data sources

Declare a primary and read replicas; the framework routes by SQL type:

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

- Write operations (`INSERT/UPDATE/DELETE/...`) go to `Primary`; reads (`SELECT`) round-robin across `Replicas`.
- With no replicas every request falls back to the primary; `MultiSourceConfig` is a **pure alias** of this configuration and routes identically today.
- Inside a transaction (`db.Transaction`) everything falls back to the primary, avoiding replica inconsistency.
- Route selection is mutex-protected and concurrency-safe.
- **The primary opens exactly one connection pool**: when `ReadWrite.Primary` shares the same source as `Config.Driver` / `DSN` (the most common setup — only replicas configured), the framework reuses the same pool, so `db.SQL()` returns the very pool routing uses, and `Stats()` / runtime `SetMaxOpenConns` / `Ping` all see real traffic. When `Primary` points at a different source it wins (transactions open there too, so you never get "transaction on primary A, queries on primary B").
- **Pool settings apply to every pool**: `MaxOpenConns` / `MaxIdleConns` / `ConnMaxLifetime` / `ConnMaxIdleTime` are applied to the primary and every replica; call `db.Close()` once to close them all before exit.
- **Detection details**: leading comments (`/* */`, `--`, MySQL's `#`) are stripped before classification, so write statements with leading comments like `/* trace-id */ UPDATE ...` are not misclassified as reads. Pessimistic reads (`FOR UPDATE`, …) go to the primary.
- **CTEs (`WITH ...`) get a dedicated lexical pass**: the prefix is always `WITH`, which cannot distinguish read-only from "CTE + write", so the whole statement is scanned lexically — skipping string literals, quoted identifiers, and comments, comparing write keywords **as words**. Any hit means write; **only CTEs containing no write keyword at all go to a replica**. So `WITH x AS (SELECT insert_count FROM t) SELECT ...` (where `insert_count` is an identifier) and `WITH x AS (SELECT ... WHERE name = 'UPDATE') SELECT ...` (`UPDATE` inside a string) are both still reads, while PG's `WITH m AS (DELETE ... RETURNING *) INSERT ...` (the write is inside the CTE, the main statement is still `SELECT`) is correctly classified as a write. Anything that cannot be classified reliably — unterminated strings / comments, etc. — conservatively counts as a write.

---

## AutoMigrate

`AutoMigrate` keeps the schema aligned with your Go structs, **idempotently and repeatably**, following an **add-only** policy:

1. Table missing → `CREATE TABLE IF NOT EXISTS` with every column;
2. Table exists → read the existing columns first (PG / MySQL query `information_schema.columns`, SQLite queries `pragma_table_info`) and **add only the missing ones** (`ALTER TABLE ADD COLUMN`);
3. Finally create secondary indexes (always after adding columns — if a missing column carries `,index`, creating the index first would hit "column does not exist").

Index DDL is dispatched by dialect: PG / SQLite use `CREATE INDEX IF NOT EXISTS`; MySQL (including 8.x) does not support that clause (it is a MariaDB extension and produces `Error 1064`), so a plain `CREATE INDEX` is generated and idempotency comes from ignoring "index already exists" at execution time. It reads struct `db` tags directly and **does not extend the `Dialect` interface** (DDL is generated by a dialect type switch), producing correct syntax for all three dialects (PG / MySQL / SQLite).

```go
type Product struct {
    Id        int64           `db:"id,pk,autoincrement"`
    SKU       string          `db:"sku,unique"`              // unique index
    Name      string          `db:"name,index"`              // regular secondary index
    Meta      map[string]any  `db:"meta,json"`               // JSON column
    Embedding []float32       `db:"embedding,vector(1536)"`  // vector column (with dimension)
}

ctx := context.Background()
// Idempotent: tables and indexes use IF NOT EXISTS, safe to re-run (no column drops, no rebuilds).
if err := db.AutoMigrate(ctx, &Product{}, &User{}, &Order{}); err != nil {
    panic(err)
}
```

- **Vector columns**: `db:"embedding,vector(1536)"` → PG generates `embedding vector(1536)`; MySQL generates `embedding VECTOR(1536)`; SQLite has no native vector type and degrades to `TEXT` (data still stored; use PG / MySQL for retrieval).
- **JSON columns**: PG → `JSONB`, MySQL → `JSON`, SQLite → `TEXT`.
- **Primary key + auto-increment**: PG `BIGSERIAL PRIMARY KEY`; MySQL `BIGINT AUTO_INCREMENT PRIMARY KEY`; SQLite `INTEGER PRIMARY KEY AUTOINCREMENT`.
- **Indexes**: `,unique` creates a unique index, `,index` a regular secondary index; when the column type declares no dimension, vectors default to 1536.

**Two hard boundaries when adding columns (both return errors, neither is silently absorbed)**:

- **Missing primary key / auto-increment column → error**: adding a primary key to an existing table would require inventing unique values for existing rows, and SQLite's `ADD COLUMN` explicitly rejects `PRIMARY KEY` / `AUTOINCREMENT`. Migrate by hand or rebuild the table.
- **A `,unique` column added later becomes `CREATE UNIQUE INDEX`, not an inline `UNIQUE` constraint**: SQLite's `ADD COLUMN` likewise rejects the `UNIQUE` modifier. The uniqueness guarantee is equivalent, but the error wording and the `ON CONFLICT` target name differ from an inline constraint.

> AutoMigrate currently **does not drop columns removed from the struct**, and **does not change existing column types / lengths** (a conservative policy that avoids data loss); for type changes or drops, use raw SQL (`RawExec`) or a dedicated migration tool.
>
> Also: metadata queries that read the schema **go to the primary** (never a replica under read/write splitting) — a replica's DDL may lag the primary, so inspecting columns there yields conclusions opposite to the real schema. A custom dialect that neither declares `DialectKinder` nor is one of the three built-ins leaves the framework no way to know its system tables, so it degrades to "create tables only".

## Schema introspection (TableExists / ColumnExists / DescribeTable)

The "governance" side beyond AutoMigrate: plugin-style scripts, dynamic table creation, migration checks, and structural verification before export / audit.

```go
// Does the table exist (may carry a schema. prefix, same contract as AutoMigrate)?
ok, err := orm.TableExists(ctx, db, "users")

// Does the column exist (case-insensitive, same normalisation as AutoMigrate)?
ok, err = orm.ColumnExists(ctx, db, "users", "Created_At")

// All column metadata, in creation order.
cols, err := orm.DescribeTable(ctx, db, "users")
for _, c := range cols {
    fmt.Printf("%s %s nullable=%v pk=%v autoinc=%v default=%v\n",
        c.Name, c.DataType, c.Nullable, c.PrimaryKey, c.AutoIncrement, c.Default)
}
// -> id bigint nullable=false pk=true autoinc=true
```

`ColumnMeta` fields come from the database's own system tables as raw text: `DataType` is the dialect's own type name (PG `bigint` / MySQL `varchar` / SQLite's declared type); `Default` is the default expression (`nil` when there is none). All three dialects (PG and MySQL via `information_schema`, SQLite via `pragma_table_info`) are translated by the framework — callers write no dialect branches.

> Metadata queries always **go to the primary** (same reason as AutoMigrate: replica DDL may lag).
> When a dialect does not support introspection (no `DialectKinder`, not one of the three built-ins) the call returns an **error** rather than `false` — letting "I don't know" masquerade as "it doesn't exist" would make callers create the table again.

---

## Preload

`Preload` batch-loads parent/child associations in one go, **avoiding the N+1 queries that per-object loading causes**. Three relation kinds are supported:

| Relation | Parent field shape | Default foreign-key column | Meaning |
|---|---|---|---|
| `has_many` | slice `[]Child` | `<parent type>_id` (e.g. `User` → `user_id`) | the child table holds a foreign key to the parent's primary key |
| `has_one` | struct / pointer | same as above | like has_many, but at most one child per parent |
| `belongs_to` | struct / pointer | `<child type>_id` (e.g. `Account` → `account_id`) | the parent table holds a foreign key to the child's primary key |

```go
type User struct {
    Id       int64     `db:"id,pk,autoincrement"`
    Name     string    `db:"name"`
    Articles []Article `db:"-" orm:"has_many"`           // child table has a user_id column
    Profile  *Profile  `db:"-" orm:"has_one;fk:user_id"` // explicit foreign-key column
}
func (User) TableName() string { return "users" }

type Article struct {
    Id     int64  `db:"id,pk,autoincrement"`
    Title  string `db:"title"`
    UserId int64  `db:"user_id"`
}
func (Article) TableName() string { return "articles" }

// Batch preload: attach Articles / Profile to all users at once.
users := []User{{Id: 1}, {Id: 2}}
if err := orm.Preload(ctx, db, &users, "Articles", "Profile"); err != nil {
    panic(err)
}

// Single-object version
u := &User{Id: 1}
if err := orm.PreloadOne(ctx, db, u, "Articles"); err != nil {
    panic(err)
}
```

A `belongs_to` example (the parent holds the foreign key, pointing at the child's primary key):

```go
type Comment struct {
    Id     int64  `db:"id,pk,autoincrement"`
    Body   string `db:"body"`
    UserId int64  `db:"user_id"`
    Author *User  `db:"-" orm:"belongs_to;fk:user_id"` // points at User's primary key
}
func (Comment) TableName() string { return "comments" }

comments := []Comment{{Id: 10, UserId: 1}, {Id: 11, UserId: 2}}
orm.Preload(ctx, db, &comments, "Author") // every Comment.Author gets populated
```

Conventions and caveats:

- ⚠️ **Always tag association fields with `db:"-"`**, otherwise they also take part in CRUD and cause errors.
- The default foreign-key column is `<type>_id` (`User` → `user_id`, `Account` → `account_id`); override with `orm:"has_many;fk:user_id"` / `orm:"belongs_to;fk:xxx"` when it differs.
- The child query **also applies soft-delete filtering**, so soft-deleted children are not loaded.
- `Preload` / `PreloadOne` are forwarded under the same names on `Repo[T]` (`repo.Preload(ctx, &list, "Articles")`).

---

## Distinct

`Distinct()` appends `DISTINCT` after `SELECT`, typically for "deduplicate by a column, then count or list the values":

```go
cityCol := orm.Col[User](func(u *User) *string { return &u.City })

// SELECT DISTINCT city FROM users WHERE age > ?
cities, err := orm.SelectList(ctx, db,
    orm.NewQuery[User]().Distinct().Select("city").Gt(ageCol, 18),
)
```

`Distinct()` composes with `Select` / `Eq` / `OrderBy` / `Limit` / `Page` and friends in any order.

> **It cannot be combined with distance ordering.** `Distinct()` + `Nearest` / `NearestBy` **always panics** in
> `SelectList` / `Count` / `Page` / aggregates: the distance column differs per row, so including it silently
> defeats `DISTINCT`; excluding it makes `ORDER BY` illegal under `SELECT DISTINCT` (PG always rejects, MySQL
> rejects when the sort column is not projected, SQLite accepts — no cross-dialect consistency). For
> "deduplicate + nearest neighbour", do it in two steps: deduplicate first via `GroupBy` or a business key, then run
> vector retrieval on the result.
>
> **A pure threshold filter via `WithinDistance` is fine**: it neither sorts nor projects `dist`, and
> `SELECT DISTINCT * FROM t WHERE distance < ?` is legal on all three dialects — "how many distinct rows are in this
> neighbourhood" remains answerable.

> `Select(cols ...string)` accepts **column names only** (quoted per dialect). To project **aggregate expressions**
> such as `SUM(col)` or `COUNT(*)`, use the dedicated `SelectSum` / `SelectCount` builders (see "Grouping
> (GroupBy / Having)"), or go through `RawQuery`. An expression passed to `Select` is quoted as an identifier:
> PostgreSQL reports "column does not exist", and SQLite is more insidious — `SELECT "COUNT(*)"` is treated as a
> string constant and **silently returns the literal instead of a count**.

---

## Security and injection prevention

`gobreath-orm` defends on three layers:

1. **Values are always parameterized**: every condition value goes through placeholders (`$N` / `?`), never string concatenation.
2. **Column-name whitelist**: column names are only ever derived from struct `db` tags, so a column absent from the tag cannot be constructed — bypass attempts are inert.
3. **Table-name whitelist + quoting**: table names cannot be bound as placeholders, so the framework validates them with a regex (letters/digits/underscore, not starting with a digit) and then quotes them per dialect (`"` / `` ` ``). An invalid table name on the query-construction path panics (a programming-time error, so no malicious SQL is ever generated); for the introspection APIs (`TableExists` / `ColumnExists` / `DescribeTable`) the table name comes from caller input and is therefore runtime data, so invalid names return `ErrInvalidArgument` instead of panicking.

---

## Supported databases

| Database | `Open` driver name | Default dialect | Notes |
|---|---|---|---|
| PostgreSQL | `postgres` / `pgx` | PostgreSQL | default; supports jsonb and vector `<->` (pgvector) |
| MySQL | `mysql` | MySQL | supports `JSON_CONTAINS`; `VECTOR(N)` columns can be created and stored on community builds (**driver must be ≥ `go-sql-driver/mysql` v1.9.0**, otherwise reading the column fails with `unknown field type 242`), but the `VECTOR_DISTANCE` function and vector indexes are available only in HeatWave on OCI / MySQL AI (community builds report the function as missing) — see [VECTOR.md §6/§7](VECTOR.en.md) |
| SQLite | `sqlite` / `sqlite3` | SQLite | supports `json_extract`; no `json_contains` function, so the dialect expands `JsonContains` into equivalent `json_each` semantics; no native vector type |

Adding a dialect means implementing the `Dialect` interface (`QuoteIdent` / `Placeholder` / `JsonPath` / `JsonContains` / `VectorDistance` / `VectorBind` / `UpsertSuffix` / `SupportsLastInsertID` / `InsertReturning`) and registering it in `dialectForDriver`. Two further interfaces — `upsertReturningDialect` / `upsertPKCapturingDialect` — are **optional** (detected by type assertion) and used to backfill auto-increment keys after `Upsert`; not implementing them is not an error, the key is simply not backfilled.

---

## Tests and CI

- **Unit tests**: `go test ./...` in the root module. The repo ships a zero-dependency pure-Go mock driver that asserts generated SQL shape and scan behaviour; `go test -run "^$" -bench BenchmarkScanList -benchmem` measures **pure ORM-layer** overhead (no database engine, `B/op` and `allocs/op` reproduce byte for byte).
- **Real-database integration tests**: [`integration/`](integration/README.en.md) is a separate submodule (it imports the drivers so the root module stays dependency-free) and runs the same suite against SQLite / PostgreSQL (pgvector) / MySQL. Everything a mock cannot verify lives here — driver return types, whether dialect syntax is actually legal, both primary-key backfill paths, and real transaction and locking behaviour. Locally, `docker compose up -d` brings the databases up; in CI it is a separate job (which degrades to SQLite-only when databases are unavailable).
- **Cross-framework benchmarks**: [`bench/`](bench/README.en.md) is another submodule comparing `database/sql`, GORM, and this library on the same table, the same data, and semantically identical SQL. It carries two kinds of value: `TestResultParity` / `TestSQLParity` assert the three layers **do the same thing** (these run in CI), plus measured numbers for 5 single-table scenarios and several batch sizes (these do not run in CI — CPU noise on shared runners makes ns/op meaningless, and even a single local run can vary by ±40%, so conclusions are always drawn from `B/op` / `allocs/op`). Measurements have fed back into the implementation — "`ListPage`'s row mapping is expensive" came out of that README, and after the fix a 50-row list dropped from 123.6 KB to 63.8 KB of allocations (see that README for details and caveats).

---

## Roadmap (Phase 2+)

- `RawQuery` ergonomics: batch expansion of `IN` placeholders (slice → `(?, ?, ...)`), `map[string]any` result sets
- ✅ Streaming large tables — shipped in v0.1.14 as `QueryIter` (single cursor, `iter.Seq2`); a streaming cursor for `RawQuery` can reuse the same `rowScanner`
- Entity-level hooks (`BeforeCreate / AfterUpdate`, …) — the current `Hook` is SQL-lifecycle level; the two are different concerns and both can coexist
- ✅ Grouped mapping of aggregate results — shipped in v0.1.13 via `SelectSum` / `SelectCount` aggregate projections + `Aggregate[DTO]` (`SelectCol` added type-safe column selection; `Select(cols ...string)` is retained for compatibility)
- ✅ Relation preload / association mapping — shipped in v0.1.7 as `Preload` / `PreloadOne`
- ✅ Database migration — shipped in v0.1.7 as `AutoMigrate` (table creation + secondary indexes; no column drops or type changes yet)

---

## Notes

This English README tracks the current repository behaviour. The Chinese documentation is the original and authoritative version: [README.md](README.md). Changelog: [CHANGELOG.md](CHANGELOG.md) · [CHANGELOG.en.md](CHANGELOG.en.md).

---

## License

MIT




