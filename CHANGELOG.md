# Changelog

本项目遵循 [Semantic Versioning](https://semver.org/)。

## v0.1.12（未发布）

- **feat: 聚合函数 `Sum` / `Avg` / `Max` / `Min`**：此前只有 `Count`，`Sum/Avg/Max/Min` 全部缺席，用户跟着 README 学完 `GroupBy` 后要算 SUM 只能退回 `RawQuery`。现补齐四个基础聚合，并额外提供强类型版本 `SumOf / AvgOf / MaxOf / MinOf`（元素类型由 `orm.TCol` 推导，PG 的 numeric → `time.Time`/`int64`/`float64` 转换交给 `database/sql`）。聚合列仍只来自结构体 `db` tag，未给「三层防注入」开后门（`aggregate.go` + `aggregate_test.go`）。
- **feat: `Pluck` / `PluckCol` 单列投影**：`SELECT col FROM ...` 直接返回 `[]F`，替代「查全表再循环取值」。`Query` 新增 `TColExpr[T, F]` 与 `orm.TCol`（`Col` 的类型参数 F 被擦除，`TCol` 保留它），因此 `Pluck` 的类型可全推导；因 Go 无法从接口类型的参数推导类型参数，列集来自 ormgen（类型是 `orm.ColExpr`）时走 `PluckCol` 并显式给出元素类型（`column.go` + `aggregate.go`）。
- **feat: `Query.ToSQL()` 与 `orm.DryRun(db, q)`**：不访问数据库即可拿到最终 SQL 与参数（对标 GORM 的 DryRun）。`ToSQL` 只反映查询自身状态；`DryRun` 会补上 db 级方言、表前缀与软删除条件，即「真正会执行的那条语句」。
- **fix: `Count` 遗漏 JOIN（总数与列表口径不一致）**：`Count` 此前自己拼 `FROM`，把 `JOIN` 整个漏掉 —— INNER JOIN 会改变行数，于是 `Count` 算出的总数与 `SelectList` 的实际行数不符，`Page` 的页码/`HasNext` 随之出错（测试环境单表时正常，上生产带联表才暴露）。现统一到 `Query.fromClause`，与聚合共用一处实现；`Exists` 顺带修正（`crud.go` + `query.go` + `aggregate_test.go`）。
- **fix: 聚合 / 单列投影下的向量列**：聚合与 `Pluck` 的投影是确定的，此时再追加向量距离列会让结果集从 1 列变 2 列（`Scan` 报 `expected 1 destination arguments in Scan`），且聚合查询里的「按距离排序」在 PG 下是非法 SQL（`ORDER BY` 表达式的列既不在投影也不在 `GROUP BY`）。现按场景剔除（`query.go` + `aggregate_test.go`）。
- **fix: `Upsert` 在自增主键上静默新增（数据损坏级）**：对已存在行执行 `Upsert(e, []string{"id"})` 会**多出一行**而不是更新目标行 —— `writableCols` 跳过 `autoInc` 列，INSERT 语句里没有 `"id"`，`ON CONFLICT ("id")` / `ON DUPLICATE KEY` 因此永不命中，数据库另发新主键后照常插入，且不报任何错。三方言真库实测全中；既有 mock 测试 `TestUpsertPG` / `TestUpsertMySQL` 恰好把这条错误 SQL 断言成了「期望值」，所以长期未暴露。现补 `upsertConflictCols`：冲突键是自增主键且实体已赋非零值时，把该列补进 INSERT 列清单（值为零时仍省略，交给数据库发号）。批量版 `upsertBatchConflictCols` 受多行 VALUES 列数一致约束，按整批决策 —— 只有部分行赋了主键时**直接报错**，而不是猜（`crud.go` + `layer1_test.go`）。
- **feat: `Upsert` 回填自增主键**：与 `Insert` 对齐 —— PG / SQLite 走 `INSERT ... ON CONFLICT ... RETURNING`（`DO UPDATE` 命中时返回的也是目标行），MySQL 在 `ON DUPLICATE KEY UPDATE` 末尾追加官方技巧 `` `id` = LAST_INSERT_ID(`id`) ``（走到更新分支时裸 `LAST_INSERT_ID()` 并不指向该行）。两个能力以**可选接口**（`upsertReturningDialect` / `upsertPKCapturingDialect`）探测，不扩展 `Dialect` 接口本体，自定义方言无需改动。批量 `BatchUpsert` 仍不回填，与 `BatchInsert` 保持一致（`crud.go` + `dialect.go`）。
- **fix: MySQL 不支持 `CREATE INDEX IF NOT EXISTS`**：AutoMigrate 此前一律生成该子句，MySQL（含 8.x）会直接报 `Error 1064` 语法错误（`IF NOT EXISTS` 只有 MariaDB 有）。现由 `createIndexSQL` 按方言分派：MySQL 生成朴素 `CREATE INDEX`，幂等性改由 `AutoMigrate` 忽略「索引已存在」错误（`isDuplicateIndexErr`，匹配 MySQL 1061 与 PG/SQLite 的 already exists）兜住（`migrate.go` + `migrate_test.go`）。
- **fix: SQLite 的 `JsonContains` 生成不存在的函数**：SQLite 的 JSON1 扩展**没有** `json_contains`，此前生成的 `json_contains(col, ?)` 执行即报错。现用 `json_each` 展开成等价的浅层「子集」语义（候选对象的每个键值对都能在列对象里同名同值同类型地找到），与 PG 的 `@>`、MySQL 的 `JSON_CONTAINS` 对齐（仅浅层，不递归嵌套）（`dialect.go` + `json_test.go`）。
- **fix: SQLite 的时间列聚合扫不回来**：`MAX(col)` / `MIN(col)` 是没有声明类型的表达式，modernc 驱动只能按 TEXT 交回（实测形态是 Go 的 `time.Time.String()`，如 `2026-09-01 16:00:00 +0000 UTC`），`sql.Null[time.Time]` 因此扫描失败；同一字段读原始列却正常（列声明了 `DATETIME`）。现 `MaxOf` / `MinOf` 的时间分支先扫成 `any` 再按多方言时间文本归一（`aggregate.go` + `aggregate_test.go`）。
- **test: 真库集成测试子模块 `integration/`**：mock 只断言「生成了什么 SQL 字符串」，验不出驱动返回类型、方言语法合法性、主键回填的两条路径、事务/锁/约束的真实行为。新增嵌套模块（驱动由它导入，主模块保持零依赖；沿用 `examples/` 的 `replace => ..` 约定）在 SQLite / PostgreSQL(pgvector) / MySQL 上各跑同一套 24 个用例，附 `docker-compose.yml` 与独立 CI job；真库跑出并回归了上面这批方言问题（`integration/` + `.github/workflows/ci.yml`）。
- **test: 横向性能基准子模块 `bench/`**：用同一张表、同一份种子数据、语义相同的 SQL 对照 `database/sql`（下界）/ GORM / 本库，跑在 SQLite 内存库上以剥离网络因素。含 5 个单表场景与 `BatchInsert` 分批大小扫描，另有一层更重要的产物：`TestResultParity` 断言三层**返回完全相同的数据**、`TestSQLParity` 打印三层**真正下发**的 SQL（走日志钩子而非 DryRun —— 后者够不着 `SelectById` / `Insert` / `UpdateById` 这些固定模板路径），二者进 CI，防止基准与实现悄悄脱节。实测结论：`InsertOne` / `UpdateByID` 接近裸 SQL（比 GORM 快 2.2× / 1.3×），`Count` 与裸 SQL 持平，**`ListPage` 是唯一明显落后的读路径**（50 行比裸 SQL 慢 26%、分配 2.4 倍，反射行映射是热点）；`BatchInsert` 的吞吐拐点在 chunk≈10 而非越大越好（`bench/` + `.github/workflows/ci.yml`）。
- **perf: 行映射从「每行一次」改为「每查询一次」（`ListPage` 的分配降 48%）**：此前每扫描一行都要 `rows.Columns()`、建两个 `map[string]*`（`idx` 与 `jsonCols` —— 后者即使模型没有 JSON 列也照建）、再分配两个 `[]any` 缓冲；而这些开销只与**结果集列集合**有关，与具体某一行无关，同一个查询的每一行重复做同一件事。现按「列签名」（FNV-1a 哈希，避免了拼接字符串的键分配）把「列序 → 字段下标 + 赋值函数」缓存到模型元数据上（`modelMeta.scanPlanFor` / `scanPlan` / `rowScanner`），`SelectList` 与 `Preload` 的列表路径一次构造、逐行复用；单行路径（`SelectById` / `RawQuery`）行为不变。同时把 `setField` 的各类型分支抽成独立函数，让计划在构建期就选定赋值函数，省掉逐行的 Kind 判断。**行为零变化**：标量特化只覆盖字符串/整数/无符号/浮点/布尔，其余（结构体、切片、map、JSON 列）原样回落到通用实现。以 mock driver 基准（不碰数据库引擎）度量 50 行 × 9 列：`SelectList` 96,341 → 23,864 ns、87,267 → 27,573 B、498 → 101 allocs；真库 `ListPage` 的分配 123,556 → 63,797 B、2,304 → 1,906 allocs（同一轮内三层 ns/op 已落在抖动范围内）。计划缓存按模型封顶 128 个，越界后退化为「每次重建」（即改动前的开销）以防动态列清单把缓存撑爆（`model.go` + `crud.go` + `preload.go`）。
- **test: 行映射计划缓存的回归用例**：`TestScanPlanColumnSets`（同一模型用全列 / 列序颠倒 / 列子集 / 含未知列 / 空结果集分别查询，映射都必须正确 —— 计划按列签名缓存，一旦解析写错，症状是「第二次查询映射到错误字段」，单次查询的用例看不出来）、`TestScanPlanCacheCap`（511 种列组合越过上限后仍须正确）、`TestScanPlanConcurrent`（共享缓存下的并发查询）；并新增 `BenchmarkScanList` / `BenchmarkScanListJSON`（`scan_bench_test.go` + `scan_test.go`）。

## v0.1.11 (2026-09-04)

- **fix: 读写分离下悲观锁读路由主库**：`executor.go` 的 `isWriteQuery` 增加 `FOR UPDATE` / `FOR SHARE` / `FOR KEY SHARE` / `FOR NO KEY UPDATE` 子串判定，命中即按写请求路由到主库，修复只读副本上 `SELECT ... FOR UPDATE` 悲观锁失效或报 `Table is read only` 的隐患（测试环境单库正常、上生产才暴露的典型坑）。
- **docs: 收敛向量检索跨库通用口径**：`VECTOR.md` 限制章节补充「MySQL 社区/商业版无 `VECTOR_DISTANCE` 与 `VECTOR INDEX`（仅 HeatWave on OCI / MySQL AI 可用）、`orm.L1` 在 MySQL 不可映射（MySQL 距离函数仅 `COSINE`/`DOT`/`EUCLIDEAN`）」；新增 §7「上线前向量能力前置自检」（PG / MySQL 对照 SQL，含查插件、建表、插入、距离查询）。`README.md` 同步把「一套 API 跨库通用」收敛为「Postgres 完整可用、MySQL 仅 HeatWave」。

## v0.1.10

- **新手快速上手指南（beginner quickstart）**：新增 `README.quickstart.zh-CN.md` / `README.quickstart.en.md`，面向首次接入的开发者，从安装、连库、第一次 CRUD、向量检索到 ormgen 生成器给出最短路径示例。

## v0.1.8 (2026-09-01)

- **PostgreSQL 自增主键回填修复（重要 bugfix）**：此前 `Insert` 仅靠 `sql.Result.LastInsertId()` 回填自增主键，但 **pgx 经 `database/sql` 不支持 `LastInsertId()`**（返回 error 后被静默吞掉），导致 PG 下 `u.Id` 始终为 `0`。现给 `Dialect` 接口新增 `SupportsLastInsertID() bool` 与 `InsertReturning(pkCol string) string`：MySQL / SQLite 维持 `LastInsertId()` 路径；PostgreSQL 自动改用 `INSERT ... RETURNING "id"` + 扫描单行回填，对调用方透明。另新增 `Executor.QueryRowContext`（及 `DB.queryRowContext`，按写操作 `HookKindExec` 上报日志/钩子）支撑该路径。`Insert` 现已三方言均正确回填主键（`crud.go` + `dialect.go` + `executor.go` + `pkwriteback_test.go`，含 mock 驱动验证 PG RETURNING 回填）。
- **db tag 格式防呆（开关控制，默认关闭）**：`orm.Config` 新增 `StrictTagCheck bool`（默认 `false`）。开启后，模型解析阶段（`parseMeta` 及缓存命中补校验）会校验 `db` tag 是否使用标准引号格式；写成 `db:col,pk`（无引号）时 `reflect` 读不到 `db` key，导致 `pk` / `autoincrement` 等修饰符全部丢失，自增主键会被当成普通列写入 `0` 值且不报错——此时**直接 panic** 并给出明确提示（`orm: 结构体 X 字段 Y 的 db tag 格式错误：缺少引号，正确写法是 db:"col,pk,autoincrement"`）。默认关闭以兼容旧行为；一旦任一 `Open` 开启 `StrictTagCheck`，全局进入严格模式（`strictTagCheck` 包级 atomic，仅增不减，越严越安全）。`go vet` 也能静态拦截同类笔误，此开关作为运行时兜底（`model.go` + `executor.go` + `model_test.go`）。

## v0.1.9

- **ormgen DDL 模式（从建表语句生成模型 + 列闭包）**：`cmd/ormgen` 新增 `-ddl <file.sql>`，直接解析 `CREATE TABLE` 生成 Go 结构体（含 `TableName()` 锁定物理表名）与 `ColOf` 列闭包文件。`gen` 包新增 `ParseDDL` / `FromDDL`：支持 **PG / MySQL / SQLite** 类型映射、`serial`/`bigserial`/`AUTO_INCREMENT`/`AUTOINCREMENT` 自增识别、`vector(N)` → `[]float32` + `,vector(N)` tag、`NOT NULL` / `DEFAULT` / `PRIMARY KEY`（列级与表级）、引号标识符与 `schema.表` 限定名、**单文件内多张表**。方言按内容嗅探（`DetectDialect`，不依赖扩展名）：`serial`/`vector(`/`::` → PG，`AUTO_INCREMENT`/`ENGINE=` → MySQL，`AUTOINCREMENT` → SQLite。
- **输出方式 `-mode`**：`perType`（每表 `xxx.go` + `xxx_cols.go`，默认）/ `twoFiles`（合并 `models.go` + `columns.go`）/ `singleFile`（合并 `models_gen.go`，结构体与闭包同文件）。
- **ormgen serve Web 生成器**：新增 `ormgen -serve`（默认 `:8080`）——纯 `net/http` + `//go:embed` 内嵌 HTML 页面，**零额外依赖**。页面左右分屏：左屏粘贴 / 上传 `.go` 或 `.sql`，自动识别 struct 与 DDL，可选数据库类型与生成参数；右屏展示生成文件（按文件切换、一键复制 / 复制全部 / 下载）；附**可复制的等价 CLI 命令**；文件输出支持「覆盖 / 存在跳过」与浏览器下载。struct 模式按粘贴源码批量生成列闭包，DDL 模式按建表语句生成 model + 闭包；CLI 与 HTTP **共用同一 `gen` 内核**。生成器不保证输出可直接编译，命名 / 包冲突由开发自行处理。
- **修复**：DDL 解析器对 `bigserial` / `smallserial` 的 `autoincrement` tag 推断此前仅匹配前缀 `serial` 而漏判，现改为包含匹配，PG 大整型自增主键正确输出 `db:"id,pk,autoincrement"`。
- **ormgen JSON 样例推断（B 组「更聪明输入」）**：新增 `ormgen -json <sample.json>`——粘贴一份 JSON 文档样例即可推断 Go 结构体与 `ColOf` 列闭包（扁平推断：`float64` 按无小数判定 `int64`/`float64`、`"2024-..T..Z"` 识别 `time.Time`、`nil`→`any`、嵌套对象退化为 `map[string]any`、数组取首元素类型）。Web 端与 DDL / struct 共用自动嗅探（`detectKind`：建表语句→ddl、`{`/`[` 且可 `json.Unmarshal`→json、`struct`+`type `/`package`→struct），无需手动选类型。`gen` 包新增 `ParseJSONSample` / `FromJSON`（`gen/json.go` + `gen/json_test.go`）。
- **生成物即所用（C 组）**：`ormgen` / `esgen` 生成后默认附带 `example.go`——可直接复制到业务代码的「零字段名」示例（orm: 插入 / 查询 / 更新删除 / Repo / 向量近邻 `NearestBy` 五段；es: 写入 / 检索 / 向量 kNN `Nearest().KnnNumCandidates` 三段），占位 `exampleDB` / `exampleClient` 避免引入具体驱动依赖；可用 `-example=false` 关闭。
- **工程化（D 组）**：新增 `Repo[T]` 便捷构造脚手架 `-repo`（orm: `<Struct>_repo.go` 的 `New<Struct>Repo(db)`；es: `<struct>_repo.go` 的 `New<Struct>Repo(cli)`），等价于 `orm.NewRepo[T](...)` / `es.NewRepo[T](...)`，默认关闭。生成物统一 `gofmt`（Web 与 CLI 共用 `format.Source`）。
- **软删除字段自包含识别（C 组）**：DDL / JSON 生成 model 时，命中 `deleted_at` / `deleted` / `is_deleted` / `is_del` / `del_at` 等命名且类型为 `time.Time`/`bool`/`int*` 的字段，自动加 `,logic` tag 并附注释——无需在 `orm.Config` 里配置 `SoftDeleteField` 即生效逻辑删除（软删字段在示例插入段被自动跳过，留零值由框架填充）。

### 修复（代码审查发现）

- **向量序列化精度（`vector.go`）**：`formatFloatVal` 把 `float32` 提升为 `float64` 后仍按 64 位格式化，导致**定长数组**向量（如 `[3]float32{0.1,1.5,2.2}`）序列化成 `[0.10000000149011612,1.5,2.200000047683716]`，而 `[]float32` 走 `floats32ToText` 得到正确的 `[0.1,1.5,2.2]`——同一份语义的两种容器写出不同文本，影响向量列写入与距离计算。现按种类分别用 bitSize 32 / 64 格式化，两者结果一致。
- **map 形式部分更新的列名校验（`crud.go`）**：`UpdateSets` / `UpdateByIdSets` / `UpdatePartial` 的字段名是纯字符串 map key，此前直接加引号拼进 SET 子句，既让拼错的列名（如 `nmae`）只能由数据库报错，也让外部输入（HTTP 参数、配置）成为注入面（`UPDATE t SET "name = 'x' OR 1=1 --" = $1`）。新增 `checkSetCols` 在拼 SQL 前校验列必须存在于模型元数据，否则返回错误且不下发 SQL；`Query.Set` 走 `ColExpr` 本已受限，一并纳入同一校验。
- **只有 Offset 没有 Limit 的 SQL 合法性（`query.go`）**：`Query.Offset(n)` 此前单独使用时生成 `... OFFSET 20`，MySQL / SQLite 下是语法错误。现按方言补无上限 LIMIT —— MySQL `LIMIT 18446744073709551615`、SQLite `LIMIT -1`；PostgreSQL 的 `OFFSET` 可独立使用且**拒绝负数 LIMIT**（报 `LIMIT must not be negative`），故不补（不能与 SQLite 共用 `LIMIT -1`）。`Limit(0)` 不会被当作「取 0 行」。
- **JSON 样例推断的列顺序不确定（`gen/json.go`）**：`ParseJSONSample` 直接 `range` JSON 对象的 map，导致同一份样例每次推断出的字段顺序都不同（生成物 diff 噪声、测试断言随机失败，CI 上已实际复现）。现按 key 排序后推断，输出稳定；`gen/json_test.go` 补稳定性断言。
- **回归测试（`fix_regression_test.go`）**：新增 `auditExecutor`（只记录下发 SQL 的轻量执行器）+ 三类断言——`float32` 数组/切片序列化一致、未知列名被拒且不下发 SQL、Offset-only 在三种方言下的 SQL 形状（PG 不补 LIMIT、SQLite 补 `LIMIT -1`、MySQL 补无上限 LIMIT）。

## v0.1.7 (2026-08-30)

- **AutoMigrate（数据库迁移）**：新增 `db.AutoMigrate(ctx, &User{}, ...)`，幂等建表（`CREATE TABLE IF NOT EXISTS`）+ 二级索引（`CREATE INDEX IF NOT EXISTS`）；自动识别 `,vector(N)`（PG `vector(N)` / MySQL `VECTOR(N)` / SQLite `TEXT`）、`,json`（PG `JSONB` / MySQL `JSON` / SQLite `TEXT`）、`,unique` / `,index`；主键 + 自增按方言生成（PG `BIGSERIAL` / MySQL `AUTO_INCREMENT` / SQLite `INTEGER PRIMARY KEY AUTOINCREMENT`）。不扩展 `Dialect` 接口，用方言类型 switch 生成 DDL，三方言全适配（`migrate.go`）。
- **关联预加载（Preload）**：新增 `orm.Preload / PreloadOne`，通过反射一次性批量加载 **has_many / has_one / belongs_to** 关联，避免 N+1；默认外键约定 `<类型名>_id`（已修复：此前误为 `<类型名>id`，如 `User` 会得到 `userid` 而非 `user_id`），可用 `orm:"has_many;fk:user_id"` / `orm:"belongs_to;fk:xxx"` 覆盖；软删除过滤对子查询同样生效（`preload.go`）。
- **Distinct 去重查询**：`Query.Distinct()` 生成 `SELECT DISTINCT`，与 `Select` / 条件 / 排序 / 分页完全兼容（`query.go`）。
- **测试**：新增 `migrate_test.go`（三方言 DDL 断言 + 向量列 + 前缀 + mock 执行）、`preload_test.go`（has_many / has_one / belongs_to / 单对象 / 未知关系 / 默认外键约定）、`distinct_test.go`（MySQL / PG 向量）。

## v0.1.6 (2026-08-30)

- **联表查询（JOIN）**：`Query` 新增 `Join / LeftJoin / RightJoin`（+ `As` 别名变体）与 `Alias()`；表名白名单校验、ON 原文拼接（文档标注注入风险）；`Select` 支持 `u.name` 带别名列（`quoteIdentPath`）。
- **Upsert（插入或更新）**：新增 `Upsert / BatchUpsert`，方言分发——PG/SQLite 走 `ON CONFLICT (key) DO UPDATE SET col = EXCLUDED.col`（无可更新列退化为 `DO NOTHING`），MySQL 走 `ON DUPLICATE KEY UPDATE col = VALUES(col)`；冲突键默认主键，可经 `conflictCols ...string` 覆盖。
- **部分更新（多字段 / map）**：新增 `Query.Set(col, val)` 链式 + `UpdateSets`，及 `UpdatePartial` / `UpdateByIdSets`（以 `map[string]any` 指定字段）；强制带 WHERE，禁止全表更新；向量列同样自动序列化绑定。
- **乐观锁**：新增 `,version` 模型 tag 与 `Config.OptimisticField` 约定；`UpdateById` / `UpdateByIdSets` 自动 `WHERE version = ?` 并 `SET version = version + 1`，受影响行数为 0 时返回 `ErrOptimisticLock`（`errors.go` 新增）。
- **SQL 生命周期钩子（Hook）**：新增 `Hook` 接口（`On(HookEvent)`）与 `Config.Hooks` / `db.WithHooks(...)`；每次 `exec` / `query` 的 before / after 阶段触发，可用于审计 / 限流 / 链路追踪；未注册零开销。（来自 PR #2）
- **读写分离 / 多数据源**：新增 `Config.ReadWrite`（及等价的 `MultiSourceConfig` 别名）与 `readWriteRouter`；按 SQL 前缀自动写走主库、读 round-robin 走副本，事务内回落主库。（来自 PR #2）
- **修复**：读写分离路由此前未在 `execContext` / `queryContext` 接线（`readWriteRouter.choose` 定义后未被调用），导致配置副本永不命中、功能实际失效；现已接入查询 / 写入路径，并为 `choose` 的 round-robin 加 `sync.Mutex` 保证并发安全。

## v0.1.5 (2026-08-29)

- **向量检索统一 API + 方言分发（AI/RAG 核心卖点）**：`Nearest` / `WithinDistance` 一套 API 同时适配 Postgres(pgvector) 与 MySQL 9+，SQL 由 `Dialect` 接口新增的 `VectorDistance` / `VectorBind` 方法自动分发——PG 生成 `<=>/<->/<#>/<+>` 运算符，MySQL 9 生成 `VECTOR_DISTANCE(col, STRING_TO_VECTOR(?), 'COSINE'|'EUCLIDEAN'|'DOT'|'MANHATTAN')`。
- 新增距离度量 `orm.VectorMetric`：**Cosine / L2 / InnerProduct / L1**；新增 `NearestBy` / `WithinDistanceBy` / `WithVectorMetric`；`Nearest` / `WithinDistance` 不指定时沿用默认 `L2`（兼容旧版 `<->` 行为）。
- 向量字段零依赖：新增 `,vector` 模型 tag，`Insert` / `BatchInsert` / `Update` / `UpdateById` 自动将 `[]float32` / `[]float64` 序列化为 `[..]` 文本并参数化绑定（MySQL 自动包裹 `STRING_TO_VECTOR(?)`），**无需引入 `pgvector-go`**。
- 新增 `examples/vector-search`：离线打印 PG / MySQL 两种方言生成的向量检索 SQL，含真实用法注释块。

## v0.1.4 (2026-08-29)

- 增强软删除：新增「约定软删除字段名」`Config.SoftDeleteField`（/ `WithSoftDeleteField`），实体列名或 Go 字段名命中且类型为 time/int/bool 即自动启用软删除，免写 `,logic` tag；优先级 `,logic` > 约定 > 物理删除。
- 软删除新增 **bool 类型**支持（`= false` / 写 `true`）；新增 `,nologic` tag 显式退出约定匹配，退化为物理删除。
- 新增 `ormgen` 代码生成器（`cmd/ormgen`）：按模型结构体生成 `UserCols` 列名集合，`UserCols.Age` 取代手写 `Col` 闭包，编译期字段安全。
- 新增 `orm.ColOf[T]("FieldName")`：按 Go 字段名构造 `ColExpr`，供生成代码使用。

## v0.1.3 (2026-08-29)

- 新增原生 SQL 出口：`RawQuery[T]` / `RawOne[T]` / `RawExec`，支持字段别名（snake_case 命名即可命中字段）与非表 DTO 接收结果。
- `Repo[T]` 增加 `RawQuery / RawOne / RawExec` 透传方法。

## v0.1.2 (2026-08-29)

- `orm.Config` 新增连接池参数：`MaxOpenConns` / `MaxIdleConns` / `ConnMaxLifetime` / `ConnMaxIdleTime`（零值保持 `database/sql` 默认行为）。
- 新增 `db.SQL() *sql.DB` 逃生舱访问器，可直连底层连接池调冷门参数与 `Stats()`。

## v0.1.1 (2026-08-29)

- `orm.Open` 支持结构体配置：`orm.Open(orm.Config{Driver, DSN, Prefix, Logger, LogLevel, SlowThreshold, ...})`，兼容旧写法 `Open(driver, dsn)`。
- 恢复被 Copilot PR 误删的完整 README。

## v0.1.0 (2026-08-24)

- 首个版本：泛型 CRUD（`Insert / BatchInsert / SelectById / SelectList / SelectOne / Page / Count / Exists / Update / Delete`）。
- `Col[T]` 闭包零字段名查询构造器（`Eq / Ne / Gt / Ge / Lt / Le / Like / In / Between / IsNull` 等），`Or()` 与 `If()` 条件块。
- 自动表名推导与表前缀、软删除（`deleted_at`）、JSON 字段自动序列化、原生事务、SQL 日志与慢查询、向量检索（`Nearest / WithinDistance`）。
- Postgres / MySQL / SQLite 三方言；三层防注入（参数绑定 + 列名 tag 白名单 + 表名校验）。
