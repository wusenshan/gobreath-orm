// vector-search：gobreath-orm 的向量检索演示。
//
// 本示例无需安装任何数据库即可运行——它直接打印查询构造器为不同方言生成的 SQL，
// 让你直观看到「一套 API、多库方言分发」的效果（Postgres 用 pgvector 运算符，
// MySQL 9 用 VECTOR_DISTANCE 函数）。
//
// 想真正跑起来，把下方「真实用法」注释块打开，并准备好 Postgres(+pgvector) 或 MySQL 9+。
//
//	cd examples/vector-search
//	go run .
package main

import (
	"fmt"

	orm "github.com/wusenshan/gobreath-orm"
)

// Article 模型：embedding 标记为向量列（db tag 含 ",vector"）。
// 向量字段用 []float32 / []float64 即可，框架自动序列化为 [..] 文本，
// 无需引入 pgvector-go 之类的第三方包。
type Article struct {
	Id        int64     `db:"id,pk,autoincrement"`
	Title     string    // → title
	Embedding []float32 `db:"embedding,vector"` // → embedding（向量列）
}

// 一条查询向量（来自某个文本 embedding 模型，如 OpenAI text-embedding-3-small）。
var queryVec = []float32{0.12, -0.34, 0.56, 0.78}

func main() {
	embCol := orm.Col[Article](func(a *Article) *[]float32 { return &a.Embedding })

	// 1) 余弦近邻（文本语义相似度首选）—— Postgres 与 MySQL 的方言差异一目了然
	pgSQL, pgArgs := orm.NewQuery[Article]().
		WithDialect(orm.PG).
		NearestBy(embCol, queryVec, 5, orm.Cosine).
		Build()
	fmt.Println("-- Postgres 余弦近邻 --")
	fmt.Println(pgSQL)
	fmt.Printf("args: %#v\n\n", pgArgs)

	mySQL, myArgs := orm.NewQuery[Article]().
		WithDialect(orm.MySQL).
		NearestBy(embCol, queryVec, 5, orm.Cosine).
		Build()
	fmt.Println("-- MySQL 9 余弦近邻 --")
	fmt.Println(mySQL)
	fmt.Printf("args: %#v\n\n", myArgs)

	// 2) 阈值过滤 + 排序（只召回距离小于 0.3 的，再按距离升序）
	thresholdSQL, _ := orm.NewQuery[Article]().
		WithDialect(orm.PG).
		NearestBy(embCol, queryVec, 20, orm.Cosine).
		WithinDistance(embCol, queryVec, 0.3).
		Build()
	fmt.Println("-- 余弦近邻 + 距离阈值 --")
	fmt.Println(thresholdSQL)
	fmt.Println()

	// 3) INSERT/UPDATE 向量列时方言差异说明（无法在此离线演示，打印提示）：
	//    - MySQL 9+：框架自动生成 `STRING_TO_VECTOR(?)` 包裹，绑定值为 '[..]' 文本
	//    - Postgres：直接绑定 '[..]' 文本，由 pgvector 解析列类型
	//    真实插入代码见文件末尾的「真实用法」注释块。
	fmt.Println("-- INSERT/UPDATE 向量列（方言差异）--")
	fmt.Println("MySQL 9+:  INSERT ... VALUES (?, STRING_TO_VECTOR(?))")
	fmt.Println("Postgres: INSERT ... VALUES (?, ?)   -- '[..]' 文本由 pgvector 解析")
	fmt.Println()
}

/*
// ---- 真实用法（需要 Postgres + pgvector 或 MySQL 9+）----

package main // 与上方 main 合并时注意去掉重复

import (
	"context"
	"fmt"

	orm "github.com/wusenshan/gobreath-orm"

	_ "github.com/jackc/pgx/v5/stdlib"   // Postgres 驱动（或 github.com/lib/pq）
	// _ "github.com/go-sql-driver/mysql" // 换 MySQL 时导入这个
)

func realUsage() {
	ctx := context.Background()

	// Postgres + pgvector
	db, err := orm.Open(orm.Config{
		Driver: "pgx",
		DSN:    "postgres://user:pass@localhost:5432/demo?sslmode=disable",
	})
	if err != nil {
		panic(err)
	}

	// 表与扩展（一次性）：
	//   CREATE EXTENSION IF NOT EXISTS vector;
	//   CREATE TABLE articles (
	//     id        BIGSERIAL PRIMARY KEY,
	//     title     TEXT NOT NULL,
	//     embedding vector(4)          -- 维度与 embedding 模型一致
	//   );
	//   CREATE INDEX ON articles USING hnsw (embedding vector_cosine_ops); -- 加速余弦检索

	// 写入：Embedding 是 []float32，框架自动序列化为 '[..]' 文本
	_ = orm.Insert(ctx, db, &Article{
		Title:     "Go 与向量检索",
		Embedding: []float32{0.12, -0.34, 0.56, 0.78},
	})

	// 检索：语义最相近的 5 篇（余弦距离升序）
	embCol := orm.Col[Article](func(a *Article) *[]float32 { return &a.Embedding })
	hits, err := orm.SelectList(ctx, db,
		orm.NewQuery[Article]().NearestBy(embCol, queryVec, 5, orm.Cosine))
	if err != nil {
		panic(err)
	}
	for _, h := range hits {
		fmt.Printf("#%d %s\n", h.Id, h.Title)
	}
}

// 换 MySQL 9+ 时：
//   Driver: "mysql"，DSN: "user:pass@tcp(localhost:3306)/demo"
//   DDL: CREATE TABLE articles (id BIGINT AUTO_INCREMENT PRIMARY KEY, title VARCHAR(255), embedding VECTOR(4));
//   其余代码完全一致——这就是「统一 API」的价值。
*/
