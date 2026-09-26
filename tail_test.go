package orm

import (
	"strings"
	"testing"
)

func TestForUpdateDialects(t *testing.T) {
	nameCol := Col[Product](func(p *Product) *string { return &p.Name })

	// PG / MySQL 应生成 FOR UPDATE；SQLite 自动降级为空（无行级锁）
	for _, tc := range []struct {
		name   string
		d      Dialect
		wantFU bool
	}{
		{"pg", PG, true},
		{"mysql", MySQL, true},
		{"sqlite", SQLite, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := NewQuery[Product]().WithDialect(tc.d).
				Eq(nameCol, "x").
				Limit(10).
				ForUpdate()
			sqlStr, _ := q.Build()
			// FOR UPDATE 必须在 LIMIT 之后
			if tc.wantFU {
				if !strings.HasSuffix(sqlStr, " FOR UPDATE") {
					t.Fatalf("期望以 FOR UPDATE 结尾，实际: %s", sqlStr)
				}
				if !strings.Contains(sqlStr, "LIMIT 10 FOR UPDATE") {
					t.Fatalf("FOR UPDATE 应位于 LIMIT 之后: %s", sqlStr)
				}
			} else {
				if strings.Contains(sqlStr, "FOR UPDATE") {
					t.Fatalf("SQLite 不应生成 FOR UPDATE: %s", sqlStr)
				}
			}
		})
	}
}

func TestLastSuffixAtEnd(t *testing.T) {
	nameCol := Col[Product](func(p *Product) *string { return &p.Name })

	// Last 应位于整个 SQL 的最末尾（在 FOR UPDATE 之后）
	q := NewQuery[Product]().WithDialect(PG).
		Eq(nameCol, "x").
		Limit(10).
		ForUpdate().
		Last("SKIP LOCKED")
	sqlStr, _ := q.Build()
	if !strings.HasSuffix(sqlStr, "SKIP LOCKED") {
		t.Fatalf("Last 应位于最末尾: %s", sqlStr)
	}
	if !strings.Contains(sqlStr, "FOR UPDATE SKIP LOCKED") {
		t.Fatalf("Last 应在 FOR UPDATE 之后: %s", sqlStr)
	}

	// 尾片段自带分页时与 Limit/Offset 互斥：这类组合会拼出两个分页子句的非法 SQL，
	// 现在在构造阶段就 panic（此前要等数据库报语法错误，且定位不到调用处）。
	assertPanics(t, "Last(自带 LIMIT) + Limit", func() {
		NewQuery[Product]().WithDialect(PG).Eq(nameCol, "x").
			Last("ORDER BY id DESC LIMIT 1").Limit(10).Build()
	})
	assertPanics(t, "Last(自带 OFFSET) + Limit", func() {
		NewQuery[Product]().WithDialect(PG).Eq(nameCol, "x").Limit(5).Last("OFFSET 0").Build()
	})

	// 非分页尾片段不受影响：`FOR UPDATE SKIP LOCKED` + Limit 是合法且常用的抢锁写法。
	q3 := NewQuery[Product]().WithDialect(PG).Eq(nameCol, "x").
		Limit(1).ForUpdate().Last("SKIP LOCKED")
	s3, _ := q3.Build()
	if !strings.HasSuffix(s3, "SKIP LOCKED") {
		t.Fatalf("SKIP LOCKED 应保留在末尾: %s", s3)
	}
	if !strings.Contains(s3, "LIMIT 1 FOR UPDATE SKIP LOCKED") {
		t.Fatalf("SKIP LOCKED 与 Limit 应能共存: %s", s3)
	}

	// 单独使用 Last（不带框架分页）时，分页片段照旧原样拼接。
	q4 := NewQuery[Product]().WithDialect(PG).Eq(nameCol, "x").Last("ORDER BY id DESC LIMIT 1")
	s4, _ := q4.Build()
	if !strings.HasSuffix(s4, "ORDER BY id DESC LIMIT 1") {
		t.Fatalf("Last 应原样拼在末尾: %s", s4)
	}
}

// assertPanics 断言 fn 触发 panic（用于校验「构造期即报错」的契约）。
func assertPanics(t *testing.T, what string, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("%s 期望 panic，实际正常返回", what)
		}
	}()
	fn()
}

func TestLimitOffsetStillWorks(t *testing.T) {
	nameCol := Col[Product](func(p *Product) *string { return &p.Name })
	q := NewQuery[Product]().WithDialect(SQLite).Eq(nameCol, "x").Limit(20).Offset(40)
	sqlStr, _ := q.Build()
	if !strings.Contains(sqlStr, `LIMIT 20 OFFSET 40`) {
		t.Fatalf("Limit/Offset 拼接错误: %s", sqlStr)
	}
}
