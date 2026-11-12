package integration

import (
	"context"
	"fmt"
	"testing"

	orm "github.com/wusenshan/gobreath-orm"
)

// Remove 家族（带行级审计的物理删除）的真库验证。
//
// 为什么必须真库：mock 层能断言「先 SELECT 再 DELETE」的 SQL 形状与实参，但断不出
// **软删行到底有没有被捞出来**、**DELETE 之后库里真的少了几行**。这两件事是 Remove
// 的全部价值所在（穿透软删 + 钩子快照 == 实际删除行），只能在真库上数行数。
func TestRemoveFamilyOnRealDB(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		users := seedUsers(t, db) // alice/bob/carol/dave

		// 先把 alice 软删：Remove 的语义是穿透软删，她必须被物理删掉。
		if err := orm.DeleteById[User](ctx, db, users[0].ID); err != nil {
			t.Fatalf("[%s] 软删 alice 失败：%v", b.name, err)
		}

		var events []orm.RemoveEvent
		audited := db.WithRemoveListener(func(ev orm.RemoveEvent) { events = append(events, ev) })

		// ---- 单个实体：软删行照样物理删除，快照带完整数据 ----
		n, err := orm.Remove[User](ctx, audited, &users[0])
		if err != nil || n != 1 {
			t.Fatalf("[%s] Remove 单个实体：n=%d err=%v", b.name, n, err)
		}
		if len(events) != 1 {
			t.Fatalf("[%s] 应发 1 个事件，实际 %d", b.name, len(events))
		}
		rows, ok := events[0].Rows.([]*User)
		if !ok || len(rows) != 1 || rows[0].Name != "alice" || rows[0].DeletedAt == nil {
			t.Fatalf("[%s] 快照应是被删的软删行 alice（含 deleted_at），实际 %#v", b.name, events[0].Rows)
		}
		if events[0].Count != 1 || len(events[0].PKs) != 1 {
			t.Fatalf("[%s] 事件 Count/PKs 异常：count=%d pks=%v", b.name, events[0].Count, events[0].PKs)
		}

		// 物理确认：Unscoped 也查不到 alice 了（真删，不是又标记一次）。
		gone, err := orm.SelectOne[User](ctx, db,
			orm.NewQuery[User]().Unscoped().Eq(orm.Col[User](func(u *User) *int64 { return &u.ID }), users[0].ID))
		if err == nil && gone != nil {
			t.Fatalf("[%s] alice 应已被物理删除，Unscoped 仍能查到：%+v", b.name, *gone)
		}

		// ---- 多个实体：一次删 bob + carol ----
		events = nil
		n, err = orm.Remove[User](ctx, audited, &users[1], &users[2])
		if err != nil || n != 2 {
			t.Fatalf("[%s] Remove 多实体：n=%d err=%v", b.name, n, err)
		}
		if len(events) != 1 || len(events[0].PKs) != 2 {
			t.Fatalf("[%s] 多实体应 1 个事件 / 2 个主键，实际 %d/%v", b.name, len(events), events)
		}

		// ---- 条件删除：剩下的 dave 按条件删除，返回快照 ----
		events = nil
		delRows, n, err := orm.RemoveQuery[User](ctx, audited,
			orm.NewQuery[User]().Eq(orm.Col[User](func(u *User) *string { return &u.Name }), "dave"))
		if err != nil || n != 1 || len(delRows) != 1 || delRows[0].Name != "dave" {
			t.Fatalf("[%s] RemoveQuery：n=%d rows=%v err=%v", b.name, n, delRows, err)
		}
		if len(events) != 1 || events[0].PKs[0] == "" {
			t.Fatalf("[%s] RemoveQuery 事件异常：%#v", b.name, events)
		}

		// ---- 全表已清空（物理层面）----
		left, err := orm.SelectList[User](ctx, db, orm.NewQuery[User]().Unscoped())
		if err != nil {
			t.Fatalf("[%s] Unscoped SelectList 失败：%v", b.name, err)
		}
		if len(left) != 0 {
			t.Fatalf("[%s] 全部 Remove 后表里应 0 行（物理删除），实际 %d", b.name, len(left))
		}

		// ---- RemoveSQL：用自定义 SELECT 圈定范围，含软删行 ----
		more := seedUsers(t, db)
		if _, err := orm.DeleteByIds[User](ctx, db, more[0].ID, more[1].ID); err != nil {
			t.Fatalf("[%s] 批量软删失败：%v", b.name, err)
		}
		events = nil
		sqlRows, n, err := orm.RemoveSQL[User](ctx, audited,
			fmt.Sprintf("SELECT * FROM it_users WHERE name IN ('%s', '%s')", more[0].Name, more[1].Name))
		if err != nil || n != 2 || len(sqlRows) != 2 {
			t.Fatalf("[%s] RemoveSQL：n=%d rows=%d err=%v", b.name, n, len(sqlRows), err)
		}
		for _, r := range sqlRows {
			if r.DeletedAt == nil {
				t.Fatalf("[%s] RemoveSQL 应连软删行一起捞出来（穿透软删），实际 %+v", b.name, *r)
			}
		}
		if len(events) != 1 || events[0].Count != 2 {
			t.Fatalf("[%s] RemoveSQL 事件应为 1 批 2 行，实际 %#v", b.name, events)
		}
		// 剩下的两行（未软删）仍在，且未被误删。
		if rest, err := orm.SelectList[User](ctx, db, orm.NewQuery[User]()); err != nil || len(rest) != 2 {
			t.Fatalf("[%s] 未列入的行不应被删，实际 %d err=%v", b.name, len(rest), err)
		}

	})
}

// TestRemoveGuardsOnRealDB 守卫在真库上同样「零 SQL 下发」：
// 零主键 / 无条件 / 表不匹配都必须在数据库侧留下零痕迹。
func TestRemoveGuardsOnRealDB(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		seedUsers(t, db)

		if _, err := orm.Remove[User](ctx, db, &User{Name: "no-pk"}); err == nil {
			t.Fatalf("[%s] 零主键应报错", b.name)
		}
		if _, _, err := orm.RemoveQuery[User](ctx, db, orm.NewQuery[User]()); err == nil {
			t.Fatalf("[%s] 无条件 RemoveQuery 应报错（全表物理删除）", b.name)
		}
		if _, _, err := orm.RemoveSQL[User](ctx, db, "SELECT * FROM it_orders WHERE id > 0"); err == nil {
			t.Fatalf("[%s] RemoveSQL 表与实体不一致应报错", b.name)
		}
		// 4 行都还在。
		all, err := orm.SelectList[User](ctx, db, orm.NewQuery[User]())
		if err != nil || len(all) != 4 {
			t.Fatalf("[%s] 守卫失败时不应删掉任何行，实际 %d err=%v", b.name, len(all), err)
		}
	})
}
