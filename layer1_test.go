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
	_ = Upsert(context.Background(), db, &User{Id: 1, Name: "a", Age: 3}, nil)
	// 冲突键是自增主键、且实体已赋值 → 必须把它写进 INSERT 列清单，
	// 否则 ON CONFLICT ("id") 永远命不中（真库实测会静默新增一行）。
	want := `INSERT INTO "users" ("id", "name", "age") VALUES ($1, $2, $3) ON CONFLICT ("id") DO UPDATE SET "name" = EXCLUDED."name", "age" = EXCLUDED."age"`
	if recQuery != want {
		t.Fatalf("PG Upsert SQL 错误:\n 实际 %s\n 期望 %s", recQuery, want)
	}
}

func TestUpsertMySQL(t *testing.T) {
	db := NewDB(mustOpenMock(t), MySQL)
	_ = Upsert(context.Background(), db, &User{Id: 1, Name: "a", Age: 3}, nil)
	want := "INSERT INTO `users` (`id`, `name`, `age`) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE `name` = VALUES(`name`), `age` = VALUES(`age`)"
	if recQuery != want {
		t.Fatalf("MySQL Upsert SQL 错误:\n 实际 %s\n 期望 %s", recQuery, want)
	}
}

// TestUpsertPGBackfillsPK 校验 PG 侧 upsert 后回填自增主键（与 Insert 对齐）。
func TestUpsertPGBackfillsPK(t *testing.T) {
	db := NewDB(mustOpenMock(t), PG)
	u := User{Name: "a", Age: 3} // Id 留零值 → 由数据库发号
	if err := Upsert(context.Background(), db, &u, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recQuery, `RETURNING "id"`) {
		t.Fatalf("PG 回填主键应走 RETURNING: %s", recQuery)
	}
	if u.Id != 1 {
		t.Fatalf("未回填自增主键，Id = %d（期望 mock 返回的 1）", u.Id)
	}
}

// TestUpsertMySQLCapturesPK 校验 MySQL 侧用 LAST_INSERT_ID(id) 技巧带回主键：
// ON DUPLICATE KEY UPDATE 走到更新分支时，裸的 LAST_INSERT_ID() 并不指向该行。
func TestUpsertMySQLCapturesPK(t *testing.T) {
	db := NewDB(mustOpenMock(t), MySQL)
	u := User{Name: "a", Age: 3}
	if err := Upsert(context.Background(), db, &u, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recQuery, "`id` = LAST_INSERT_ID(`id`)") {
		t.Fatalf("MySQL 应追加 LAST_INSERT_ID 主键捕获: %s", recQuery)
	}
	if strings.Contains(recQuery, "RETURNING") {
		t.Fatalf("MySQL 不支持 RETURNING: %s", recQuery)
	}
	if u.Id != 1 {
		t.Fatalf("未回填自增主键，Id = %d（期望 mock 返回的 1）", u.Id)
	}
}

// TestUpsertZeroPKOmitsPKColumn 是上面两条的反面：
// 主键留零值时语义就是「插入新行、主键交给数据库」，此时不应把 id 写进列清单。
func TestUpsertZeroPKOmitsPKColumn(t *testing.T) {
	for _, d := range []struct {
		name string
		d    Dialect
	}{{"pg", PG}, {"mysql", MySQL}, {"sqlite", SQLite}} {
		recQuery = ""
		db := NewDB(mustOpenMock(t), d.d)
		_ = Upsert(context.Background(), db, &User{Name: "a", Age: 3}, nil)
		if contains(insertColNames(recQuery), "id") {
			t.Fatalf("[%s] 主键为零值时不应写入 id 列: %s", d.name, recQuery)
		}
	}
}

// TestUpsertNoUpdateCols 校验「只有冲突键一列」时退化为 DO NOTHING。
func TestUpsertNoUpdateCols(t *testing.T) {
	db := NewDB(mustOpenMock(t), PG)
	_ = Upsert(context.Background(), db, &OnlyPK{Id: 7}, nil)
	if !strings.Contains(recQuery, `("id") VALUES ($1)`) {
		t.Fatalf("仅冲突键时应带上该列: %s", recQuery)
	}
	if !strings.Contains(recQuery, `ON CONFLICT ("id") DO NOTHING`) {
		t.Fatalf("无可更新列时应退化为 DO NOTHING: %s", recQuery)
	}
}

// TestBatchUpsertConflictPKRules 覆盖批量 upsert 在自增冲突键上的整批决策。
func TestBatchUpsertConflictPKRules(t *testing.T) {
	ctx := context.Background()

	// 全批都赋了值 → 整批带上主键列。
	recQuery = ""
	db := NewDB(mustOpenMock(t), PG)
	if err := BatchUpsert(ctx, db, []User{{Id: 1, Name: "a"}, {Id: 2, Name: "b"}}, []string{"id"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recQuery, `("id", "name", "age")`) {
		t.Fatalf("整批已赋值应带上主键列: %s", recQuery)
	}

	// 全批都留零值 → 不补主键列（主键交给数据库）。
	recQuery = ""
	if err := BatchUpsert(ctx, db, []User{{Name: "a"}, {Name: "b"}}, []string{"id"}); err != nil {
		t.Fatal(err)
	}
	if contains(insertColNames(recQuery), "id") {
		t.Fatalf("整批未赋值不应带主键列: %s", recQuery)
	}

	// 只赋了部分 → 无法表达，必须报错而不是静默犯错。
	if err := BatchUpsert(ctx, db, []User{{Id: 1, Name: "a"}, {Name: "b"}}, []string{"id"}); err == nil {
		t.Fatal("部分行赋主键时应报错（多行 VALUES 列数必须一致）")
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

// insertColNames 从记录的 SQL 里取出 INSERT 的列名（去掉方言引号）。
// 不能直接对整条 SQL 做 Contains 断言 —— 冲突键列名同样会出现在
// ON CONFLICT ("id") / ON DUPLICATE KEY 里，会给出假阳性。
func insertColNames(sqlStr string) []string {
	open := strings.Index(sqlStr, "(")
	if open < 0 {
		return nil
	}
	rel := strings.Index(sqlStr[open:], ")")
	if rel < 0 {
		return nil
	}
	parts := strings.Split(sqlStr[open+1:open+rel], ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.Trim(strings.TrimSpace(p), "`\""))
	}
	return out
}

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
func (zeroAffectedExecutor) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return nil
}
func (zeroAffectedExecutor) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return zeroResult{}, nil
}

type zeroResult struct{}

func (zeroResult) LastInsertId() (int64, error) { return 0, nil }
func (zeroResult) RowsAffected() (int64, error) { return 0, nil }
