package integration

import (
	"context"
	"testing"
	"time"

	orm "github.com/wusenshan/gobreath-orm"
)

// TestCrossDriverValueParity 验证 PG 的两条驱动路径（pgx / lib/pq）经框架读出的**用户可见值一致**。
//
// 为什么值得单独一条：两条驱动虽然同为 database/sql 实现，但交给 scan 的 Go 类型**并不一致**。
// 下表是在本机 PostgreSQL + pgvector 上用 `Scan(&any)` 实测到的 %T：
//
//	列类型         pgx                      lib/pq
//	------------   ----------------------   ----------------------
//	vector         string                   []uint8
//	numeric        string                   []uint8
//	uuid           string                   []uint8
//	text[]         string                   []uint8
//	timestamptz    time.Time (+0800 CST)    time.Time (+0000 UTC)
//
// 即 pgx 会把 numeric / uuid / 数组解码成 string，而 lib/pq 一律给 []byte。
// 本用例走的是 RawOne / RawQuery 的**标量**路径：scanRaw 对非结构体目标直接 rows.Scan，
// 类型转换交给 database/sql 的 convertAssign（框架不介入）—— 实测两条驱动在这里结果一致，
// 是标准库兜住的。框架**自己**分派类型的只有模型字段路径（model.go 的 assignString /
// assignFloat ...），那条路径的跨驱动验证见 driver_types_it_test.go。
//
// 断言的是用户可见的等价值（数值、文本内容、时间点），不是驱动实现细节：
// 驱动升级改变返回形态是允许的，但读出的值必须仍然正确。
func TestCrossDriverValueParity(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		if b.dialect != orm.PG {
			t.Skipf("[%s] 本用例取证 PG 的两条驱动路径（pgx / lib/pq），非 PG 后端跳过", b.name)
		}
		ctx := context.Background()

		// numeric：pgx 给 string "1.5"，lib/pq 给 []byte("1.5")
		t.Run("numeric_to_float64", func(t *testing.T) {
			got, err := orm.RawOne[float64](ctx, db, `SELECT 1.5::numeric`)
			if err != nil {
				t.Fatalf("[%s] numeric 落到 float64 失败（lib/pq 在此处给的是 []byte）：%v", b.name, err)
			}
			if got != 1.5 {
				t.Errorf("[%s] numeric = %v；期望 1.5", b.name, got)
			}
		})

		// int8 / int4：两条驱动都给 int64，属于「本就一致」的对照组
		t.Run("int8_to_int64", func(t *testing.T) {
			got, err := orm.RawOne[int64](ctx, db, `SELECT 42::int8`)
			if err != nil {
				t.Fatalf("[%s] int8 落到 int64 失败：%v", b.name, err)
			}
			if got != 42 {
				t.Errorf("[%s] int8 = %v；期望 42", b.name, got)
			}
		})

		// uuid：pgx 给 string，lib/pq 给 []byte
		t.Run("uuid_to_string", func(t *testing.T) {
			const want = "00000000-0000-0000-0000-000000000001"
			got, err := orm.RawOne[string](ctx, db, `SELECT '00000000-0000-0000-0000-000000000001'::uuid`)
			if err != nil {
				t.Fatalf("[%s] uuid 落到 string 失败（lib/pq 在此处给的是 []byte）：%v", b.name, err)
			}
			if got != want {
				t.Errorf("[%s] uuid = %q；期望 %q", b.name, got, want)
			}
		})

		// text[]：pgx 给 {a,b} 的 string，lib/pq 给 []byte。
		// 框架不做数组解析，原样交出 PG 的数组字面量 —— 这里钉住这个已知形态。
		t.Run("text_array_to_string", func(t *testing.T) {
			got, err := orm.RawOne[string](ctx, db, `SELECT ARRAY['a','b']::text[]`)
			if err != nil {
				t.Fatalf("[%s] text[] 落到 string 失败（lib/pq 在此处给的是 []byte）：%v", b.name, err)
			}
			if got != "{a,b}" {
				t.Errorf("[%s] text[] = %q；期望 PG 数组字面量 %q", b.name, got, "{a,b}")
			}
		})

		// timestamptz：驱动不同则返回值的 Location 表示也不同（实测 pgx 给会话时区、
		// lib/pq 归一到 UTC），但**时间点**必须一致。框架不做时区归一化，
		// 所以断言用 Equal（比较时间点）而不是 == 或比较 Location。
		t.Run("timestamptz_time_point", func(t *testing.T) {
			got, err := orm.RawOne[time.Time](ctx, db, `SELECT '2026-09-01 13:00:00+00'::timestamptz`)
			if err != nil {
				t.Fatalf("[%s] timestamptz 落到 time.Time 失败：%v", b.name, err)
			}
			want := time.Date(2026, 9, 1, 13, 0, 0, 0, time.UTC)
			if !got.Equal(want) {
				t.Errorf("[%s] timestamptz 时间点 = %v；期望 %v", b.name, got.UTC(), want)
			}
			t.Logf("[%s] timestamptz 读回 %v（Location=%v）—— 两条驱动的表示不同、时间点相同",
				b.name, got, got.Location())
		})
	})
}
