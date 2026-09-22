package orm

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// auditExecutor 只记录下发的 SQL 与参数，不连真实数据库（供 SQL 生成断言使用）。
type auditExecutor struct {
	query string
	args  []any
}

func (e *auditExecutor) QueryContext(_ context.Context, query string, args ...any) (*sql.Rows, error) {
	e.query, e.args = query, args
	return nil, sql.ErrNoRows
}

func (e *auditExecutor) QueryRowContext(_ context.Context, query string, args ...any) *sql.Row {
	e.query, e.args = query, args
	return &sql.Row{}
}

func (e *auditExecutor) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	e.query, e.args = query, args
	return auditResult(1), nil
}

type auditResult int64

func (r auditResult) LastInsertId() (int64, error) { return int64(r), nil }
func (r auditResult) RowsAffected() (int64, error) { return int64(r), nil }

type auditVecUser struct {
	Id   int64     `db:"id,pk"`
	Name string    `db:"name"`
	Age  int       `db:"age"`
	Vec  []float32 `db:"vec,vector(3)"`
}

// float32 定长数组不应因位宽提升而多出无意义精度位（与 []float32 结果一致）。
func TestVectorFloat32ArrayPrecision(t *testing.T) {
	want := "[0.1,1.5,2.2]"
	if got := serializeVector([]float32{0.1, 1.5, 2.2}); got != want {
		t.Fatalf("[]float32 序列化应为 %s，实际 %v", want, got)
	}
	if got := serializeVector([3]float32{0.1, 1.5, 2.2}); got != want {
		t.Fatalf("[3]float32 序列化应为 %s，实际 %v", want, got)
	}
	if got := serializeVector([]float64{0.1, 1.5, 2.2}); got != want {
		t.Fatalf("[]float64 序列化应为 %s，实际 %v", want, got)
	}
}

// map 形式的部分更新必须拒绝不存在的列名（拼写错误 / 外部输入拼接的注入面）。
func TestUpdateSetsRejectsUnknownCol(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"nmae", `name = 'x' OR 1=1 --`} {
		exec := &auditExecutor{}
		db := NewDB(exec, PG)
		q := NewQuery[auditVecUser]().Eq(Col[auditVecUser](func(u *auditVecUser) *int64 { return &u.Id }), 1)
		if _, err := UpdateByIdSets[auditVecUser](ctx, db, int64(1), map[string]any{name: "x"}); err == nil {
			t.Fatalf("UpdateByIdSets 应拒绝未知列 %q，实际未报错（SQL=%s）", name, exec.query)
		}
		if _, err := UpdateSets[auditVecUser](ctx, db, q.Set(ColExpr{name: name}, "x")); err == nil {
			t.Fatalf("UpdateSets 应拒绝未知列 %q", name)
		}
		if exec.query != "" {
			t.Fatalf("校验失败时不应下发 SQL，实际 %s", exec.query)
		}
	}
}

// 合法列名仍可正常更新，且参数顺序与占位符一致。
func TestUpdateByIdSetsKeepsValidCols(t *testing.T) {
	exec := &auditExecutor{}
	db := NewDB(exec, PG)
	if _, err := UpdateByIdSets[auditVecUser](context.Background(), db, int64(7), map[string]any{"name": "bob"}); err != nil {
		t.Fatalf("合法列名不应报错: %v", err)
	}
	if !strings.Contains(exec.query, `SET "name" = $1`) || !strings.Contains(exec.query, `WHERE "id" = $2`) {
		t.Fatalf("SQL 形状不符: %s", exec.query)
	}
	if len(exec.args) != 2 || exec.args[0] != "bob" || exec.args[1] != int64(7) {
		t.Fatalf("参数应与占位符同序 [bob 7]，实际 %v", exec.args)
	}
}

// 只有 Offset 没有 Limit 时也要生成合法 SQL：MySQL 补无上限 LIMIT、SQLite 补 LIMIT -1；
// PostgreSQL 的 OFFSET 可独立使用且拒绝负数 LIMIT，故不补。未设 Limit/Offset 时无 LIMIT。
func TestOffsetWithoutLimit(t *testing.T) {
	withLimit := NewQuery[auditVecUser]().Limit(10).Offset(20)
	if sqlStr, _ := withLimit.Build(); !strings.Contains(sqlStr, "LIMIT 10 OFFSET 20") {
		t.Fatalf("Limit+Offset 应生成 LIMIT 10 OFFSET 20，实际 %s", sqlStr)
	}

	only := NewQuery[auditVecUser]().Offset(20)
	// PG 拒绝负数 LIMIT（"LIMIT must not be negative"），但允许独立的 OFFSET，故不应补 LIMIT。
	pgSQL, _ := only.WithDialect(PG).Build()
	if strings.Contains(pgSQL, "LIMIT") {
		t.Fatalf("PG 不应补 LIMIT（PG 拒绝负数 LIMIT），实际 %s", pgSQL)
	}
	if !strings.Contains(pgSQL, "OFFSET 20") {
		t.Fatalf("PG 应保留 OFFSET 20，实际 %s", pgSQL)
	}
	sqliteSQL, _ := only.WithDialect(SQLite).Build()
	if !strings.Contains(sqliteSQL, "LIMIT -1 OFFSET 20") {
		t.Fatalf("SQLite 仅 Offset 应补 LIMIT -1，实际 %s", sqliteSQL)
	}
	mySQL, _ := only.WithDialect(MySQL).Build()
	if !strings.Contains(mySQL, "LIMIT 18446744073709551615 OFFSET 20") {
		t.Fatalf("MySQL 仅 Offset 应补无上限 LIMIT，实际 %s", mySQL)
	}

	if sqlStr, _ := NewQuery[auditVecUser]().Build(); strings.Contains(sqlStr, "LIMIT") {
		t.Fatalf("未设置 Limit/Offset 时不应出现 LIMIT，实际 %s", sqlStr)
	}
}
