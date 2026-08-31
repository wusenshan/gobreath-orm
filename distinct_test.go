package orm

import (
	"strings"
	"testing"
)

func TestQueryDistinct(t *testing.T) {
	q := NewQuery[User]().WithDialect(MySQL).Distinct().Select("name").
		Eq(Col[User](func(u *User) *string { return &u.Name }), "a")
	sql, _ := q.Build()
	if !strings.Contains(sql, "SELECT DISTINCT `name` FROM `users`") {
		t.Fatalf("MySQL Distinct 错误: %s", sql)
	}
	if !strings.Contains(sql, "`name` = ?") {
		t.Fatalf("Distinct 应保留 WHERE 条件: %s", sql)
	}
}

func TestQueryDistinctPGWithVector(t *testing.T) {
	// Distinct 与向量检索共存时，distinct 仅作用于普通列，距离列 dist 照常追加
	q := NewQuery[Article]().WithDialect(PG).Distinct().
		Nearest(Col[Article](func(d *Article) *[]float32 { return &d.Embedding }), []float32{0.1, 0.2}, 5)
	sql, _ := q.Build()
	if !strings.Contains(sql, "SELECT DISTINCT *, ") {
		t.Fatalf("PG Distinct + 向量应在 SELECT DISTINCT 后仍有 dist 列: %s", sql)
	}
}
