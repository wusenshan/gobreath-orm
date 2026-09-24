# 集成测试（真实数据库）

用真库验证 gobreath-orm。**这些用例不在根模块里**，`go test ./...` 不会跑到它们 ——
原因见下。

```powershell
cd integration
$env:ORM_IT_PG_DSN    = "postgres://gobreath:gobreath@127.0.0.1:5433/gobreath?sslmode=disable"
$env:ORM_IT_MYSQL_DSN = "root:gobreath@tcp(127.0.0.1:3307)/gobreath?parseTime=true&loc=Local"
go test -v ./...
```

不设环境变量也能跑：SQLite 恒可用（modernc 纯 Go 驱动 + 内存库，零安装）。

```powershell
go test ./...                 # 只跑 SQLite
```

## 起数据库

```bash
docker compose up -d          # pgvector:5433 + mysql:3307
docker compose down -v        # 清理
```

端口选 5433 / 3307 是为了避开本机已装的 PostgreSQL（5432）。用 `pgvector/pgvector`
而不是 `postgres` 是因为向量相关用例需要 `vector` 扩展，`initdb/01-vector.sql` 会自动创建。

**关于 MySQL 版本**：compose 钉的是 `mysql:8.0`，而 8.0 **没有** `VECTOR` 类型，于是
向量用例在 MySQL 上会连「存储级」都跳过（详见下文「向量用例按能力分级」）。想让 MySQL 的
存储级真正跑起来，换成 **`mysql:9`**（≥ 9.0 有 `VECTOR(N)` 与 `STRING_TO_VECTOR`）即可 ——
实测 9.7.2 社区版能建表、能存取，但**没有** `VECTOR_DISTANCE` 函数，所以距离级仍然跳过。
换版本这件事会影响其它 MySQL 用例的方言行为，是否上 CI 请自行判断；本地单跑可以直接
`docker run -p 3308:3306 mysql:9` 再把 `ORM_IT_MYSQL_DSN` 指过去。

## PG 的两条驱动路径都会真跑

README 把 `github.com/lib/pq`（驱动名 `postgres`）与 `github.com/jackc/pgx/v5/stdlib`
（驱动名 `pgx`）并列为可选 PG 驱动，**主示例给的还是前者** —— 所以两条都得验。
只需提供一个 `ORM_IT_PG_DSN`，harness 会在同一个 DSN 上铺开两条后端：
`postgres`（pgx）与 `postgres-libpq`（lib/pq）。两者顺序执行、各自 `resetSchema`，互不干扰。

**它们交给 `database/sql` 的类型并不一样**（同一台库上 `Scan(&any)` 取 `%T` 实测）：

| 服务端列类型 | pgx | lib/pq |
|---|---|---|
| `numeric` | `string` | `[]byte` |
| `uuid` | `string` | `[]byte` |
| `text[]` | `string` | `[]byte` |
| `vector`（pgvector） | `string` | `[]byte` |
| `jsonb` / `bytea` | `[]byte` | `[]byte` |
| `text` / `varchar` | `string` | `string` |
| `int2/4/8`、`float4/8`、`bool` | `int64` / `float64` / `bool` | 同左 |
| `timestamptz` | `time.Time`（会话时区） | `time.Time`（归一到 UTC） |

「同为 database/sql」不等于「形态相同」。框架**自己**分派类型的只有模型字段扫描
（`assignString` / `assignFloat` ...），它的 `case []byte` 分支**只在 lib/pq 下会被走到**；
而 `RawQuery` / `RawOne` 的标量路径与聚合都直接 `rows.Scan`，由标准库 `convertAssign` 兜住。
两条路径各有一条用例守着（`TestModelScanAcrossDrivers` / `TestCrossDriverValueParity`），
前者手工建 `NUMERIC` / `UUID` / `TEXT[]` 列，故意制造「服务端类型 ≠ Go 字段类型」——
`AutoMigrate` 按 Go 类型反推 DDL，建不出这些列，也就造不出这个前提。

## CI

`.github/workflows/ci.yml` 里有一个独立的 `integration` job（ubuntu，service 容器起
pgvector + mysql，端口与本地一致）。它**不依赖 `initdb/` 挂载** —— service 容器启动时
工作区还不存在，所以 `pgvector` 扩展改由测试自身 `CREATE EXTENSION IF NOT EXISTS vector`
建立（见 `harness_test.go` 的 `ensureVectorExtension`），这样「DSN 指向一个 pgvector 容器」
就足够跑完整套用例。

数据库没就绪时不设 DSN 即可：SQLite 恒可用，套件会照常跑完 SQLite 那 24 个用例。

## 为什么单独一个模块

主模块（仓库根的 `go.mod`）是**零依赖**的，驱动必须由使用者自行导入。真库测试需要
pgx / mysql / sqlite 驱动，放进主模块会把它们写进 `go.sum`，破坏这个卖点，CI 也会被
迫下载一堆无关包。这里沿用 `examples/` 已经确立的嵌套模块约定（`replace` 指回 `..`）。

另外 mysql 驱动钉在 **`v1.9.3`**，上下都有理由：

- **不能低于 `v1.9.0`**：MySQL 9.0 为 VECTOR 引入了新字段类型码 242（`MYSQL_TYPE_VECTOR`），
  v1.8.x 不认识它，读向量列时**驱动层**直接报 `unknown field type 242` —— 实测 MySQL 9.7.2
  上，用 v1.8.1 连「把向量写进去再读回来」都做不到（v1.9.0 changelog:
  "Add support for VECTOR type introduced in MySQL 9.0. (#1609)"）。
- **不升到 `v1.10.0`**：它把 `go.mod` 抬到 `go 1.24.0`，会连带把本模块的最低 Go 版本顶上去，
  与仓库支持 1.23 的承诺冲突；`v1.9.3` 的 go 指令仍是 1.21+。

## mock 测试验不到什么

仓库既有的 mock 执行器只断言「生成了什么 SQL 字符串」。以下四类问题**只有真库能暴露**，
本套件就是为它们写的：

1. **驱动返回值与扫描目标的类型匹配** —— PG 的 `SUM(bigint)` 是 `numeric`，
   经 `database/sql` 回来说不定是 `[]byte`、`string` 还是 `int64`，跟 mock 里想象的无关。
2. **方言语法是否真的合法** —— 生成的 SQL 在 PG 能跑，在 SQLite / MySQL 未必（见下）。
3. **主键回填的两条完全不同的代码路径** —— PG 不支持 `LastInsertId`，必须走
   `INSERT ... RETURNING`；MySQL / SQLite 走 `LastInsertId`。
4. **事务、锁、约束的真实行为** —— 回滚、`FOR UPDATE`、乐观锁冲突。

## 用例覆盖

| 测试 | 验什么 |
|---|---|
| `TestInsertPKWritebackAndRoundtrip` | 两条主键回填路径 + 八类列写入/读出往返（含 JSON、时间） |
| `TestSelectVariants` | `SelectOne` / `Exists` / `IN` / `Distinct` / `LIMIT+OFFSET` |
| `TestPageMetadata` | 分页的 `Total` / `Pages` / `HasNext` / `HasPrev` |
| `TestUpdateVariants` | `UpdateById` / 条件 `Update` / `UpdateByIdSets` |
| `TestSoftDelete` | 默认过滤、`Unscoped` 逃逸、`ForceDelete` 真删 |
| `TestUpsertByUniqueColumn` | 三方言冲突处理（`ON CONFLICT` vs `ON DUPLICATE KEY`）+ 主键回填 |
| `TestUpsertByAutoIncPK` | 自增主键上的 upsert 命中更新而非静默新增（回归） |
| `TestAutoMigrateIndexTag` | `,index` 建索引 + MySQL 无 `IF NOT EXISTS` 时的幂等路径 |
| `TestTransactionRollback` | 回滚不落地、提交生效 |
| `TestOptimisticLock` | 版本自增、陈旧写入被拒（`ErrOptimisticLock`） |
| `TestForUpdate` | 悲观锁子句在真库合法；SQLite 按设计返回空串 |
| `TestPreloadHasManyAndBelongsTo` | `has_many` / `belongs_to` 真的把子表查回来了 |
| `TestAutoMigrateIsIdempotent` | 反复 `AutoMigrate` 不报错也不动数据 |
| `TestTablePrefixCRUD` | 前缀只作用于自动推导的表名，显式 `TableName()` 不加前缀 |
| `TestAggregates` | `Sum/Avg/Max/Min` + 强类型 `SumOf/AvgOf` + 空集归零 + 调用即报错 |
| `TestMaxOfTimeColumn` | 时间列 `MAX`（`MaxOf` 文档点名的用法） |
| `TestPluck` / `TestPluckNullAlignment` | 单列投影：顺序、分页、NULL 补零且**下标不错位** |
| `TestCountIncludesJoin` | Count 漏 JOIN 的回归（`Page` 元信息一并验） |
| `TestJsonQuery` / `TestJsonContains` | JSON 路径比较与片段包含 |
| `TestDryRunIsExecutable` | `DryRun` 的 SQL **直接在真库执行**（不只是字符串对得上） |
| `TestBatchInsertVarLimit` | 批量插入的绑定参数上限（取证，见下） |
| `TestRepoLayerFullWalk` | `Repo[T]` 门面**每一个方法**都真跑（含事务提交 / 回滚） |
| `TestQueryPredicateMatrix` | 每个谓词构造器都真执行，并用行数断言核对语义 |
| `TestJoinVariantsExecutable` | 六种 JOIN 变体 + 别名；`Count` 与 `SelectList` 口径一致 |
| `TestWriteOptionsOnRealDB` | `OnlyColumns` / `OmitZero` 及其组合；非法列必须报错 |
| `TestDbConfigMethodsExecutable` | `DB` 的配置方法逐项验「可观测效果」（日志 / 钩子 / 软删 / 乐观锁 …） |
| `TestVectorPathOnRealDB` | 向量路径按能力分级：存储级（建列 / 写 / 读回）与距离级（排序 / 阈值 / 聚合） |
| `TestCrossDriverValueParity` | `RawOne` 标量路径：两条 PG 驱动读出的值一致（该路径的转换由标准库负责） |
| `TestModelScanAcrossDrivers` | **模型字段路径**：`NUMERIC` / `UUID` / `TEXT[]` 列在 pgx 与 lib/pq 下都落到强类型字段 |

## 向量用例按能力分级

向量能力**不是一个布尔值**，`TestVectorPathOnRealDB` 按两级探测，走到哪级跑哪级：

| 级别 | 内容 | 需要什么 |
|---|---|---|
| **tier 1 存储级** | 建向量列、写入、单行读回、列表读回、`UpdateById` 改向量 | 有 `VECTOR` 类型即可 |
| **tier 2 距离级** | 按距离排序、阈值过滤、聚合 × 向量、`Pluck` + 度量切换 | 还要有距离函数 |

分级的起因是一个教训：早期把「没有距离函数」直接等价于「整条向量路径跳过」，
于是 MySQL 上**能跑的那一半也从没跑过** —— 而读回恰恰就坏在那里（详见下面的第 7 条）。

实测口径（各后端在日志里留下原始证据）：

| 后端 | tier 1 | tier 2 |
|---|---|---|
| PostgreSQL + pgvector | ✅ | ✅ |
| MySQL 9.7.2 社区版 | ✅（`VECTOR(3)` / `STRING_TO_VECTOR`） | ❌ `ERROR 1305: FUNCTION VECTOR_DISTANCE does not exist` |
| MySQL 8.0（compose 默认） | ❌ 连 `VECTOR` 类型都没有 | ❌ |
| SQLite | ❌ 无向量运算符 | ❌ |

用例入口先 `DROP TABLE IF EXISTS` 保证幂等：上一轮若在某个 `t.Fatalf` 中断就没走到收尾，
残留行会让计数断言成倍偏大（实测踩过 `Nearest + Count = 6`）。

## 曾经的缺口：已修复，现为回归测试

下面这些条目都是本套件在真库上跑出来的问题。修完之后 `t.Skipf` 已换成断言，
现在是常规回归测试（对应名字见每个小节）。

它们共同的教训：**mock 只断言「生成了什么 SQL 字符串」，看不出这段 SQL 在真库里是否成立。**

### 1. 自增主键上的 `Upsert` 会静默新增（三方言全中）

对已存在的行做 `Upsert(entity, []string{"id"})`，结果是**多出一行**而不是更新目标行。

根因：`writableCols` 跳过 `autoInc` 列，于是 INSERT 语句里没有 `"id"`，
`ON CONFLICT ("id")` / `ON DUPLICATE KEY` 永不命中，数据库另发一个新 id 后插入。

既有 mock 测试 `TestUpsertPG` / `TestUpsertMySQL` 把这条错误 SQL 断言成了「期望值」，
所以一直没暴露 —— 这正是「mock 只断言 SQL 形状」的典型失效方式。

修法（`crud.go` 的 `upsertConflictCols`）：冲突键是自增主键、且实体上的主键值非零时，
把该列补进 INSERT 的列清单（值为零时仍省略，交给数据库分配）。
批量版本（`upsertBatchConflictCols`）因多行 VALUES 必须列数一致，按整批决策：
全赋了值才补列，只赋了一部分是**报错**而不是猜。

### 2. SQLite 的 `JsonContains` 用了不存在的函数

`sqliteDialect.JsonContains` 曾生成 `json_contains(col, ?)`，而 SQLite 的 JSON1
扩展**没有**这个函数（只有 `json_extract` / `json_each` / `json_tree` 等），执行即报错。

修法：改用 `json_each` 展开成等价的浅层「子集」语义（候选对象的每个键值对都能在列对象里
同名同值同类型地找到），与 PG 的 `@>`、MySQL 的 `JSON_CONTAINS` 对齐。仅浅层，不递归嵌套。

### 3. SQLite 的时间列聚合扫不回来

`MAX(created_at)` / `MIN(created_at)` 的返回值是表达式、**不带列类型信息**，
modernc 驱动只能按 `TEXT` 返回（实测形态是 Go 的 `time.Time.String()`，
如 `2026-09-01 16:00:00 +0000 UTC`），于是 `sql.Null[time.Time]` 扫描失败。

对照：同一字段走 `SelectById` 读原始列是正常的，因为原始列带 `DATETIME` 声明类型。

修法（`aggregate.go` 的 `aggScalarAsTime`）：聚合结果先扫成 `any` 再按多方言时间文本归一。

### 4. MySQL 不支持 `CREATE INDEX IF NOT EXISTS`

`migrate.go` 曾一律生成 `CREATE INDEX IF NOT EXISTS ...`。这在 PostgreSQL / SQLite 合法，
但 MySQL 会报语法错误（`IF NOT EXISTS` 只有 MariaDB 支持）。

修法：`createIndexSQL` 按方言分派 —— MySQL 生成朴素 `CREATE INDEX`，
幂等性由 `AutoMigrate` 忽略「索引已存在」错误（`isDuplicateIndexErr`）兜住；
`TestAutoMigrateIndexTag` 连跑两次来同时验证建索引与幂等。

### 5. `Upsert` 不回填自增主键

`Insert` 会回填、`Upsert` 不会，属于一致性缺口。

修法：三方言各按其可靠路径回填 —— PG / SQLite 走 `INSERT ... ON CONFLICT ... RETURNING`
（`DO UPDATE` 命中时返回的也是目标行），MySQL 在 `ON DUPLICATE KEY UPDATE` 末尾追加
官方的 `` `id` = LAST_INSERT_ID(`id`) `` 技巧（否则走到更新分支时 `LAST_INSERT_ID()` 不指向该行）。
批量 `BatchUpsert` 仍不回填，与 `BatchInsert` 保持一致。

### 6. `Delete` 的实参顺序在位置型方言上错位（逻辑删除静默 no-op）

`Delete` 发出的是 `UPDATE t SET deleted_at = <ph> WHERE col = <ph>`，但实参原先**先按 WHERE
分配、后补 SET**：位置型方言（MySQL / SQLite 的 `?` 按出现次序逐个取用）拿到的是
`[条件值, 删除值]`，WHERE 于是把**删除时间戳**绑给了字符串列，条件匹配 0 行 ——
**不报错、不删除**（真库实测 `visible=4 unscoped=4`，一行都没删掉）。

PG 因为 `$1/$2` 自带序号而完全免疫，所以 PG 集成测试与 mock 断言都看不见它。
**教训：计数类断言（`Count` / 行数）是「参数顺序错位」的结构性盲区**，必须逐位断言
`args[i]` 与占位符的对应关系（`delete_args_test.go` 就是这么写的）。

修法：SET 的实参在 WHERE 之前分配，占位符顺序与实参顺序严格对齐。

### 7. 向量列「写得进去、读不回来」；MySQL 侧还不是文本

写方向的序列化（`serializeVector`）早就有，读方向却只有通用兜底：

- **PG**：`vector` 列经驱动交回的是文本 `"[1,2,3]"`，落到 `[]float32` 字段上报
  `orm: 无法把 string 赋给 []float32 字段`。
- **MySQL**：`VECTOR` 列底层是 BLOB（worklog 原文 `Field_vector : public Field_blob`），
  `SELECT` 回来的是**小端序 float32 裸字节**，根本不是文本 —— 纯文本解析必然失败。

修法：`setterFor` 按 `f.vector` 分派给 `setVectorField`，后者按载荷形态分派
（`[` 开头走文本，否则按二进制解码）。文本形态的证据来自 PG 实测，二进制形态的样例取自
MySQL 官方文档（`STRING_TO_VECTOR("[3.14,2024,18]") = 0xC3F548400000FD4400009041`，
逐字节核对小端 float32）。

**MySQL 侧另有一个驱动级前提**：VECTOR 是 MySQL 9.0 新增的字段类型码 242
（`MYSQL_TYPE_VECTOR`），`go-sql-driver/mysql` 要到 **v1.9.0** 才认识它；v1.8.x 读向量列时
直接报 `unknown field type 242`。本模块的驱动因此从 v1.8.1 升到 v1.9.3（理由见上文
「为什么单独一个模块」）。

**mutation check**：把读路径临时退回纯文本解析后重跑，**只有 MySQL 子用例失败**，报错正是
预期的二进制乱码（`向量文本第 0 个分量 "fff?\xcd\xcc\xcc..." 无法解析`，`ff 66 66 3F`
小端即 `0.9`），PG 照过 —— 证明这条用例确实拦得住该 bug，而不是「碰巧全绿」。

**已知限制（未改）**：「向量列存 NULL」目前无法表达 —— `serializeVector` 对 `[]float32{}`
与 nil 切片**都**产出 `"[]"`，而 PG 的 `vector` 列至少 1 维，写 `"[]"` 会报
`ERROR: vector must have at least 1 dimension (SQLSTATE 22000)`。需要 NULL 向量时，
只能用 `OnlyColumns` 把该列排除在写入之外。

## 保留的方言差异（有意不修）

### MySQL 拒收零值 `time.Time`（`TestZeroTimeWrite/mysql` 保持 SKIP）

未赋值的 `time.Time` 会被 mysql 驱动格式化成 `'0000-00-00 00:00:00'`，
MySQL 默认的严格模式直接报 `Error 1292`。PG（接受公元 1 年）与 SQLite（不校验）无此问题。

不修的理由：把零值时间悄悄改写成 `NULL` 是替调用方改语义，会掩盖「忘了赋值」。
时间列请显式赋值，需要 NULL 语义就用指针字段。

## 其它实测记录

- **`BatchInsert` 不分批，且有实测到的硬上限**（`TestBatchInsertVarLimit` 会把触顶行为打进日志）。
  `BatchInsert` 把全部实体拼成**一条** INSERT，绑定参数随行数线性增长，而各库都有上限。
  以 `User`（**6 个可写列**：8 个字段里 `id` 是自增列、`deleted_at` 是逻辑删除列，都被排除）实测：

  | 方言 | 最后一次成功 | 触顶时报错 |
  |---|---|---|
  | SQLite | 3000 行（18000 参数） | 9000 行（54000 参数）→ `too many SQL variables`（上限 32766） |
  | PostgreSQL | 9000 行（54000 参数） | 20000 行（120000 参数）→ `extended protocol limited to 65535 parameters` |
  | MySQL | 9000 行（54000 参数） | 20000 行（120000 参数）→ `Error 1390: Prepared statement contains too many placeholders` |

  换成 6 列的表，安全行数上限是 **10922（PG/MySQL，65535 占位符）** 与 **5461（SQLite，32766）**。
  单表列数越少、上限越高，所以「多少行要分批」必须按列数算，不能写死行数 ——
  这正是 `orm.WithChunkSize(...)` 该由框架算而不是用户猜的理由。
  属于「测试环境没事（几百行）、上生产才暴露」的坑。
- **JSON 里的数字比较语义不统一**：PG 的 `->>` 出来是 `text`，直接拿数字比会报
  `operator does not exist: text = integer`；SQLite 的 `json_extract` 出来是整数，
  拿字符串比会因为类型不同而不相等。所以 `TestJsonQuery` 刻意用字符串值。
  跨库统一需要方言层做类型转换。
