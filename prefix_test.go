package orm

import (
	"context"
	"strings"
	"testing"
)

// Product 未实现 TableName()，表名自动推导为 products，可测试前缀拼接。
type Product struct {
	Id   int64  `db:"id,pk,autoincrement"`
	Name string `db:"name"`
}

func TestNewQueryAutoPrefix(t *testing.T) {
	q := NewQuery[Product]().
		WithDialect(SQLite).
		WithPrefix("t_").
		Eq(Col[Product](func(p *Product) *string { return &p.Name }), "x")
	sqlStr, _ := q.Build()
	if !strings.Contains(sqlStr, `FROM "t_products"`) {
		t.Fatalf("自动推导表名未加前缀: %s", sqlStr)
	}
	if !strings.Contains(sqlStr, `"name" = ?`) {
		t.Fatalf("where 错误: %s", sqlStr)
	}
}

func TestNewQueryNoPrefixWhenEmpty(t *testing.T) {
	// 未设置前缀时与原来行为一致
	q := NewQuery[Product]().WithDialect(SQLite)
	sqlStr, _ := q.Build()
	if !strings.Contains(sqlStr, `FROM "products"`) {
		t.Fatalf("无前缀时应为 products: %s", sqlStr)
	}
}

func TestTableNameExplicitNoPrefix(t *testing.T) {
	// User 实现了 TableName() 返回 "users"，即使开了前缀也不应叠加
	q := NewQuery[User]().WithDialect(SQLite).WithPrefix("t_")
	sqlStr, _ := q.Build()
	if strings.Contains(sqlStr, `FROM "t_users"`) {
		t.Fatalf("显式 TableName 不应加前缀: %s", sqlStr)
	}
	if !strings.Contains(sqlStr, `FROM "users"`) {
		t.Fatalf("显式 TableName 应原样保留: %s", sqlStr)
	}
}

func TestTableOverrideNoPrefix(t *testing.T) {
	// .Table() 显式指定，前缀被忽略
	q := NewQuery[User]().WithDialect(SQLite).WithPrefix("t_").Table("my_orders")
	sqlStr, _ := q.Build()
	if !strings.Contains(sqlStr, `FROM "my_orders"`) {
		t.Fatalf(".Table() 覆盖名应保持原样: %s", sqlStr)
	}
	if strings.Contains(sqlStr, "t_my_orders") {
		t.Fatalf(".Table() 不应叠加前缀: %s", sqlStr)
	}
}

func TestTableOverrideSchemaNoPrefix(t *testing.T) {
	// .Table() 显式指定（含 schema 限定），前缀被忽略，原样保留
	q := NewQuery[Product]().WithDialect(SQLite).WithPrefix("t_").Table("public.products")
	sqlStr, _ := q.Build()
	if !strings.Contains(sqlStr, `FROM "public"."products"`) {
		t.Fatalf("显式 .Table() 应原样保留 schema 限定名: %s", sqlStr)
	}
	if strings.Contains(sqlStr, "t_products") {
		t.Fatalf("显式 .Table() 不应叠加前缀: %s", sqlStr)
	}
}

func TestDBPrefixPropagationCRUD(t *testing.T) {
	ctx := context.Background()
	db := newMockDB(t).WithPrefix("t_")

	_ = Insert(ctx, db, &Product{Name: "phone"})
	if !strings.Contains(recQuery, `INSERT INTO "t_products" ("name") VALUES (?)`) {
		t.Fatalf("Insert 未带前缀: %s", recQuery)
	}

	_ = UpdateById(ctx, db, &Product{Id: 1, Name: "pc"})
	if !strings.Contains(recQuery, `UPDATE "t_products" SET "name" = ? WHERE "id" = ?`) {
		t.Fatalf("UpdateById 未带前缀: %s", recQuery)
	}

	_ = DeleteById[Product](ctx, db, 1)
	if !strings.Contains(recQuery, `DELETE FROM "t_products" WHERE "id" = ?`) {
		t.Fatalf("DeleteById 未带前缀: %s", recQuery)
	}

	_, _ = SelectById[Product](ctx, db, 1)
	if !strings.Contains(recQuery, `SELECT * FROM "t_products" WHERE "id" = ?`) {
		t.Fatalf("SelectById 未带前缀: %s", recQuery)
	}

	_, _ = Count[Product](ctx, db, NewQuery[Product]())
	if !strings.Contains(recQuery, `SELECT COUNT(*) FROM "t_products"`) {
		t.Fatalf("Count 未带前缀: %s", recQuery)
	}

	_, _ = SelectList(ctx, db, NewQuery[Product]())
	if !strings.Contains(recQuery, `FROM "t_products"`) {
		t.Fatalf("SelectList 未带前缀: %s", recQuery)
	}
}

func TestDBPrefixNotAppliedToExplicitModel(t *testing.T) {
	// User 有显式 TableName，DB 前缀不应叠加
	ctx := context.Background()
	db := newMockDB(t).WithPrefix("t_")
	_, _ = SelectById[User](ctx, db, 1)
	if strings.Contains(recQuery, `"t_users"`) {
		t.Fatalf("显式模型不应加前缀: %s", recQuery)
	}
	if !strings.Contains(recQuery, `FROM "users"`) {
		t.Fatalf("显式模型应保持 users: %s", recQuery)
	}
}
