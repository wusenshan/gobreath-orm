package integration

import (
	"context"
	"errors"
	"testing"

	orm "github.com/wusenshan/gobreath-orm"
)

// 本文件是第二轮走查的两条真库回归用例，专挑** mock 测不出来** 的场景：
//
//  1. 乐观锁版本回写 —— mock 上只断言了实体字段，真库才能证明「连续两次
//     更新同一实体」不会再撞上假的 ErrOptimisticLock；
//  2. 近邻检索的 Count 是否尊重 k —— 既有的 Nearest + Count 用例只插 3 行、
//     k 恰好也是 3，全表计数与近邻计数相等，所以「k 被当成 LIMIT 清掉」这条 bug
//     在真库上一直没暴露。这里插 5 行、取 k = 2，两者必然不等。

// TestOptimisticLockWriteBackOnRealDB 同一个实体连续两次 UpdateById 都必须成功。
//
// 修复前 SQL 里是 `SET version = version + 1`，但实体的 Version 字段停在旧值：
// 第二次更新拿旧版本去比对，库里已经是新值 → RowsAffected = 0 → 返回
// ErrOptimisticLock。此时并没有任何并发写者，是一次纯假冲突。
func TestOptimisticLockWriteBackOnRealDB(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()

		doc := Doc{Title: "v1", Version: 1}
		if err := orm.Insert(ctx, db, &doc); err != nil {
			t.Fatalf("[%s] Insert Doc 失败：%v", b.name, err)
		}
		loaded, err := orm.SelectById[Doc](ctx, db, doc.ID)
		if err != nil || loaded == nil {
			t.Fatalf("[%s] 读回 Doc 失败：%v", b.name, err)
		}

		loaded.Title = "v2"
		if err := orm.UpdateById(ctx, db, loaded); err != nil {
			t.Fatalf("[%s] 第一次更新应成功：%v", b.name, err)
		}
		if loaded.Version != 2 {
			t.Fatalf("[%s] 自增后的版本应回写实体（期望 2），实际 %d", b.name, loaded.Version)
		}

		loaded.Title = "v3"
		if err := orm.UpdateById(ctx, db, loaded); err != nil {
			if errors.Is(err, orm.ErrOptimisticLock) {
				t.Fatalf("[%s] 同一实体连续更新报了假的乐观锁冲突（没有并发写者）：%v", b.name, err)
			}
			t.Fatalf("[%s] 第二次更新失败：%v", b.name, err)
		}
		if loaded.Version != 3 {
			t.Fatalf("[%s] 第二次更新后版本应为 3，实际 %d", b.name, loaded.Version)
		}

		after, err := orm.SelectById[Doc](ctx, db, doc.ID)
		if err != nil || after == nil {
			t.Fatalf("[%s] 最终读回失败：%v", b.name, err)
		}
		if after.Version != 3 || after.Title != "v3" {
			t.Fatalf("[%s] 库里的最终状态不对：%+v", b.name, after)
		}
		t.Logf("[%s] 连续两次更新成功，版本 1 → 2 → 3（回写生效）✓", b.name)
	})
}

// TestNearestCountRespectsKOnRealDB 近邻查询的 total 必须是 min(k, 匹配行数)。
//
// 表里插 5 行、k = 2：修好之后 Count 应得 2；修之前 k 被 countBuild 当分页
// LIMIT 一起清掉，得到的是全表行数 5。
func TestNearestCountRespectsKOnRealDB(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		cap := probeVector(t, db, b)
		if !cap.storage || !cap.distance {
			t.Skipf("[%s] 该后端没有可用的向量距离能力，近邻计数无法真库验证：%s", b.name, cap.why)
		}
		// 入口先清：残留行会让计数断言成倍偏大（既有踩坑）。
		if !dropVecTable(t, db, b) {
			t.Fatalf("[%s] 入口清理 it_vec_docs 失败，计数断言不可信", b.name)
		}
		t.Cleanup(func() { dropVecTable(t, db, b) })
		if err := db.AutoMigrate(ctx, &VecDoc{}); err != nil {
			t.Fatalf("[%s] AutoMigrate(VecDoc) 失败：%v", b.name, err)
		}

		const rows = 5
		for i := 0; i < rows; i++ {
			// 沿 x 轴排开，距离互不相等，便于确认「取最近的 k 条」有明确答案。
			d := VecDoc{Txt: "n", Emb: []float32{float32(i) + 1, 0, 0}}
			if err := orm.Insert(ctx, db, &d); err != nil {
				t.Fatalf("[%s] 插入第 %d 行失败：%v", b.name, i, err)
			}
		}

		// 先确认不带 k 时确实是 5 行（排除「表里行数不对」这种假阳性）。
		all, err := orm.Count(ctx, db, orm.NewQuery[VecDoc]())
		if err != nil {
			t.Fatalf("[%s] 全表计数失败：%v", b.name, err)
		}
		if all != rows {
			t.Fatalf("[%s] 表里应有 %d 行，实际计数 %d（残留数据？）", b.name, rows, all)
		}

		const k = 2
		q := orm.NewQuery[VecDoc]().Nearest(vEmb, []float32{0, 0, 0}, k)
		got, err := orm.Count(ctx, db, q)
		if err != nil {
			t.Fatalf("[%s] Nearest + Count 失败：%v", b.name, err)
		}
		if got != k {
			t.Fatalf("[%s] Nearest(k=%d) 的 total 应为 %d，实际 %d —— "+
				"k 若被当成分页 LIMIT 清掉，这里会得到全表行数 %d", b.name, k, k, got, rows)
		}

		// 列表口径同样要受 k 限制，且两者必须一致（分页器的页数才对得上）。
		list, err := orm.SelectList(ctx, db, q)
		if err != nil {
			t.Fatalf("[%s] Nearest 列表失败：%v", b.name, err)
		}
		if len(list) != k {
			t.Fatalf("[%s] Nearest(k=%d) 应返回 %d 行，实际 %d", b.name, k, k, len(list))
		}
		t.Logf("[%s] 近邻计数 = %d（全表 %d 行），k 未被吞掉 ✓", b.name, got, rows)
	})
}
