// examples/vector-search/pg/main.go
//
// Postgres + pgvector 向量检索实例（gobreath-orm）。
//
// 本文件离线即可运行：go run . 会打印框架为 Postgres 方言生成的向量 SQL，
// 让你直观看到「一套 API」在 PG 上长什么样，以及最容易踩的坑——
// 向量维度不一致会报什么错（见 mysql/main.go 看 MySQL 版差异）。
//
// 想连真实库跑：取消文件末尾「真实可跑版」注释块，填 DSN 并 go mod tidy 拉取驱动。
package main

import (
	"fmt"

	orm "github.com/wusenshan/gobreath-orm"
)

// Article 模型：embedding 用 []float32 + ",vector" tag 声明为向量列。
// 注意：列维度在建表时写死（vector(N)），必须与 embedding 模型输出维度一致。
type Article struct {
	Id        int64     `db:"id,pk,autoincrement"`
	Title     string    `db:"title"`
	Embedding []float32 `db:"embedding,vector"` // ← 向量列
}

// queryVec：通常来自某个 embedding 模型（如阿里千问 text-embedding-v3 输出 1024 维）。
// 下面故意用 4 维，方便对照建表 DDL 里的 vector(N) 维度。
var queryVec = []float32{0.12, -0.34, 0.56, 0.78}

func main() {
	embCol := orm.Col[Article](func(a *Article) *[]float32 { return &a.Embedding })

	// 1) 余弦近邻 top-5：PG 用 pgvector 的 <=> 运算符
	pgSQL, pgArgs := orm.NewQuery[Article]().
		WithDialect(orm.PG).
		NearestBy(embCol, queryVec, 5, orm.Cosine).
		Build()
	fmt.Println("-- Postgres 余弦近邻 top-5 --")
	fmt.Println(pgSQL)
	fmt.Printf("args: %#v\n\n", pgArgs)

	// 2) 余弦近邻 + 距离阈值（只召回距离 < 0.3 的，再按距离升序）
	thresholdSQL, _ := orm.NewQuery[Article]().
		WithDialect(orm.PG).
		NearestBy(embCol, queryVec, 20, orm.Cosine).
		WithinDistance(embCol, queryVec, 0.3).
		Build()
	fmt.Println("-- 余弦近邻 + 距离阈值 --")
	fmt.Println(thresholdSQL)
	fmt.Println()

	// 3) INSERT/UPDATE 向量列：PG 直接绑定 '[..]' 文本，由 vector 列类型解析。
	fmt.Println("-- INSERT 向量列（PG）--")
	fmt.Println(`INSERT INTO "articles" ("title","embedding") VALUES ($1, $2)`)
	fmt.Println("   -- $2 = '[0.12,-0.34,0.56,0.78]'（文本，pgvector 自动解析，无需 pgvector-go）")
	fmt.Println()

	// 4) ⚠️ 坑演示：维度不一致会报什么错（见下方打印，真实运行才会出现在 error 里）
	pgPitfall()
}

func pgPitfall() {
	fmt.Println("-- ⚠️ 最容易踩的坑：向量维度不一致 --")
	fmt.Println("假如建表时 embedding vector(1536)，但入库/查询的向量只有 1024 维：")
	fmt.Println("  INSERT INTO articles (..., embedding) VALUES (..., '[...1024 个数...]');")
	fmt.Println("  -- PG(pgvector) 报错（原文，database/sql 抛出的 error 字符串里就带着它）：")
	fmt.Println("     ERROR: expected 1536 dimensions, not 1024")
	fmt.Println("  根因：vector(N) 的 N 在建表时固定，写入/检索维度对不上 100% 报错，")
	fmt.Println("        不会静默截断或补零；且列维度不可直接 ALTER 成别的固定值")
	fmt.Println("        （需新建列 + 重建索引）。")
	fmt.Println("  对策：建表维度 = 模型输出维度 = 入库向量维度，三者必须一致；")
	fmt.Println("        换模型导致维度变了，要新建列/新表并重灌数据。")
	fmt.Println("  （MySQL 版见 mysql/main.go：错误文案不同，但同样 100% 报错。）")
}
