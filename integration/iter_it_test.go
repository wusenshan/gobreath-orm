package integration

import (
	"context"
	"testing"

	orm "github.com/wusenshan/gobreath-orm"
)

// QueryIter 流式遍历的真库验证。
//
// mock 已覆盖：行序、空结果集、扫描错误通道、提前停止的游标位置。
// 真库要验证的是 mock 给不了的连接语义：游标持有期间连接不归还，
// break 后必须真正归还 —— 归还失败的表现是后续查询取不到连接（连接池耗尽），
// 或游标关闭时驱动报错。以及四方言下逐行产出与 SQL 语义真实一致。

func TestQueryIterOnRealDB(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		users := seedUsers(t, db) // alice/bob/carol/dave

		// ---- 全量遍历：4 行，行序与主键序一致（无 ORDER BY 时不承诺序，
		// 这里只是收集后按键值比对集合，不依赖游标顺序）----
		seen := map[int64]string{}
		n := 0
		for u, err := range orm.QueryIter[User](ctx, db, orm.NewQuery[User]()) {
			if err != nil {
				t.Fatalf("[%s] 遍历出错：%v", b.name, err)
			}
			seen[u.ID] = u.Name
			n++
		}
		if n != 4 {
			t.Fatalf("[%s] 遍历应产出 4 行，实际 %d", b.name, n)
		}
		for _, u := range users {
			if seen[u.ID] != u.Name {
				t.Fatalf("[%s] id=%d 应为 %q，实际 %q", b.name, u.ID, u.Name, seen[u.ID])
			}
		}

		// ---- break 提前停止：第 2 行后断开，连接立即归还 ----
		consumed := 0
		for range orm.QueryIter[User](ctx, db, orm.NewQuery[User]()) {
			consumed++
			if consumed == 2 {
				break
			}
		}
		if consumed != 2 {
			t.Fatalf("[%s] break 前应消耗 2 行，实际 %d", b.name, consumed)
		}
		// 连接已归还：立刻再发查询必须正常（若游标未 Close，
		// 小连接池下这里会挂起或报连接耗尽）。
		after, err := orm.Count[User](ctx, db, orm.NewQuery[User]())
		if err != nil || after != 4 {
			t.Fatalf("[%s] break 后连接应可复用：count=%d err=%v", b.name, after, err)
		}

		// ---- 逻辑删除口径：软删 1 行后只遍历得到 3 行 ----
		if err := orm.DeleteById[User](ctx, db, users[0].ID); err != nil {
			t.Fatalf("[%s] 软删出错：%v", b.name, err)
		}
		m := 0
		for _, err := range orm.QueryIter[User](ctx, db, orm.NewQuery[User]()) {
			if err != nil {
				t.Fatalf("[%s] 软删后遍历出错：%v", b.name, err)
			}
			m++
		}
		if m != 3 {
			t.Fatalf("[%s] 软删 1 行后应遍历得到 3 行，实际 %d", b.name, m)
		}

		// ---- Limit 生效：遍历到 LIMIT 就停 ----
		k := 0
		for _, err := range orm.QueryIter[User](ctx, db, orm.NewQuery[User]().Limit(2)) {
			if err != nil {
				t.Fatalf("[%s] Limit 遍历出错：%v", b.name, err)
			}
			k++
		}
		if k != 2 {
			t.Fatalf("[%s] Limit(2) 应只产出 2 行，实际 %d", b.name, k)
		}
	})
}
