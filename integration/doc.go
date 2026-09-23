// Package integration 用「真实数据库」验证 gobreath-orm 的端到端行为。
//
// # 为什么单独一个模块
//
// 主模块（仓库根的 go.mod）是零依赖的，驱动必须由使用者自行导入。
// 真库测试需要 pgx / mysql / sqlite 驱动，若放进主模块会把它们写进 go.sum，
// 破坏「零依赖」这一核心卖点，CI 也会被迫下载一堆无关包。
// 因此这里沿用 examples/ 已经确立的嵌套模块约定（replace 指回 ..）；
// 根模块执行 go test ./... 时不会进入本目录。
//
// # 为什么需要真库（mock 验不出来什么）
//
// 本仓库既有的测试用 mock 执行器只断言「生成了什么 SQL 字符串」，
// 以下四类问题**只有真库能暴露**：
//
//  1. 驱动返回值与扫描目标的类型匹配 —— 例如 PG 的 SUM(bigint) 是 numeric，
//     经 database/sql 回来的形态与 mock 完全无关；
//  2. 方言语法是否真的合法 —— 生成的 SQL 在 PG 能跑，在 SQLite 未必；
//  3. 主键回填的两条路径 —— PG 走 INSERT ... RETURNING，MySQL/SQLite 走 LastInsertId；
//  4. 事务、锁、约束的真实行为（回滚、FOR UPDATE、乐观锁冲突）。
//
// # 运行
//
//	cd integration
//	go test ./... -v
//
// 默认只跑 SQLite（modernc 纯 Go 驱动 + 内存库，零安装）。
// 设置环境变量后自动追加对应后端，未设置则跳过：
//
//	# PostgreSQL
//	$env:ORM_IT_PG_DSN = "postgres://postgres:postgres@127.0.0.1:5432/postgres?sslmode=disable"
//	# MySQL 8（parseTime=true 是 time.Time 扫描的前提）
//	$env:ORM_IT_MYSQL_DSN = "root:root@tcp(127.0.0.1:3306)/ormtest?parseTime=true&loc=Local"
//
// 同一个测试函数会在每个可用后端下各跑一遍，便于横向对比方言差异。
package integration
