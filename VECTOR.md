# 向量检索（AI / RAG 原生支持）

> 一份文档讲清楚三件事：**向量在数据库里是怎么存的（逻辑）**、**它能带来什么效果**、**怎么接入一个 AI 向量模型把它跑起来**。
> 配套可运行示例见 [`examples/vector-search`](examples/vector-search)：
> - `main.go` 离线看 Postgres / MySQL 两套方言 SQL
> - `pg/main.go` · `mysql/main.go` 各自完整实例（含两库差异、坑、维度不一致报错原文）
> - `qwen/main.go` 阿里千问（百炼）向量模型接入，**国内可用**，替代 OpenAI
> - `en/main.go` 英文注释版
> 子目录 `README.md` 有一份「Postgres vs MySQL 差异速查」与坑清单。

---

## 1. 它解决什么问题（效果 / 用途）

传统 `LIKE` 和全文检索做的是**字符串匹配**——你搜「怎么退订会员」，它找不到标题写「取消订阅方法」的文章，因为字面不一样。

向量检索做的是**语义匹配**：先把文本用 embedding 模型变成一个高维浮点数组（向量），语义相近的文本，它们的向量在空间中离得近。检索时不再比「字」，而是比「向量之间的距离」——距离越小越相关。

典型落地场景：

| 场景 | 做法 |
|---|---|
| 知识库问答 / RAG | 把文档切片 embedding 入库；用户提问时 embedding 同一向量，取最近的 k 段作为上下文喂给大模型 |
| 语义搜索 | 用自然语言当 query，召回意思最近似的条目，而非关键词命中 |
| 相似推荐 / 去重 | 「找和这篇最像的 N 篇」「同一张图 / 同一段话是否已在库里」 |
| 聚类 / 分类 | 按向量距离做粗分组 |

`gobreath-orm` 把"存向量 + 近邻检索"做成**一行 API**，且 Postgres 与 MySQL 同一套代码。

---

## 2. 向量是怎么存的（向量库的逻辑）

### 2.1 模型里声明向量列

向量字段就是普通的 `[]float32` / `[]float64`，加一个 `,vector` tag：

```go
type Article struct {
    Id        int64     `db:"id,pk,autoincrement"`
    Title     string    `db:"title"`
    Embedding []float32 `db:"embedding,vector"` // ← 向量列
}
```

### 2.2 序列化：零依赖、统一文本格式

框架在 `Insert / Update` 时通过 `serializeVector` 把切片统一序列化成 **`[0.12,-0.34,0.56,0.78]` 文本**并参数化绑定：

- **Postgres（pgvector）**：直接绑定 `[..]` 文本，由 `vector` 列类型解析；
- **MySQL 9+**：框架自动用 `STRING_TO_VECTOR(?)` 包裹绑定值；
- 全程**不引入 `pgvector-go` 之类的第三方包**——你只需装数据库驱动。

### 2.3 数据库里的列类型

| 数据库 | 列类型声明 | 备注 |
|---|---|---|
| Postgres + pgvector | `embedding vector(1536)` | 维度 = embedding 模型输出维度；需 `CREATE EXTENSION vector;` |
| MySQL 9+ | `embedding VECTOR(1536)` | 9.0+ 原生支持 |
| SQLite | 无原生向量类型 | 仅能离线拼 SQL，真实检索请用 PG / MySQL |

> ⚠️ **维度必须一致**：模型输出多少维，列就建多少维，入库的向量也必须是同一维度，**否则写入 / 检索会硬报错**（两库都不会静默截断）：
> - Postgres(pgvector)：`ERROR: expected N dimensions, not M`
> - MySQL 9+：`ERROR 3535 (HY000): Vector dimension mismatch: expected N, got M`（文案随版本略有差异）
>
> 列维度在建表时固定、不可直接 `ALTER` 成别的固定值；换模型导致维度变了要新建列/新表并重灌数据。详见 `examples/vector-search` 的 `pg`/`mysql` 示例与 README。

---

## 3. 四种距离度量与适用场景（逻辑 / 数学直觉）

`NearestBy(col, vec, k, metric)` 里 `metric` 决定 `ORDER BY` 用的运算符（PG）或函数（MySQL）。**距离越小越相似**（InnerProduct 见下方说明）。

| 度量 | 常量 | 直觉 | PG 运算符 | MySQL 函数 | 什么时候用 |
|---|---|---|---|---|---|
| 欧几里得 | `orm.L2`（默认） | 直线几何距离，`0` = 完全相同 | `<->` | `EUCLIDEAN` | 图像 / 音频等**未归一化**的特征 |
| 余弦 | `orm.Cosine` | 夹角距离 `1 - cosθ`，只看方向不看长度 | `<=>` | `COSINE` | **文本语义相似度首选**（RAG 默认） |
| 内积 | `orm.InnerProduct` | 点积，向量归一化后与 cosine 单调等价 | `<#>`（负内积） | `DOT` | 向量已归一化时最快 |
| 曼哈顿 | `orm.L1` | 各维绝对值之差求和 | `<+>` | `MANHATTAN`（9.7+） | 稀疏向量 |

几个关键点：

- **Cosine**：范围 `[0, 2]`，`0` = 方向完全一致（最相似），`2` = 完全相反。文本 embedding 通常已归一化，cosine 最稳，是语义检索的默认选择。
- **InnerProduct（内积）**：相似度 = 点积越大越好。pgvector 的 `<#>` 返回**负内积**，所以 `ORDER BY col <#> $1 ASC` 自然把点积最大的排最前——API 层面仍然是"`ORDER BY 距离 ASC LIMIT k`"，无需你关心符号。
- **L2 / L1**：范围 `[0, +∞)`，`0` = 完全相同。
- `Nearest / WithinDistance` 不指定度量时沿用默认 `L2`，兼容旧版 `<->` 行为。

---

## 4. 效果演示（一套 API 跨库）

### 4.1 各方言生成的 SQL（来自 `examples/vector-search`）

```go
embCol := orm.Col[Article](func(a *Article) *[]float32 { return &a.Embedding })
queryVec := []float32{0.12, -0.34, 0.56, 0.78}

// 余弦近邻 top-5
orm.NewQuery[Article]().WithDialect(orm.PG).
    NearestBy(embCol, queryVec, 5, orm.Cosine).Build()
// → SELECT * FROM "articles" ORDER BY "embedding" <=> $1 LIMIT 5

orm.NewQuery[Article]().WithDialect(orm.MySQL).
    NearestBy(embCol, queryVec, 5, orm.Cosine).Build()
// → SELECT * FROM `articles`
//   ORDER BY VECTOR_DISTANCE(`embedding`, STRING_TO_VECTOR(?), 'COSINE') LIMIT 5
```

距离阈值 + 排序（只召回距离 `< 0.3` 的，再按距离升序）：

```go
orm.NewQuery[Article]().WithDialect(orm.PG).
    NearestBy(embCol, queryVec, 20, orm.Cosine).
    WithinDistance(embCol, queryVec, 0.3).Build()
// → SELECT * FROM "articles"
//   WHERE "embedding" <=> $1 < 0.3
//   ORDER BY "embedding" <=> $1 LIMIT 20
```

### 4.2 真实检索流程（Postgres 为例）

```go
db, _ := orm.Open(orm.Config{Driver: "pgx",
    DSN: "postgres://user:pass@localhost:5432/demo?sslmode=disable"})

// 写：Embedding 是 []float32，框架自动序列化为 '[..]'
_ = orm.Insert(ctx, db, &Article{Title: "Go 与向量检索",
    Embedding: []float32{0.12, -0.34, 0.56, 0.78}})

// 读：语义最相近的 5 篇（余弦距离升序）
hits, _ := orm.SelectList(ctx, db,
    orm.NewQuery[Article]().NearestBy(embCol, queryVec, 5, orm.Cosine))
for _, h := range hits {
    fmt.Printf("#%d %s\n", h.Id, h.Title) // 离 queryVec 最近的文档排最前
}
```

换 MySQL 9+ 只需改 `Driver` / `DSN` 和建表 DDL（`VECTOR(4)`），**检索代码一行不变**——这就是"统一 API"的价值。

### 4.3 百万级性能：上向量索引

近邻检索要快，必须建向量索引，否则是全表扫描：

```sql
-- Postgres：按你用的度量建对应 ops 的 HNSW 索引
CREATE INDEX ON articles USING hnsw (embedding vector_cosine_ops);  -- cosine
-- CREATE INDEX ON articles USING hnsw (embedding vector_l2_ops);    -- l2

-- MySQL 9+
CREATE VECTOR INDEX idx_articles_embedding ON articles(embedding);
```

框架生成的 `ORDER BY 距离 ASC LIMIT k` 能直接命中这些索引。

---

## 5. 接入 AI 向量模型（简单布置）

向量本身不神秘——难点只在"怎么把文本变成 `[]float32`"。下面给出**零额外依赖**的接入方式（只用到标准库 `net/http` + `encoding/json`），支持任意 OpenAI 兼容接口，也支持本地 Ollama。

### 5.1 选一个 embedding 模型

| 模型 | 维度 | 获取方式 |
|---|---|---|
| **阿里千问 `text-embedding-v3`** ✅ 国内可用 | **1024（默认）**，也支持 768/512/256/128/64 | 百炼云端 API（需 key），兼容 OpenAI 接口，见 `qwen/main.go` |
| 阿里千问 `text-embedding-v4`（Qwen3-Embedding） | 2048/1536/**1024(默认)**/768/512/256/128/64 | 同上 |
| OpenAI `text-embedding-3-small` | 1536 | 云端 API（需 key，国内网络不稳） |
| OpenAI `text-embedding-3-large` | 3072 | 云端 API |
| `nomic-embed-text` / `bge-m3` 等 | 各异 | 本地 Ollama，零成本、可离线 |

> 🇨🇳 **国内优先用千问**：访问 `api.openai.com` 在国内网络不稳定且需境外支付；千问 `text-embedding-v3` 默认 1024 维、兼容 OpenAI `/v1/embeddings` 接口，零额外依赖接入（见 `examples/vector-search/qwen/main.go`）。
> ⚠️ **迁移坑**：从 OpenAI（1536 维）切到千问（默认 1024 维）时，务必把列维度（`vector(N)`/`VECTOR(N)` 的 N）改成 1024，否则立刻触发上面的维度不匹配报错。

### 5.2 把文本变成 `[]float32`（OpenAI 兼容）

```go
import (
    "bytes", "context", "encoding/json", "io", "net/http"
)

// embed 调用任意 OpenAI 兼容的 /v1/embeddings 接口，返回 []float32。
func embed(text, baseURL, apiKey, model string) ([]float32, error) {
    body, _ := json.Marshal(map[string]any{
        "input": text,
        "model": model,
    })
    req, _ := http.NewRequestWithContext(context.Background(),
        http.MethodPost, baseURL+"/embeddings", bytes.NewReader(body))
    req.Header.Set("Content-Type", "application/json")
    if apiKey != "" { // 本地 Ollama 可不填
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
    for i, x := range v { f[i] = float32(x) } // float64 → float32
    return f, nil
}
```

### 5.3 本地 Ollama（零成本、可离线）

Ollama 暴露同样的 shape，只是 endpoint 与字段略不同：

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

### 5.4 端到端：建库 → 入库 → 检索

```go
// 1) 建表（一次性，维度与模型一致）
//    Postgres: CREATE EXTENSION IF NOT EXISTS vector;
//              CREATE TABLE articles (id BIGSERIAL PRIMARY KEY, title TEXT,
//                                     embedding vector(1536));
//    MySQL 9+: CREATE TABLE articles (id BIGINT AUTO_INCREMENT PRIMARY KEY,
//              title VARCHAR(255), embedding VECTOR(1536));

// 2) 把语料 embedding 后入库（生产请用 BatchInsert + 批量 embed）
corpus := []string{"Go 的并发模型", "向量检索原理", "如何做 RAG"}
for _, doc := range corpus {
    vec, _ := embed(doc, "https://api.openai.com/v1", os.Getenv("OPENAI_API_KEY"),
        "text-embedding-3-small")
    _ = orm.Insert(ctx, db, &Article{Title: doc, Embedding: vec})
}

// 3) 用户提问 → embedding → 近邻检索（RAG 取 top-k 喂给 LLM）
question := "Go 怎么处理高并发"
qVec, _ := embed(question, "https://api.openai.com/v1", os.Getenv("OPENAI_API_KEY"),
    "text-embedding-3-small")

embCol := orm.Col[Article](func(a *Article) *[]float32 { return &a.Embedding })
hits, _ := orm.SelectList(ctx, db,
    orm.NewQuery[Article]().NearestBy(embCol, qVec, 3, orm.Cosine))
// hits 即语义最相关的 3 段，直接作为大模型上下文
```

要点：
- **维度一定要对齐**：建表维度、模型输出维度、入库向量维度三者一致。
- **批量**：真实语料大时，批量调用 embedding 接口 + `BatchInsert`，别一条条嵌。
- **度量匹配训练方式**：绝大多数文本 embedding 用 **Cosine**；不确定就用 Cosine，最稳。

---

## 6. 限制与注意

- **SQLite 无原生向量类型**：框架仅能离线拼出 SQL，真实近邻检索请用 Postgres / MySQL。
- **维度一致性**：见上文，维度不匹配会写入 / 检索**硬报错**（PG `ERROR: expected N dimensions, not M`；MySQL `Vector dimension mismatch: expected N, got M`），不会静默截断。建表维度 = 模型输出维度 = 入库向量维度，三者一致。
- **度量选择**：用错度量（如对归一化向量用 L2 而非 Cosine）会劣化召回质量；语义检索默认 Cosine。
- **阈值 `WithinDistance` 的量纲**：Cosine 距离在 `[0,2]`，L2/L1 在 `[0,+∞)`，设阈值前先了解所选度量的取值范围。
- **索引**：百万级以上务必建 HNSW / VECTOR 索引（见 4.3），否则退化为全表扫描。

---

## 7. 相关链接

- 速览与 API 全集：[README.md](README.md)
- 可运行示例（离线看两套方言 SQL）：[examples/vector-search](examples/vector-search)
- 距离度量常量：`orm.L2` / `orm.Cosine` / `orm.InnerProduct` / `orm.L1`
- 核心 API：`Nearest` / `NearestBy` / `WithinDistance` / `WithinDistanceBy` / `WithVectorMetric`
