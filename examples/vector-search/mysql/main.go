// examples/vector-search/mysql/main.go
//
// MySQL 9+ 原生 VECTOR 向量检索实例（gobreath-orm）。
//
// 离线即可运行：go run . 打印框架为 MySQL 方言生成的向量 SQL。
// 想连库跑：取消末尾「真实可跑版」注释块，填 DSN 并 go mod tidy。
//
// 本文件重点：与 Postgres 的差异、以及最容易踩的坑（尤其向量维度不一致报错）。
package main

import (
	"fmt"

	orm "github.com/wusenshan/gobreath-orm"
)

// Article 模型与 PG 完全一致——这就是「统一 API」：换库只改 Driver/DSN 和 DDL，
// 检索代码一行不变。注意 Embedding 是普通的 []float32。
type Article struct {
	Id        int64     `db:"id,pk,autoincrement"`
	Title     string    `db:"title"`
	Embedding []float32 `db:"embedding,vector"` // ← 向量列
}

var queryVec = []float32{0.12, -0.34, 0.56, 0.78}

func main() {
	embCol := orm.Col[Article](func(a *Article) *[]float32 { return &a.Embedding })

	// 1) 余弦近邻 top-5：MySQL 用 VECTOR_DISTANCE(...) 函数
	mySQL, myArgs := orm.NewQuery[Article]().
		WithDialect(orm.MySQL).
		NearestBy(embCol, queryVec, 5, orm.Cosine).
		Build()
	fmt.Println("-- MySQL 9 余弦近邻 top-5 --")
	fmt.Println(mySQL)
	fmt.Printf("args: %#v\n\n", myArgs)

	// 2) 阈值 + 排序
	thresholdSQL, _ := orm.NewQuery[Article]().
		WithDialect(orm.MySQL).
		NearestBy(embCol, queryVec, 20, orm.Cosine).
		WithinDistance(embCol, queryVec, 0.3).
		Build()
	fmt.Println("-- 余弦近邻 + 距离阈值 --")
	fmt.Println(thresholdSQL)
	fmt.Println()

	// 3) INSERT/UPDATE：MySQL 自动用 STRING_TO_VECTOR(?) 包裹
	fmt.Println("-- INSERT 向量列（MySQL 9）--")
	fmt.Println("INSERT INTO `articles` (`title`,`embedding`) VALUES (?, STRING_TO_VECTOR(?))")
	fmt.Println("   -- 绑定值 = '[0.12,-0.34,0.56,0.78]'（文本，框架自动包 STRING_TO_VECTOR）")
	fmt.Println()

	mysqlPitfalls()
}

func mysqlPitfalls() {
	fmt.Println("-- MySQL 专属坑 & 与 PG 的差异 --")
	fmt.Println("① 维度不一致报错（建表 VECTOR(1536)，向量却是 1024 维）：")
	fmt.Println("     ERROR 3535 (HY000): Vector dimension mismatch: expected 1536, got 1024")
	fmt.Println("   （错误码/文案随 MySQL 版本略有差异，但同样 100% 报错、不会静默截断）")
	fmt.Println("   → 见本目录 README.md「Postgres vs MySQL 差异速查」对照 PG 的报错文案。")
	fmt.Println()
	fmt.Println("② VECTOR 列不能当主键/唯一键/外键/分区键（MySQL 硬性限制）。")
	fmt.Println("     → Article 的 id 必须单独一个普通列，不能拿 embedding 当 key。")
	fmt.Println()
	fmt.Println("③ VECTOR(N) 省略 N 时默认 2048 维！别以为不写就是「任意维」。")
	fmt.Println("     维度必须显式写死且与模型一致；范围 1~16383。")
	fmt.Println()
	fmt.Println("④ L1(曼哈顿) 需要 MySQL 9.7+ 才有 MANHATTAN 度量；9.0~9.6 用 L1 会报")
	fmt.Println("     函数不存在。不确定版本就用 Cosine（9.0 起就有 COSINE）。")
	fmt.Println()
	fmt.Println("⑤ 没有 pgvector 那种 CREATE EXTENSION；VECTOR 是 9.0 内核自带类型。")
	fmt.Println()
	fmt.Println("⑥ 向量索引语法不同：MySQL 用 CREATE VECTOR INDEX idx ON articles(embedding);")
	fmt.Println("     而 PG 用 CREATE INDEX ... USING hnsw (embedding vector_cosine_ops)。")
	fmt.Println()
	fmt.Println("⑦ 国内迁移坑：阿里千问 text-embedding-v3 默认 1024 维，")
	fmt.Println("     而 OpenAI text-embedding-3-small 是 1536 维。从 OpenAI 切到千问")
	fmt.Println("     若不改列维度，会立刻触发上面的 3535 维度不匹配报错。")
	fmt.Println("     → 列维度跟着模型走；详见 qwen/main.go。")
}
