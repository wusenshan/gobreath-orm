package integration

import (
	"context"
	"fmt"
	"testing"

	orm "github.com/wusenshan/gobreath-orm"
)

// 本文件是「已修缺陷」的真库回归：单元测试断言的是生成出来的 SQL 形状，
// 这里断言的是数据在真库里的最终去向 —— 静默丢数据、关联整片为空这类问题，
// 只有在真库上「写进去再读回来」才算取证完成。

// TestFixBatchOmitZeroOnRealDB —— P0：批量写入 + OmitZero 不得静默丢数据。
//
// 旧实现用 entities[0] 一行的零值情况决定整批的列集合：只要后续行有任何非零字段
// 恰好落在「首行的零值列」里，该字段在 SQL 里根本不出现（不报错），
// 调用方以为已经写进去了。这里写→读逐行比对。
func TestFixBatchOmitZeroOnRealDB(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		rows := []ZeroUser{
			{ID: 1, Name: "", Age: 0},       // 首行全零 —— 旧实现据此定列
			{ID: 2, Name: "second", Age: 0}, // 非零值正落在首行的零值列 name 上
			{ID: 3, Name: "", Age: 42},      // 非零值落在首行的零值列 age 上
		}
		if err := orm.BatchInsert(ctx, db, rows, orm.OmitZero()); err != nil {
			t.Fatalf("[%s] BatchInsert 失败：%v", b.name, err)
		}

		got, err := orm.SelectList(ctx, db, orm.NewQuery[ZeroUser]())
		if err != nil {
			t.Fatalf("[%s] 读回失败：%v", b.name, err)
		}
		if len(got) != 3 {
			t.Fatalf("[%s] 写入 3 行，读回 %d 行", b.name, len(got))
		}
		byID := make(map[int64]ZeroUser, len(got))
		for _, r := range got {
			byID[r.ID] = r
		}
		if r, ok := byID[2]; !ok {
			t.Errorf("[%s] 主键 2 的行没写进去", b.name)
		} else if r.Name != "second" {
			t.Errorf("[%s] 第二行的非零 name 丢失：%+v", b.name, r)
		}
		if r, ok := byID[3]; !ok {
			t.Errorf("[%s] 主键 3 的行没写进去", b.name)
		} else if r.Age != 42 {
			t.Errorf("[%s] 第三行的非零 age 丢失：%+v", b.name, r)
		}
	})
}

// TestFixEmptyInOnRealDB —— P0：In(空) 折叠为恒假、NotIn(空) 折叠为恒真。
//
// 旧实现把空切片直接拼成 `IN ()`：MySQL 1064 / PG 42601 / SQLite 语法错，
// 数据库侧报错且定位不到调用点。动态多选条件「一个都没勾」是常态输入。
func TestFixEmptyInOnRealDB(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		seedUsers(t, db) // 4 行基准数据

		list, err := orm.SelectList(ctx, db, orm.NewQuery[User]().In(uID, []any{}))
		if err != nil {
			t.Fatalf("[%s] In(空) 把非法 SQL 抛给了数据库：%v", b.name, err)
		}
		if len(list) != 0 {
			t.Errorf("[%s] In(空) 应恒假（0 行），实际 %d 行", b.name, len(list))
		}

		all, err := orm.SelectList(ctx, db, orm.NewQuery[User]().NotIn(uID, nil))
		if err != nil {
			t.Fatalf("[%s] NotIn(空) 失败：%v", b.name, err)
		}
		if len(all) != 4 {
			t.Errorf("[%s] NotIn(空) 应恒真（4 行），实际 %d 行", b.name, len(all))
		}
	})
}

// TestFixPreloadCrossTypeKeyOnRealDB —— P0：Preload 的键匹配必须按「值」归一化。
//
// 父表主键 int64、子表外键 int：库里的值完全一致，但 reflect.DeepEqual(int64(1), int(1))
// 是 false —— 旧实现下所有关联都为空，且不报任何错（分页/批量预加载最容易被它坑到）。
func TestFixPreloadCrossTypeKeyOnRealDB(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()

		users := []TypedUser{{ID: 1, Name: "u1"}, {ID: 2, Name: "u2"}, {ID: 3, Name: "u3"}}
		if err := orm.BatchInsert(ctx, db, users); err != nil {
			t.Fatalf("[%s] 插入 TypedUser 失败：%v", b.name, err)
		}
		notes := []TypedNote{
			{ID: 1, UserID: 1, Body: "n1"},
			{ID: 2, UserID: 1, Body: "n2"},
			{ID: 3, UserID: 2, Body: "n3"},
		}
		if err := orm.BatchInsert(ctx, db, notes); err != nil {
			t.Fatalf("[%s] 插入 TypedNote 失败：%v", b.name, err)
		}

		list, err := orm.SelectList(ctx, db, orm.NewQuery[TypedUser]())
		if err != nil {
			t.Fatalf("[%s] SelectList TypedUser 失败：%v", b.name, err)
		}
		if err := orm.Preload(ctx, db, &list, "Notes"); err != nil {
			t.Fatalf("[%s] Preload 失败：%v", b.name, err)
		}
		want := map[int64]int{1: 2, 2: 1, 3: 0}
		for _, u := range list {
			if len(u.Notes) != want[u.ID] {
				t.Errorf("[%s] 用户 %d 的 Notes 有 %d 条；期望 %d（int64 主键 vs int 外键未匹配）",
					b.name, u.ID, len(u.Notes), want[u.ID])
			}
		}
	})
}

// TestFixPreloadChunkedLargeSetOnRealDB —— P1：关联键超过 500 必须分片查询后合并。
//
// 不分片时 IN 的绑定参数会随集合线性增长，撞上 SQLite 999 / PG 65535 之类的上限；
// 这里取 501 个父键，刚好跨过 preloadInChunk=500 的分片边界。
func TestFixPreloadChunkedLargeSetOnRealDB(t *testing.T) {
	const n = 501
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()

		users := make([]TypedUser, 0, n)
		for i := 1; i <= n; i++ {
			users = append(users, TypedUser{ID: int64(i), Name: fmt.Sprintf("u%04d", i)})
		}
		if err := orm.BatchInsert(ctx, db, users); err != nil {
			t.Fatalf("[%s] 批量插入 %d 个父行失败：%v", b.name, n, err)
		}
		notes := make([]TypedNote, 0, n)
		for i := 1; i <= n; i++ {
			// 每行都挂一条；外键是 int（与父表 int64 不同），顺带再压一次跨类型匹配。
			notes = append(notes, TypedNote{ID: int64(i), UserID: i, Body: fmt.Sprintf("b%04d", i)})
		}
		if err := orm.BatchInsert(ctx, db, notes); err != nil {
			t.Fatalf("[%s] 批量插入子行失败：%v", b.name, err)
		}

		list, err := orm.SelectList(ctx, db, orm.NewQuery[TypedUser]())
		if err != nil {
			t.Fatalf("[%s] SelectList 失败：%v", b.name, err)
		}
		if err := orm.Preload(ctx, db, &list, "Notes"); err != nil {
			t.Fatalf("[%s] Preload(%d 个键) 失败：%v", b.name, n, err)
		}
		if len(list) != n {
			t.Fatalf("[%s] 父行读回 %d 条；期望 %d", b.name, len(list), n)
		}
		missing := 0
		for _, u := range list {
			if len(u.Notes) != 1 {
				missing++
			}
		}
		if missing != 0 {
			t.Errorf("[%s] %d/%d 行的关联未命中（分片边界 500 附近最易漏）", b.name, missing, n)
		}
	})
}
