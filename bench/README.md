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
go test -run "^$" -bench . -benchmem -benchtime=1s
```

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

环境：Windows / amd64，Intel i5-1135G7，Go 1.24.0，`-benchtime=1s`，单次运行。

> 这不是「谁更快」的排行榜。下面每个数字都包含 SQLite 驱动自身的开销，
> 而且全部落在**内存库 + 无网络**的乐观条件下 —— 真实环境下网络往返会主导一切。
> 读数方式：`raw` 是同一件事的成本下界，ORM 与它的**差额**才是 ORM 层的价钱。

### 单行操作

| 场景 | 层次 | ns/op | B/op | allocs/op |
|---|---|---|---|---|
| **SelectByID** | raw | 32,587 | 1,704 | 68 |
| | gorm | 62,893 | 7,066 | 134 |
| | **gobreath** | **39,203** | **3,164** | **87** |
| **InsertOne** | raw | 27,291 | 762 | 31 |
| | gorm | 74,264 | 6,567 | 91 |
| | **gobreath** | **33,725** | **1,885** | **56** |
| **UpdateByID** | raw | 24,615 | 632 | 27 |
| | gorm | 56,639 | 6,704 | 92 |
| | **gobreath** | **43,164** | **2,420** | **82** |

- 读：gobreath 比裸 SQL 慢 20%，分配是 1.9 倍；比 GORM 快 38%，分配少 55%。
- 写：`InsertOne` 比 GORM 快 **2.2 倍**，`UpdateByID` 快 **1.3 倍**，且都已接近裸 SQL。

### 多行读

| 场景 | 层次 | ns/op | B/op | allocs/op |
|---|---|---|---|---|
| **ListPage**（50 行） | raw | 3,056,308 | 51,696 | 1,839 |
| | gorm | 3,274,014 | 77,708 | 2,353 |
| | **gobreath** | **3,846,105** | **123,556** | **2,304** |
| **Count** | raw | 1,341,955 | 432 | 16 |
| | gorm | 1,446,199 | 4,384 | 61 |
| | **gobreath** | **1,289,693** | **1,779** | **48** |

- `Count` 与裸 SQL 基本持平（差 4%，在噪声内），分配多于 raw 但少于 GORM。
- **`ListPage` 是唯一明显落后的读场景**：比裸 SQL 慢 26%，分配是 2.4 倍。
  50 行要逐列映射到 Go 结构体，这里是 `scanStruct` 反射路径的开销最集中的地方 ——
  也就是最值得优化的那一处（见文末）。

### BatchInsert 的分批大小

每轮固定插入 500 行，只改分批大小。单位 **µs/行**（由总耗时除以 500 换算）。

| chunk | raw | gorm | gobreath |
|---|---|---|---|
| 1 | 18.2 | 76.1 | 36.9 |
| **10** | **16.9** | 24.6 | **19.4** |
| 50 | 21.8 | 24.5 | 21.4 |
| 100 | 21.1 | 25.2 | 21.2 |
| 250 | 30.3 | 35.7 | 23.9 |
| 500 | 44.5 | 47.6 | 40.3 |

结论：

1. **拐点在 chunk ≈ 10**，不是越大越好。SQLite 下超过 100 之后单语句变大，解析成本反超往返收益，500 行一批比 10 行一批还慢 2 倍多。
2. gobreath 在 chunk=10 时 19.4 µs/行，比裸 SQL 的 16.9 µs/行多 15% —— 这一档是它的最优工作点。
3. **GORM 的 `CreateInBatches` 在 chunk=1 时是 76.1 µs/行**，比 gobreath 慢 2 倍；chunk 加大后差距收敛到同一水平。

### 这些数字**不能**用来主张什么

- ❌ 「gobreath 比 GORM 快 N 倍」—— 只在 SQLite 内存库 + 这几个场景下成立。
  单行写路径的差距主要来自 GORM 的 hook / callback 链路，换成 PG 或 MySQL 后比例会变。
- ❌ 「批量插入最优是 10」—— 这是 SQLite 的结论。SQLite 无网络往返，进程内调用的代价极低，
  所以「减少往返次数」的收益本就不大；PG / MySQL 上 chunk=1 会惨得多，最优点大概率更大。
  要拿这组数据去定 `WithChunkSize` 的默认值，先在三方言真库上各跑一遍。
- ❌ 绝对 ns/op 的横向对比 —— 单次运行、单机、8 逻辑核。正式引用前请
  `-count=5` 后用 benchstat 比较。

## 改动这里时注意

- **`TestResultParity` 是基准的地基**。改了 `harness_test.go` 里任何一层的 SQL，它必须先过。
- 新增场景时，`rawListPageSQL` 之类的常量与 GORM / gobreath 的写法要保持语义一致，
  并在 `TestSQLParity` 里把三者都打出来。
- `updatePayload` 必须带上目标行**真实的 `created_at`**：gobreath 的 `UpdateById` 会写入
  全部可写列（含 `created_at`），留零值的话每轮迭代都会把该行时间戳抹成 `0001-01-01`。
  raw / GORM 两侧的 SET 列表里没有 `created_at`，所以只有 gobreath 会踩——也正因如此，
  payload 要按最严格的那一方准备。
- 基准里一律用 `newOrmHarness`（Silent）。`newOrmHarnessWith` 只给 `TestSQLParity` 抓 SQL 用。

## 已知偏差 / 未覆盖

- 只跑 SQLite。PG / MySQL 需要把 `harness_test.go` 里的三个 backend 抽象出来，
  目前刻意没做——先把 SQLite 上的差异看清楚，再决定要不要扩到三方言。
- GORM 侧用的是「关掉自动时间填充 + 关掉默认事务」的公平配置，不是它的默认行为。
  想看默认配置的影响，把 `newGormHarness` 里那两行注释掉的设置恢复即可。
- 未覆盖 JOIN、Preload 关联、事务内批量、向量检索 —— 这些都是 gobreath 有而场景里没测的部分。
