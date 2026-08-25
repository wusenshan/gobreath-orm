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

	// 仅 Last、无 ForUpdate 时，直接拼在 LIMIT/OFFSET 之后
	q2 := NewQuery[Product]().WithDialect(PG).Eq(nameCol, "x").Limit(5).Last("OFFSET 0")
	s2, _ := q2.Build()
	if !strings.Contains(s2, "LIMIT 5 OFFSET 0") {
		t.Fatalf("Last 应与 LIMIT 衔接: %s", s2)
	}
}

func TestLimitOffsetStillWorks(t *testing.T) {
	nameCol := Col[Product](func(p *Product) *string { return &p.Name })
	q := NewQuery[Product]().WithDialect(SQLite).Eq(nameCol, "x").Limit(20).Offset(40)
	sqlStr, _ := q.Build()
	if !strings.Contains(sqlStr, `LIMIT 20 OFFSET 40`) {
		t.Fatalf("Limit/Offset 拼接错误: %s", sqlStr)
	}
}
