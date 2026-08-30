package orm

import (
	"context"
	"database/sql"
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
	if len(args) != 1 || args[0] != "[0.1,0.2,0.3]" {
		t.Fatalf("MySQL Nearest 参数错误: %v", args)
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
	// 参数顺序：向量优先（占位符 1），随后是 title 过滤值
	if len(args) != 2 || args[0] != "[0.1,0.2,0.3]" || args[1] != "go" {
		t.Fatalf("参数顺序/值错误: %v", args)
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
