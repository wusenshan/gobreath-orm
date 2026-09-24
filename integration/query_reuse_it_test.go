package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"

	orm "github.com/wusenshan/gobreath-orm"
)

// 一个 Query 允许被复用：查询入口只能在**内部副本**上补本次查询的上下文
// （方言 / 表前缀 / LIMIT），不能写回调用方的 Query。
//
// 回归背景：applyLogic 在「模型没有生效的逻辑删除列」或「已显式 Unscoped」时走短路径，
// 曾经把调用方的 Query 原样返回；而调用方紧接着链的是**就地修改**的 WithDialect /
// WithPrefix / Limit。于是调用方被上一次查询粘住 —— 真库上表现为**静默少取数据**：
// SelectOne 内部补的 LIMIT 1 留在 q 上，之后用同一个 q 查列表只拿回 1 行，且不报任何错。
// 带软删除列的模型本来就返回副本，所以这个坑只在无软删除列的模型上出现 ——
// 也正因如此，既有用例（大多用带软删除的 User）全都看不见它。
func TestQueryReuseAcrossEntrypoints(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		// 前缀 DB + 无软删除列的 PrefUser：正是历史上出问题的那条组合。
		pdb := db.WithPrefix("t_")
		pName := orm.Col[PrefUser](func(p *PrefUser) *string { return &p.Name })

		const rows = 3
		for i := 0; i < rows; i++ {
			p := PrefUser{Name: fmt.Sprintf("r%d", i)}
			if err := orm.Insert(ctx, pdb, &p); err != nil {
				t.Fatalf("[%s] 插入 PrefUser 失败：%v", b.name, err)
			}
		}

		// newQ 每个子用例都取一个全新查询，条件命中全部 3 行。
		newQ := func() *orm.Query[PrefUser] {
			return orm.NewQuery[PrefUser]().LikeRight(pName, "r")
		}

		entries := []struct {
			name string
			run  func(q *orm.Query[PrefUser])
		}{
			{"SelectOne", func(q *orm.Query[PrefUser]) { _, _ = orm.SelectOne[PrefUser](ctx, pdb, q) }},
			{"Page", func(q *orm.Query[PrefUser]) { _, _ = orm.Page[PrefUser](ctx, pdb, q, 1, 2) }},
			{"Count", func(q *orm.Query[PrefUser]) { _, _ = orm.Count[PrefUser](ctx, pdb, q) }},
			{"Exists", func(q *orm.Query[PrefUser]) { _, _ = orm.Exists[PrefUser](ctx, pdb, q) }},
		}

		for _, e := range entries {
			t.Run(e.name, func(t *testing.T) {
				q := newQ()
				before, _ := q.ToSQL()

				e.run(q)

				// ① 查询构造器自身状态不变 —— ToSQL 文档承诺「不会补上 db 级表前缀、方言」。
				if after, _ := q.ToSQL(); after != before {
					t.Fatalf("[%s] %s 把本次查询的上下文写回了调用方：\n前: %s\n后: %s",
						b.name, e.name, before, after)
				}
				// ② 用户可见的后果：复用同一个查询必须仍能取到全部 3 行。
				list, err := orm.SelectList[PrefUser](ctx, pdb, q)
				if err != nil {
					t.Fatalf("[%s] %s 之后复用查询出错：%v", b.name, e.name, err)
				}
				if len(list) != rows {
					t.Fatalf("[%s] %s 之后复用同一个 Query 查列表返回 %d 行，期望 %d 行（LIMIT / 前缀被写回了调用方）",
						b.name, e.name, len(list), rows)
				}
			})
		}

		// DryRun 不执行查询，用 ToSQL 对比即可 —— 它同样不该在调用方留下痕迹。
		t.Run("DryRun", func(t *testing.T) {
			q := newQ()
			before, _ := q.ToSQL()

			sqlStr, _ := orm.DryRun[PrefUser](pdb, q)
			if !strings.Contains(sqlStr, "t_pref_users") {
				t.Fatalf("[%s] DryRun 未应用表前缀，用例失去意义：%s", b.name, sqlStr)
			}
			if after, _ := q.ToSQL(); after != before {
				t.Fatalf("[%s] DryRun 把本次查询的上下文写回了调用方：\n前: %s\n后: %s", b.name, before, after)
			}
		})
	})
}
