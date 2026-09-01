# Changelog

本项目遵循 [Semantic Versioning](https://semver.org/)。

## v0.1.8 (unreleased)

- **db tag 格式防呆（开关控制，默认关闭）**：`orm.Config` 新增 `StrictTagCheck bool`（默认 `false`）。开启后，模型解析阶段（`parseMeta` 及缓存命中补校验）会校验 `db` tag 是否使用标准引号格式；写成 `db:col,pk`（无引号）时 `reflect` 读不到 `db` key，导致 `pk` / `autoincrement` 等修饰符全部丢失，自增主键会被当成普通列写入 `0` 值且不报错——此时**直接 panic** 并给出明确提示（`orm: 结构体 X 字段 Y 的 db tag 格式错误：缺少引号，正确写法是 db:"col,pk,autoincrement"`）。默认关闭以兼容旧行为；一旦任一 `Open` 开启 `StrictTagCheck`，全局进入严格模式（`strictTagCheck` 包级 atomic，仅增不减，越严越安全）。`go vet` 也能静态拦截同类笔误，此开关作为运行时兜底（`model.go` + `executor.go` + `model_test.go`）。

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
