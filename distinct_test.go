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

// TestCountRejectsDistinctWithVector Count / Page 必须与 SelectList 走同一条闸门。
//
// 这个用例此前断言的正是缺陷本身：标题写「Count 的内层不带 dist，那里不存在去重
// 失效问题，必须照常可用」，但只检查了 SQL 里没有 dist 列，**没检查 ORDER BY**。
// 实际内层是
//
//	SELECT DISTINCT * FROM "articles" ORDER BY "emb" <-> $1 LIMIT 5
//
// 关掉 dist 只影响投影，按距离排序仍由 hasVector 兜底生成 —— 真库实测三种形态：
// PG 一律报 "for SELECT DISTINCT, ORDER BY expressions must appear in select list"；
// MySQL 只在排序列不在投影里时报 3065（默认 `DISTINCT *` 反而接受）；SQLite 完全接受。
// 同一条查询跨方言「一个炸两个过」正是要拦住的东西，而清掉排序也不是出路：
// LIMIT k 会截到任意 k 行，Count 静默给出无意义的数字。
func TestCountRejectsDistinctWithVector(t *testing.T) {
	db := mockDB(t, PG)
	vec := Col[Article](func(d *Article) *[]float32 { return &d.Embedding })

	cases := []struct {
		name string
		run  func()
	}{
		{"Count", func() {
			_, _ = Count(context.Background(), db, NewQuery[Article]().WithDialect(PG).Distinct().
				Nearest(vec, []float32{0.1, 0.2}, 5))
		}},
		{"Page", func() {
			_, _ = Page[Article](context.Background(), db, NewQuery[Article]().WithDialect(PG).Distinct().
				Nearest(vec, []float32{0.1, 0.2}, 5), 1, 10)
		}},
		{"Sum", func() {
			_, _ = Sum[Article](context.Background(), db, NewQuery[Article]().WithDialect(PG).Distinct().
				Nearest(vec, []float32{0.1, 0.2}, 5),
				Col[Article](func(a *Article) *int64 { return &a.Id }))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("[%s] Distinct + 向量必须显式 panic，实际放行到了数据库（会拿到语法错误或静默错误结果）", c.name)
				}
				if msg := fmt.Sprint(r); !strings.Contains(msg, "Distinct") {
					t.Fatalf("[%s] panic 信息应点名 Distinct 冲突，实际: %s", c.name, msg)
				}
			}()
			c.run()
		})
	}
}
