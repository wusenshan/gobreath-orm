package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// UpdateByIds 的单元测试用独立驱动：「逐条执行」必须能数出语句条数与每条的实参，
// 而共享的 recQuery 只保留最后一条，且被并发用例写过。
var (
	uibExecs    int32
	uibAffected int64 // 每次 Exec 的 RowsAffected，默认 1；置 0 可构造乐观锁冲突
	uibLastSQL  string
	uibLastArgs []any
)

type uibDriver struct{}

func (uibDriver) Open(string) (driver.Conn, error) { return uibConn{}, nil }

type uibConn struct{}

func (uibConn) Prepare(q string) (driver.Stmt, error) { return uibStmt{query: q}, nil }
func (uibConn) Close() error                          { return nil }
func (uibConn) Begin() (driver.Tx, error)             { return &mockTx{}, nil }

type uibStmt struct{ query string }

func (uibStmt) Close() error  { return nil }
func (uibStmt) NumInput() int { return -1 }
func (s uibStmt) Exec(args []driver.Value) (driver.Result, error) {
	atomic.AddInt32(&uibExecs, 1)
	uibLastSQL = s.query
	uibLastArgs = valuesToAny(args)
	return uibResult{n: atomic.LoadInt64(&uibAffected)}, nil
}
func (uibStmt) Query([]driver.Value) (driver.Rows, error) { return &mockRows{}, nil }

// uibResult 让 RowsAffected 可配 —— mockResult 恒返回 1，构造不出「命中 0 行」。
type uibResult struct{ n int64 }

func (uibResult) LastInsertId() (int64, error)   { return 0, nil }
func (r uibResult) RowsAffected() (int64, error) { return r.n, nil }

func init() { sql.Register("ormuib", uibDriver{}) }

func uibDB(t *testing.T) *DB {
	t.Helper()
	sqlDB, err := sql.Open("ormuib", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return NewDB(sqlDB, SQLite)
}

func uibReset() {
	atomic.StoreInt32(&uibExecs, 0)
	atomic.StoreInt64(&uibAffected, 1)
	uibLastSQL, uibLastArgs = "", nil
}

func uibCalls() int32 { return atomic.LoadInt32(&uibExecs) }

// noPKUser 没有任何 pk 标记，用于验证无主键入口的守卫。
type noPKUser struct {
	NoKey string `db:"no_key"`
}

// TestUpdateByIdsExecutesPerRow 逐条执行：两行两条 UPDATE，实参与主键各自对应。
func TestUpdateByIdsExecutesPerRow(t *testing.T) {
	uibReset()
	db := uibDB(t)
	list := []User{
		{Id: 1, Name: "alice", Age: 10},
		{Id: 2, Name: "bob", Age: 20},
	}
	if err := UpdateByIds[User](context.Background(), db, list); err != nil {
		t.Fatalf("UpdateByIds 出错：%v", err)
	}
	if got := uibCalls(); got != 2 {
		t.Fatalf("应执行 2 条 UPDATE（逐行），实际 %d 条", got)
	}
	if !strings.Contains(uibLastSQL, `UPDATE "users" SET "name" = ?, "age" = ? WHERE "id" = ?`) {
		t.Fatalf("最后一条应为按主键的整行更新，实际: %s", uibLastSQL)
	}
	// 顺序是 updateCols 的列序：name、age、id。
	want := []any{"bob", int64(20), int64(2)}
	if len(uibLastArgs) != len(want) {
		t.Fatalf("实参个数应为 %d，实际 %v", len(want), uibLastArgs)
	}
	for i := range want {
		if uibLastArgs[i] != want[i] {
			t.Fatalf("第 %d 个实参应为 %v，实际 %v（全部：%v）", i, want[i], uibLastArgs[i], uibLastArgs)
		}
	}
}

// TestUpdateByIdsEmptyIsNoop 空切片是空操作，不是错误，且一条语句都不发。
func TestUpdateByIdsEmptyIsNoop(t *testing.T) {
	uibReset()
	db := uibDB(t)
	if err := UpdateByIds[User](context.Background(), db, nil); err != nil {
		t.Fatalf("空切片不应报错：%v", err)
	}
	if err := UpdateByIds[User](context.Background(), db, []User{}); err != nil {
		t.Fatalf("空切片不应报错：%v", err)
	}
	if got := uibCalls(); got != 0 {
		t.Fatalf("空切片不应执行任何语句，实际 %d 条", got)
	}
}

// TestUpdateByIdsZeroPKStops 零主键在批量里必须报错：静默跳过一条比报错危险。
// 同时验证「遇错即停」—— 第 0 行已经更新，第 1 行拦下。
func TestUpdateByIdsZeroPKStops(t *testing.T) {
	uibReset()
	db := uibDB(t)
	list := []User{
		{Id: 1, Name: "ok", Age: 1},
		{Id: 0, Name: "no-pk", Age: 2},
		{Id: 3, Name: "never", Age: 3},
	}
	err := UpdateByIds[User](context.Background(), db, list)
	if err == nil {
		t.Fatal("零主键应报错，实际通过")
	}
	if !strings.Contains(err.Error(), "第 1 行") {
		t.Fatalf("错误应指出是第 1 行，实际: %v", err)
	}
	if !strings.Contains(err.Error(), "Insert / BatchInsert") {
		t.Fatalf("错误应给出正确替代（插入），实际: %v", err)
	}
	if got := uibCalls(); got != 1 {
		t.Fatalf("遇错即停：第 0 行执行后应停下，实际执行 %d 条", got)
	}
}

// TestUpdateByIdsNoPrimaryKey 无主键模型直接拒绝。
func TestUpdateByIdsNoPrimaryKey(t *testing.T) {
	uibReset()
	err := UpdateByIds[noPKUser](context.Background(), uibDB(t), []noPKUser{{NoKey: "x"}})
	if err == nil || !strings.Contains(err.Error(), "无主键") {
		t.Fatalf("无主键模型应报错，实际: %v", err)
	}
	if got := uibCalls(); got != 0 {
		t.Fatalf("无主键模型不应执行语句，实际 %d 条", got)
	}
}

// TestUpdateByIdsOptimisticLockWraps 乐观锁冲突的错误必须可被 errors.Is 判别 ——
// 批量入口在外面包了一层「第 N 行」上下文，用 %v 拼接会把错误链弄断。
func TestUpdateByIdsOptimisticLockWraps(t *testing.T) {
	uibReset()
	atomic.StoreInt64(&uibAffected, 0) // 命中 0 行 → 带 version 列时即 ErrOptimisticLock
	err := UpdateByIds[batchVerUser](context.Background(), uibDB(t),
		[]batchVerUser{{Id: 1, Name: "x", Version: 3}})
	if err == nil {
		t.Fatal("版本冲突应报错，实际通过")
	}
	if !errors.Is(err, ErrOptimisticLock) {
		t.Fatalf("应可用 errors.Is 判到 ErrOptimisticLock，实际: %v", err)
	}
	if !strings.Contains(err.Error(), "第 0 行") || !strings.Contains(err.Error(), "id = 1") {
		t.Fatalf("错误应带行号与主键列名，实际: %v", err)
	}
}

// TestRepoUpdateByIds 确认仓储层转发没有走样。
func TestRepoUpdateByIds(t *testing.T) {
	uibReset()
	r := NewRepo[User](uibDB(t))
	if err := r.UpdateByIds(context.Background(), []User{{Id: 7, Name: "repo", Age: 7}}); err != nil {
		t.Fatalf("Repo.UpdateByIds 出错：%v", err)
	}
	if got := uibCalls(); got != 1 {
		t.Fatalf("应执行 1 条语句，实际 %d 条", got)
	}
}
