# Changelog

本项目遵循 [Semantic Versioning](https://semver.org/)。

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
