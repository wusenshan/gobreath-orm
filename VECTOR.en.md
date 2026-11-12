# Vector search (native AI / RAG support)

> One document covering three things: **how vectors are stored in the database (the logic)**, **what they buy you**, and **how to wire an AI embedding model to get it running**.
> Runnable examples live in [`examples/vector-search`](examples/vector-search):
> - `main.go` — inspect the SQL generated for both PostgreSQL and MySQL dialects offline
> - `pg/main.go` · `mysql/main.go` — complete instances for each database (including differences, pitfalls, and the exact dimension-mismatch errors)
> - `qwen/main.go` — Alibaba Qwen (Bailian) embedding integration, **usable from mainland China**, an alternative to OpenAI
> - `en/main.go` — English-commented version
> Its subdirectory `README.md` carries a "Postgres vs MySQL differences cheat sheet" and a pitfall list.

---

## 1. What problem it solves (effect / use cases)

Traditional `LIKE` and full-text search do **string matching** — search for "how do I cancel my membership" and an article titled "unsubscribe instructions" will not be found, because the literal strings differ.

Vector search does **semantic matching**: an embedding model first turns text into a high-dimensional float array (a vector), and texts with similar meaning end up close together in that space. Retrieval then compares **distances between vectors** rather than characters — the smaller the distance, the more relevant.

Typical applications:

| Scenario | Approach |
|---|---|
| Knowledge-base Q&A / RAG | Chunk documents, embed them, store them; embed the user's question the same way and take the nearest k chunks as context for the LLM |
| Semantic search | Use natural language as the query and recall entries closest in meaning, not keyword hits |
| Similar-item recommendation / deduplication | "Find the N items most like this one", "is this image / passage already in the database?" |
| Clustering / classification | Coarse grouping by vector distance |

`gobreath-orm` reduces "store vectors + nearest-neighbour retrieval" to **a single line of API**, with the same code for PostgreSQL and MySQL.

---

## 2. How vectors are stored (the logic of a vector store)

### 2.1 Declaring a vector column on the model

A vector field is an ordinary `[]float32` / `[]float64` annotated with a `,vector` tag:

```go
type Article struct {
    Id        int64     `db:"id,pk,autoincrement"`
    Title     string    `db:"title"`
    Embedding []float32 `db:"embedding,vector"` // <- vector column
}
```

### 2.2 Serialization: zero dependencies, one text format

On `Insert / Update`, `serializeVector` turns the slice into **`[0.12,-0.34,0.56,0.78]` text** and binds it as a parameter:

- **PostgreSQL (pgvector)**: binds the `[..]` text directly, parsed by the `vector` column type;
- **MySQL 9+**: the framework wraps the bound value with `STRING_TO_VECTOR(?)`;
- **no third-party package such as `pgvector-go` is involved** — you only install a database driver.

### 2.3 Column types in the database

| Database | Column declaration | Notes |
|---|---|---|
| PostgreSQL + pgvector | `embedding vector(1536)` | dimension = your embedding model's output size; requires `CREATE EXTENSION vector;` |
| MySQL 9+ | `embedding VECTOR(1536)` | native support since 9.0 |
| SQLite | no native vector type | can only assemble SQL offline; use PG / MySQL for real retrieval |

> ⚠️ **Dimensions must match**: build the column with the same dimension your model outputs, and store vectors of that same dimension — **otherwise writes and retrieval fail hard** (neither database truncates silently):
> - PostgreSQL (pgvector): `ERROR: expected N dimensions, not M`
> - MySQL 9+: `ERROR 3535 (HY000): Vector dimension mismatch: expected N, got M` (wording varies by version)
>
> A column's dimension is fixed at creation and cannot be `ALTER`ed to another fixed size; if switching models changes the dimension, create a new column/table and reload the data. See the `pg`/`mysql` examples and their README in `examples/vector-search`.

---

## 3. The four distance metrics and when to use them (logic / intuition)

In `NearestBy(col, vec, k, metric)`, `metric` decides the operator (PG) or function (MySQL) used in `ORDER BY`. **Smaller distance means more similar** (see the InnerProduct note below).

| Metric | Constant | Intuition | PG operator | MySQL function | When to use |
|---|---|---|---|---|---|
| Euclidean | `orm.L2` (default) | straight-line geometric distance, `0` = identical | `<->` | `EUCLIDEAN` | **un-normalised** features such as images / audio |
| Cosine | `orm.Cosine` | angular distance `1 - cosθ`, direction only, length ignored | `<=>` | `COSINE` | **first choice for text semantics** (the RAG default) |
| Inner product | `orm.InnerProduct` | dot product; monotonically equivalent to cosine once vectors are normalised | `<#>` (negative inner product) | `DOT` | fastest when vectors are already normalised |
| Manhattan | `orm.L1` | sum of per-dimension absolute differences | `<+>` | `MANHATTAN` (9.7+) | sparse vectors |

Key points:

- **Cosine**: range `[0, 2]`, `0` = identical direction (most similar), `2` = opposite. Text embeddings are usually normalised already, so cosine is the most robust choice and the default for semantic retrieval.
- **InnerProduct**: similarity = the larger the dot product the better. pgvector's `<#>` returns the **negative inner product**, so `ORDER BY col <#> $1 ASC` naturally puts the largest dot product first — at the API level this is still "`ORDER BY distance ASC LIMIT k`", and you never deal with the sign.
- **L2 / L1**: range `[0, +∞)`, `0` = identical.
- `Nearest / WithinDistance` default to `L2` when no metric is given, preserving the legacy `<->` behaviour.

---

## 4. Worked examples (one API across databases)

### 4.1 SQL generated per dialect (from `examples/vector-search`)

```go
embCol := orm.Col[Article](func(a *Article) *[]float32 { return &a.Embedding })
queryVec := []float32{0.12, -0.34, 0.56, 0.78}

// Cosine top-5
orm.NewQuery[Article]().WithDialect(orm.PG).
    NearestBy(embCol, queryVec, 5, orm.Cosine).Build()
// -> SELECT * FROM "articles" ORDER BY "embedding" <=> $1 LIMIT 5

orm.NewQuery[Article]().WithDialect(orm.MySQL).
    NearestBy(embCol, queryVec, 5, orm.Cosine).Build()
// -> SELECT * FROM `articles`
//    ORDER BY VECTOR_DISTANCE(`embedding`, STRING_TO_VECTOR(?), 'COSINE') LIMIT 5
```

Distance threshold plus ordering (only recall those with distance `< 0.3`, ordered by ascending distance):

```go
orm.NewQuery[Article]().WithDialect(orm.PG).
    NearestBy(embCol, queryVec, 20, orm.Cosine).
    WithinDistance(embCol, queryVec, 0.3).Build()
// -> SELECT * FROM "articles"
//    WHERE "embedding" <=> $1 < 0.3
//    ORDER BY "embedding" <=> $1 LIMIT 20
```

Filter only (no ordering, no `dist` projection) — calling `WithinDistance` alone is a pure `WHERE` condition:

```go
orm.NewQuery[Article]().WithDialect(orm.PG).
    WithinDistance(embCol, queryVec, 0.3).Build()
// -> SELECT * FROM "articles" WHERE "embedding" <=> $1 < 0.3
```

> Pure filtering without ordering has been the semantics since v0.1.15 (older versions helpfully appended an `ORDER BY distance` and a `dist` column).
> If you need similarity ordering, chain `Nearest` / `NearestBy` explicitly. This is also what lets `Distinct()` coexist with threshold filtering.

### 4.2 A real retrieval flow (PostgreSQL)

```go
db, _ := orm.Open(orm.Config{Driver: "pgx",
    DSN: "postgres://user:pass@localhost:5432/demo?sslmode=disable"})

// Write: Embedding is []float32, the framework serializes it to '[..]'
_ = orm.Insert(ctx, db, &Article{Title: "Go and vector search",
    Embedding: []float32{0.12, -0.34, 0.56, 0.78}})

// Read: the 5 most semantically similar articles (ascending cosine distance)
hits, _ := orm.SelectList(ctx, db,
    orm.NewQuery[Article]().NearestBy(embCol, queryVec, 5, orm.Cosine))
for _, h := range hits {
    fmt.Printf("#%d %s\n", h.Id, h.Title) // closest to queryVec first
}
```

Switching to MySQL 9+ only changes `Driver` / `DSN` and the DDL (`VECTOR(4)`) — **not one line of retrieval code** — which is the value of a unified API.

### 4.3 Million-scale performance: add a vector index

Nearest-neighbour retrieval must have a vector index to be fast; otherwise it is a full table scan:

```sql
-- PostgreSQL: build an HNSW index with the ops matching your metric
CREATE INDEX ON articles USING hnsw (embedding vector_cosine_ops);  -- cosine
-- CREATE INDEX ON articles USING hnsw (embedding vector_l2_ops);    -- l2

-- MySQL 9+
CREATE VECTOR INDEX idx_articles_embedding ON articles(embedding);
```

The `ORDER BY distance ASC LIMIT k` the framework generates hits these indexes directly.

> **`AutoMigrate` creates vector columns but not vector indexes — deliberately, not by omission.**
> Any one of three reasons makes automatic creation impossible:
> 1. PG's HNSW index must bind the **ops matching the metric** (`vector_cosine_ops` / `vector_l2_ops` /
>    `vector_ip_ops`), while the metric is a **query-time parameter** (`NearestBy(..., orm.Cosine)`) — the
>    model layer has no way to know which one you intend. An index with the wrong ops never gets used, creating
>    the illusion that "I built the index, why is it still slow";
> 2. MySQL community editions have no `VECTOR INDEX` (HeatWave on OCI / MySQL AI only) — creating one errors outright;
> 3. SQLite has no vector capability at all, so there is nothing to create.
>
> Index types such as IVFFlat also need **data in the table first** (the lists parameter depends on row count), so
> table-creation time is the wrong moment anyway. Build them with the scripts above, or as a separate step in your
> migration pipeline.

---

## 5. Wiring an AI embedding model (simple setup)

Vectors themselves are not mysterious — the only hard part is "how do I turn text into `[]float32`". Below is a **zero-dependency** integration (standard library `net/http` + `encoding/json` only) that works with any OpenAI-compatible endpoint and with a local Ollama.

### 5.1 Pick an embedding model

| Model | Dimensions | How to get it |
|---|---|---|
| **Alibaba Qwen `text-embedding-v3`** ✅ usable in mainland China | **1024 (default)**, also 768/512/256/128/64 | Bailian cloud API (key required), OpenAI-compatible; see `qwen/main.go` |
| Alibaba Qwen `text-embedding-v4` (Qwen3-Embedding) | 2048/1536/**1024 default**/768/512/256/128/64 | same as above |
| OpenAI `text-embedding-3-small` | 1536 | cloud API (key required; unstable from mainland China) |
| OpenAI `text-embedding-3-large` | 3072 | cloud API |
| `nomic-embed-text` / `bge-m3` etc. | varies | local Ollama, free and offline |

> 🇨🇳 **Prefer Qwen from mainland China**: reaching `api.openai.com` is unstable there and requires a foreign payment method; Qwen's `text-embedding-v3` defaults to 1024 dimensions, is compatible with the OpenAI `/v1/embeddings` interface, and integrates with zero extra dependencies (see `examples/vector-search/qwen/main.go`).
> ⚠️ **Migration pitfall**: switching from OpenAI (1536) to Qwen (1024 by default) requires changing the column dimension (`vector(N)` / `VECTOR(N)`'s N) to 1024, otherwise you immediately hit the dimension mismatch error above.

### 5.2 Turning text into `[]float32` (OpenAI-compatible)

```go
import (
    "bytes", "context", "encoding/json", "io", "net/http"
)

// embed calls any OpenAI-compatible /v1/embeddings endpoint and returns []float32.
func embed(text, baseURL, apiKey, model string) ([]float32, error) {
    body, _ := json.Marshal(map[string]any{
        "input": text,
        "model": model,
    })
    req, _ := http.NewRequestWithContext(context.Background(),
        http.MethodPost, baseURL+"/embeddings", bytes.NewReader(body))
    req.Header.Set("Content-Type", "application/json")
    if apiKey != "" { // optional for a local Ollama
        req.Header.Set("Authorization", "Bearer "+apiKey)
    }

    resp, err := http.DefaultClient.Do(req)
    if err != nil { return nil, err }
    defer resp.Body.Close()

    var out struct {
        Data []struct {
            Embedding []float64 `json:"embedding"`
        } `json:"data"`
    }
    if err := json.NewDecoder(resp.Body).Decode(&out); err != nil { return nil, err }
    if len(out.Data) == 0 { return nil, io.EOF }

    v := out.Data[0].Embedding
    f := make([]float32, len(v))
    for i, x := range v { f[i] = float32(x) } // float64 -> float32
    return f, nil
}
```

### 5.3 Local Ollama (free, offline)

Ollama exposes a similar shape with a slightly different endpoint and fields:

```go
func embedOllama(text, model string) ([]float32, error) {
    body, _ := json.Marshal(map[string]any{"model": model, "prompt": text})
    req, _ := http.NewRequestWithContext(context.Background(),
        http.MethodPost, "http://localhost:11434/api/embeddings",
        bytes.NewReader(body))
    req.Header.Set("Content-Type", "application/json")

    resp, err := http.DefaultClient.Do(req)
    if err != nil { return nil, err }
    defer resp.Body.Close()

    var out struct{ Embedding []float64 `json:"embedding"` }
    if err := json.NewDecoder(resp.Body).Decode(&out); err != nil { return nil, err }
    f := make([]float32, len(out.Embedding))
    for i, x := range out.Embedding { f[i] = float32(x) }
    return f, nil
}
```

### 5.4 End to end: create the table -> load -> retrieve

```go
// 1) Create the table once, with dimensions matching your model.
//    PostgreSQL: CREATE EXTENSION IF NOT EXISTS vector;
//                CREATE TABLE articles (id BIGSERIAL PRIMARY KEY, title TEXT,
//                                       embedding vector(1536));
//    MySQL 9+:   CREATE TABLE articles (id BIGINT AUTO_INCREMENT PRIMARY KEY,
//                title VARCHAR(255), embedding VECTOR(1536));

// 2) Embed the corpus and load it (in production, batch the embedding calls and use BatchInsert)
corpus := []string{"Go's concurrency model", "How vector search works", "How to build RAG"}
for _, doc := range corpus {
    vec, _ := embed(doc, "https://api.openai.com/v1", os.Getenv("OPENAI_API_KEY"),
        "text-embedding-3-small")
    _ = orm.Insert(ctx, db, &Article{Title: doc, Embedding: vec})
}

// 3) User question -> embedding -> nearest-neighbour retrieval (feed the top-k to the LLM for RAG)
question := "How does Go handle high concurrency"
qVec, _ := embed(question, "https://api.openai.com/v1", os.Getenv("OPENAI_API_KEY"),
    "text-embedding-3-small")

embCol := orm.Col[Article](func(a *Article) *[]float32 { return &a.Embedding })
hits, _ := orm.SelectList(ctx, db,
    orm.NewQuery[Article]().NearestBy(embCol, qVec, 3, orm.Cosine))
// hits are the 3 most semantically relevant passages, ready to use as LLM context
```

Key points:
- **Dimensions must line up**: the table's dimension, the model's output size, and the stored vectors' dimension must all agree.
- **Batch**: for real corpora, batch the embedding calls and use `BatchInsert` — do not embed one document at a time.
- **Match the metric to how the model was trained**: the vast majority of text embeddings work best with **Cosine**; when in doubt, use Cosine.

---

## 6. Limitations and caveats

- **SQLite has no native vector type**: the framework can only assemble SQL offline; use PostgreSQL / MySQL for real nearest-neighbour retrieval.
- **Dimension consistency**: as above, a mismatch makes writes and retrieval **fail hard** (PG `ERROR: expected N dimensions, not M`; MySQL `Vector dimension mismatch: expected N, got M`) — never a silent truncation. Table dimension = model output size = stored vector dimension.
- **Metric choice**: the wrong metric (e.g. L2 instead of Cosine on normalised vectors) degrades recall quality; the default for semantic retrieval is Cosine.
- **`WithinDistance` thresholds depend on the metric's scale**: cosine distance is `[0,2]`, L2/L1 are `[0,+∞)` — understand the range before choosing a threshold.
- **Indexes**: at millions of rows, build an HNSW / VECTOR index (see 4.3) or you degenerate to a full table scan.
- **MySQL community / commercial builds have no vector retrieval functions (the most fatal issue)**: `VECTOR_DISTANCE()` and `VECTOR INDEX` are provided only by **MySQL HeatWave on OCI** and **MySQL AI**; community and commercial packages do not include them (per the official note on dev.mysql.com). Community builds can create `VECTOR(N)` columns and store/read via `STRING_TO_VECTOR()` / `VECTOR_TO_STRING()` / `VECTOR_DIM()`, but any distance query fails with `FUNCTION VECTOR_DISTANCE does not exist`. So "one API across databases" really means: **fully usable on PostgreSQL (pgvector); usable on MySQL only when deployed as HeatWave / MySQL AI**. Always run the self-check in §7 below before integrating.
- **MySQL's distance metrics vary by distribution, and `MANHATTAN` is not standard**: Oracle MySQL / HeatWave's `VECTOR_DISTANCE` supports only `COSINE` / `DOT` / `EUCLIDEAN` — **no `MANHATTAN`** — whereas **Percona Server for MySQL 9.7.2+** supports `MANHATTAN` and `EUCLIDEAN_SQUARED` in `DISTANCE()` / `VECTOR_DISTANCE()`. The framework maps `orm.L1` to `'MANHATTAN'`, so `orm.L1` **only works on distributions providing that metric (e.g. Percona 9.7+)** and reports an unknown metric on Oracle MySQL / HeatWave. The other three metrics (`orm.Cosine` / `orm.L2` / `orm.InnerProduct`) are unaffected.

---

## 7. Pre-launch verification: vector capability self-check (PG + MySQL side by side)

> **Why this is needed**: the most fatal pitfall in §6 is that **MySQL community / commercial builds' `VECTOR_DISTANCE()` and `VECTOR INDEX` are provided only by MySQL HeatWave on OCI / MySQL AI** (per the official note on dev.mysql.com). Community builds can create `VECTOR(N)` columns and store/read via `STRING_TO_VECTOR()`, but any distance query fails with `FUNCTION VECTOR_DISTANCE does not exist`. The two command sets below confirm whether the target database can actually "run vector retrieval" before you integrate gobreath-orm, so it does not blow up in production.

### 7.1 PostgreSQL (pgvector) — confirm the extension first

```sql
-- 1) Is the vector extension installed? (a version number means it is available)
SELECT extname, extversion FROM pg_extension WHERE extname = 'vector';
-- If empty, create it with superuser rights:
-- CREATE EXTENSION IF NOT EXISTS vector;

-- 2) Create a table with a vector column
CREATE TABLE articles (
  id        BIGSERIAL PRIMARY KEY,
  title     TEXT,
  embedding vector(4)          -- dimension = your embedding model's output size
);

-- 3) Insert vector rows (plain text, exactly what the framework writes)
INSERT INTO articles (title, embedding) VALUES
  ('gobreath-orm', '[0.12,-0.34,0.56,0.78]'),
  ('vector search', '[0.90, 0.10, 0.20, 0.30]');

-- 4) Distance query (cosine): <=> is the COSINE distance operator
SELECT id, title,
       embedding <=> '[0.12,-0.34,0.56,0.78]' AS dist
FROM articles
ORDER BY embedding <=> '[0.12,-0.34,0.56,0.78]'
LIMIT 3;

-- 5) (Optional) million-scale acceleration: build an HNSW index (must match the query metric)
CREATE INDEX ON articles USING hnsw (embedding vector_cosine_ops);
```

If ① returns `vector` and ④ computes a `dist`, PG vector retrieval is available.

### 7.2 MySQL 9+ — confirm it really is HeatWave / MySQL AI

```sql
-- 1) Version check (confirm 9.x and note the distribution channel)
SELECT VERSION();

-- 2) The key self-check: does the distance function exist?
--    Returning 0 (distance to itself) means HeatWave / MySQL AI and vector retrieval works;
--    "FUNCTION VECTOR_DISTANCE does not exist" means a community / commercial build without vector retrieval.
SELECT VECTOR_DISTANCE(
         STRING_TO_VECTOR('[1,2,3,4]'),
         STRING_TO_VECTOR('[1,2,3,4]'),
         'COSINE') AS d;

-- 3) Create a table with a vector column (works on community builds too)
CREATE TABLE articles (
  id        BIGINT AUTO_INCREMENT PRIMARY KEY,
  title     VARCHAR(255),
  embedding VECTOR(4)            -- dimension = model output size
);

-- 4) Insert vector rows (also works on community builds, via STRING_TO_VECTOR)
INSERT INTO articles (title, embedding) VALUES
  ('gobreath-orm', STRING_TO_VECTOR('[0.12,-0.34,0.56,0.78]')),
  ('vector search', STRING_TO_VECTOR('[0.90,0.10,0.20,0.30]'));

-- 5) Distance query (only meaningful if step 2 passed): metrics are COSINE / DOT / EUCLIDEAN only
SELECT id, title,
       VECTOR_DISTANCE(embedding, STRING_TO_VECTOR('[0.12,-0.34,0.56,0.78]'), 'COSINE') AS dist
FROM articles
ORDER BY VECTOR_DISTANCE(embedding, STRING_TO_VECTOR('[0.12,-0.34,0.56,0.78]'), 'COSINE')
LIMIT 3;

-- 6) (Optional) Vector index: likewise HeatWave / MySQL AI only; community builds report the feature as unsupported
-- CREATE VECTOR INDEX idx_articles_embedding ON articles(embedding);
```

> ⚠️ MySQL's distance metrics support only `COSINE` / `DOT` / `EUCLIDEAN` — **no `MANHATTAN`**. `orm.L1` (Manhattan) therefore cannot be mapped on MySQL; never use `orm.L1` with the MySQL dialect.

### 7.3 Self-check summary

| Check | PostgreSQL | MySQL (community / commercial) | MySQL (HeatWave / MySQL AI) |
|---|---|---|---|
| Vector type / column | ✅ `vector(N)` | ✅ `VECTOR(N)` (9.0+) | ✅ `VECTOR(N)` |
| Write / read vectors | ✅ text `"[1,2,3]"` | ✅ write via `STRING_TO_VECTOR`; **reads come back binary** (the framework decodes it) | ✅ |
| Driver prerequisite | pgx / lib/pq | **`go-sql-driver/mysql` ≥ v1.9.0** | same |
| Distance functions | ✅ `<=>` `<->` `<#>` `<+>` | ❌ function missing (measured `ERROR 1305` on 9.7.2) | ✅ `VECTOR_DISTANCE` |
| Vector index | ✅ HNSW | ❌ unsupported | ✅ `VECTOR INDEX` |
| `orm.L1` Manhattan | ✅ `<+>` | ❌ no MANHATTAN metric | ❌ none (Percona 9.7+ only) |

> **Two MySQL 9 prerequisites** (both measured — do not discover them in production):
> ① **The driver must be ≥ `go-sql-driver/mysql` v1.9.0** — `VECTOR` is field type code 242 (`MYSQL_TYPE_VECTOR`) introduced in MySQL 9.0; v1.8.x does not recognise it and reading a vector column fails with `unknown field type 242`. The problem is in the driver layer, so upgrading the server without the driver gets you nowhere.
> ② **A `VECTOR` column is a BLOB underneath**, so `SELECT` returns little-endian raw float32 **bytes**, not `"[1,2,3]"` text; the framework's read path dispatches on payload shape automatically (`[` prefix → text, otherwise binary), so callers need not care.

**Conclusion**: gobreath-orm's vector retrieval is **usable out of the box on PostgreSQL**; on **MySQL it works only when the database is HeatWave on OCI or MySQL AI** — community / commercial builds can store but not query (and `orm.L1` additionally needs a distribution providing that metric, such as Percona 9.7+). Confirm the target database's capabilities with the self-checks in 7.1 / 7.2 before integrating.

---

## 8. Related links

- Overview and full API: [README.md](README.md) · [README_EN.md](README_EN.md)
- Runnable examples (inspect both dialects' SQL offline): [examples/vector-search](examples/vector-search)
- Distance metric constants: `orm.L2` / `orm.Cosine` / `orm.InnerProduct` / `orm.L1`
- Core API: `Nearest` / `NearestBy` / `WithinDistance` / `WithinDistanceBy` / `WithVectorMetric`
