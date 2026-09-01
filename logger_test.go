package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"
)

// ---- 等级过滤逻辑 ----

func TestLogSilent(t *testing.T) {
	var got int
	db := &DB{logger: func(LogLevel, string, []any, time.Duration, error) { got++ }}
	db.logLevel = Silent
	db.logSlowOrErr("SELECT 1", nil, time.Millisecond, nil)
	if got != 0 {
		t.Fatalf("Silent 不应输出日志，实际 %d 次", got)
	}
}

func TestLogInfoAlways(t *testing.T) {
	var levels []LogLevel
	db := &DB{
		logger:   func(l LogLevel, _ string, _ []any, _ time.Duration, _ error) { levels = append(levels, l) },
		logLevel: Info,
	}
	db.logSlowOrErr("SELECT 1", nil, time.Millisecond, nil)                // Info
	db.logSlowOrErr("SELECT 1", nil, time.Millisecond, errors.New("boom")) // Error
	if len(levels) != 2 || levels[0] != Info || levels[1] != Error {
		t.Fatalf("Info 级别应输出全部，实际 %v", levels)
	}
}

func TestLogErrorOnly(t *testing.T) {
	var levels []LogLevel
	db := &DB{
		logger:   func(l LogLevel, _ string, _ []any, _ time.Duration, _ error) { levels = append(levels, l) },
		logLevel: Error,
	}
	db.logSlowOrErr("SELECT 1", nil, time.Millisecond, nil)                // Info < Error，跳过
	db.logSlowOrErr("SELECT 1", nil, time.Millisecond, errors.New("boom"))  // Error
	if len(levels) != 1 || levels[0] != Error {
		t.Fatalf("Error 级别应只输出错误，实际 %v", levels)
	}
}

func TestLogSlowWarn(t *testing.T) {
	var levels []LogLevel
	db := &DB{
		logger:        func(l LogLevel, _ string, _ []any, _ time.Duration, _ error) { levels = append(levels, l) },
		logLevel:      Warn,
		slowThreshold: time.Millisecond,
	}
	db.logSlowOrErr("SELECT 1", nil, time.Millisecond, nil)                 // 不慢，Info < Warn，跳过
	db.logSlowOrErr("SELECT 1", nil, 5*time.Millisecond, nil)               // 慢，Warn
	db.logSlowOrErr("SELECT 1", nil, time.Millisecond, errors.New("boom"))  // Error
	if len(levels) != 2 || levels[0] != Warn || levels[1] != Error {
		t.Fatalf("Warn 级别应输出慢查询与错误，实际 %v", levels)
	}
}

// ---- 真实执行是否触发日志（Insert 走 execContext） ----

type logExec struct {
	queries []string
}

func (e *logExec) ExecContext(_ context.Context, q string, _ ...any) (sql.Result, error) {
	e.queries = append(e.queries, q)
	return driver.RowsAffected(0), nil
}
func (e *logExec) QueryContext(_ context.Context, q string, _ ...any) (*sql.Rows, error) {
	e.queries = append(e.queries, q)
	return nil, errors.New("logger test: query not supported")
}
func (e *logExec) QueryRowContext(_ context.Context, q string, _ ...any) *sql.Row {
	e.queries = append(e.queries, q)
	return nil
}

type logRow struct {
	ID   int64  `db:"id,pk"`
	Name string `db:"name"`
}

func TestLogExecTriggered(t *testing.T) {
	var logged []string
	ex := &logExec{}
	db := NewDB(ex, PG).
		WithLogger(func(l LogLevel, q string, _ []any, _ time.Duration, _ error) { logged = append(logged, q) }).
		WithLogLevel(Info)

	if err := Insert(context.Background(), db, &logRow{Name: "Alice"}); err != nil {
		t.Fatalf("Insert 不应报错: %v", err)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "INSERT") {
		t.Fatalf("Insert 应触发一条 INSERT 日志，实际 %v", logged)
	}
}

func TestLogInheritedByTx(t *testing.T) {
	var logged []string
	var beginCalled bool
	ex := &logExec{}
	db := NewDB(ex, PG).
		WithLogger(func(l LogLevel, q string, _ []any, _ time.Duration, _ error) { logged = append(logged, q) }).
		WithLogLevel(Info)

	// 用不支持事务的执行器验证 WithExecutor 继承了 logger（这里直接测试 WithExecutor 拷贝）
	tx := db.WithExecutor(ex)
	if err := Insert(context.Background(), tx, &logRow{Name: "Bob"}); err != nil {
		t.Fatalf("Insert 不应报错: %v", err)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "INSERT") {
		t.Fatalf("事务副本应继承 logger，实际 %v", logged)
	}
	_ = beginCalled
}
