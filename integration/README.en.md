# Integration tests (real databases)

Verifying gobreath-orm against real databases. **These cases do not live in the root module** — `go test ./...`
will not run them. The reason is below.

```powershell
cd integration
$env:ORM_IT_PG_DSN    = "postgres://gobreath:gobreath@127.0.0.1:5433/gobreath?sslmode=disable"
$env:ORM_IT_MYSQL_DSN = "root:gobreath@tcp(127.0.0.1:3307)/gobreath?parseTime=true&loc=Local"
go test -v ./...
```

It runs without any environment variables too: SQLite is always available (modernc pure-Go driver + in-memory database, zero installation).

```powershell
go test ./...                 # SQLite only
```

## Bringing the databases up

```bash
docker compose up -d          # pgvector:5433 + mysql:3307
docker compose down -v        # clean up
```

Ports 5433 / 3307 avoid a locally installed PostgreSQL (5432). `pgvector/pgvector` is used instead of `postgres`
because the vector cases need the `vector` extension, which `initdb/01-vector.sql` creates automatically.

**About the MySQL version**: compose pins `mysql:8.0`, and 8.0 has **no** `VECTOR` type, so the vector cases skip
even the "storage level" on MySQL (see "Vector cases tiered by capability" below). To make MySQL's storage level
actually run, switch to **`mysql:9`** (≥ 9.0 has `VECTOR(N)` and `STRING_TO_VECTOR`) — measured on 9.7.2 community,
tables and store/read work, but there is **no** `VECTOR_DISTANCE` function, so the distance level still skips.
Changing the version affects other MySQL cases' dialect behaviour, so decide for yourself whether to put it in CI;
for a local one-off, just `docker run -p 3308:3306 mysql:9` and point `ORM_IT_MYSQL_DSN` at it.

## Both PG driver paths really run

The README lists `github.com/lib/pq` (driver name `postgres`) and `github.com/jackc/pgx/v5/stdlib` (driver name
`pgx`) as alternative PG drivers, **and the main example uses the former** — so both must be verified.
Provide a single `ORM_IT_PG_DSN` and the harness fans it out into two backends: `postgres` (pgx) and
`postgres-libpq` (lib/pq). They run sequentially, each with its own `resetSchema`, so they do not interfere.

**They do not hand the same types to `database/sql`** (measured on the same database via `Scan(&any)` and `%T`):

| Server column type | pgx | lib/pq |
|---|---|---|
| `numeric` | `string` | `[]byte` |
| `uuid` | `string` | `[]byte` |
| `text[]` | `string` | `[]byte` |
| `vector` (pgvector) | `string` | `[]byte` |
| `jsonb` / `bytea` | `[]byte` | `[]byte` |
| `text` / `varchar` | `string` | `string` |
| `int2/4/8`, `float4/8`, `bool` | `int64` / `float64` / `bool` | same |
| `timestamptz` | `time.Time` (session time zone) | `time.Time` (normalised to UTC) |

"Both are database/sql" does not mean "the shapes are the same". The framework **itself** dispatches types only when
scanning model fields (`assignString` / `assignFloat`, …), and its `case []byte` branches **are only reached under
lib/pq**; `RawQuery` / `RawOne`'s scalar path and aggregates call `rows.Scan` directly, leaving it to the standard
library's `convertAssign`. Each path has a case guarding it (`TestModelScanAcrossDrivers` /
`TestCrossDriverValueParity`); the former creates `NUMERIC` / `UUID` / `TEXT[]` columns by hand, deliberately
manufacturing "server type ≠ Go field type" — `AutoMigrate` derives DDL from Go types and cannot create those
columns, so it cannot create that premise.

## CI

`.github/workflows/ci.yml` has a separate `integration` job (ubuntu, with service containers for pgvector + mysql,
same ports as local). It **does not rely on mounting `initdb/`** — the workspace does not exist yet when service
containers start, so the `pgvector` extension is created by the tests themselves via
`CREATE EXTENSION IF NOT EXISTS vector` (see `ensureVectorExtension` in `harness_test.go`). That way "a DSN pointing
at a pgvector container" is enough to run the whole suite.

When the databases are not ready, simply do not set the DSNs: SQLite is always available and the suite still
completes the 24 SQLite cases.

## Why a separate module

The main module (the repository root's `go.mod`) is **dependency-free**; drivers must be imported by the user.
Real-database tests need the pgx / mysql / sqlite drivers, and putting them in the main module would write them into
`go.sum`, breaking that selling point and forcing CI to download a pile of unrelated packages. This follows the
nested-module convention already established by `examples/` (`replace` pointing back to `..`).

The mysql driver is pinned to **`v1.9.3`** for reasons on both sides:

- **Not below `v1.9.0`**: MySQL 9.0 introduced field type code 242 (`MYSQL_TYPE_VECTOR`) for VECTOR, and v1.8.x does
  not recognise it — reading a vector column fails **in the driver layer** with `unknown field type 242`. Measured on
  MySQL 9.7.2, v1.8.1 cannot even "write a vector then read it back" (v1.9.0 changelog: "Add support for VECTOR type
  introduced in MySQL 9.0. (#1609)").
- **Not bumping to `v1.10.0`**: it raises `go.mod` to `go 1.24.0`, which would drag this module's minimum Go version
  up and conflict with the repository's promise to support 1.23; `v1.9.3`'s go directive is still 1.21+.

## What mock tests cannot verify

The repository's existing mock executor only asserts "which SQL string was generated". The following four classes of
problems **are only exposed by a real database**, and this suite exists for them:

1. **Type matching between driver return values and scan targets** — PG's `SUM(bigint)` is `numeric`, and coming back
   through `database/sql` it may be `[]byte`, `string`, or `int64`, regardless of what a mock imagines.
2. **Whether dialect syntax is actually legal** — SQL that runs on PG may not on SQLite / MySQL (see below).
3. **The two completely different primary-key backfill code paths** — PG does not support `LastInsertId`, so it must
   use `INSERT ... RETURNING`; MySQL / SQLite use `LastInsertId`.
4. **Real transaction, lock, and constraint behaviour** — rollback, `FOR UPDATE`, optimistic-lock conflicts.

## Case coverage

| Test | What it verifies |
|---|---|
| `TestInsertPKWritebackAndRoundtrip` | Both primary-key backfill paths + eight column types written and read back (including JSON and time) |
| `TestSelectVariants` | `SelectOne` / `Exists` / `IN` / `Distinct` / `LIMIT+OFFSET` |
| `TestPageMetadata` | Paging `Total` / `Pages` / `HasNext` / `HasPrev` |
| `TestUpdateVariants` | `UpdateById` / conditional `Update` / `UpdateByIdSets` |
| `TestSoftDelete` | Default filtering, `Unscoped` escape, `ForceDelete` real deletion |
| `TestUpsertByUniqueColumn` | Conflict handling across three dialects (`ON CONFLICT` vs `ON DUPLICATE KEY`) + primary-key backfill |
| `TestUpsertByAutoIncPK` | Upsert on an auto-increment primary key hits the update branch instead of silently inserting (regression) |
| `TestAutoMigrateIndexTag` | `,index` creates an index + the idempotent path when MySQL lacks `IF NOT EXISTS` |
| `TestTransactionRollback` | Rollback does not persist, commit does |
| `TestOptimisticLock` | Version increment, stale writes rejected (`ErrOptimisticLock`) |
| `TestForUpdate` | The pessimistic-lock clause is legal on a real database; SQLite returns an empty string by design |
| `TestPreloadHasManyAndBelongsTo` | `has_many` / `belongs_to` really load the child tables |
| `TestAutoMigrateIsIdempotent` | Repeated `AutoMigrate` neither errors nor touches data |
| `TestTablePrefixCRUD` | The prefix applies only to inferred table names; an explicit `TableName()` gets none |
| `TestAggregates` | `Sum/Avg/Max/Min` + strongly typed `SumOf/AvgOf` + empty set → zero + calls that must error |
| `TestMaxOfTimeColumn` | `MAX` on a time column (the usage `MaxOf` documents) |
| `TestPluck` / `TestPluckNullAlignment` | Single-column projection: order, paging, NULL padding with **correct indexes** |
| `TestCountIncludesJoin` | Regression for Count dropping JOINs (Page metadata verified alongside) |
| `TestJsonQuery` / `TestJsonContains` | JSON path comparison and fragment containment |
| `TestDryRunIsExecutable` | `DryRun`'s SQL is **executed directly on a real database** (not just string-matched) |
| `TestBatchInsertVarLimit` | Bind-parameter ceiling for batch inserts (evidence gathering, see below) |
| `TestRepoLayerFullWalk` | **Every** `Repo[T]` method really runs (including transaction commit / rollback) |
| `TestQueryPredicateMatrix` | Every predicate constructor really executes, verified semantically by row counts |
| `TestJoinVariantsExecutable` | Six JOIN variants + aliases; `Count` and `SelectList` agree |
| `TestWriteOptionsOnRealDB` | `OnlyColumns` / `OmitZero` and combinations; invalid columns must error |
| `TestDbConfigMethodsExecutable` | `DB`'s configuration methods, each verified for its "observable effect" (logging / hooks / soft delete / optimistic lock, …) |
| `TestVectorPathOnRealDB` | The vector path tiered by capability: storage level (create column / write / read back) and distance level (ordering / thresholds / aggregates) |
| `TestCrossDriverValueParity` | `RawOne` scalar path: both PG drivers read identical values (conversion on this path is the standard library's job) |
| `TestModelScanAcrossDrivers` | **Model field path**: `NUMERIC` / `UUID` / `TEXT[]` columns land in strongly typed fields under both pgx and lib/pq |

## Vector cases tiered by capability

Vector capability **is not a boolean**; `TestVectorPathOnRealDB` probes two tiers and runs as far as it can:

| Tier | Contents | Requires |
|---|---|---|
| **tier 1, storage** | Create a vector column, write, single-row read, list read, `UpdateById` on the vector | Just the `VECTOR` type |
| **tier 2, distance** | Order by distance, threshold filtering, aggregate × vector, `Pluck` with metric switching | Also the distance function |

The tiering came from a lesson: early on, "no distance function" was treated as equivalent to "skip the whole vector
path", so **the half that could run on MySQL never ran** — and reading back is exactly what was broken there (see
item 7 below).

Measured behaviour (each backend leaves raw evidence in the log):

| Backend | tier 1 | tier 2 |
|---|---|---|
| PostgreSQL + pgvector | ✅ | ✅ |
| MySQL 9.7.2 community | ✅ (`VECTOR(3)` / `STRING_TO_VECTOR`) | ❌ `ERROR 1305: FUNCTION VECTOR_DISTANCE does not exist` |
| MySQL 8.0 (compose default) | ❌ no `VECTOR` type at all | ❌ |
| SQLite | ❌ no vector operators | ❌ |

The case starts with `DROP TABLE IF EXISTS` for idempotency: a previous run interrupted by a `t.Fatalf` never reached
cleanup, and leftover rows inflate count assertions (measured `Nearest + Count = 6`).

## Past gaps: fixed, now regression tests

Every item below came out of running this suite against real databases. After the fixes, `t.Skipf` became
assertions and they are now ordinary regression tests (names given in each section).

Their shared lesson: **a mock only asserts "which SQL string was generated" and cannot tell whether that SQL holds
up in a real database.**

### 1. `Upsert` on an auto-increment primary key silently inserts (all three dialects)

Running `Upsert(entity, []string{"id"})` against an existing row **added a row** instead of updating the target.

Root cause: `writableCols` skips `autoInc` columns, so the INSERT had no `"id"`, `ON CONFLICT ("id")` /
`ON DUPLICATE KEY` never matched, and the database assigned a fresh id and inserted.

The existing mock tests `TestUpsertPG` / `TestUpsertMySQL` had asserted that broken SQL as their "expected" value, so
it never surfaced — a textbook example of how "mocks only assert SQL shape" fails.

Fix (`upsertConflictCols` in `crud.go`): when the conflict key is the auto-increment primary key and the entity's key
value is non-zero, add that column to the INSERT's column list (still omitted at zero, letting the database assign).
The batch version (`upsertBatchConflictCols`) is constrained by consistent column counts across multi-row VALUES and
decides per batch: add the column only when every row has a value, and **error** rather than guess when only some do.

### 2. SQLite's `JsonContains` used a function that does not exist

`sqliteDialect.JsonContains` used to generate `json_contains(col, ?)`, but SQLite's JSON1 extension **has no such
function** (only `json_extract` / `json_each` / `json_tree` and friends), so it failed at execution.

Fix: expand with `json_each` into equivalent shallow "subset" semantics (every key/value pair of the candidate object
is found in the column object with the same name, value, and type), aligning with PG's `@>` and MySQL's
`JSON_CONTAINS`. Shallow only, not recursive.

### 3. SQLite time-column aggregates could not be scanned back

`MAX(created_at)` / `MIN(created_at)` return expressions **without column type information**, so the modernc driver
can only hand them back as `TEXT` (measured as Go's `time.Time.String()`, e.g. `2026-09-01 16:00:00 +0000 UTC`), and
`sql.Null[time.Time]` fails to scan.

For contrast: reading the same field through `SelectById` from the original column works, because that column has a
declared `DATETIME` type.

Fix (`aggScalarAsTime` in `aggregate.go`): scan the aggregate result into `any` first, then normalise across dialect
time-text formats.

### 4. MySQL does not support `CREATE INDEX IF NOT EXISTS`

`migrate.go` used to emit `CREATE INDEX IF NOT EXISTS ...` unconditionally. That is legal on PostgreSQL / SQLite, but
MySQL reports a syntax error (only MariaDB supports `IF NOT EXISTS`).

Fix: `createIndexSQL` dispatches by dialect — MySQL gets a plain `CREATE INDEX`, with idempotency provided by
`AutoMigrate` ignoring "index already exists" (`isDuplicateIndexErr`); `TestAutoMigrateIndexTag` runs twice to verify
both index creation and idempotency.

### 5. `Upsert` did not backfill auto-increment primary keys

`Insert` backfilled, `Upsert` did not — a consistency gap.

Fix: each dialect backfills through its reliable path — PG / SQLite use `INSERT ... ON CONFLICT ... RETURNING` (which
returns the target row even on the `DO UPDATE` branch), and MySQL appends the official
`` `id` = LAST_INSERT_ID(`id`) `` trick to `ON DUPLICATE KEY UPDATE` (otherwise `LAST_INSERT_ID()` does not point at
that row on the update branch). `BatchUpsert` still does not backfill, consistent with `BatchInsert`.

### 6. `Delete`'s argument order was misaligned on positional dialects (soft delete silently no-op)

`Delete` issues `UPDATE t SET deleted_at = <ph> WHERE col = <ph>`, but arguments were allocated **for WHERE first and
SET afterwards**: positional dialects (MySQL / SQLite consume `?` in order) received `[condition value, delete value]`,
so the WHERE bound the **deletion timestamp** to a string column and matched 0 rows — **no error, no deletion**
(measured: `visible=4 unscoped=4`, not a single row removed).

PG is immune thanks to `$1/$2` ordinals, so neither PG integration tests nor mock assertions could see it.
**Lesson: count-based assertions (`Count` / row counts) are a structural blind spot for "argument order
misalignment"**, and `args[i]` must be asserted positionally against placeholders (which is how `delete_args_test.go`
is written).

Fix: SET arguments are allocated before WHERE, so placeholder and argument order align strictly.

### 7. Vector columns "wrote fine, read back never"; and on MySQL it is not even text

Write-side serialization (`serializeVector`) existed from the start, but the read side only had the generic fallback:

- **PG**: the `vector` column comes back from the driver as text `"[1,2,3]"`, and assigning it to a `[]float32` field
  reports `orm: cannot assign string to field of type []float32`.
- **MySQL**: a `VECTOR` column is a BLOB underneath (worklog: `Field_vector : public Field_blob`), so `SELECT`
  returns **little-endian raw float32 bytes**, not text at all — pure text parsing is bound to fail.

Fix: `setterFor` dispatches on `f.vector` to `setVectorField`, which dispatches on payload shape (`[` prefix → text,
otherwise binary decoding). Evidence for the text form comes from a live PG instance; the binary sample comes from
MySQL's official docs (`STRING_TO_VECTOR("[3.14,2024,18]") = 0xC3F548400000FD4400009041`, verified byte by byte as
little-endian float32).

**MySQL has an additional driver-level prerequisite**: VECTOR is field type code 242 (`MYSQL_TYPE_VECTOR`) added in
MySQL 9.0, and `go-sql-driver/mysql` only recognises it from **v1.9.0**; v1.8.x fails with
`unknown field type 242` when reading vector columns. This module's driver therefore moved from v1.8.1 to v1.9.3
(reasoning under "Why a separate module").

**mutation check**: temporarily reverting the read path to pure text parsing and re-running failed **only the MySQL
sub-case**, with exactly the expected binary garbage (`vector text component 0 "fff?\xcd\xcc\xcc..." cannot be
parsed`, where little-endian `ff 66 66 3F` is `0.9`), while PG still passed — proving the case really catches this bug
rather than being "accidentally green".

**Known limitation (unchanged)**: "store NULL in a vector column" cannot be expressed — `serializeVector` produces
`"[]"` for both `[]float32{}` and nil slices, while PG's `vector` column has at least 1 dimension, so writing `"[]"`
reports `ERROR: vector must have at least 1 dimension (SQLSTATE 22000)`. When a NULL vector is needed, exclude the
column from the write via `OnlyColumns`.

## Deliberately preserved dialect differences

### MySQL rejects a zero `time.Time` (`TestZeroTimeWrite/mysql` stays SKIP)

An unassigned `time.Time` is formatted by the mysql driver as `'0000-00-00 00:00:00'`, and MySQL's default strict
mode reports `Error 1292` outright. PG (accepts year 1) and SQLite (no validation) have no such problem.

Why it is not "fixed": silently rewriting a zero time to `NULL` changes semantics on the caller's behalf and would
mask "forgot to assign it". Assign time columns explicitly, and use pointer fields when NULL semantics are needed.

## Other measured notes

- **`BatchInsert` chunks automatically, and the ceiling is measured** (`TestBatchInsertVarLimit` logs the behaviour at
  the ceiling). `BatchInsert` / `BatchUpsert` split entities into batches of "dialect parameter limit ÷ columns per
  row" and issue one INSERT per batch, because a single statement's bind parameters grow linearly with row count and
  every database has a limit. Measured on `User` (**6 writable columns**: of 8 fields, `id` is auto-increment and
  `deleted_at` is the logical-delete column, both excluded):

  | Dialect | Last successful single statement | Error at the ceiling |
  |---|---|---|
  | SQLite | 3000 rows (18,000 parameters) | 9000 rows (54,000 parameters) → `too many SQL variables` (limit 32766) |
  | PostgreSQL | 9000 rows (54,000 parameters) | 20,000 rows (120,000 parameters) → `extended protocol limited to 65535 parameters` |
  | MySQL | 9000 rows (54,000 parameters) | 20,000 rows (120,000 parameters) → `Error 1390: Prepared statement contains too many placeholders` |

  For a 6-column table the safe single-statement ceiling is **10,922 rows (PG/MySQL, 65535 placeholders)** and
  **5,461 rows (SQLite, 32766)**. The fewer columns, the higher the ceiling, so "how many rows before batching" must
  be computed from column count rather than hard-coded — which is exactly why the framework computes it
  (`orm.WithChunkSize(...)` can override) instead of leaving users to guess. This is a classic
  "fine in a test environment with hundreds of rows, exposed only in production" trap.
- **Number comparison inside JSON is not uniform across dialects**: PG's `->>` yields `text`, so comparing against a
  number reports `operator does not exist: text = integer`; SQLite's `json_extract` yields an integer, so comparing
  against a string is unequal due to type. `TestJsonQuery` therefore deliberately uses string values.
  Cross-database uniformity would require the dialect layer to cast.

---

English edition. The Chinese original is the authoritative version: [README.md](README.md).
