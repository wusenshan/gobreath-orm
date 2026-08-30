package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"testing"
)

// ---- 测试用模型 ----

// Post 带乐观锁版本列的模型（注意：vector_test.go 已占用 Article 名）。
type Post struct {
	Id      int64  `db:"id,pk,autoincrement"`
	Title   string `db:"title"`
	Content string `db:"content"`
	Version int    `db:"version,version"`
}

// OnlyPK 只有主键的模型，用于验证 upsert 无可更新列时退化为 DO NOTHING。
type OnlyPK struct {
	Id int64 `db:"id,pk"`
}

func TestJoinBuild(t *testing.T) {
	q := NewQuery[User]().
		WithDialect(SQLite).
		Alias("u").
		LeftJoin("departments", `"u"."dept_id" = "departments"."id"`).
		Select("u.name", "departments.dept_name").
		Eq(Col[User](func(u *User) *string { return &u.Name }), "a")

	sqlStr, _ := q.Build()
	if !strings.Contains(sqlStr, `FROM "users" "u"`) {
		t.Fatalf("主表别名缺失: %s", sqlStr)
	}
	if !strings.Contains(sqlStr, `LEFT JOIN "departments" ON "u"."dept_id" = "departments"."id"`) {
		t.Fatalf("LEFT JOIN 渲染错误: %s", sqlStr)
	}
	if !strings.Contains(sqlStr, `SELECT "u"."name", "departments"."dept_name"`) {
		t.Fatalf("带别名的 SELECT 列渲染错误: %s", sqlStr)
	}
	if !strings.Contains(sqlStr, `"name" = ?`) {
		t.Fatalf("JOIN 后 WHERE 条件缺失: %s", sqlStr)
	}
}

func TestJoinRightAndInner(t *testing.T) {
	q := NewQuery[User]().WithDialect(PG).RightJoin("logs", "users.id = logs.uid")
	sqlStr, _ := q.Build()
	if !strings.Contains(sqlStr, `RIGHT JOIN "logs" ON users.id = logs.uid`) {
		t.Fatalf("RIGHT JOIN 渲染错误: %s", sqlStr)
	}

	q2 := NewQuery[User]().WithDialect(PG).JoinAs("deps", "d", "d.id = users.dep_id")
	sqlStr2, _ := q2.Build()
	if !strings.Contains(sqlStr2, `INNER JOIN "deps" "d" ON d.id = users.dep_id`) {
		t.Fatalf("INNER JOIN As 渲染错误: %s", sqlStr2)
	}
}

func TestJoinInvalidTablePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("非法表名未触发 panic")
		}
	}()
	_ = NewQuery[User]().LeftJoin("bad;table", "1=1")
}

func TestUpsertPG(t *testing.T) {
	db := NewDB(mustOpenMock(t), PG)
	_ = Upsert(context.Background(), db, &User{Id: 1, Name: "a", Age: 3})
	want := `INSERT INTO "users" ("name", "age") VALUES ($1, $2) ON CONFLICT ("id") DO UPDATE SET "name" = EXCLUDED."name", "age" = EXCLUDED."age"`
	if recQuery != want {
		t.Fatalf("PG Upsert SQL 错误:\n 实际 %s\n 期望 %s", recQuery, want)
	}
}

func TestUpsertMySQL(t *testing.T) {
	db := NewDB(mustOpenMock(t), MySQL)
	_ = Upsert(context.Background(), db, &User{Id: 1, Name: "a", Age: 3})
	want := "INSERT INTO `users` (`name`, `age`) VALUES (?, ?) ON DUPLICATE KEY UPDATE `name` = VALUES(`name`), `age` = VALUES(`age`)"
	if recQuery != want {
		t.Fatalf("MySQL Upsert SQL 错误:\n 实际 %s\n 期望 %s", recQuery, want)
	}
}

func TestUpsertNoUpdateCols(t *testing.T) {
	db := NewDB(mustOpenMock(t), PG)
	_ = Upsert(context.Background(), db, &OnlyPK{Id: 7})
	if !strings.Contains(recQuery, `ON CONFLICT ("id") DO NOTHING`) {
		t.Fatalf("无可更新列时应退化为 DO NOTHING: %s", recQuery)
	}
}

func TestUpdateSets(t *testing.T) {
	db := newMockDB(t)
	q := NewQuery[User]().
		Eq(Col[User](func(u *User) *int64 { return &u.Id }), 1).
		Set(Col[User](func(u *User) *string { return &u.Name }), "bob").
		Set(Col[User](func(u *User) *int { return &u.Age }), 30)
	n, err := UpdateSets(context.Background(), db, q)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("UpdateSets 受影响行数应为 1，实际 %d", n)
	}
	if !strings.Contains(recQuery, `UPDATE "users" SET`) ||
		!strings.Contains(recQuery, `"name" = ?`) ||
		!strings.Contains(recQuery, `"age" = ?`) ||
		!strings.Contains(recQuery, `WHERE "id" = ?`) {
		t.Fatalf("UpdateSets SQL 错误: %s", recQuery)
	}
}

func TestUpdatePartialMap(t *testing.T) {
	db := newMockDB(t)
	q := NewQuery[User]().Gt(Col[User](func(u *User) *int { return &u.Age }), 18)
	n, err := UpdatePartial(context.Background(), db, q, map[string]any{"name": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("UpdatePartial 受影响行数应为 1，实际 %d", n)
	}
	if !strings.Contains(recQuery, `UPDATE "users" SET "name" = ? WHERE "age" > ?`) {
		t.Fatalf("UpdatePartial SQL 错误: %s", recQuery)
	}
}

func TestUpdateByIdSets(t *testing.T) {
	db := newMockDB(t)
	n, err := UpdateByIdSets[User](context.Background(), db, int64(1), map[string]any{"name": "bob"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("UpdateByIdSets 受影响行数应为 1，实际 %d", n)
	}
	if !strings.Contains(recQuery, `UPDATE "users" SET "name" = ? WHERE "id" = ?`) {
		t.Fatalf("UpdateByIdSets SQL 错误: %s", recQuery)
	}
}

func TestOptimisticLockSQL(t *testing.T) {
	db := newMockDB(t)
	_ = UpdateById(context.Background(), db, &Post{Id: 1, Title: "x", Content: "c", Version: 5})
	if !strings.Contains(recQuery, `"title" = ?`) {
		t.Fatalf("乐观锁：普通列应正常赋值: %s", recQuery)
	}
	if !strings.Contains(recQuery, `"version" = "version" + 1`) {
		t.Fatalf("乐观锁：版本应自增: %s", recQuery)
	}
	if !strings.Contains(recQuery, `WHERE "id" = ? AND "version" = ?`) {
		t.Fatalf("乐观锁：WHERE 应带版本条件: %s", recQuery)
	}
}

func TestOptimisticLockConflict(t *testing.T) {
	db := NewDB(zeroAffectedExecutor{}, SQLite)
	err := UpdateById(context.Background(), db, &Post{Id: 1, Title: "x", Version: 5})
	if err != ErrOptimisticLock {
		t.Fatalf("乐观锁冲突应返回 ErrOptimisticLock，实际: %v", err)
	}
}

// ---- 测试辅助 ----

func mustOpenMock(t *testing.T) *sql.DB {
	t.Helper()
	sqlDB, err := sql.Open("ormmock", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return sqlDB
}

// zeroAffectedExecutor 执行成功但 RowsAffected 返回 0，用于验证乐观锁冲突分支。
type zeroAffectedExecutor struct{}

func (zeroAffectedExecutor) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return nil, driver.ErrSkip
}
func (zeroAffectedExecutor) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return zeroResult{}, nil
}

type zeroResult struct{}

func (zeroResult) LastInsertId() (int64, error) { return 0, nil }
func (zeroResult) RowsAffected() (int64, error) { return 0, nil }
