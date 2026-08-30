package orm

import (
	"context"
	"database/sql"
	"sync"
	"testing"
)

// stubExec 仅用于 router 路由断言，不真正执行 SQL。
type stubExec struct{ id string }

func (s *stubExec) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return &stubResult{}, nil
}
func (s *stubExec) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return nil, nil
}

type stubResult struct{}

func (r *stubResult) LastInsertId() (int64, error) { return 0, nil }
func (r *stubResult) RowsAffected() (int64, error) { return 0, nil }

func TestReadWriteRouterNilSafe(t *testing.T) {
	var r *readWriteRouter
	if r.choose("SELECT 1") != nil {
		t.Fatal("nil router 应选择器应返回 nil（走默认 db.exec）")
	}
}

func TestReadWriteRouterSingleReplica(t *testing.T) {
	primary := &stubExec{"P"}
	replica := &stubExec{"R"}
	r := &readWriteRouter{primary: primary, replicas: []Executor{replica}}
	if got := r.choose("SELECT * FROM users"); got != replica {
		t.Fatalf("SELECT 应路由到 replica，实际 %v", got)
	}
	if got := r.choose("INSERT INTO users VALUES (1)"); got != primary {
		t.Fatalf("INSERT 应路由到 primary，实际 %v", got)
	}
	// 边界：以注释开头或 CTE 写语句，目前 isWriteQuery 仍按前缀判定，此处仅验证正常路径
	if got := r.choose("UPDATE users SET name='x'"); got != primary {
		t.Fatalf("UPDATE 应路由到 primary，实际 %v", got)
	}
}

func TestReadWriteRouterRoundRobin(t *testing.T) {
	primary := &stubExec{"P"}
	r1 := &stubExec{"R1"}
	r2 := &stubExec{"R2"}
	r := &readWriteRouter{primary: primary, replicas: []Executor{r1, r2}}
	a := r.choose("SELECT 1")
	b := r.choose("SELECT 1")
	if a == b {
		t.Fatalf("多 replica 应轮换，两次不应命中同一节点: %v %v", a, b)
	}
	if (a != r1 && a != r2) || (b != r1 && b != r2) {
		t.Fatalf("轮换结果应在 replica 集合内")
	}
}

func TestReadWriteRouterConcurrent(t *testing.T) {
	primary := &stubExec{"P"}
	replicas := make([]Executor, 4)
	for i := range replicas {
		replicas[i] = &stubExec{"R"}
	}
	r := &readWriteRouter{primary: primary, replicas: replicas}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = r.choose("SELECT 1")
		}()
	}
	wg.Wait()
}
