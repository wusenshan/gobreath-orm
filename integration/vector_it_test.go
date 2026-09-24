package integration

import (
	"context"
	"testing"

	orm "github.com/wusenshan/gobreath-orm"
)

// 本文件是仓库**第一次**在真库上走向量检索路径。
//
// 背景：v0.1.12 修掉的两个向量 bug（聚合多传一个参数、MySQL 的 Nearest 从未跑通）
// 之所以长期隐藏，根因不是 mock 骗人，而是**一条向量用例都没有** ——
// 而这两个 bug 恰恰是「SQL 字符串看着对、参数个数不对」，正好落在 mock 的盲区里。
//
// 设计原则：把「服务器有没有这个能力」与「我们生成的 SQL 对不对」严格分开。
// 向量能力**不是一个布尔值**，这里按两级探测、走到哪级跑哪级：
//
//	tier 1 存储级：能建向量列、能写入、能读回（PG 的 vector 扩展；MySQL 9+ 的 VECTOR 类型）
//	tier 2 距离级：还能算距离 —— 按距离排序 / 阈值过滤 / 聚合 × 向量（PG 的 <=>/<->/<#>/<+>；
//	               MySQL 需要 VECTOR_DISTANCE，只有 HeatWave on OCI / MySQL AI / Percona 9.7+ 有）
//
// 关键点：**tier 1 可跑 ≠ tier 2 可跑**。MySQL 9 社区版正是这种情况 —— 实测 9.7.2
// 能建 `VECTOR(3)`、能 `STRING_TO_VECTOR` 存取，但 `VECTOR_DISTANCE` / `DISTANCE`
// 都报 `ERROR 1305 FUNCTION ... does not exist`。早期版本把整条路径判为「不支持」
// 一并跳过，于是「能建表、能读写」这一半从来没被真库验证过 ——
// 而 MySQL 的向量读回恰恰是坏在这里（VECTOR 列底层是 BLOB，读回来是二进制不是文本）。
//
// 能力缺失一律**先显式探测、把原始报错打出来**，再按级降级；不允许「因为跑不了所以静默通过」。

// VecDoc 是向量用例的模型。列名用 emb，维度固定 3 便于手算期望距离。
type VecDoc struct {
	ID  int64     `db:"id,pk,autoincrement"`
	Txt string    `db:"txt"`
	Emb []float32 `db:"emb,vector(3)"`
}

func (VecDoc) TableName() string { return "it_vec_docs" }

var (
	vEmb = orm.Col[VecDoc](func(d *VecDoc) *[]float32 { return &d.Emb })
	// vTxt 带字段类型，给 Pluck 用；vTxtCol 只带列名，给谓词用。
	// （TColExpr 嵌了 ColExpr 但 Go 不做隐式转换，谓词处必须显式拿 ColExpr 形态。）
	vTxt    = orm.TCol(func(d *VecDoc) *string { return &d.Txt })
	vTxtCol = orm.Col[VecDoc](func(d *VecDoc) *string { return &d.Txt })
)

// vecCap 是一个后端**实测**得到的向量能力。
type vecCap struct {
	storage  bool   // tier 1：能建向量列并读写
	distance bool   // tier 2：能算距离（排序 / 阈值 / 聚合）
	version  string // 服务器版本，写进日志便于日后复核
	why      string // 缺哪一级能力、原始证据是什么
}

// TestVectorPathOnRealDB 在真库上走向量路径，按实测能力分级执行。
func TestVectorPathOnRealDB(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		cap := probeVector(t, db, b)
		if !cap.storage {
			t.Skipf("[%s] 连向量存储能力都没有，整条向量路径无法真库验证：%s", b.name, cap.why)
		}

		// 本表不进 harness 的 itTables，自己收尾；并且**入口先清干净** ——
		// 上一轮若在某个 t.Fatalf 上中断，就没走到收尾，残留行会让随后每条计数断言
		// 成倍偏大（实测踩过：Nearest + Count = 6）。本用例的期望值全部按
		// 「表里恰好 3 行」手算，所以建表前必须先清。
		if !dropVecTable(t, db, b) {
			t.Fatalf("[%s] 入口清理残留 it_vec_docs 失败，计数断言不可信", b.name)
		}
		t.Cleanup(func() { dropVecTable(t, db, b) })

		rows := vectorSetup(t, db, b)
		vectorStorageChecks(t, db, b, rows)

		if !cap.distance {
			// 这是**能力缺口**，不是我们生成的 SQL 有错：MySQL 9 社区版有 VECTOR 类型
			// 却没有距离函数。把证据留下，等换到 HeatWave / Percona 9.7+ 时这一级会自动开跑。
			t.Logf("[%s] 只验到存储级（tier 1）；距离级（tier 2）用例跳过：%s", b.name, cap.why)
			return
		}
		vectorDistanceChecks(t, db, b)
	})
}

// probeVector 实测当前后端的向量能力。**不做任何跳过** —— 把结论交回调用方，
// 由调用方按级决定跑什么；探测过程本身失败一律带上原始报错作为证据。
func probeVector(t *testing.T, db *orm.DB, b backend) vecCap {
	t.Helper()
	c := vecCap{}
	ctx := context.Background()

	switch b.dialect {
	case orm.PG:
		_ = db.SQL().QueryRowContext(ctx, "SELECT version()").Scan(&c.version)
		if _, err := orm.RawExec(ctx, db, "CREATE EXTENSION IF NOT EXISTS vector"); err != nil {
			c.why = "CREATE EXTENSION vector 失败：" + err.Error()
			return c
		}
		c.storage = true
		var d float64
		if err := db.SQL().QueryRowContext(ctx,
			`SELECT '[1,2,3]'::vector <-> '[1,2,4]'::vector`).Scan(&d); err != nil {
			c.why = "有 vector 类型但 <-> 运算符不可用：" + err.Error()
			return c
		}
		c.distance = true
		t.Logf("[%s] PostgreSQL 的 pgvector 完整可用（tier 1+2）；例：'[1,2,3]' <-> '[1,2,4]' = %v", b.name, d)

	case orm.MySQL:
		_ = db.SQL().QueryRowContext(ctx, "SELECT VERSION()").Scan(&c.version)
		// ① 有没有 VECTOR 类型（MySQL 9.0+ 社区版就有，8.x 没有）
		if _, err := orm.RawExec(ctx, db, "CREATE TEMPORARY TABLE gb_vec_probe (v VECTOR(3))"); err != nil {
			c.why = "MySQL " + c.version + " 不支持 VECTOR 类型：" + err.Error()
			return c
		}
		c.storage = true
		// ② 有没有距离函数（只有 HeatWave on OCI / MySQL AI / Percona 9.7+ 有）
		var d float64
		if err := db.SQL().QueryRowContext(ctx,
			`SELECT VECTOR_DISTANCE(STRING_TO_VECTOR('[1,2,3]'), STRING_TO_VECTOR('[1,2,4]'), 'EUCLIDEAN')`).
			Scan(&d); err != nil {
			c.why = "MySQL " + c.version + " 有 VECTOR 类型，但没有距离函数（社区 / 商业版把 VECTOR_DISTANCE " +
				"留给 HeatWave on OCI / MySQL AI；Percona 9.7+ 才有）：" + err.Error()
			return c
		}
		c.distance = true
		t.Logf("[%s] MySQL %s 具备原生向量能力（tier 1+2）；例：EUCLIDEAN 距离 = %v", b.name, c.version, d)

	default:
		_ = db.SQL().QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&c.version)
		// sqliteDialect 为向量场景生成的是 `<->` / `<=>` / `<#>` / `<+>` 这类运算符
		// （与 pgvector 同名），而 SQLite 本身没有这些运算符 —— 所以 SQLite 上的向量 SQL
		// 只能做形状校验，不能执行。这里把这个事实钉成可复现的探测。
		rows, err := db.SQL().QueryContext(ctx, `SELECT '[1,2,3]' <-> '[1,2,4]'`)
		if err != nil {
			c.why = "SQLite " + c.version + " 无向量运算符：" + err.Error()
			return c
		}
		_ = rows.Close()
		c.storage, c.distance = true, true
		t.Logf("[%s] SQLite %s 竟然接受 <-> 运算符 —— 请核对 sqliteDialect.VectorDistance 的假设是否已过期",
			b.name, c.version)
	}
	return c
}

// dropVecTable 删除本用例的表。返回 false 表示删除失败（调用方按场景决定是否致命）。
func dropVecTable(t *testing.T, db *orm.DB, b backend) bool {
	t.Helper()
	if _, err := orm.RawExec(context.Background(), db, "DROP TABLE IF EXISTS it_vec_docs"); err != nil {
		t.Logf("[%s] DROP TABLE IF EXISTS it_vec_docs 失败：%v", b.name, err)
		return false
	}
	return true
}

// vectorSetup 建表并写入三个向量。三个向量（单位 x、单位 y、接近 x）让
// 「按距离排序」和「阈值过滤」都有可手算的期望值。
func vectorSetup(t *testing.T, db *orm.DB, b backend) []VecDoc {
	t.Helper()
	ctx := context.Background()

	// 向量列在各方言下的物理类型不同（PG vector(3) / MySQL VECTOR(3) / SQLite TEXT），
	// 由 AutoMigrate 按方言生成。
	if err := db.AutoMigrate(ctx, &VecDoc{}); err != nil {
		t.Fatalf("[%s] AutoMigrate(VecDoc) 失败：%v", b.name, err)
	}
	rows := []VecDoc{
		{Txt: "x 轴", Emb: []float32{1, 0, 0}},
		{Txt: "y 轴", Emb: []float32{0, 1, 0}},
		{Txt: "近 x", Emb: []float32{0.9, 0.1, 0}},
	}
	for i := range rows {
		if err := orm.Insert(ctx, db, &rows[i]); err != nil {
			t.Fatalf("[%s] 写入第 %d 个向量行失败：%v", b.name, i, err)
		}
		if rows[i].ID == 0 {
			t.Fatalf("[%s] 写入向量行后主键未回填", b.name)
		}
	}
	return rows
}

// vectorStorageChecks 只依赖「能存能读」，不依赖距离函数 —— tier 1。
//
// 这一级的读回是本文件存在的直接原因：MySQL 的 VECTOR 列底层是 BLOB，
// `SELECT emb` 拿回的是小端序 float32 裸字节，而读路径早期只认 "[1,2,3]" 文本。
func vectorStorageChecks(t *testing.T, db *orm.DB, b backend, rows []VecDoc) {
	t.Helper()
	ctx := context.Background()

	// ① 单行读回（走单行扫描路径）
	back, err := orm.SelectById[VecDoc](ctx, db, rows[2].ID)
	if err != nil || back == nil {
		t.Fatalf("[%s] SelectById 读回向量行失败：%v", b.name, err)
	}
	assertVec(t, b.name, "SelectById 读回", back.Emb, []float32{0.9, 0.1, 0})

	// ② 列表读回（走缓存过的 scanPlan 路径，与单行不是同一条代码路径）
	list, err := orm.SelectList(ctx, db,
		orm.NewQuery[VecDoc]().Eq(vTxtCol, "y 轴"))
	if err != nil {
		t.Fatalf("[%s] SelectList 读回向量行失败：%v", b.name, err)
	}
	if len(list) != 1 {
		t.Fatalf("[%s] 按 txt 过滤应返回 1 行，实际 %d 行", b.name, len(list))
	}
	assertVec(t, b.name, "SelectList 读回", list[0].Emb, []float32{0, 1, 0})

	// ③ 用 UpdateById 改向量列（走 UPDATE 的向量绑定路径：MySQL 要 STRING_TO_VECTOR(?) 包裹），
	//    再读回确认。改成 [0.99,0.01,0] 而不是原值 —— 既证明真的写进去了，
	//    又仍然落在下面 tier 2 的阈值 0.2 之内（L2 距离 ≈0.0141），不破坏期望值。
	upd := rows[2]
	upd.Emb = []float32{0.99, 0.01, 0}
	if err := orm.UpdateById(ctx, db, &upd); err != nil {
		t.Fatalf("[%s] UpdateById 更新向量列失败：%v", b.name, err)
	}
	back2, err := orm.SelectById[VecDoc](ctx, db, rows[2].ID)
	if err != nil || back2 == nil {
		t.Fatalf("[%s] 更新后读回失败：%v", b.name, err)
	}
	assertVec(t, b.name, "UpdateById 后读回", back2.Emb, []float32{0.99, 0.01, 0})

	// ④ 「空向量 / NULL 向量」**不在这里验证**，原因是当前写路径根本表达不出 NULL：
	//    serializeVector 对 `[]float32{}`（TestSerializeVector 的 empty 用例，已是契约）
	//    与 nil 切片**都**产出 "[]" —— 类型 switch 的 `case []float32` 对 nil 同样命中。
	//    而 PG 的 vector 列至少 1 维，写 "[]" 会直接报
	//    `ERROR: vector must have at least 1 dimension (SQLSTATE 22000)`（实测）。
	//    也就是说「向量列存 NULL」目前做不到，需要用 OnlyColumns 把该列排除在写入之外。
	//    这是独立的设计问题，不该混进本用例 —— 记在这里，等真要支持 NULL 向量时再处理。
}

// assertVec 比较向量，失败信息带上出处。
func assertVec(t *testing.T, backend, what string, got, want []float32) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("[%s] %s 得到 %d 维（%v）；期望 %d 维（%v）", backend, what, len(got), got, len(want), want)
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%s] %s 第 %d 个分量 = %v；期望 %v（整串 %v）", backend, what, i, got[i], want[i], got)
			return
		}
	}
}

// vectorDistanceChecks 需要距离函数 —— tier 2。表里必须恰好 3 行。
func vectorDistanceChecks(t *testing.T, db *orm.DB, b backend) {
	t.Helper()
	ctx := context.Background()

	// 最近邻：查询向量 = 单位 x → 首行必须是精确匹配的「x 轴」
	near, err := orm.SelectList(ctx, db, orm.NewQuery[VecDoc]().Nearest(vEmb, []float32{1, 0, 0}, 3))
	if err != nil {
		t.Fatalf("[%s] Nearest 执行失败：%v", b.name, err)
	}
	if len(near) != 3 {
		t.Fatalf("[%s] Nearest(k=3) 返回 %d 行；期望 3", b.name, len(near))
	}
	if near[0].Txt != "x 轴" {
		t.Errorf("[%s] 最近邻首行 = %q；期望「x 轴」（距离 0）", b.name, near[0].Txt)
	}
	// 距离列本身要能读回来（投影里的 AS dist 有没有被正确扫描）
	if near[0].Emb[0] != 1 {
		t.Errorf("[%s] 最近邻首行的向量列 = %v；期望 [1 0 0]", b.name, near[0].Emb)
	}

	// 阈值过滤：L2 距离 < 0.2 → 「x 轴」(0) 与「近 x」(≈0.0141) 命中，「y 轴」(≈1.414) 不中
	within, err := orm.SelectList(ctx, db,
		orm.NewQuery[VecDoc]().WithinDistance(vEmb, []float32{1, 0, 0}, 0.2))
	if err != nil {
		t.Fatalf("[%s] WithinDistance 执行失败：%v", b.name, err)
	}
	if len(within) != 2 {
		t.Errorf("[%s] 阈值 0.2 命中 %d 行；期望 2", b.name, len(within))
	}

	// ---- 聚合 × 向量：v0.1.12 修掉的 bug A 的回归点 ----
	// RAG 里「统计某向量邻域内有多少条」就是这个写法：聚合会关掉距离列与距离排序，
	// 向量参数本就不该出现；修复前这里会报 sql: expected 0 arguments, got 1。
	if n, err := orm.Count(ctx, db, orm.NewQuery[VecDoc]().Nearest(vEmb, []float32{1, 0, 0}, 3)); err != nil {
		t.Errorf("[%s] Nearest + Count 执行失败（bug A 回归点）：%v", b.name, err)
	} else if n != 3 {
		t.Errorf("[%s] Nearest + Count = %d；期望 3", b.name, n)
	}
	if n, err := orm.Count(ctx, db,
		orm.NewQuery[VecDoc]().WithinDistance(vEmb, []float32{1, 0, 0}, 0.2)); err != nil {
		t.Errorf("[%s] WithinDistance + Count 执行失败（阈值路径要保留向量参数）：%v", b.name, err)
	} else if n != 2 {
		t.Errorf("[%s] WithinDistance + Count = %d；期望 2", b.name, n)
	}

	// ---- 度量切换 + 单列投影：bug B 的回归点 ----
	// 位置型方言（MySQL）下，投影去掉距离列之后 ORDER BY 仍要引用向量，
	// 修复前只分配了一份向量参数 → sql: expected 2 arguments, got 1。
	names, err := orm.Pluck(ctx, db,
		orm.NewQuery[VecDoc]().NearestBy(vEmb, []float32{1, 0, 0}, 2, orm.Cosine), vTxt)
	if err != nil {
		t.Errorf("[%s] NearestBy(Cosine) + Pluck 执行失败（bug B 回归点）：%v", b.name, err)
	} else if len(names) != 2 {
		t.Errorf("[%s] NearestBy(Cosine) + Pluck 返回 %d 项；期望 2", b.name, len(names))
	} else if names[0] != "x 轴" {
		t.Errorf("[%s] 余弦最近邻首项 = %q；期望「x 轴」", b.name, names[0])
	}
}
