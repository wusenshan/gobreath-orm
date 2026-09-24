# Changelog

本项目遵循 [Semantic Versioning](https://semver.org/)。

## v0.1.13（未发布）

- **fix: `Delete` 的实参顺序在位置型方言上错位（MySQL / SQLite 的逻辑删除静默失效，数据损坏级）**：`Delete` 生成的是 `UPDATE t SET deleted_at = <ph> WHERE col = <ph>`，但实参原先**先按 WHERE 分配、后补 SET** —— 位置型方言（MySQL / SQLite 的 `?` 按出现次序逐个取用）拿到的是 `[条件值, 删除值]`，于是 WHERE 把**删除时间戳**绑给了字符串列，条件匹配 0 行：**不报错、不删除、静默 no-op**（真库实测 `visible=4 unscoped=4`，一行都没删掉）。PG 因为 `$1/$2` 自带序号而完全免疫，所以 mock 断言与 PG 集成测试都看不见它 —— 这类「参数顺序错位」是计数类断言的结构性盲区。现把 SET 的实参在 WHERE 之前分配，占位符顺序与实参顺序严格对齐；条件为空时仍拒绝全表删除（`crud.go` + `delete_args_test.go`）。
- **fix: 向量列「写得进去、读不回来」**：写方向的序列化（`serializeVector`）早已存在，读方向却只有通用兜底，于是 PG 的 `vector` 列经驱动交回的文本落到 `[]float32` 字段上直接报 `orm: 无法把 string 赋给 []float32 字段` —— 而检索结果里最需要读的恰恰就是向量列本身。现新增 `setVectorField`（支持 `[]float32` / `[]float64` / 其定长数组；`dim > 0` 时校验维度，不一致报错而不是静默截断），在 `setterFor` 里按 `f.vector` 分派给专用 setter。**MySQL 走的是另一条路**：`VECTOR` 列底层是 BLOB（MySQL worklog 原文 `Field_vector : public Field_blob`，精度 = `sizeof(float)`），`SELECT` 回来的是小端序 float32 **裸字节**而不是 `"[1,2,3]"` 文本，纯文本解析必然失败 —— 现按载荷首字节分派（`[` 开头走文本，否则按二进制解码）。两种形态都有真实证据：文本来自 PG 实测，二进制样例取自 MySQL 官方文档（`STRING_TO_VECTOR("[3.14,2024,18]") = 0xC3F548400000FD4400009041`，逐字节核对小端 float32）（`model.go` + `vector_binary_test.go`）。
- **fix: MySQL 9 的向量列要求 `go-sql-driver/mysql` ≥ v1.9.0**：MySQL 9.0 为 VECTOR 引入了新的字段类型码 242（`MYSQL_TYPE_VECTOR`），v1.8.x 不认识它，读向量列时**驱动层**直接报 `unknown field type 242` —— 也就是说把服务端升到 MySQL 9 并不够，驱动不升向量路径照样走不通。集成子模块的驱动因此从 v1.8.1 升到 **v1.9.3**（v1.9.0 changelog: "Add support for VECTOR type introduced in MySQL 9.0. (#1609)"）；刻意不升 v1.10.0 —— 它把 go.mod 抬到 `go 1.24.0`，会顶掉本仓库「支持 1.23」的承诺（`integration/go.mod`）。
- **test: 真库走查「能跑的接口都跑一遍」**：此前集成测试只覆盖 CRUD / 查询主干，`Repo[T]` 门面、谓词构造矩阵、JOIN 的六种变体、写入选项、`DB` 配置方法**从未在真库上执行过**。新增 `TestRepoLayerFullWalk`（`Repo[T]` 全部方法，含事务提交与回滚）、`TestQueryPredicateMatrix`（每个谓词构造器都真执行并用行数断言核对）、`TestJoinVariantsExecutable`（`Join` / `LeftJoin` / `RightJoin` 及其 `As` 变体 + 别名，并断言 `Count` 与 `SelectList` 口径一致）、`TestWriteOptionsOnRealDB`（`OnlyColumns` / `OmitZero` 及组合，以及非法列必须报错）、`TestDbConfigMethodsExecutable`（软删除列 / 乐观锁列 / 日志与慢查询阈值 / 钩子 / 替换 `Executor` / `NewDB` / `Transaction` / 两参数 `Open`，每项都断言可观测效果）。这批用例直接抓出了上面的 `Delete` 实参错位（`integration/api_it_test.go`）。
- **test: 向量用例按能力分级，MySQL 9 的存取路径首次进真库**：原先「没有距离函数」等价于「整条向量路径跳过」，于是 MySQL 上**能跑的那一半也从没跑过** —— 而读回恰恰坏在这里。现改为两级探测（`probeVector` 返回能力结构、不再自己 Skip）：**tier 1 存储级**（建列 / 写入 / 单行读回 / 列表读回 / `UpdateById` 改向量）只要有 `VECTOR` 类型就跑；**tier 2 距离级**（排序 / 阈值 / 聚合 × 向量 / `Pluck` + 度量切换）才需要距离函数。实测 MySQL 9.7.2 社区版：`VECTOR(3)` 可建、`STRING_TO_VECTOR` 可存取，`VECTOR_DISTANCE` / `DISTANCE` 均报 `ERROR 1305 (42000): FUNCTION ... does not exist` → 只跑 tier 1，并把原始报错留在日志里。用例入口先 `DROP TABLE` 保证幂等（上一轮若在 `t.Fatalf` 中断就没走到收尾，残留行会让计数断言成倍偏大，实测踩过 `Nearest + Count = 6`）。**mutation check**：把读路径临时退回纯文本解析后，只有 MySQL 子用例失败、报错正是预期的二进制乱码（`向量文本第 0 个分量 "fff?\xcd\xcc\xcc..." 无法解析`，`ff 66 66 3F` 小端即 `0.9`），PG 照过 —— 证明这条用例确实拦得住该 bug（`integration/vector_it_test.go`）。
- **test: PG 的两条驱动路径（pgx / lib/pq）都进真库验证**：README 把 `github.com/lib/pq`（驱动名 `postgres`）与 `github.com/jackc/pgx/v5/stdlib`（驱动名 `pgx`）并列为可选驱动，且**主示例给的是前者** —— 但集成 harness 只挂了 pgx 一条，lib/pq 连依赖都不在，等于公开承诺的一半从未在真库上执行过。现让同一个 `ORM_IT_PG_DSN` 同时铺开 `postgres`（pgx）与 `postgres-libpq` 两条后端。**两者的差异是实测出来的**（`Scan(&any)` 取 `%T`）：`numeric` / `uuid` / `text[]` / pgvector 的 `vector` 列，pgx 一律解码成 `string`，lib/pq 一律给 `[]byte`；`timestamptz` 两者都给 `time.Time` 但 Location 不同（pgx 带会话时区、lib/pq 归一到 UTC，时间点一致）。即「同为 database/sql」并不等于「交给 scan 的形态相同」。新增两条用例：`TestCrossDriverValueParity` 走 `RawOne` / `RawQuery` 的**标量**路径（该路径直接 `rows.Scan`，转换由标准库 `convertAssign` 负责，框架不介入），`TestModelScanAcrossDrivers` 走**模型字段**路径（框架自己的 `assign*` 分派）—— 后者手工建 `NUMERIC` / `UUID` / `TEXT[]` / `BYTEA` 列，让「服务端类型」与「Go 字段类型」故意错开，因为 `assignFloat` / `assignString` 的 `case []byte` 分支**只在 lib/pq 下会被走到**，而在此之前能造出 `[]byte` 载荷的模型列只有向量列。**mutation check**：临时禁用 `assignFloat` 的 `[]byte` 分支后，`postgres` 照过、`postgres-libpq` 失败，报文正是预期的 `[]byte→float` 被禁用 —— 证明该用例确实守着这条只在 lib/pq 下出现的路径。顺带修掉一处测试基建缺陷：`placeholderOf` 按后端**名**判方言（`b.name == "postgres"`），新后端会拿到 `"?"` 而拼出 PG 无法解析的 SQL，现改按 `b.dialect` 判断。四条后端全量：**164 通过 / 6 跳过 / 0 失败**（`integration/harness_test.go` + `driver_shapes_it_test.go` + `driver_types_it_test.go` + `query_it_test.go`）。
- **docs: 精确化 MySQL 距离度量口径**：`VECTOR.md` 原写「`MANHATTAN` 只在 Oracle Database 的 `VECTOR_DISTANCE` 里，是另一个产品」—— 实际上 **Percona Server for MySQL 9.7.2 起**的 `DISTANCE()` / `VECTOR_DISTANCE()` 已支持 `MANHATTAN`（另有 `EUCLIDEAN_SQUARED`），所以 `mysqlDialect` 把 `orm.L1` 映射为 `'MANHATTAN'` 是有依据的、`vector.go` 注释里的「需 9.7+」指的正是这条。改后的口径：Oracle MySQL / HeatWave 只支持 `COSINE` / `DOT` / `EUCLIDEAN`（在其上使用 `orm.L1` 会报未知度量），`MANHATTAN` 需要 Percona 9.7+ 或其他带该度量的发行版。同时补上 MySQL 9 的驱动前提（≥ v1.9.0）与「社区版能存不能查」的判定命令。
- **已知限制（未改，先记下）**：「向量列存 NULL」目前无法表达 —— `serializeVector` 对 `[]float32{}` 与 nil 切片**都**产出 `"[]"`（`TestSerializeVector` 的 empty 用例已是既有契约），而 PG 的 `vector` 列至少 1 维，写 `"[]"` 会直接报 `ERROR: vector must have at least 1 dimension (SQLSTATE 22000)`（实测）。确实需要 NULL 向量时，只能靠 `OnlyColumns` 把该列排除在写入之外。

## v0.1.12 (2026-09-24)

- **fix: 向量参数个数与占位符不一致（聚合查询多传一个参数，且 MySQL 的 `Nearest` 从未能跑通）**：`Build()` 原先在开头**无条件**把向量放进 `args[0]`，而渲染距离表达式的地方有三处、各带独立开关（投影里的 dist 列受 `noVecCol` 控制、距离阈值过滤受 `vecFilterOn` 控制、按距离排序在聚合时整段跳过）。两个方向都出问题：(1) `Nearest(...)` 之后调 `Count` / `Sum` / `Avg` —— 也就是 RAG 里「统计某向量邻域内有多少条」这个写法 —— 投影只剩 `COUNT(*)`，SQL 里一个占位符都没有却仍带着向量，`database/sql` 在 `driverArgsConnLocked` 里比对驱动自报的 `NumInput` 后直接报 `sql: expected 0 arguments, got 1`；若查询还带 `WHERE`，则参数整体错位（`SELECT COUNT(*) FROM "articles" WHERE "title" = $2`，向量占了 `$1`）。(2) 同一向量在两处出现时只传了一份：PG 的 `$1` 是**引用型**，dist 列与 `ORDER BY` 引用同一个参数没有代价，MySQL / SQLite 的 `?` 是**位置型**，出现几次就要几个参数，于是 `SELECT ..., VECTOR_DISTANCE(?, ...) AS dist ... ORDER BY VECTOR_DISTANCE(?, ...)` 只带一份向量，报 `sql: expected 2 arguments, got 1` —— **即 MySQL 上的 `Nearest` 从未能跑通**（SQLite 无原生向量类型，该路径本就只做 SQL 生成，但形状同样错）。现把「分配参数」与「写出占位符」绑成同一个动作（`vecPH` 惰性分配，不再无条件注入），并按 `positionalPlaceholder` 区分引用型 / 位置型方言：PG 复用同一个 `$1`（SQL 形状与改动前一致），位置型方言按占位符出现次数各保一份向量。判定用「`Placeholder(1) == Placeholder(2)`」而不是给 `Dialect` 接口加方法 —— 加方法会让外部自实现方言编译不过，而这个判据自带方言无关性（`query.go` + `dialect.go`）。
- **test: 占位符 / 参数个数一致性的回归用例 + 严格 mock 驱动**：新增 `TestVectorArgsMatchPlaceholders`（PG / MySQL / SQLite 共 17 例）覆盖「聚合 × 向量」与「位置型复用」的完整矩阵，并且**两个方向都断言**：聚合、阈值过滤、`Pluck`、普通向量查询各自该有几个参数就是几个，既防止多传也防止修过头。更关键的是新增 `strictDriver`，按真实驱动的规则实现 `NumInput`（位置型方言 = `?` 的出现次数，对齐 `go-sql-driver/mysql` 的 `mysqlStmt.NumInput() → paramCount`；引用型方言 = `$n` 的最大序号），让 `database/sql` 自己拦下个数不符 —— 项目原有的 `ormmock` 驱动 `NumInput()` 恒返回 `-1`（等于放弃校验），这类错误在 mock 上**完全不可见**，MySQL 的向量路径就是这么漏掉的；`TestStrictMockEnforcesArgCount` 反向证明该驱动真的会拦，否则用例全绿也只是因为「压根没校验」。同时修正两条把错误行为固化成期望值的既有断言：`TestVectorNearestMySQL`（用 1 个参数对应 2 个 `?`）、`TestVectorNearestWithWhereFilter`（用 2 个参数对应 3 个 `?`）—— 又是「测试全绿掩盖回归」的一例（`vector_args_test.go` + `vector_test.go`）。
- **fix: 复合主键静默截断（数据损坏级）**：`parseMeta` 此前对每个 `,pk` 列都执行 `m.pk = &m.fields[len(m.fields)-1]`，**后者静默覆盖前者** —— 多主键模型能正常构造，但主键只认最后一列，于是 `DeleteById` / `UpdateById` / `Upsert` 生成的 WHERE 少一半条件，**可能命中并改写多行**；`AutoMigrate` 还会为每个 `,pk` 列各生成一个内联 `PRIMARY KEY`，产出 MySQL 1068 / PG `multiple primary keys` 级别的非法 DDL。两处都不报错、不警告。现改为在模型解析阶段**硬失败**（`compositePKPanic`，与 `validateDbTag` 同一风格），诊断信息点名涉及的列；因 `AutoMigrate` 也走 `getMeta`，一处闸门同时挡住两条路径。复合主键本身仍未支持，需要时请先用显式条件（`Eq` / `In`）代替按主键操作（`model.go` + `composite_pk_test.go`）。
- **feat: `OnlyColumns` 写入白名单**：`UpdateById` / `Update` 默认写入**全部**可写列，实体上未赋值的字段会被一并写回 —— 典型事故是 `time.Time` 的零值（`bench/` 里实测踩到：payload 没给 `created_at`，每迭代一次就把该行时间戳抹成 `0001-01-01`）。新增 `OnlyColumns(cols ...string)` 静态白名单，与 `OmitZero` 的动态过滤互补、可叠加（先取白名单，再按零值过滤）。与 `OmitZero` 的取舍相反：白名单里出现**拼错的列名、主键 / 自增 / 逻辑删除列时报错而不是静默丢弃** —— 静默丢弃会让「我以为更新了 name，其实 SQL 里根本没有它」一直藏着。空列集的报错信息也改为按实际原因区分（此前一律归咎 `OmitZero`，用 `OnlyColumns()` 传空时会把排查方向带偏）。另把 `parseMeta` 里主键 / 逻辑列 / 版本列的指针改为循环结束后按下标统一取址 —— 原先在循环内 `&m.fields[len-1]`，后续 `append` 一旦触发扩容就会指向旧底层数组的副本，从此与 `m.fields` 脱钩（`crud.go` + `model.go` + `only_columns_test.go`）。
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
