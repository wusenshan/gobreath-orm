package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// ---- 测试用模型 ----

type User struct {
	Id   int64  `db:"id,pk,autoincrement"`
	Name string `db:"name"`
	Age  int    `db:"age"`
}

func (User) TableName() string { return "users" }

// ---- 纯 Go mock driver：零依赖，用于验证真实结构体扫描 + 记录生成的 SQL ----

var (
	recQuery string
	recArgs  []any
)

func init() { sql.Register("ormmock", mockDriver{}) }

type mockDriver struct{}

func (mockDriver) Open(dsn string) (driver.Conn, error) { return &mockConn{}, nil }

type mockConn struct{}

func (c *mockConn) Prepare(query string) (driver.Stmt, error) { return &mockStmt{query: query}, nil }
func (c *mockConn) Close() error                              { return nil }
func (c *mockConn) Begin() (driver.Tx, error)                 { return &mockTx{}, nil }

type mockTx struct{}

func (t *mockTx) Commit() error   { return nil }
func (t *mockTx) Rollback() error { return nil }

type mockStmt struct{ query string }

func (s *mockStmt) Close() error  { return nil }
func (s *mockStmt) NumInput() int { return -1 }
func (s *mockStmt) Exec(args []driver.Value) (driver.Result, error) {
	recQuery, recArgs = s.query, valuesToAny(args)
	return mockResult{}, nil
}
func (s *mockStmt) Query(args []driver.Value) (driver.Rows, error) {
	recQuery, recArgs = s.query, valuesToAny(args)
	return mockRowsFor(s.query), nil
}

func valuesToAny(vs []driver.Value) []any {
	out := make([]any, len(vs))
	for i, v := range vs {
		out[i] = v
	}
	return out
}

type mockResult struct{}

func (mockResult) LastInsertId() (int64, error) { return 1, nil }
func (mockResult) RowsAffected() (int64, error) { return 1, nil }

type mockRows struct {
	cols []string
	data [][]driver.Value
	pos  int
}

func (r *mockRows) Columns() []string { return r.cols }
func (r *mockRows) Close() error      { return nil }
func (r *mockRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.data) {
		return io.EOF
	}
	for i, v := range r.data[r.pos] {
		dest[i] = v
	}
	r.pos++
	return nil
}

var mockRegistry = map[string]*mockRows{}

func mockRowsFor(query string) driver.Rows {
	for k, v := range mockRegistry {
		if strings.Contains(query, k) {
			return v
		}
	}
	return &mockRows{}
}

func newMockDB(t *testing.T) *DB {
	t.Helper()
	sqlDB, err := sql.Open("ormmock", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return NewDB(sqlDB, SQLite)
}

type testHook struct {
	events []HookEvent
}

func (h *testHook) On(event HookEvent) {
	h.events = append(h.events, event)
}

func TestOpenConfigStruct(t *testing.T) {
	db, err := Open(Config{Driver: "ormmock", DSN: ""})
	if err != nil {
		t.Fatalf("Open(Config) 返回错误: %v", err)
	}
	if db == nil {
		t.Fatal("Open(Config) 返回 nil DB")
	}
	if db.prefix != "" {
		t.Fatalf("Open(Config) 默认前缀应为空，实际 %q", db.prefix)
	}
	if db.dialect != PG {
		t.Fatalf("Open(Config) 未知驱动应回退到 PG，实际 %v", db.dialect)
	}
	if err := db.exec.(*sql.DB).Close(); err != nil {
		t.Fatalf("关闭 DB 错误: %v", err)
	}
}

func TestHooksConfigAndLifecycle(t *testing.T) {
	hook := &testHook{}
	db, err := Open(Config{Driver: "ormmock", Hooks: []Hook{hook}})
	if err != nil {
		t.Fatalf("Open(Config{Hooks}) 返回错误: %v", err)
	}
	if err := Insert(context.Background(), db, &User{Name: "alice", Age: 30}); err != nil {
		t.Fatalf("Insert 触发 hook 失败: %v", err)
	}
	if len(hook.events) < 2 {
		t.Fatalf("期望至少触发 before/after 两个 hook 事件，实际 %d", len(hook.events))
	}
	seenBefore, seenAfter := false, false
	for _, e := range hook.events {
		if e.Kind == HookKindExec && e.Phase == HookPhaseBefore {
			seenBefore = true
		}
		if e.Kind == HookKindExec && e.Phase == HookPhaseAfter {
			seenAfter = true
		}
	}
	if !seenBefore || !seenAfter {
		t.Fatalf("hook 生命周期不完整: %+v", hook.events)
	}
	if err := db.exec.(*sql.DB).Close(); err != nil {
		t.Fatalf("关闭 DB 错误: %v", err)
	}
}

func TestOpenConfigPool(t *testing.T) {
	db, err := Open(Config{
		Driver:          "ormmock",
		MaxOpenConns:    5,
		MaxIdleConns:    2,
		ConnMaxLifetime: time.Minute,
		ConnMaxIdleTime: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Open(Config) 返回错误: %v", err)
	}
	defer db.SQL().Close()

	if got := db.SQL(); got == nil {
		t.Fatal("SQL() 应返回底层 *sql.DB")
	} else if got.Stats().MaxOpenConnections != 5 {
		t.Fatalf("MaxOpenConns 应为 5，实际 %d", got.Stats().MaxOpenConnections)
	}
}

func TestSQLAccessorNil(t *testing.T) {
	// 底层执行器不是 *sql.DB 时，SQL() 应返回 nil。
	db := NewDB(stubExecutor{}, SQLite)
	if db.SQL() != nil {
		t.Fatal("非 *sql.DB 底层时 SQL() 应返回 nil")
	}
}

type stubExecutor struct{}

func (stubExecutor) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return nil, fmt.Errorf("stub")
}
func (stubExecutor) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return nil, fmt.Errorf("stub")
}

// ---- 测试用例 ----

func TestQueryBuildBasic(t *testing.T) {
	q := NewQuery[User]().
		WithDialect(SQLite).
		Select("name", "age").
		Eq(Col(func(u *User) *int { return &u.Age }), 18).
		Like(Col[User](func(u *User) *string { return &u.Name }), "a").
		OrderBy(Col[User](func(u *User) *string { return &u.Name }), true).
		Limit(10).Offset(20)

	sqlStr, args := q.Build()
	if !strings.Contains(sqlStr, `SELECT "name", "age" FROM "users"`) {
		t.Fatalf("select/table 错误: %s", sqlStr)
	}
	if !strings.Contains(sqlStr, `"age" = ?`) || !strings.Contains(sqlStr, `"name" LIKE ?`) {
		t.Fatalf("where 错误: %s", sqlStr)
	}
	if !strings.Contains(sqlStr, `ORDER BY "name" ASC`) {
		t.Fatalf("order 错误: %s", sqlStr)
	}
	if !strings.Contains(sqlStr, "LIMIT 10 OFFSET 20") {
		t.Fatalf("limit/offset 错误: %s", sqlStr)
	}
	if len(args) != 2 {
		t.Fatalf("args 数量应为 2，实际 %d: %v", len(args), args)
	}
}

func TestQueryLikeFamily(t *testing.T) {
	name := Col[User](func(u *User) *string { return &u.Name })
	cases := []struct {
		name string
		q    *Query[User]
		want string // 期望的占位参数值
		frag string // SQL 中期望出现的片段
	}{
		{"Like", NewQuery[User]().WithDialect(SQLite).Like(name, "a"), "%a%", `"name" LIKE ?`},
		{"LikeRight", NewQuery[User]().WithDialect(SQLite).LikeRight(name, "a"), "a%", `"name" LIKE ?`},
		{"LikeLeft", NewQuery[User]().WithDialect(SQLite).LikeLeft(name, "a"), "%a", `"name" LIKE ?`},
		{"NotLike", NewQuery[User]().WithDialect(SQLite).NotLike(name, "a"), "%a%", `"name" NOT LIKE ?`},
		{"NotLikeRight", NewQuery[User]().WithDialect(SQLite).NotLikeRight(name, "a"), "a%", `"name" NOT LIKE ?`},
		{"NotLikeLeft", NewQuery[User]().WithDialect(SQLite).NotLikeLeft(name, "a"), "%a", `"name" NOT LIKE ?`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sqlStr, args := c.q.Build()
			if !strings.Contains(sqlStr, c.frag) {
				t.Fatalf("%s SQL 期望含 %q，实际: %s", c.name, c.frag, sqlStr)
			}
			if len(args) != 1 || args[0] != c.want {
				t.Fatalf("%s 占位值期望 %q，实际 %v", c.name, c.want, args)
			}
		})
	}
}

func TestQueryBuildOrAndGroupBy(t *testing.T) {
	q := NewQuery[User]().
		WithDialect(PG).
		Eq(Col[User](func(u *User) *string { return &u.Name }), "a").
		Or().
		Eq(Col[User](func(u *User) *string { return &u.Name }), "b").
		Gt(Col[User](func(u *User) *int { return &u.Age }), 30).
		IsNull(Col[User](func(u *User) *string { return &u.Name })).
		NotIn(Col[User](func(u *User) *int { return &u.Age }), []any{1, 2, 3}).
		GroupBy(Col[User](func(u *User) *string { return &u.Name })).
		Having(Col[User](func(u *User) *int { return &u.Age }), ">", 18)

	sqlStr, _ := q.Build()
	if !strings.Contains(sqlStr, `("name" = $1 OR "name" = $2)`) {
		t.Fatalf("OR 组错误: %s", sqlStr)
	}
	if !strings.Contains(sqlStr, `"name" IS NULL`) {
		t.Fatalf("IS NULL 错误: %s", sqlStr)
	}
	if !strings.Contains(sqlStr, `"age" NOT IN ($4, $5, $6)`) {
		t.Fatalf("NOT IN 错误: %s", sqlStr)
	}
	if !strings.Contains(sqlStr, `GROUP BY "name"`) {
		t.Fatalf("GROUP BY 错误: %s", sqlStr)
	}
	if !strings.Contains(sqlStr, `HAVING "age" > $7`) {
		t.Fatalf("HAVING 错误: %s", sqlStr)
	}
}

func TestCRUDSQL(t *testing.T) {
	ctx := context.Background()
	db := newMockDB(t)

	_ = Insert(ctx, db, &User{Name: "alice", Age: 30})
	if !strings.Contains(recQuery, `INSERT INTO "users" ("name", "age") VALUES (?, ?)`) {
		t.Fatalf("Insert SQL 错误: %s", recQuery)
	}

	_ = UpdateById(ctx, db, &User{Id: 1, Name: "bob", Age: 25})
	if !strings.Contains(recQuery, `UPDATE "users" SET "name" = ?, "age" = ? WHERE "id" = ?`) {
		t.Fatalf("UpdateById SQL 错误: %s", recQuery)
	}

	_ = DeleteById[User](ctx, db, 1)
	if !strings.Contains(recQuery, `DELETE FROM "users" WHERE "id" = ?`) {
		t.Fatalf("DeleteById SQL 错误: %s", recQuery)
	}

	err := Delete[User](ctx, db, NewQuery[User]())
	if err == nil {
		t.Fatalf("Delete 无条件未报错，存在全表删除风险")
	}

	_, _ = SelectById[User](ctx, db, 1)
	if !strings.Contains(recQuery, `SELECT * FROM "users" WHERE "id" = ?`) {
		t.Fatalf("SelectById SQL 错误: %s", recQuery)
	}

	_, _ = Count[User](ctx, db, NewQuery[User]().
		Gt(Col[User](func(u *User) *int { return &u.Age }), 18))
	if !strings.Contains(recQuery, `SELECT COUNT(*) FROM "users" WHERE "age" > ?`) {
		t.Fatalf("Count SQL 错误: %s", recQuery)
	}

	pr, err := Page[User](ctx, db, NewQuery[User](), 2, 15)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recQuery, "LIMIT 15 OFFSET 15") {
		t.Fatalf("Page SQL 错误: %s", recQuery)
	}
	if pr.Page != 2 || pr.Size != 15 {
		t.Fatalf("Page 元信息错误: %+v", pr)
	}
}

func TestComputePageMeta(t *testing.T) {
	cases := []struct {
		page, size  int
		total       int64
		wantPages   int
		wantHasNext bool
		wantHasPrev bool
	}{
		{1, 10, 0, 0, false, false}, // 无数据
		{1, 10, 5, 1, false, false}, // 不足一页
		{1, 10, 25, 3, true, false}, // 第一页，有下一页
		{2, 10, 25, 3, true, true},  // 中间页
		{3, 10, 25, 3, false, true}, // 最后一页
		{5, 10, 25, 3, false, true}, // 越界页，归正后仍无下一页
	}
	for _, c := range cases {
		pages, hasNext, hasPrev := computePageMeta(c.page, c.size, c.total)
		if pages != c.wantPages || hasNext != c.wantHasNext || hasPrev != c.wantHasPrev {
			t.Fatalf("computePageMeta(%d,%d,%d) = (%d,%v,%v)，期望 (%d,%v,%v)",
				c.page, c.size, c.total, pages, hasNext, hasPrev, c.wantPages, c.wantHasNext, c.wantHasPrev)
		}
	}
}

func TestRepoBinding(t *testing.T) {
	ctx := context.Background()
	db := newMockDB(t)

	// 创建仓储句柄
	repo := NewRepo[User](db)
	if repo.DB() != db {
		t.Fatal("Repo.DB() 应返回底层 *DB")
	}

	// SelectById 通过 Repo 调用，SQL 与直接使用一致
	_, _ = repo.SelectById(ctx, 1)
	if !strings.Contains(recQuery, `SELECT * FROM "users" WHERE "id" = ?`) {
		t.Fatalf("Repo.SelectById SQL 错误: %s", recQuery)
	}

	// Page 通过 Repo 调用
	pr, err := repo.Page(ctx, NewQuery[User](), 2, 15)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recQuery, `FROM "users" LIMIT 15 OFFSET 15`) {
		t.Fatalf("Repo.Page SQL 错误: %s", recQuery)
	}
	if pr.Page != 2 || pr.Size != 15 {
		t.Fatalf("Repo.Page 元信息错误: %+v", pr)
	}

	// Count 通过 Repo 调用
	_, _ = repo.Count(ctx, NewQuery[User]().Gt(Col[User](func(u *User) *int { return &u.Age }), 18))
	if !strings.Contains(recQuery, `SELECT COUNT(*) FROM "users" WHERE "age" > ?`) {
		t.Fatalf("Repo.Count SQL 错误: %s", recQuery)
	}

	// Transaction 回调内也应拿到绑定 T 的 Repo，且 SQL 正常
	mockRegistry["users"] = &mockRows{
		cols: []string{"id", "name", "age"},
		data: [][]driver.Value{{int64(1), "alice", int64(30)}},
	}
	err = repo.Transaction(ctx, func(tx *Repo[User]) error {
		_, e := tx.SelectById(ctx, 1)
		return e
	})
	if err != nil {
		t.Fatalf("Repo.Transaction 失败: %v", err)
	}
	if !strings.Contains(recQuery, `SELECT * FROM "users" WHERE "id" = ?`) {
		t.Fatalf("Repo.Transaction 内 SQL 错误: %s", recQuery)
	}
}

func TestScanRealRows(t *testing.T) {
	mockRegistry["users"] = &mockRows{
		cols: []string{"id", "name", "age"},
		data: [][]driver.Value{{int64(1), "alice", int64(30)}},
	}
	db := newMockDB(t)

	u, err := SelectById[User](context.Background(), db, 1)
	if err != nil {
		t.Fatalf("SelectById 失败: %v", err)
	}
	if u.Id != 1 || u.Name != "alice" || u.Age != 30 {
		t.Fatalf("扫描结果错误: %+v", *u)
	}
}
