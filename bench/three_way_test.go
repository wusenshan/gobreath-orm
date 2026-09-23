package bench

import (
	"context"
	"testing"
	"time"
)

// 场景参数（三方共用）：
//
//	targetID —— 单行查询/更新的目标主键，落在种子数据的中间位置
//	queryCity / queryMinAge / queryLimit / queryOffset —— 见 model.go
const targetID = int64(5000)

// updatePayload 所有后端共用的更新内容：同一行、同样的新值。
// 值本身不重要，重要的是三方写的是同一份数据。
//
// createdAt 必须由调用方用**目标行的真实值**填上，不能留零值：
// gobreath 的 UpdateById 会写入全部可写列（含 created_at），留零值的话
// 基准每迭代一次就把该行的时间戳抹成 0001-01-01 —— 那不是测量，是顺手破坏数据。
// （raw / GORM 两侧的 SET 列表里没有 created_at，所以只有 gobreath 会踩这个坑，
// 也正因如此 payload 必须按最严格的那一方来准备。）
func updatePayload(id int64, createdAt time.Time) rawRow {
	return rawRow{
		ID:        id,
		Name:      "updated-name",
		Age:       42,
		City:      queryCity,
		Email:     "updated@example.com",
		Score:     99.5,
		CreatedAt: createdAt,
		UpdatedAt: baseTime.Add(999 * time.Hour),
	}
}

// BenchmarkSelectByID 单行主键查询：最热、最简单的读路径。
// 三层生成的 SQL 都是 SELECT <9 列> FROM bench_users WHERE id = ? AND deleted_at IS NULL。
func BenchmarkSelectByID(b *testing.B) {
	ctx := context.Background()

	b.Run("raw", func(b *testing.B) {
		h := newRawHarness(b)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := h.selectByID(ctx, targetID); err != nil {
				b.Fatalf("raw SelectByID 失败：%v", err)
			}
		}
	})

	b.Run("gorm", func(b *testing.B) {
		h := newGormHarness(b)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := h.selectByID(ctx, targetID); err != nil {
				b.Fatalf("gorm SelectByID 失败：%v", err)
			}
		}
	})

	b.Run("gobreath", func(b *testing.B) {
		h := newOrmHarness(b)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := h.selectByID(ctx, targetID); err != nil {
				b.Fatalf("gobreath SelectByID 失败：%v", err)
			}
		}
	})
}

// BenchmarkListPage 条件列表 + 分页：WHERE city = ? AND age >= ?
// ORDER BY id LIMIT 50 OFFSET 200，返回 50 行。
//
// 这是行映射开销最容易暴露的场景 —— 50 行要逐列扫描到 Go 结构体，
// 反射/映射的实现质量会直接体现在这里。
func BenchmarkListPage(b *testing.B) {
	ctx := context.Background()

	b.Run("raw", func(b *testing.B) {
		h := newRawHarness(b)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			got, err := h.listPage(ctx, queryCity, queryMinAge, queryLimit, queryOffset)
			if err != nil {
				b.Fatalf("raw ListPage 失败：%v", err)
			}
			if len(got) == 0 {
				b.Fatal("raw ListPage 返回空结果，场景参数有问题")
			}
		}
	})

	b.Run("gorm", func(b *testing.B) {
		h := newGormHarness(b)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			got, err := h.listPage(ctx, queryCity, queryMinAge, queryLimit, queryOffset)
			if err != nil {
				b.Fatalf("gorm ListPage 失败：%v", err)
			}
			if len(got) == 0 {
				b.Fatal("gorm ListPage 返回空结果，场景参数有问题")
			}
		}
	})

	b.Run("gobreath", func(b *testing.B) {
		h := newOrmHarness(b)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			got, err := h.listPage(ctx, queryCity, queryMinAge, queryLimit, queryOffset)
			if err != nil {
				b.Fatalf("gobreath ListPage 失败：%v", err)
			}
			if len(got) == 0 {
				b.Fatal("gobreath ListPage 返回空结果，场景参数有问题")
			}
		}
	})
}

// BenchmarkCount 条件计数：SELECT COUNT(*) WHERE city = ? AND deleted_at IS NULL。
// 结果集只有 1 列，所以这里最能看出「查询构造 + 参数绑定」的纯框架开销。
func BenchmarkCount(b *testing.B) {
	ctx := context.Background()

	b.Run("raw", func(b *testing.B) {
		h := newRawHarness(b)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := h.count(ctx, queryCity); err != nil {
				b.Fatalf("raw Count 失败：%v", err)
			}
		}
	})

	b.Run("gorm", func(b *testing.B) {
		h := newGormHarness(b)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := h.count(ctx, queryCity); err != nil {
				b.Fatalf("gorm Count 失败：%v", err)
			}
		}
	})

	b.Run("gobreath", func(b *testing.B) {
		h := newOrmHarness(b)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := h.count(ctx, queryCity); err != nil {
				b.Fatalf("gobreath Count 失败：%v", err)
			}
		}
	})
}

// BenchmarkInsertOne 单行插入（主键交给自增，插入后回填）。
//
// 注意：表会随 b.N 增长，三方受同样影响。
// GORM 侧关掉了 SkipDefaultTransaction 之外的默认行为，见 newGormHarness 注释。
func BenchmarkInsertOne(b *testing.B) {
	ctx := context.Background()

	b.Run("raw", func(b *testing.B) {
		h := newRawHarness(b)
		payload := makeRows(1)[0]
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := h.insertOne(ctx, payload); err != nil {
				b.Fatalf("raw Insert 失败：%v", err)
			}
		}
	})

	b.Run("gorm", func(b *testing.B) {
		h := newGormHarness(b)
		payload := makeRows(1)[0]
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := h.insertOne(ctx, payload); err != nil {
				b.Fatalf("gorm Insert 失败：%v", err)
			}
		}
	})

	b.Run("gobreath", func(b *testing.B) {
		h := newOrmHarness(b)
		payload := makeRows(1)[0]
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := h.insertOne(ctx, payload); err != nil {
				b.Fatalf("gobreath Insert 失败：%v", err)
			}
		}
	})
}

// BenchmarkUpdateByID 按主键整行更新（带软删除条件），重复更新同一行。
func BenchmarkUpdateByID(b *testing.B) {
	ctx := context.Background()

	b.Run("raw", func(b *testing.B) {
		h := newRawHarness(b)
		src, err := h.selectByID(ctx, targetID)
		if err != nil {
			b.Fatalf("raw 读取目标行失败：%v", err)
		}
		payload := updatePayload(targetID, src.CreatedAt)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := h.updateByID(ctx, payload); err != nil {
				b.Fatalf("raw Update 失败：%v", err)
			}
		}
	})

	b.Run("gorm", func(b *testing.B) {
		h := newGormHarness(b)
		src, err := h.selectByID(ctx, targetID)
		if err != nil {
			b.Fatalf("gorm 读取目标行失败：%v", err)
		}
		payload := updatePayload(targetID, src.CreatedAt)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := h.updateByID(ctx, payload); err != nil {
				b.Fatalf("gorm Update 失败：%v", err)
			}
		}
	})

	b.Run("gobreath", func(b *testing.B) {
		h := newOrmHarness(b)
		src, err := h.selectByID(ctx, targetID)
		if err != nil {
			b.Fatalf("gobreath 读取目标行失败：%v", err)
		}
		payload := updatePayload(targetID, src.CreatedAt)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := h.updateByID(ctx, payload); err != nil {
				b.Fatalf("gobreath Update 失败：%v", err)
			}
		}
	})
}
