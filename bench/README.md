# bench —— 横向性能对照基准

用**同一张表、同一份种子数据、语义相同的 SQL**，对比三个层次在 SQLite 内存库上的开销。

```
raw      database/sql 手写 SQL —— 下界，告诉你「ORM 之外」花了多少
gorm     GORM v1.25.12 —— 生态里最常被拿来对照的参照物
gobreath gobreath-orm（本仓库，经 replace 指向工作区代码）
```

跑起来：

```bash
cd bench
go test -run "^$" -bench . -benchmem -benchtime=2s -count=3
```

`-count=3` 不是为了「取最好的一次」，而是为了看见抖动 —— 本机 `ns/op`
单次抖动可达 ±40%（见「结果」开头的说明），只跑一次很容易把噪声当成结论。

只跑语义一致性校验（快，适合放进 CI）：

```bash
cd bench
go test ./... -count=1
```

## 为什么这么设计

基准最容易出的问题不是「数错」，而是「比的不是同一件事」。为了堵住这个口子：

| 措施 | 原因 |
|---|---|
| 手动建表，不用 `AutoMigrate` | 两个 ORM 产出的 DDL（列类型、索引命名）并不相同，各自建表就等于在不同的表结构上比 |
| 固定随机种子 | 三方必须拿到逐字节相同的 10000 行 |
| 三方共用一个 SQLite 驱动实现 | 差异只能来自 ORM 层，而不是驱动（见 `harness_test.go` 里的注释） |
| 关闭 GORM 的 `autoCreateTime` / `autoUpdateTime` | gobreath-orm 目前没有自动填充，开着就等于一方写值、另一方不写 |
| 关闭 GORM 的 `SkipDefaultTransaction` 默认值 | 否则比的是「GORM 带事务 vs 别人不带事务」 |
| 关闭所有 ORM 的 SQL 日志 | 日志是 I/O，会淹没被测代码的开销 |
| `TestResultParity` 断言三方返回完全相同的数据 | 语义一旦漂移，先炸测试，而不是让基准悄悄地在比不同的活 |
| raw 层用 `strings.Builder` 拼语句 | `+=` 对多行 VALUES 是 O(n²)，会给下界一个虚假的高分配水位 |

另外，`TestSQLParity` 会把三层实际执行的 SQL 打出来供人工复核：

```bash
cd bench && go test -run TestSQLParity -v
```

它抓的是 gobreath **真正下发到驱动**的语句（走日志钩子，而不是 DryRun —— DryRun 够不着 `SelectById` / `Insert` / `UpdateById` 这些固定模板路径）。

## 结果

环境：Windows / amd64，Intel i5-1135G7，Go 1.24.0，`-benchtime=2s`。

> 这不是「谁更快」的排行榜。下面每个数字都包含 SQLite 驱动自身的开销，
> 而且全部落在**内存库 + 无网络**的乐观条件下 —— 真实环境下网络往返会主导一切。
> 读数方式：`raw` 是同一件事的成本下界，ORM 与它的**差额**才是 ORM 层的价钱。

**先看怎么读这张表**（重要，否则会得出错误结论）：

| 指标 | 可信度 | 原因 |
|---|---|---|
| `B/op`、`allocs/op` | **可复现**，逐字节确定 | 与调度、GC 时机、机器负载无关，同一份代码永远同一个数 |
| `ns/op` | **只当量级参考** | 单次 op 是 3ms 量级（SQLite 执行计划主导），本机同一份代码同一轮内抖动可达 ±40% |

`ns/op` 抖动有多离谱，用同一份代码的实测说明：`ListPage/gobreath` 在一次
`-count=3` 运行里是 3.19 / 4.22 / 5.02 ms，另一次 `-count=7` 运行里是
2.92 ~ 3.38 ms —— 两次运行的**中位数差了 30%**，而代码一字未改。
所以下面凡涉及「谁比谁快多少」的判断，都以 `B/op` 与 `allocs/op` 为准；
`ns/op` 只有当差距超过 50% 时才当回事。
（想拿可复现的 ORM 层延迟，用根模块的 `go test -bench BenchmarkScanList -benchmem`，
它走 mock driver、单次 op 只有微秒量级，见文末。）

采样方式：每个场景 `-count=3` 取中位数；`ListPage` 是被判定项，样本加倍到 `-count=7`。

### 单行操作

| 场景 | 层次 | ns/op | B/op | allocs/op |
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

- 读：gobreath 分配是裸 SQL 的 1.36 倍、GORM 的 33%。
- 写：`InsertOne` 比 GORM 快 **3 倍**（分配少 71%），`UpdateByID` 快 1.3 倍。

### 多行读

| 场景 | 层次 | ns/op | B/op | allocs/op |
|---|---|---|---|---|
| **ListPage**（50 行） | raw | 3,782,821 | 51,696 | 1,839 |
| | gorm | 3,338,564 | 77,692 | 2,353 |
| | **gobreath** | **3,244,296** | **63,797** | **1,906** |
| **Count** | raw | 1,995,453 | 432 | 16 |
| | gorm | 1,854,373 | 4,384 | 61 |
| | **gobreath** | **1,246,456** | **1,779** | **48** |

- `Count`：分配 1,779 B —— 比 GORM 少 59%（GORM 的 4,384 B 来自它构造 `Model`/`Statement` 的固定开销）。
- `ListPage`：**三层的 ns/op 中位数已经落在同一区间**（3.24 / 3.34 / 3.78 ms），
  差值远小于上面说的单次抖动，不能据此排序。可复现的差别在分配上：
  gobreath 63.8 KB vs raw 51.7 KB（1.23 倍）、GORM 77.7 KB。
  50 行的行映射开销已经从「比裸 SQL 多 1.4 倍分配」压到「多 23%」——
  明细与改法见下一节。

### 行映射：从 `ListPage` 落后 26% 到三层持平

`bench/` 用真库测的是**端到端**，SQLite 执行计划会把 ORM 层的差异淹掉。
要看 ORM 层本身，用根模块里的 mock driver 基准（不碰任何数据库引擎）：

```bash
go test -run "^$" -bench BenchmarkScanList -benchmem   # 根模块
```

| 项目 | 优化前 | 优化后 | |
|---|---|---|---|
| `SelectList`（50 行 × 9 列） | 96,341 ns / 87,267 B / 498 allocs | **23,864 ns / 27,573 B / 101 allocs** | ns −75%、B −68%、allocs −80% |
| 其中「行映射 + SQL 构造」增量 | 93,326 ns（1,867 ns/行） | **20,843 ns（417 ns/行）** | **−78%** |
| `SelectById` | 8,123 ns / 2,281 B / 32 allocs | **6,244 ns / 1,433 B / 27 allocs** | ns −23%、B −37% |
| `SelectList`（含 JSON 列） | 201,778 ns / 43,130 B / 824 allocs | **157,789 ns / 38,487 B / 727 allocs** | 见下 |

同一个表里 `SelectById` 的 B/op 在真库基准里是 3,164 → 2,315，与这里同向。

改动的实质：**把「列序 → 字段」的解析从「每行一次」改成「每查询一次」**，
按结果集列签名缓存到模型元数据上（`modelMeta.scanPlanFor`）。
优化前每行都要 `rows.Columns()`、建两个 map（其中 `jsonCols` 即使没有 JSON 列也照建）、
分配两个 `[]any` 缓冲；这些开销只与**列集合**有关，与行无关。

剩下最大的一块是 **JSON 列**：3,095 ns/行、14.5 allocs/行，几乎全在
`json.Unmarshal` 到 `map[string]any` 上（map 与其中每个字符串都要分配）。
这一项没有便宜的做法，且 GORM 侧同样付出这个成本，所以留着。

### BatchInsert 的分批大小

每轮固定插入 500 行，只改分批大小。单位 **µs/行**（总耗时 ÷ 500）。
三个层次各 `-count=3` 取中位数：

| chunk | raw | gorm | gobreath |
|---|---|---|---|
| 1 | 21.8 | 40.3 | 24.3 |
| **10** | **11.2** | **16.9** | **12.8** |
| 50 | 12.7 | 15.7 | 13.1 |
| 100 | 14.6 | 18.4 | 15.2 |
| 250 | 26.8 | 32.9 | 27.1 |
| 500 | 39.1 | 48.2 | **37.2** |

结论：

1. **拐点在 chunk ≈ 10**，不是越大越好。SQLite 下超过 50 之后单语句变大，解析成本反超往返收益，500 行一批比 10 行一批慢 3 倍多。
2. gobreath 全区间都贴着裸 SQL 走（chunk=10 时 12.8 vs 11.2 µs/行，+14%），chunk=500 时 37.2 µs/行反而略优于裸 SQL 的 39.1 —— 那两个数在噪声内，只能说明「没有额外开销」。
3. **GORM 的 `CreateInBatches` 在 chunk=1 时 40.3 µs/行**，比 gobreath 慢 66%；chunk 加大后差距收敛到同一水平。

### 这些数字**不能**用来主张什么

- ❌ 「gobreath 比 GORM 快 N 倍」—— 只在 SQLite 内存库 + 这几个场景下成立。
  单行写路径的差距主要来自 GORM 的 hook / callback 链路，换成 PG 或 MySQL 后比例会变。
- ❌ **用这里的 ns/op 做任何小于 50% 的排序**。同一份代码两次运行的中位数能差 30%
  （见开头），`ListPage` 三层差值 6% 这种量级完全落在抖动里。
  要比出可信差异，需要 `-count=10` 以上 + `benchstat`，或者干脆用根模块的 mock 基准。
- ❌ 「批量插入最优是 10」—— 这是 SQLite 的结论。SQLite 无网络往返，进程内调用的代价极低，
  所以「减少往返次数」的收益本就不大；PG / MySQL 上 chunk=1 会惨得多，最优点大概率更大。
  要拿这组数据去定 `WithChunkSize` 的默认值，先在三方言真库上各跑一遍。

## 改动这里时注意

- **`TestResultParity` 是基准的地基**。改了 `harness_test.go` 里任何一层的 SQL，它必须先过。
- 新增场景时，`rawListPageSQL` 之类的常量与 GORM / gobreath 的写法要保持语义一致，
  并在 `TestSQLParity` 里把三者都打出来。
- `updatePayload` 必须带上目标行**真实的 `created_at`**：gobreath 的 `UpdateById` 会写入
  全部可写列（含 `created_at`），留零值的话每轮迭代都会把该行时间戳抹成 `0001-01-01`。
  raw / GORM 两侧的 SET 列表里没有 `created_at`，所以只有 gobreath 会踩——也正因如此，
  payload 要按最严格的那一方准备。
- 基准里一律用 `newOrmHarness`（Silent）。`newOrmHarnessWith` 只给 `TestSQLParity` 抓 SQL 用。
- 判定「行映射变快/变慢」不要用这里的 `ns/op`（理由见开头）。用根模块的
  `go test -run "^$" -bench BenchmarkScanList -benchmem`：它走 mock driver、
  单次 op 是微秒量级，`B/op` 与 `allocs/op` 逐字节可复现。
  改动行映射（`model.go` 的 `setterFor` / `scanPlanFor`）后，
  同时要过 `TestScanPlanColumnSets`（不同列集合各自映射正确）与
  `TestScanPlanCacheCap`（越过计划缓存上限后仍正确）。

## 已知偏差 / 未覆盖

- 只跑 SQLite。PG / MySQL 需要把 `harness_test.go` 里的三个 backend 抽象出来，
  目前刻意没做——先把 SQLite 上的差异看清楚，再决定要不要扩到三方言。
- GORM 侧用的是「关掉自动时间填充 + 关掉默认事务」的公平配置，不是它的默认行为。
  想看默认配置的影响，把 `newGormHarness` 里那两行注释掉的设置恢复即可。
- 未覆盖 JOIN、Preload 关联、事务内批量、向量检索 —— 这些都是 gobreath 有而场景里没测的部分。
- **`ns/op` 在本机不可复现**（±40%，见开头）。表中的绝对值仅供量级参考，
  想要可比的延迟数字请用根模块的 mock 基准，或换一台无后台负载的机器 `-count=10` + benchstat。
