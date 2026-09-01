package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"testing"
)

// ---- 方言方法单测 ----

func TestDialectSupportsLastInsertID(t *testing.T) {
	if PG.SupportsLastInsertID() {
		t.Fatal("PostgreSQL(pgx) 不应支持 LastInsertId")
	}
	if !MySQL.SupportsLastInsertID() {
		t.Fatal("MySQL 应支持 LastInsertId")
	}
	if !SQLite.SupportsLastInsertID() {
		t.Fatal("SQLite 应支持 LastInsertId")
	}
}

func TestDialectInsertReturning(t *testing.T) {
	if got := PG.InsertReturning("id"); got != ` RETURNING "id"` {
		t.Fatalf("PG.InsertReturning 期望 ` RETURNING \"id\"`，实际 %q", got)
	}
	if got := MySQL.InsertReturning("id"); got != "" {
		t.Fatalf("MySQL.InsertReturning 期望空串，实际 %q", got)
	}
	if got := SQLite.InsertReturning("id"); got != "" {
		t.Fatalf("SQLite.InsertReturning 期望空串，实际 %q", got)
	}
}

// ---- PostgreSQL 自增主键回填集成测试 ----
// pgx 不支持 LastInsertId()，框架必须走 INSERT ... RETURNING "id" + 扫描单行。
// 以下 mock 驱动对任一 Query 均返回一行单列 id=42，用于验证回写逻辑。

func init() {
	sql.Register("mockpg_returning", mockPGDriver{})
}

type mockPGDriver struct{}

func (mockPGDriver) Open(name string) (driver.Conn, error) { return mockPGConn{}, nil }

type mockPGConn struct{}

func (mockPGConn) Prepare(query string) (driver.Stmt, error) { return mockPGStmt{}, nil }
func (mockPGConn) Close() error                              { return nil }
func (mockPGConn) Begin() (driver.Tx, error)                 { return mockPGTx{}, nil }

type mockPGStmt struct{}

func (mockPGStmt) Close() error                  { return nil }
func (mockPGStmt) NumInput() int                 { return -1 }
func (mockPGStmt) Exec(args []driver.Value) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}
func (mockPGStmt) Query(args []driver.Value) (driver.Rows, error) { return &mockPGRows{}, nil }

type mockPGTx struct{}

func (mockPGTx) Commit() error   { return nil }
func (mockPGTx) Rollback() error { return nil }

type mockPGRows struct {
	done bool
}

func (r mockPGRows) Columns() []string { return []string{"id"} }
func (r mockPGRows) Close() error      { return nil }
func (r *mockPGRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = driver.Value(int64(42))
	return nil
}

type pkUser struct {
	Id   int64  `db:"id,pk,autoincrement"`
	Name string `db:"name"`
}

func TestInsertPGBackfillsAutoIncPK(t *testing.T) {
	// Driver 未知 → dialectForDriver 默认返回 PG（与真实 pgx 同方言）
	db, err := Open(Config{Driver: "mockpg_returning", DSN: "x"})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	u := &pkUser{Name: "1hao"}
	if err := Insert(context.Background(), db, u); err != nil {
		t.Fatalf("Insert 失败: %v", err)
	}
	if u.Id != 42 {
		t.Fatalf("期望自增主键回写为 42，实际 %d", u.Id)
	}
}
