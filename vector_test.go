package orm

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// Article 向量检索测试模型：embedding 标记为向量列。
type Article struct {
	Id        int64     `db:"id,pk,autoincrement"`
	Title     string    // → title
	Embedding []float32 `db:"embedding,vector"` // → embedding（向量列）
}

// mockDB 用 ormmock 记录型驱动构造指定方言的 DB，便于断言生成的 SQL。
func mockDB(t *testing.T, d Dialect) *DB {
	t.Helper()
	sqlDB, err := sql.Open("ormmock", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return NewDB(sqlDB, d)
}

// vecCol 取 embedding 列的 ColExpr。
func vecCol(t *testing.T) ColExpr {
	t.Helper()
	return Col[Article](func(d *Article) *[]float32 { return &d.Embedding })
}

// --- 向量参数序列化 ---

func TestSerializeVector(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"float32", []float32{0.1, 0.2, -0.3}, "[0.1,0.2,-0.3]"},
		{"float64", []float64{1, 2, 3}, "[1,2,3]"},
		{"string-passthrough", "[4,5,6]", "[4,5,6]"},
		{"empty", []float32{}, "[]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := serializeVector(c.in)
			s, ok := got.(string)
			if !ok {
				t.Fatalf("serializeVector 应返回 string，实际 %T (%v)", got, got)
			}
			if s != c.want {
				t.Fatalf("serializeVector(%v) = %q，期望 %q", c.in, s, c.want)
			}
		})
	}
}

// --- Nearest：方言感知的距离表达式 ---

func TestVectorNearestPG(t *testing.T) {
	q := NewQuery[Article]().WithDialect(PG).Nearest(vecCol(t), []float32{0.1, 0.2, 0.3}, 5)
	sql, args := q.Build()
	want := `SELECT *, "embedding" <-> $1 AS dist FROM "articles" ORDER BY "embedding" <-> $1 ASC LIMIT 5`
	if sql != want {
		t.Fatalf("PG Nearest SQL 错误:\n got = %s\nwant = %s", sql, want)
	}
	if len(args) != 1 || args[0] != "[0.1,0.2,0.3]" {
		t.Fatalf("PG Nearest 参数错误: %v", args)
	}
}

func TestVectorNearestMySQL(t *testing.T) {
	q := NewQuery[Article]().WithDialect(MySQL).Nearest(vecCol(t), []float32{0.1, 0.2, 0.3}, 5)
	sql, args := q.Build()
	want := "SELECT *, VECTOR_DISTANCE(`embedding`, STRING_TO_VECTOR(?), 'EUCLIDEAN') AS dist FROM `articles` ORDER BY VECTOR_DISTANCE(`embedding`, STRING_TO_VECTOR(?), 'EUCLIDEAN') ASC LIMIT 5"
	if sql != want {
		t.Fatalf("MySQL Nearest SQL 错误:\n got = %s\nwant = %s", sql, want)
	}
	// MySQL 的 ? 是位置型占位符：dist 列与 ORDER BY 各写一个 ? 就要两个参数
	// （同一个向量值传两遍）。见 dialect.go 的 positionalPlaceholder。
	if len(args) != 2 || args[0] != "[0.1,0.2,0.3]" || args[1] != "[0.1,0.2,0.3]" {
		t.Fatalf("MySQL Nearest 参数错误（应为两份向量副本）: %v", args)
	}
}

func TestVectorNearestWithWhereFilter(t *testing.T) {
	q := NewQuery[Article]().WithDialect(MySQL).
		Eq(Col[Article](func(d *Article) *string { return &d.Title }), "go").
		NearestBy(vecCol(t), []float32{0.1, 0.2, 0.3}, 5, Cosine)
	sql, args := q.Build()
	if !strings.Contains(sql, "`title` = ?") {
		t.Fatalf("应包含 title 过滤: %s", sql)
	}
	if !strings.Contains(sql, "VECTOR_DISTANCE(`embedding`, STRING_TO_VECTOR(?), 'COSINE') AS dist") {
		t.Fatalf("应包含余弦距离投影: %s", sql)
	}
	if !strings.Contains(sql, "ORDER BY VECTOR_DISTANCE(`embedding`, STRING_TO_VECTOR(?), 'COSINE') ASC LIMIT 5") {
		t.Fatalf("应包含余弦排序+LIMIT: %s", sql)
	}
	// 参数按占位符出现次序：dist 列的向量 → WHERE 的 title → ORDER BY 的向量。
	// MySQL 的 ? 是位置型、逐个消耗参数，两处距离表达式各要一份向量 ——
	// 原先这里断言 len(args)==2（三个 ? 配两个参数），把「MySQL 上必然报
	// "sql: expected 3 arguments, got 2"」的坏行为固化了。
	if len(args) != 3 || args[0] != "[0.1,0.2,0.3]" || args[1] != "go" || args[2] != "[0.1,0.2,0.3]" {
		t.Fatalf("参数顺序/值错误（应为 [向量 go 向量]）: %v", args)
	}
}

// --- 不同度量在 PG 的运算符映射 ---

func TestVectorMetricOperatorsPG(t *testing.T) {
	cases := []struct {
		name string
		m    VectorMetric
		want string // ORDER BY 片段中的运算符
	}{
		{"L2", L2, "<->"},
		{"Cosine", Cosine, "<=>"},
		{"InnerProduct", InnerProduct, "<#>"},
		{"L1", L1, "<+>"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := NewQuery[Article]().WithDialect(PG).NearestBy(vecCol(t), []float32{1, 2, 3}, 3, c.m)
			sql, _ := q.Build()
			if !strings.Contains(sql, `"embedding" `+c.want+` $1`) {
				t.Fatalf("%s 应生成运算符 %q，实际: %s", c.name, c.want, sql)
			}
		})
	}
}

// --- WithinDistance 阈值过滤 ---

func TestVectorWithinDistancePG(t *testing.T) {
	q := NewQuery[Article]().WithDialect(PG).Nearest(vecCol(t), []float32{0.1, 0.2, 0.3}, 10).WithinDistance(vecCol(t), []float32{0.1, 0.2, 0.3}, 0.3)
	sql, args := q.Build()
	if !strings.Contains(sql, `"embedding" <-> $1 < $2`) {
		t.Fatalf("应包含阈值过滤: %s", sql)
	}
	if !strings.Contains(sql, `ORDER BY "embedding" <-> $1 ASC LIMIT 10`) {
		t.Fatalf("应包含排序+LIMIT: %s", sql)
	}
	if len(args) != 2 || args[0] != "[0.1,0.2,0.3]" || args[1] != float64(0.3) {
		t.Fatalf("参数错误: %v", args)
	}
}

func TestVectorWithinDistanceMySQL(t *testing.T) {
	q := NewQuery[Article]().WithDialect(MySQL).WithinDistanceBy(vecCol(t), []float32{0.1, 0.2, 0.3}, 0.3, Cosine)
	sql, _ := q.Build()
	if !strings.Contains(sql, "VECTOR_DISTANCE(`embedding`, STRING_TO_VECTOR(?), 'COSINE') < ?") {
		t.Fatalf("应包含余弦阈值过滤: %s", sql)
	}
}

// --- INSERT 含向量列：方言化的占位符包裹 ---

func TestVectorInsertPG(t *testing.T) {
	db := mockDB(t, PG)
	_ = Insert(context.Background(), db, &Article{Title: "hello", Embedding: []float32{0.1, 0.2, 0.3}})
	want := `INSERT INTO "articles" ("title", "embedding") VALUES ($1, $2)`
	if !strings.Contains(recQuery, want) {
		t.Fatalf("PG 向量 INSERT 应为纯占位符，实际: %s", recQuery)
	}
}

func TestVectorInsertMySQL(t *testing.T) {
	db := mockDB(t, MySQL)
	_ = Insert(context.Background(), db, &Article{Title: "hello", Embedding: []float32{0.1, 0.2, 0.3}})
	want := "INSERT INTO `articles` (`title`, `embedding`) VALUES (?, STRING_TO_VECTOR(?))"
	if !strings.Contains(recQuery, want) {
		t.Fatalf("MySQL 向量 INSERT 应包裹 STRING_TO_VECTOR，实际: %s", recQuery)
	}
}

func TestVectorBatchInsertMySQL(t *testing.T) {
	db := mockDB(t, MySQL)
	_ = BatchInsert(context.Background(), db, []Article{
		{Title: "a", Embedding: []float32{1, 2}},
		{Title: "b", Embedding: []float32{3, 4}},
	})
	if !strings.Contains(recQuery, "STRING_TO_VECTOR(?)") {
		t.Fatalf("MySQL 批量插入应包裹 STRING_TO_VECTOR: %s", recQuery)
	}
}

func TestVectorUpdateByIdMySQL(t *testing.T) {
	db := mockDB(t, MySQL)
	_ = UpdateById(context.Background(), db, &Article{Id: 1, Title: "x", Embedding: []float32{9, 9}})
	if !strings.Contains(recQuery, "`embedding` = STRING_TO_VECTOR(?)") {
		t.Fatalf("MySQL 更新向量列应包裹 STRING_TO_VECTOR: %s", recQuery)
	}
}

// --- WithinDistance 是纯过滤：不投影 dist、不按距离排序 ---
//
// 回归背景（v0.1.15）：此前 WithinDistance 单独使用时，Build 里的
// 「hasVector && !noVecCol → 追加 dist 列」与「orders 为空 → 兜底 ORDER BY 距离」
// 两个分支会同时命中。只想按阈值过滤的调用方于是白付一次全表距离计算 + 排序，
// 结果集还多出一列 dist（连带 SELECT DISTINCT 彻底失效）。现在两者都只跟随 vecOrder
// （只有 Nearest / NearestBy 置位）。
func TestWithinDistanceIsFilterOnly(t *testing.T) {
	vec := []float32{0.1, 0.2, 0.3}
	cases := []struct {
		name  string
		d     Dialect
		where string
	}{
		{"pg", PG, `WHERE "embedding" <-> $1 < $2`},
		{"mysql", MySQL, "WHERE VECTOR_DISTANCE(`embedding`, STRING_TO_VECTOR(?), 'EUCLIDEAN') < ?"},
		{"sqlite", SQLite, `WHERE "embedding" <-> ? < ?`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sqlStr, args := NewQuery[Article]().WithDialect(c.d).
				WithinDistance(vecCol(t), vec, 0.3).Build()
			if !strings.Contains(sqlStr, c.where) {
				t.Fatalf("应保留阈值过滤，实际: %s", sqlStr)
			}
			if strings.Contains(sqlStr, "AS dist") {
				t.Fatalf("纯阈值过滤不应投影 dist 列，实际: %s", sqlStr)
			}
			if strings.Contains(sqlStr, "ORDER BY") {
				t.Fatalf("纯阈值过滤不应按距离排序，实际: %s", sqlStr)
			}
			// 位置型方言每个 ? 各消耗一个参数：向量 + 阈值，共 2 个。
			if len(args) != 2 {
				t.Fatalf("参数应为 2 个（向量 + 阈值），实际 %d: %v", len(args), args)
			}
		})
	}
}

// TestNearestStillProjectsAndOrders 保证收窄 vecOrder 没有伤到近邻检索本身。
func TestNearestStillProjectsAndOrders(t *testing.T) {
	sqlStr, _ := NewQuery[Article]().WithDialect(PG).
		NearestBy(vecCol(t), []float32{0.1, 0.2, 0.3}, 5, Cosine).Build()
	if !strings.Contains(sqlStr, `AS dist`) {
		t.Fatalf("Nearest 应投影 dist 列，实际: %s", sqlStr)
	}
	if !strings.Contains(sqlStr, `ORDER BY "embedding" <=> $1 ASC LIMIT 5`) {
		t.Fatalf("Nearest 应按距离排序并 LIMIT k，实际: %s", sqlStr)
	}
}

// TestDistinctAllowsVectorThreshold：去掉隐式排序后，DISTINCT + 阈值过滤是合法组合，
// 「邻域内去重后有多少条」应当能算 —— 这正是把闸门判据从 hasVector 换成 vecOrder 的理由。
func TestDistinctAllowsVectorThreshold(t *testing.T) {
	q := NewQuery[Article]().WithDialect(PG).Distinct().
		WithinDistance(vecCol(t), []float32{0.1, 0.2, 0.3}, 0.3)
	sqlStr, _ := q.Build()
	if !strings.HasPrefix(sqlStr, `SELECT DISTINCT * FROM "articles" WHERE`) {
		t.Fatalf("应为 SELECT DISTINCT + 阈值过滤，实际: %s", sqlStr)
	}
	if strings.Contains(sqlStr, "ORDER BY") {
		t.Fatalf("阈值过滤不该带排序，实际: %s", sqlStr)
	}
	// Count 走「包一层子查询」才能让去重真实发生。
	inner, _ := q.countBuild()
	want := `SELECT COUNT(*) FROM (SELECT DISTINCT * FROM "articles" WHERE`
	if !strings.Contains(inner, want) {
		t.Fatalf("Count 内层应保留 DISTINCT，实际: %s", inner)
	}
}

// TestDistinctWithNearestStillPanics：按距离排序 + DISTINCT 仍然无解，必须显式失败。
func TestDistinctWithNearestStillPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Distinct + Nearest 应当 panic，实际正常返回")
		}
		if !strings.Contains(fmt.Sprint(r), "Distinct() 不能与按距离排序") {
			t.Fatalf("panic 文案应指明是按距离排序冲突，实际: %v", r)
		}
	}()
	NewQuery[Article]().WithDialect(PG).Distinct().
		NearestBy(vecCol(t), []float32{0.1, 0.2, 0.3}, 5, Cosine).Build()
}
