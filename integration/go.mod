module github.com/wusenshan/gobreath-orm/integration

go 1.23

require (
	// 刻意不用 v1.9+：mysql 驱动自 v1.10.0 起把 go.mod 抬到 go 1.24.0，
	// 会连带把本模块的最低 Go 版本顶上去，与仓库「支持 1.23」的承诺冲突。
	// v1.8.1 的方言行为与新版一致，做集成测试足够。
	github.com/go-sql-driver/mysql v1.8.1
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
