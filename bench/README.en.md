# bench — cross-framework performance benchmark

Using **the same table, the same seed data, and semantically identical SQL**, this compares the overhead of three layers on an in-memory SQLite database.

```
raw      database/sql with hand-written SQL — the lower bound, telling you how much is spent "outside the ORM"
gorm     GORM v1.25.12 — the most common reference point in the ecosystem
gobreath gobreath-orm (this repository, `replace`-ed to the workspace code)
```

Running it:

```bash
cd bench
go test -run "^$" -bench . -benchmem -benchtime=2s -count=3
```

`-count=3` is not about "picking the best run" — it is about seeing the jitter. On a local machine `ns/op`
can swing by ±40% between runs (see the note at the top of "Results"), so a single run easily mistakes noise for a conclusion.

To run only the semantic-parity checks (fast, suitable for CI):

```bash
cd bench
go test ./... -count=1
```

## Why it is designed this way

The most common failure in benchmarking is not "counting wrong" but "not comparing the same thing". These measures close that hole:

| Measure | Reason |
|---|---|
| Create tables by hand, not via `AutoMigrate` | The two ORMs emit different DDL (column types, index names), so letting each create its own table means comparing across different schemas |
| Fix the random seed | All three sides must receive byte-identical 10,000 rows |
| All three share one SQLite driver implementation | Differences must come from the ORM layer, not the driver (see the comment in `harness_test.go`) |
| Disable GORM's `autoCreateTime` / `autoUpdateTime` | gobreath-orm does not auto-populate them yet; leaving them on means one side writes values and the other does not |
| Restore GORM's `SkipDefaultTransaction` default | Otherwise you are comparing "GORM with transactions vs the others without" |
| Disable SQL logging on every ORM | Logging is I/O and would drown out the code under test |
| `TestResultParity` asserts all three return identical data | If semantics drift, the test blows up first instead of letting the benchmark quietly compare different work |
| The raw layer builds statements with `strings.Builder` | `+=` is O(n²) for multi-row VALUES and would hand the lower bound a falsely high allocation baseline |

In addition, `TestSQLParity` prints the SQL each layer actually executes for manual review:

```bash
cd bench && go test -run TestSQLParity -v
```

It captures what gobreath **really sends to the driver** (via the logging hook rather than DryRun — DryRun cannot reach fixed-template paths like `SelectById` / `Insert` / `UpdateById`).

## Results

Environment: Windows / amd64, Intel i5-1135G7, Go 1.24.0, `-benchtime=2s`.

> This is not a "who is faster" leaderboard. Every number below includes the SQLite driver's own overhead,
> and all of it sits under optimistic conditions: **in-memory database, no network**. In the real world, network
> round-trips dominate everything.
> How to read it: `raw` is the cost lower bound for the same work, and the **gap** between an ORM and raw is the
> price of that ORM layer.

**Read this table first** (important, or you will draw wrong conclusions):

| Metric | Trustworthiness | Reason |
|---|---|---|
| `B/op`, `allocs/op` | **Reproducible**, byte-for-byte deterministic | Independent of scheduling, GC timing, and machine load; the same code always yields the same number |
| `ns/op` | **Magnitude reference only** | A single op is in the 3 ms range (dominated by SQLite's execution plan), and on a local machine the same code can swing ±40% within one run |

How extreme the `ns/op` jitter is, measured on identical code: `ListPage/gobreath` was 3.19 / 4.22 / 5.02 ms in one
`-count=3` run and 2.92 ~ 3.38 ms in another `-count=7` run — **a 30% difference in medians across two runs**, with
not a character of code changed. So every judgement below about "who is faster" is based on `B/op` and `allocs/op`;
`ns/op` only counts when the gap exceeds 50%.
(For reproducible ORM-layer latency, use the root module's `go test -bench BenchmarkScanList -benchmem` — it runs on
a mock driver, a single op is in the microsecond range, see the end of this file.)

Sampling: `-count=3` median per scenario; `ListPage` is the subject under judgement, so its sample is doubled to `-count=7`.

### Single-row operations

| Scenario | Layer | ns/op | B/op | allocs/op |
|---|---|---|---|---|
| **SelectByID** | raw | 31,320 | 1,704 | 68 |
| | gorm | 64,903 | 7,065 | 134 |
| | **gobreath** | **36,938** | **2,315** | **82** |
| **InsertOne** | raw | 28,386 | 762 | 31 |
| | gorm | 69,216 | 6,566 | 91 |
| | **gobreath** | **22,952** | **1,886** | **56** |
| **UpdateByID** | raw | 23,776 | 632 | 27 |
| | gorm | 54,991 | 6,704 | 92 |
| | **gobreath** | **41,023** | **2,420** | **82** |

- Reads: gobreath allocates 1.36× raw SQL and 33% of GORM.
- Writes: `InsertOne` is **3× faster** than GORM (71% fewer allocations), `UpdateByID` 1.3× faster.

### Multi-row reads

| Scenario | Layer | ns/op | B/op | allocs/op |
|---|---|---|---|---|
| **ListPage** (50 rows) | raw | 3,782,821 | 51,696 | 1,839 |
| | gorm | 3,338,564 | 77,692 | 2,353 |
| | **gobreath** | **3,244,296** | **63,797** | **1,906** |
| **Count** | raw | 1,995,453 | 432 | 16 |
| | gorm | 1,854,373 | 4,384 | 61 |
| | **gobreath** | **1,246,456** | **1,779** | **48** |

- `Count`: 1,779 B allocated — 59% less than GORM (GORM's 4,384 B comes from constructing its `Model`/`Statement` fixed overhead).
- `ListPage`: **all three layers' ns/op medians already fall in the same band** (3.24 / 3.34 / 3.78 ms), and the
  differences are far smaller than the single-run jitter described above, so no ranking can be derived from them.
  The reproducible difference is in allocations: gobreath 63.8 KB vs raw 51.7 KB (1.23×), GORM 77.7 KB.
  Row mapping for 50 rows has been reduced from "1.4× raw's allocations" to "23% more" — details and the fix are in
  the next section.

### Row mapping: from 26% behind to level with the others

`bench/` measures **end to end** against a real database, and SQLite's execution plan drowns out ORM-layer differences.
To see the ORM layer itself, use the mock-driver benchmark in the root module (touching no database engine):

```bash
go test -run "^$" -bench BenchmarkScanList -benchmem   # root module
```

| Item | Before | After | |
|---|---|---|---|
| `SelectList` (50 rows × 9 columns) | 96,341 ns / 87,267 B / 498 allocs | **23,864 ns / 27,573 B / 101 allocs** | ns −75%, B −68%, allocs −80% |
| of which "row mapping + SQL building" delta | 93,326 ns (1,867 ns/row) | **20,843 ns (417 ns/row)** | **−78%** |
| `SelectById` | 8,123 ns / 2,281 B / 32 allocs | **6,244 ns / 1,433 B / 27 allocs** | ns −23%, B −37% |
| `SelectList` (with a JSON column) | 201,778 ns / 43,130 B / 824 allocs | **157,789 ns / 38,487 B / 727 allocs** | see below |

For the same table, `SelectById`'s B/op in the real-database benchmark went from 3,164 to 2,315 — the same direction.

The essence of the change: **"column order → field" resolution moved from "once per row" to "once per query"**,
cached on the model metadata keyed by the result set's column signature (`modelMeta.scanPlanFor`).
Before, every row called `rows.Columns()`, built two maps (`jsonCols` even for models without JSON columns), and
allocated two `[]any` buffers; all of that depends only on the **column set**, not on any row.

The largest remaining chunk is **JSON columns**: 3,095 ns/row and 14.5 allocs/row, almost entirely in
`json.Unmarshal` into `map[string]any` (the map and every string in it must be allocated).
There is no cheap way around it, and GORM pays the same cost, so it stays.

### BatchInsert batch size

Each round inserts exactly 500 rows, varying only the batch size. Units are **µs/row** (total time ÷ 500).
Each layer is `-count=3`, median:

| chunk | raw | gorm | gobreath |
|---|---|---|---|
| 1 | 21.8 | 40.3 | 24.3 |
| **10** | **11.2** | **16.9** | **12.8** |
| 50 | 12.7 | 15.7 | 13.1 |
| 100 | 14.6 | 18.4 | 15.2 |
| 250 | 26.8 | 32.9 | 27.1 |
| 500 | 39.1 | 48.2 | **37.2** |

Conclusions:

1. **The knee is at chunk ≈ 10**, not "bigger is better". On SQLite, past 50 a single statement grows large enough that parsing costs overtake the saved round-trips — 500-row batches are more than 3× slower than 10-row batches.
2. gobreath tracks raw SQL across the whole range (12.8 vs 11.2 µs/row at chunk=10, +14%), and at chunk=500 its 37.2 µs/row is slightly better than raw's 39.1 — those two numbers are within noise, so all it shows is "no extra overhead".
3. **GORM's `CreateInBatches` is 40.3 µs/row at chunk=1**, 66% slower than gobreath; the gap converges as the chunk grows.

### What these numbers **cannot** be used to claim

- ❌ "gobreath is N× faster than GORM" — that only holds on an in-memory SQLite database in these scenarios.
  The single-row write gap comes largely from GORM's hook / callback chain, and the ratio will change on PG or MySQL.
- ❌ **Any ranking based on a `ns/op` difference below 50%**. The median of identical code can differ by 30% between
  two runs (see the top), so something like `ListPage`'s 6% spread across three layers is entirely inside the jitter.
  To establish a trustworthy difference you need `-count=10` plus `benchstat`, or the root module's mock benchmark.
- ❌ "The optimal batch size is 10" — that is a SQLite conclusion. SQLite has no network round-trip and in-process
  calls are extremely cheap, so "fewer round-trips" buys little; on PG / MySQL chunk=1 will be far worse and the
  optimum is likely larger. Before using these numbers to set a `WithChunkSize` default, run all three dialects
  against real databases.

## Notes when changing this

- **`TestResultParity` is the foundation of the benchmark**. If you change any layer's SQL in `harness_test.go`, it must pass first.
- When adding a scenario, keep constants like `rawListPageSQL` semantically identical to the GORM / gobreath versions,
  and print all three in `TestSQLParity`.
- `updatePayload` must carry the target row's **real `created_at`**: gobreath's `UpdateById` writes every writable
  column (including `created_at`), so leaving it zero would wipe that row's timestamp to `0001-01-01` on every
  iteration. The raw / GORM SET lists do not include `created_at`, so only gobreath trips over it — which is exactly
  why the payload must be prepared for the strictest side.
- Always use `newOrmHarness` (Silent) in benchmarks. `newOrmHarnessWith` exists only for `TestSQLParity` to capture SQL.
- Do not judge "row mapping got faster/slower" from the `ns/op` here (see the top). Use the root module's
  `go test -run "^$" -bench BenchmarkScanList -benchmem`: it runs on a mock driver, a single op is in the microsecond
  range, and `B/op` / `allocs/op` are byte-for-byte reproducible.
  After changing row mapping (`setterFor` / `scanPlanFor` in `model.go`), also pass
  `TestScanPlanColumnSets` (each column set maps correctly) and `TestScanPlanCacheCap` (still correct past the plan cache limit).

## Known deviations / not covered

- SQLite only. PG / MySQL would require abstracting the three backends out of `harness_test.go`, which is deliberately
  not done yet — understand the SQLite differences first, then decide whether to extend to three dialects.
- The GORM side uses a fair configuration ("auto time fill off + default transaction off"), not its defaults.
  To see the impact of the defaults, restore the two commented-out settings in `newGormHarness`.
- Not covered: JOIN, Preload associations, batched work inside transactions, and vector search — all things gobreath
  offers that these scenarios do not exercise.
- **`ns/op` is not reproducible on a local machine** (±40%, see the top). The absolute values in the tables are
  magnitude references only; for comparable latency numbers use the root module's mock benchmark, or a machine with
  no background load running `-count=10` plus benchstat.
