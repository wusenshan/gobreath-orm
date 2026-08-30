// examples/vector-search/en/main.go
//
// gobreath-orm — Vector search example (English, fully commented).
//
// This file demonstrates the *unified* vector API that works identically on
// Postgres (pgvector) and MySQL 9+. Run it offline to see the dialect-specific
// SQL each engine produces:
//
//	cd examples/vector-search/en
//	go run .
//
// To actually hit a database, uncomment the "Real run" block at the bottom,
// fill in the DSN, and `go mod tidy` to pull the driver.
//
// Why vectors? Traditional LIKE / full-text search matches *characters*.
// Vector (semantic) search matches *meaning*: text is turned into a high-
// dimensional float array by an embedding model; similar text sits close in
// space, so we retrieve by nearest distance instead of by keywords. This is
// the backbone of RAG / knowledge-base QA / semantic search.
package main

import (
	"fmt"

	orm "github.com/wusenshan/gobreath-orm"
)

// Article: an embedding is just a []float32 tagged with ",vector".
// The column dimension is fixed at CREATE TABLE time and must match the
// embedding model's output dimension (see pitfalls below).
type Article struct {
	ID        int64     `db:"id,pk,autoincrement"`
	Title     string    `db:"title"`
	Embedding []float32 `db:"embedding,vector"`
}

// queryVec would normally come from an embedding model (e.g. Alibaba Qwen
// text-embedding-v3 emits 1024 dims; OpenAI text-embedding-3-small emits 1536).
var queryVec = []float32{0.12, -0.34, 0.56, 0.78}

func main() {
	embCol := orm.Col[Article](func(a *Article) *[]float32 { return &a.Embedding })

	// 1) Cosine nearest-neighbour top-5 — Postgres uses pgvector's <=> operator.
	pgSQL, _ := orm.NewQuery[Article]().
		WithDialect(orm.PG).
		NearestBy(embCol, queryVec, 5, orm.Cosine).
		Build()
	fmt.Println("-- Postgres cosine NN top-5 --")
	fmt.Println(pgSQL)
	fmt.Println()

	// 2) Same call, MySQL dialect: VECTOR_DISTANCE(...) function instead.
	mySQL, _ := orm.NewQuery[Article]().
		WithDialect(orm.MySQL).
		NearestBy(embCol, queryVec, 5, orm.Cosine).
		Build()
	fmt.Println("-- MySQL 9 cosine NN top-5 --")
	fmt.Println(mySQL)
	fmt.Println()

	// 3) Threshold + ordering (only recall distance < 0.3, then ascending).
	thSQL, _ := orm.NewQuery[Article]().
		WithDialect(orm.PG).
		NearestBy(embCol, queryVec, 20, orm.Cosine).
		WithinDistance(embCol, queryVec, 0.3).
		Build()
	fmt.Println("-- cosine NN + distance threshold --")
	fmt.Println(thSQL)
	fmt.Println()

	// 4) INSERT: PG binds the '[..]' text directly; MySQL wraps it in
	//    STRING_TO_VECTOR(?). gobreath-orm handles this per-dialect for you.
	fmt.Println("-- INSERT vector column --")
	fmt.Println(`Postgres: INSERT INTO "articles" (...) VALUES (..., $1)   -- $1 = '[..]'`)
	fmt.Println("MySQL 9 : INSERT INTO `articles` (...) VALUES (..., STRING_TO_VECTOR(?))")
	fmt.Println()

	pitfalls()
}

func pitfalls() {
	fmt.Println("-- Pitfalls (the ones people actually hit) --")
	fmt.Println("P1  Dimension mismatch is a HARD error on both engines.")
	fmt.Println("    PG(pgvector):     ERROR: expected 1536 dimensions, not 1024")
	fmt.Println("    MySQL 9:         ERROR 3535 (HY000): Vector dimension mismatch: expected 1536, got 1024")
	fmt.Println("    The column's N is fixed at CREATE TABLE; insert/query with a")
	fmt.Println("    different width fails 100% (no silent truncation/padding).")
	fmt.Println("    Fix: CREATE TABLE dim == model output dim == stored vector dim.")
	fmt.Println()
	fmt.Println("P2  Switching embedding models changes the dimension.")
	fmt.Println("    OpenAI text-embedding-3-small = 1536; Alibaba Qwen")
	fmt.Println("    text-embedding-v3 = 1024 (default). Migrating OpenAI->Qwen")
	fmt.Println("    without resizing the column throws P1 immediately.")
	fmt.Println("    Domestic-friendly: Qwen works without reaching api.openai.com;")
	fmt.Println("    see qwen/main.go for the zero-dep HTTP client.")
	fmt.Println()
	fmt.Println("P3  MySQL: a VECTOR column cannot be PK / unique / foreign / partition key.")
	fmt.Println("    Keep a separate normal id column (as in Article above).")
	fmt.Println()
	fmt.Println("P4  MySQL: VECTOR(N) with N omitted defaults to 2048, not 'any dimension'.")
	fmt.Println("    Range 1..16383. Be explicit and match your model.")
	fmt.Println()
	fmt.Println("P5  Metric support differs: MySQL L1(MANHATTAN) needs 9.7+;")
	fmt.Println("    Cosine/COSINE exists since 9.0. When unsure, use Cosine.")
	fmt.Println()
	fmt.Println("P6  Without a vector index it's a full table scan. On scale,")
	fmt.Println("    PG:  CREATE INDEX ... USING hnsw (embedding vector_cosine_ops);")
	fmt.Println("    MySQL: CREATE VECTOR INDEX idx ON articles(embedding);")
	fmt.Println()
	fmt.Println("P7  Metric scale for thresholds: Cosine in [0,2], L2/L1 in [0,+inf).")
	fmt.Println("    Pick the metric the embedding was trained for; Cosine is the")
	fmt.Println("    safe default for text semantic search.")
}
