package integration

import (
	"context"
	"testing"

	orm "github.com/wusenshan/gobreath-orm"
)

// 本文件延续 fix_it_test.go 的定位：把「单元测试只断言 SQL 形状」的修复
// 放到真库上取证一遍。SQL 拼得对不等于数据去向对 —— 尤其是
// 「该报错却把字符串拼进了 SQL」和「聚合返回类型与字段类型错开」这两类。

// TestFixMapUpdateRejectsUnknownColumnOnRealDB —— map 形式的部分更新必须拒绝
// 不存在的列名，而不是把字符串原样拼进 SET 发给数据库。
//
// 旧行为下 `map[string]any{"name = 'x' OR 1=1 --": ...}` 会生成
// `UPDATE t SET "name = 'x' OR 1=1 --" = $1`：列名来自外部输入时是注入口子，
// 即使纯属拼写错误（"nmae"）也只能等数据库报 unknown column。
//
// 注：这里只覆盖 UpdateByIdSets。UpdateSets 的列名来自 ColExpr，包外构造不出
// 任意字符串（字段未导出），该路径由单元测试固定。
func TestFixMapUpdateRejectsUnknownColumnOnRealDB(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		seedUsers(t, db)

		bad := []string{"nmae", `name = 'x' OR 1=1 --`}
		for _, col := range bad {
			if _, err := orm.UpdateByIdSets[User](ctx, db, int64(1), map[string]any{col: "x"}); err == nil {
				t.Errorf("[%s] UpdateByIdSets 未拒绝未知列 %q（本应在框架侧报错）", b.name, col)
			}
		}

		// 拒绝必须发生在拼 SQL 之前：数据不能被改坏。
		got, err := orm.SelectById[User](ctx, db, int64(1))
		if err != nil {
			t.Fatalf("[%s] 读回失败：%v", b.name, err)
		}
		if got.Name != "alice" {
			t.Errorf("[%s] 被拒绝的更新不应改动数据，name 变成 %q", b.name, got.Name)
		}

		// 合法列名照常可用，且真库确已生效。
		if _, err := orm.UpdateByIdSets[User](ctx, db, got.ID, map[string]any{"name": "alice2"}); err != nil {
			t.Fatalf("[%s] 合法列名更新失败：%v", b.name, err)
		}
		after, err := orm.SelectById[User](ctx, db, got.ID)
		if err != nil {
			t.Fatalf("[%s] 二次读回失败：%v", b.name, err)
		}
		if after.Name != "alice2" {
			t.Errorf("[%s] 合法更新未生效，name=%q", b.name, after.Name)
		}
	})
}

// F32Vec 专供浮点向量往返取证。值刻意选成 float32 无法精确表示的小数
// （0.1 / 0.2 / 0.3），任何一处按 float64 处理都会在往返后露出差异。
type F32Vec struct {
	ID  int64     `db:"id,pk,autoincrement"`
	Emb []float32 `db:"emb,vector(3)"`
}

func (F32Vec) TableName() string { return "it_f32_vecs" }

// TestFloat32VectorRoundTripOnRealDB —— 向量列的写入文本与读回值必须自洽。
//
// 背景：formatFloatVal 曾把 float32 提升为 float64 后按 64 位格式化，
// []float32{0.1} 会写成 "0.10000000149011612"（[]float32 分支走 floats32ToText
// 得到的是 "0.1"）—— 同一种语义的两种容器写出不同文本。
//
// 取证边界要说清楚：float4 解析会把两种文本重新归一到同一个 bit pattern，
// 所以**存储级 round-trip 对精度位本身不敏感**（该差异由单元测试逐字固定）。
// 本用例的价值在另一头：向量列在 PG/MySQL/SQLite 下的物理类型各不相同
// （vector / VECTOR(BLOB) / TEXT），往返一次能证明写入路径与读回路径在
// 「同一份浮点数据」上闭合，且小数不会被截断成整数或错误端序。
func TestFloat32VectorRoundTripOnRealDB(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		if _, err := orm.RawExec(ctx, db, "DROP TABLE IF EXISTS it_f32_vecs"); err != nil {
			t.Fatalf("[%s] 清理残留表失败：%v", b.name, err)
		}
		if err := db.AutoMigrate(ctx, &F32Vec{}); err != nil {
			t.Skipf("[%s] 无向量列建表能力，跳过：%v", b.name, err)
		}
		t.Cleanup(func() {
			_, _ = orm.RawExec(context.Background(), db, "DROP TABLE IF EXISTS it_f32_vecs")
		})

		want := []float32{0.1, 0.2, 0.3}
		row := F32Vec{Emb: want}
		if err := orm.Insert(ctx, db, &row); err != nil {
			t.Skipf("[%s] 向量列写入不支持，跳过：%v", b.name, err)
		}
		if row.ID == 0 {
			t.Fatalf("[%s] 向量行写入后主键未回填", b.name)
		}

		back, err := orm.SelectById[F32Vec](ctx, db, row.ID)
		if err != nil {
			t.Fatalf("[%s] 读回向量行失败：%v", b.name, err)
		}
		assertVec(t, b.name, "float32 向量往返", back.Emb, want)
	})
}

// TestSumOfIntColumnOnRealDB —— 聚合投影对整数列的强类型路径。
//
// 可疑点：`SUM(int)` 在各库里都不是 int4/int8 —— PG 是 numeric、MySQL 是 DECIMAL、
// SQLite 是 INTEGER —— 经 database/sql 交回来可能是文本形态。aggScalarAs 用
// sql.Null[int] 承接这一口径，只有真库才知道驱动会不会在这里翻车
// （单元测试用的是 mock 行，永远给的是理想类型）。
func TestSumOfIntColumnOnRealDB(t *testing.T) {
	forEachBackend(t, func(t *testing.T, db *orm.DB, b backend) {
		ctx := context.Background()
		seedUsers(t, db) // age: 30 25 35 25 → SUM=115, AVG=28.75

		sum, err := orm.SumOf[User, int](ctx, db, orm.NewQuery[User](), uAgeT)
		if err != nil {
			t.Fatalf("[%s] SumOf[int] 失败：%v", b.name, err)
		}
		if sum != 115 {
			t.Errorf("[%s] SumOf[int] = %v, want 115", b.name, sum)
		}

		// AVG 恒为小数，而列类型是 int：TColExpr 的 F 与列类型强绑定（不协变），
		// 所以这种「结果类型 ≠ 列类型」的聚合只能走 Col 形态的 Avg（返回 float64）。
		avg, err := orm.Avg[User](ctx, db, orm.NewQuery[User](),
			orm.Col[User](func(u *User) *int { return &u.Age }))
		if err != nil {
			t.Fatalf("[%s] Avg 失败：%v", b.name, err)
		}
		if avg != 28.75 {
			t.Errorf("[%s] Avg = %v, want 28.75", b.name, avg)
		}

		// 单列投影（Pluck）走的是 sql.Null[F]，同样是「驱动给什么就得吃什么」。
		ages, err := orm.Pluck[User, int](ctx, db,
			orm.NewQuery[User]().OrderBy(uAge, true).OrderBy(uID, true), uAgeT)
		if err != nil {
			t.Fatalf("[%s] Pluck[int] 失败：%v", b.name, err)
		}
		want := []int{25, 25, 30, 35}
		if len(ages) != len(want) {
			t.Fatalf("[%s] Pluck 返回 %d 项，want %d：%v", b.name, len(ages), len(want), ages)
		}
		for i := range want {
			if ages[i] != want[i] {
				t.Errorf("[%s] Pluck[%d] = %v, want %v（全量 %v）", b.name, i, ages[i], want[i], ages)
			}
		}
	})
}
