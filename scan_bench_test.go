package orm

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"
)

// 本文件用内置 mock driver 度量**纯 ORM 层**开销：不涉及 SQLite/PG/MySQL 引擎、
// 不涉及磁盘与网络，所以测出来的就是「SQL 构造 + 行映射」本身。
//
// 为什么需要它：bench/ 子模块的真库基准里，ns/op 被 SQLite 执行计划主导
// （单次 op 3ms 量级），同一台机器上 run-to-run 抖动可达 ±40%，
// 用它判断「行映射改快了没有」等于掷骰子。这里的单次 op 只有微秒量级，
// 采样数量高两三个数量级，allocs/op 更是完全确定的整数。
//
// 口径提示：mock 直接返回 int64/string/[]byte/time.Time 这些 driver.Value，
// 与真实驱动一致，所以 setField 走的分支相同。不含方言驱动的类型转换开销。

// scanBenchUser 与 bench/ 子模块的 User 列结构一一对应（9 列，含时间列与
// 指针时间列），保证这里的数据能代表真库场景。
type scanBenchUser struct {
	ID        int64      `db:"id,pk,autoincrement"`
	Name      string     `db:"name"`
	Age       int        `db:"age"`
	City      string     `db:"city"`
	Email     string     `db:"email"`
	Score     float64    `db:"score"`
	CreatedAt time.Time  `db:"created_at"`
	UpdatedAt time.Time  `db:"updated_at"`
	DeletedAt *time.Time `db:"deleted_at,logic"`
}

func (scanBenchUser) TableName() string { return "scan_bench_users" }

// scanBenchDoc 在之上加一列 JSON —— 那条路径（json.Unmarshal）与标量列完全不同，
// 混在一起测会让两者的成本互相掩盖。
type scanBenchDoc struct {
	ID   int64          `db:"id,pk,autoincrement"`
	Name string         `db:"name"`
	Meta map[string]any `db:"meta,json"`
}

func (scanBenchDoc) TableName() string { return "scan_bench_docs" }

// scanBenchRows 结果集行数：与 bench 的 ListPage 场景一致（LIMIT 50）。
const scanBenchRows = 50

var scanBenchTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// installScanBenchFactory 注册一个「每次查询现造一份」的结果集。
func installScanBenchFactory(tb testing.TB, key string, cols []string, rows [][]driver.Value) {
	tb.Helper()
	// 每个查询返回全新的 *mockRows：mockRows.Next 会推进内部 pos，
	// 复用同一实例的话第二次查询立刻 EOF。
	mockFactories[key] = func() driver.Rows {
		return &mockRows{cols: cols, data: rows}
	}
	tb.Cleanup(func() { delete(mockFactories, key) })
}

var scanBenchUserCols = []string{
	"id", "name", "age", "city", "email", "score", "created_at", "updated_at", "deleted_at",
}

func scanBenchUserData() [][]driver.Value {
	data := make([][]driver.Value, scanBenchRows)
	for i := range data {
		data[i] = []driver.Value{
			int64(i + 1), "user-000001", int64(30), "Beijing", "user@example.com",
			float64(88.5), scanBenchTime, scanBenchTime, nil,
		}
	}
	return data
}

// BenchmarkScanList 分母：同一个 SQL、同一个结果集，只驱动 rows.Next() 不做任何扫描。
// SelectList 与它的差值 = SQL 构造 + 行映射。
func BenchmarkScanList(b *testing.B) {
	installScanBenchFactory(b, "scan_bench_users", scanBenchUserCols, scanBenchUserData())
	ctx := context.Background()

	b.Run("query_only", func(b *testing.B) {
		db := newMockDBFor(b)
		meta := getMeta[scanBenchUser]()
		q := NewQuery[scanBenchUser]().Eq(Col[scanBenchUser](func(u *scanBenchUser) *string { return &u.City }), "Beijing").Limit(scanBenchRows)
		sqlStr, args := q.applyLogic(meta, db).WithDialect(db.dialect).WithPrefix(db.prefix).Build()

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			rows, err := db.queryContext(ctx, sqlStr, args...)
			if err != nil {
				b.Fatal(err)
			}
			n := 0
			for rows.Next() {
				n++
			}
			rows.Close()
			if n != scanBenchRows {
				b.Fatalf("结果集行数 %d，期望 %d", n, scanBenchRows)
			}
		}
	})

	b.Run("SelectList", func(b *testing.B) {
		db := newMockDBFor(b)
		q := NewQuery[scanBenchUser]().Eq(Col[scanBenchUser](func(u *scanBenchUser) *string { return &u.City }), "Beijing").Limit(scanBenchRows)

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			got, err := SelectList(ctx, db, q)
			if err != nil {
				b.Fatal(err)
			}
			if len(got) != scanBenchRows {
				b.Fatalf("返回 %d 行，期望 %d", len(got), scanBenchRows)
			}
		}
	})

	b.Run("SelectById", func(b *testing.B) {
		db := newMockDBFor(b)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			u, err := SelectById[scanBenchUser](ctx, db, 1)
			if err != nil {
				b.Fatal(err)
			}
			if u == nil {
				b.Fatal("SelectById 返回 nil")
			}
		}
	})
}

// BenchmarkScanListJSON 单独度量 JSON 列：json.Unmarshal 是行内最贵的一步，
// 它与标量列的优化手段完全不同，混测会互相掩盖。
func BenchmarkScanListJSON(b *testing.B) {
	installScanBenchFactory(b, "scan_bench_docs",
		[]string{"id", "name", "meta"},
		func() [][]driver.Value {
			data := make([][]driver.Value, scanBenchRows)
			for i := range data {
				data[i] = []driver.Value{int64(i + 1), "doc", []byte(`{"status":"active","n":7}`)}
			}
			return data
		}())

	b.Run("SelectList", func(b *testing.B) {
		db := newMockDBFor(b)
		q := NewQuery[scanBenchDoc]().Limit(scanBenchRows)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			got, err := SelectList(context.Background(), db, q)
			if err != nil {
				b.Fatal(err)
			}
			if len(got) != scanBenchRows {
				b.Fatalf("返回 %d 行，期望 %d", len(got), scanBenchRows)
			}
		}
	})
}
