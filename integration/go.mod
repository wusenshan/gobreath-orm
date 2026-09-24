module github.com/wusenshan/gobreath-orm/integration

go 1.23

require (
	// 必须 ≥ v1.9.0：MySQL 9.0 的 VECTOR 列是新的字段类型码 242（MYSQL_TYPE_VECTOR），
	// v1.8.x 不认识它会直接报 "unknown field type 242"，向量读回连驱动都过不去
	// （v1.9.0 changelog: "Add support for VECTOR type introduced in MySQL 9.0. (#1609)"）。
	// 同时刻意**不升到 v1.10.0**：它把 go.mod 抬到 go 1.24.0，会连带把本模块的
	// 最低 Go 版本顶上去，与仓库「支持 1.23」的承诺冲突；v1.9.3 的 go 指令仍是 1.21+。
	github.com/go-sql-driver/mysql v1.9.3
	github.com/jackc/pgx/v5 v5.5.5
	github.com/wusenshan/gobreath-orm v0.1.3
	modernc.org/sqlite v1.34.5
)

require (
	filippo.io/edwards25519 v1.1.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20221227161230-091c0ba34f0a // indirect
	github.com/jackc/puddle/v2 v2.2.1 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v0.1.9 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/crypto v0.17.0 // indirect
	golang.org/x/sync v0.1.0 // indirect
	golang.org/x/sys v0.22.0 // indirect
	golang.org/x/text v0.14.0 // indirect
	modernc.org/libc v1.55.3 // indirect
	modernc.org/mathutil v1.6.0 // indirect
	modernc.org/memory v1.8.0 // indirect
)

replace github.com/wusenshan/gobreath-orm => ..
