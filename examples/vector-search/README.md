# 向量检索示例（examples/vector-search）

gobreath-orm 的向量检索「一套 API、跨 Postgres / MySQL」可运行示例。所有文件离线即可
`go run .` 看 SQL；想连真实库，取消文件末尾「真实可跑版」注释块、填 DSN、`go mod tidy` 拉驱动。

## 目录

| 文件 | 内容 |
|---|---|
| `main.go`（本目录） | 离线看 Postgres / MySQL 两套方言生成的向量 SQL（快速预览） |
| `pg/main.go` | Postgres + pgvector 实例：方言 SQL + 坑与维度报错 |
| `mysql/main.go` | MySQL 9 VECTOR 实例：与 PG 差异 + 坑与维度报错 |
| `qwen/main.go` | 阿里千问（百炼）向量模型接入，**国内可用**，替代 OpenAI |
| `en/main.go` | 英文注释版：统一 API 跨库 + 千问接入 + 坑清单 |

## 怎么跑

```bash
cd examples/vector-search
go run .                 # 根目录：打印 PG/MySQL 两套方言 SQL
go run ./pg              # Postgres 实例 + 维度报错演示
go run ./mysql           # MySQL 实例 + 与 PG 差异 + 坑
go run ./qwen            # 千问接入（设 DASHSCOPE_API_KEY 后真实调用）
go run ./en              # 英文注释版
```

连真实库（以 Postgres 为例，MySQL 仅改 Driver/DSN/DDL）：

```bash
# 1) 取消 pg/main.go 末尾「真实可跑版」注释块，填 DSN
# 2) 拉驱动
go mod tidy
# 3) 运行
go run ./pg
```

## Postgres vs MySQL 差异速查

| 维度 | Postgres + pgvector | MySQL 9+ |
|---|---|---|
| 列类型 | `embedding vector(N)` | `embedding VECTOR(N)` |
| 前置 | 需 `CREATE EXTENSION vector;`（按库启用） | 内核自带，无需扩展 |
| 写入绑定 | 直接绑 `[..]` 文本 | 框架自动 `STRING_TO_VECTOR(?)` |
| 近邻运算符/函数 | `<->` `<=>` `<#>` `<+>` | `VECTOR_DISTANCE(col, STRING_TO_VECTOR(?), 'COSINE'\|...)` |
| 向量索引 | `CREATE INDEX ... USING hnsw (embedding vector_cosine_ops)` | `CREATE VECTOR INDEX idx ON articles(embedding)` |
| L1 曼哈顿 | 支持 | 需 9.7+（MANHATTAN）；9.0–9.6 报函数不存在 |
| 列当 key | 视场景可用 | ❌ 不能当 PK / 唯一 / 外键 / 分区键 |
| VECTOR 缺省维度 | `vector`（无 N）可存任意维，但建索引需指定 | `VECTOR` 缺省 **2048**，范围 1–16383 |
| 维度不一致报错 | `ERROR: expected N dimensions, not M` | `ERROR 3535 (HY000): Vector dimension mismatch: expected N, got M`（文案随版本略有差异） |

## 最容易踩的坑（务必看）

1. **维度不一致 = 硬报错（两库都 100% 失败，不会静默截断）**。
   建表维度、模型输出维度、入库向量维度三者必须一致。换 embedding 模型（维度变了）
   要新建列/新表并重灌数据；列维度不可直接 `ALTER` 成别的固定值。
2. **迁移维度坑（国内常见）**：OpenAI `text-embedding-3-small` = 1536 维；
   阿里千问 `text-embedding-v3` 默认 **1024** 维。从 OpenAI 切千问而不改列，
   立刻触发上面的维度不匹配报错。→ 列维度跟着模型走（见 `qwen/main.go`）。
3. **MySQL `VECTOR` 列不能当主键/唯一键**——`Article` 的 `id` 必须单独普通列。
4. **MySQL `VECTOR(N)` 省略 N 默认 2048**，别当「任意维」；显式写死并对齐模型。
5. **度量要匹配训练方式**：文本语义默认 **Cosine**；用错度量（归一化向量用 L2）会
   劣化召回。阈值量纲：Cosine∈[0,2]，L2/L1∈[0,+∞)。
6. **必须建向量索引**，否则百万级退化为全表扫描。

## 接入 AI 向量模型

- **国内可用**：阿里千问 `text-embedding-v3` / `text-embedding-v4`（百炼），
  兼容 OpenAI `/v1/embeddings`，见 `qwen/main.go`，零额外依赖。
- 也可接任意 OpenAI 兼容接口或本地 Ollama，见根目录 `VECTOR.md` 第 5 节。
- 要点：**维度对齐**、批量 embed + `BatchInsert`、默认 Cosine。

完整「向量存法 / 效果 / 四种度量」说明见根目录 [`VECTOR.md`](../VECTOR.md)。
