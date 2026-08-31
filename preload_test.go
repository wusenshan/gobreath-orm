package orm

import (
	"context"
	"database/sql/driver"
	"testing"
)

// ---- Preload 测试模型 ----

type PUser struct {
	Id       int64      `db:"id,pk,autoincrement"`
	Name     string     `db:"name"`
	Articles []PArticle `db:"-" orm:"has_many;fk:user_id"`
}

func (PUser) TableName() string { return "users" }

type PArticle struct {
	Id     int64  `db:"id,pk,autoincrement"`
	Title  string `db:"title"`
	UserId int64  `db:"user_id"`
}

func (PArticle) TableName() string { return "articles" }

type PComment struct {
	Id     int64     `db:"id,pk,autoincrement"`
	Body   string    `db:"body"`
	UserId int64     `db:"user_id"`
	Author *PAccount `db:"-" orm:"belongs_to;fk:user_id"`
}

func (PComment) TableName() string { return "comments" }

type PAccount struct {
	Id   int64  `db:"id,pk,autoincrement"`
	Name string `db:"name"`
}

func (PAccount) TableName() string { return "accounts" }

type POwner struct {
	Id      int64     `db:"id,pk,autoincrement"`
	Primary *PArticle `db:"-" orm:"has_one;fk:user_id"`
}

func (POwner) TableName() string { return "owners" }

func TestPreloadHasMany(t *testing.T) {
	mockRegistry["articles"] = &mockRows{
		cols: []string{"id", "title", "user_id"},
		data: [][]driver.Value{
			{int64(1), "a1", int64(1)},
			{int64(2), "a2", int64(1)},
			{int64(3), "a3", int64(2)},
		},
	}
	db := newMockDB(t)
	users := []PUser{{Id: 1, Name: "u1"}, {Id: 2, Name: "u2"}}
	if err := Preload(context.Background(), db, &users, "Articles"); err != nil {
		t.Fatalf("Preload 失败: %v", err)
	}
	if len(users[0].Articles) != 2 {
		t.Fatalf("user1 应关联 2 篇，实际 %d", len(users[0].Articles))
	}
	if len(users[1].Articles) != 1 || users[1].Articles[0].Title != "a3" {
		t.Fatalf("user2 应关联 1 篇(a3)，实际 %+v", users[1].Articles)
	}
}

func TestPreloadBelongsTo(t *testing.T) {
	mockRegistry["accounts"] = &mockRows{
		cols: []string{"id", "name"},
		data: [][]driver.Value{
			{int64(1), "alice"},
			{int64(2), "bob"},
		},
	}
	db := newMockDB(t)
	comments := []PComment{
		{Id: 10, Body: "c1", UserId: 1},
		{Id: 11, Body: "c2", UserId: 2},
	}
	if err := Preload(context.Background(), db, &comments, "Author"); err != nil {
		t.Fatalf("Preload 失败: %v", err)
	}
	if comments[0].Author == nil || comments[0].Author.Name != "alice" {
		t.Fatalf("comment1 应关联 alice，实际 %+v", comments[0].Author)
	}
	if comments[1].Author == nil || comments[1].Author.Name != "bob" {
		t.Fatalf("comment2 应关联 bob，实际 %+v", comments[1].Author)
	}
}

func TestPreloadHasOne(t *testing.T) {
	mockRegistry["articles"] = &mockRows{
		cols: []string{"id", "title", "user_id"},
		data: [][]driver.Value{
			{int64(1), "a1", int64(1)},
			{int64(2), "a2", int64(1)},
			{int64(3), "a3", int64(2)},
		},
	}
	db := newMockDB(t)
	owners := []POwner{{Id: 1}, {Id: 2}}
	if err := Preload(context.Background(), db, &owners, "Primary"); err != nil {
		t.Fatalf("Preload 失败: %v", err)
	}
	if owners[0].Primary == nil || owners[0].Primary.Title != "a1" {
		t.Fatalf("owner1 应关联 a1，实际 %+v", owners[0].Primary)
	}
	if owners[1].Primary == nil || owners[1].Primary.Title != "a3" {
		t.Fatalf("owner2 应关联 a3，实际 %+v", owners[1].Primary)
	}
}

func TestPreloadOne(t *testing.T) {
	mockRegistry["articles"] = &mockRows{
		cols: []string{"id", "title", "user_id"},
		data: [][]driver.Value{
			{int64(1), "a1", int64(1)},
			{int64(2), "a2", int64(1)},
		},
	}
	db := newMockDB(t)
	user := &PUser{Id: 1, Name: "u1"}
	if err := PreloadOne(context.Background(), db, user, "Articles"); err != nil {
		t.Fatalf("PreloadOne 失败: %v", err)
	}
	if len(user.Articles) != 2 {
		t.Fatalf("PreloadOne 应关联 2 篇，实际 %d", len(user.Articles))
	}
}

func TestPreloadUnknownRelation(t *testing.T) {
	db := newMockDB(t)
	users := []PUser{{Id: 1}}
	err := Preload(context.Background(), db, &users, "NotExist")
	if err == nil {
		t.Fatal("关联字段不存在时应报错")
	}
}

// ---- 默认外键命名约定测试（不显式写 fk:，验证 toSnake(Type)+"_id" 推导）----

type Cat struct {
	Id   int64 `db:"id,pk,autoincrement"`
	Name string `db:"name"`
	Kits []Kit `db:"-" orm:"has_many"` // 默认外键 = cat_id
}

func (Cat) TableName() string { return "cats" }

type Kit struct {
	Id    int64  `db:"id,pk,autoincrement"`
	Label string `db:"label"`
	CatId int64  `db:"cat_id"` // 与默认外键约定一致
}

func (Kit) TableName() string { return "kits" }

func TestPreloadDefaultFK(t *testing.T) {
	mockRegistry["kits"] = &mockRows{
		cols: []string{"id", "label", "cat_id"},
		data: [][]driver.Value{
			{int64(1), "k1", int64(1)},
			{int64(2), "k2", int64(1)},
			{int64(3), "k3", int64(2)},
		},
	}
	db := newMockDB(t)
	cats := []Cat{{Id: 1, Name: "c1"}, {Id: 2, Name: "c2"}}
	if err := Preload(context.Background(), db, &cats, "Kits"); err != nil {
		t.Fatalf("Preload 失败: %v", err)
	}
	if len(cats[0].Kits) != 2 {
		t.Fatalf("cat1 应关联 2 个，实际 %d", len(cats[0].Kits))
	}
	if len(cats[1].Kits) != 1 || cats[1].Kits[0].Label != "k3" {
		t.Fatalf("cat2 应关联 1 个(k3)，实际 %+v", cats[1].Kits)
	}
}
