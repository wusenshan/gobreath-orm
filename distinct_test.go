package orm

import (
	"context"
	"fmt"
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

// TestQueryDistinctRejectsVector Distinct 与向量检索不能共存，必须是显式失败。
//
// 这个用例此前断言的正是缺陷本身：它把 `SELECT DISTINCT *, <dist> FROM ...`
// 当成期望值。而 dist 每行都不同 → DISTINCT 作用在整行上等于没去重；
// 更糟的是 Count 的内层关掉了 dist（noVecCol），那里是真去重 ——
// 列表行数与分页总数按两套口径算，对不上且不报错。
// 也不能靠「DISTINCT 时不追加 dist」绕过：PG 要求 SELECT DISTINCT 的 ORDER BY
// 表达式必须在选择列表里（MySQL 8 同样拒绝），去掉 dist 排序就是非法 SQL。
func TestQueryDistinctRejectsVector(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Distinct + 向量检索应 panic，实际静默生成了失效的 SQL")
		}
		if msg := fmt.Sprint(r); !strings.Contains(msg, "Distinct") {
			t.Fatalf("panic 信息应说明是 Distinct 与向量检索冲突，实际: %s", msg)
		}
	}()
	q := NewQuery[Article]().WithDialect(PG).Distinct().
		Nearest(Col[Article](func(d *Article) *[]float32 { return &d.Embedding }), []float32{0.1, 0.2}, 5)
	_, _ = q.Build()
}

// TestCountStillAllowsDistinctWithVector Count 的内层不带 dist 列（noVecCol），
// 那里不存在「去重失效」的问题，因此必须照常可用 —— 否则上面的闸门会误伤分页总数。
func TestCountStillAllowsDistinctWithVector(t *testing.T) {
	db := mockDB(t, PG)
	q := NewQuery[Article]().WithDialect(PG).Distinct().
		Nearest(Col[Article](func(d *Article) *[]float32 { return &d.Embedding }), []float32{0.1, 0.2}, 5)
	if _, err := Count(context.Background(), db, q); err != nil {
		t.Fatalf("Count 不应受 Distinct+向量的闸门影响: %v", err)
	}
	if !strings.Contains(recQuery, "SELECT COUNT(*) FROM (") {
		t.Fatalf("Distinct 的 COUNT 应走子查询，实际: %s", recQuery)
	}
	if strings.Contains(recQuery, "dist") {
		t.Fatalf("Count 内层不该带 dist 列: %s", recQuery)
	}
}
