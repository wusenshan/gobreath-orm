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

// ---------------------------------------------------------------- 空集合条件

// In(空) 必须折叠为恒假（1 = 0），而不是拼出非法的 `IN ()`（MySQL 1064 / PG 42601 /
// SQLite 1）。动态多选条件「一个都没勾」是常态输入，不能把非法 SQL 抛给数据库。
func TestInEmptySliceFoldsToFalse(t *testing.T) {
	col := ColExpr{name: "age"}
	for _, vals := range [][]any{nil, {}} {
		sqlStr, args := NewQuery[auditVecUser]().WithDialect(PG).In(col, vals).Build()
		if !strings.Contains(sqlStr, "1 = 0") {
			t.Fatalf("In(空) 应折叠为 1 = 0，实际 %s", sqlStr)
		}
		if strings.Contains(sqlStr, "IN") {
			t.Fatalf("In(空) 不应出现 IN 子句，实际 %s", sqlStr)
		}
		if len(args) != 0 {
			t.Fatalf("In(空) 折叠后不应绑定参数，实际 %v", args)
		}
	}
}

// NotIn(空) 按集合语义是恒真（所有行匹配），整条条件应被省略 —— 剩余条件照常生效。
func TestNotInEmptySliceOmitsCondition(t *testing.T) {
	q := NewQuery[auditVecUser]().WithDialect(PG).
		Eq(ColExpr{name: "id"}, int64(1)).
		NotIn(ColExpr{name: "age"}, []any{})
	sqlStr, args := q.Build()
	if strings.Contains(sqlStr, "NOT IN") {
		t.Fatalf("NotIn(空) 不应出现 NOT IN 子句，实际 %s", sqlStr)
	}
	if !strings.Contains(sqlStr, `"id" = $1`) {
		t.Fatalf("其余条件应保留，实际 %s", sqlStr)
	}
	if len(args) != 1 {
		t.Fatalf("只应绑定剩余条件的参数，实际 %v", args)
	}
}

// ---------------------------------------------------------------- 批量写入 + OmitZero

// batchZeroUser 刻意让 pk 无自增、字段可全零，便于构造「首行全零、后续行非零」的场景。
type batchZeroUser struct {
	ID   int64  `db:"id,pk"`
	Name string `db:"name"`
	Age  int    `db:"age"`
}

func (batchZeroUser) TableName() string { return "bz_users" }

// 批量写入 + OmitZero 的列集合必须取「所有行的交集」，而不是只按 entities[0] 决定。
// 旧实现下首行的零值列（name）会被整批剔除，第二行的 name="later" 静默丢失：
// SQL 里根本没有这一列、不报错，调用方以为写进去了。
func TestBatchInsertOmitZeroMultiRowKeepsLaterNonZero(t *testing.T) {
	exec := &auditExecutor{}
	db := NewDB(exec, PG)
	rows := []batchZeroUser{
		{ID: 1, Name: "", Age: 0},      // 首行全零 —— 旧实现据此定列
		{ID: 2, Name: "later", Age: 0}, // 第二行 name 非零，必须留在列集合里
	}
	if err := BatchInsert(context.Background(), db, rows, OmitZero()); err != nil {
		t.Fatalf("BatchInsert 失败：%v", err)
	}
	if !strings.Contains(exec.query, `"name"`) {
		t.Fatalf("后续行的非零列 name 被丢弃，SQL=%s", exec.query)
	}
	if strings.Contains(exec.query, `"age"`) {
		t.Fatalf("所有行都为零值的列 age 应被跳过，SQL=%s", exec.query)
	}
	if len(exec.args) != 4 {
		t.Fatalf("两行 × 两列应绑定 4 个参数，实际 %d 个：%v", len(exec.args), exec.args)
	}
	if exec.args[1] != "" || exec.args[3] != "later" {
		t.Fatalf("参数顺序应为 [1 \"\" 2 later]，实际 %v", exec.args)
	}
}

// BatchUpsert 走的是同一条列决策路径，必须与 BatchInsert 行为一致。
func TestBatchUpsertOmitZeroMultiRowKeepsLaterNonZero(t *testing.T) {
	exec := &auditExecutor{}
	db := NewDB(exec, PG)
	rows := []batchZeroUser{
		{ID: 1, Name: "", Age: 7},
		{ID: 2, Name: "later", Age: 7},
	}
	if err := BatchUpsert(context.Background(), db, rows, []string{"id"}, OmitZero()); err != nil {
		t.Fatalf("BatchUpsert 失败：%v", err)
	}
	if !strings.Contains(exec.query, `"name"`) {
		t.Fatalf("后续行的非零列 name 被丢弃，SQL=%s", exec.query)
	}
	if !strings.Contains(exec.query, `"age"`) {
		t.Fatalf("两行都非零的 age 应保留，SQL=%s", exec.query)
	}
	if len(exec.args) != 6 {
		t.Fatalf("两行 × 三列应绑定 6 个参数，实际 %d 个：%v", len(exec.args), exec.args)
	}
	if exec.args[1] != "" || exec.args[4] != "later" {
		t.Fatalf("参数顺序应为 [1 \"\" 7 2 later 7]，实际 %v", exec.args)
	}
}

// 全行都是零值的列仍应被跳过（OmitZero 的本意不能被修复改成「一律不跳过」）。
func TestBatchInsertOmitZeroDropsAllZeroColumn(t *testing.T) {
	exec := &auditExecutor{}
	db := NewDB(exec, PG)
	rows := []batchZeroUser{{ID: 1, Name: "", Age: 0}, {ID: 2, Name: "", Age: 0}}
	if err := BatchInsert(context.Background(), db, rows, OmitZero()); err != nil {
		t.Fatalf("BatchInsert 失败：%v", err)
	}
	if !strings.Contains(exec.query, `("id")`) {
		t.Fatalf("两行全零时列集合应只剩主键列，SQL=%s", exec.query)
	}
}

// ---------------------------------------------------------------- Select 列名校验

// Select 的每个参数必须是单个列名：多列写在一个字符串里会被整体加引号，
// 拼出 `name, age` 这种非法列名 —— 框架不报错、只在数据库侧以 unknown column 暴露。
func TestSelectRejectsMultiColumnString(t *testing.T) {
	for _, bad := range []string{"name, age", "name age", "COUNT(*)", ""} {
		bad := bad
		assertPanics(t, "Select("+bad+")", func() { NewQuery[auditVecUser]().Select(bad) })
	}
	// 合法用法不受影响：逐个传入、带表别名前缀。
	q := NewQuery[auditVecUser]().WithDialect(PG).Select("id", "u.name")
	sqlStr, _ := q.Build()
	if !strings.Contains(sqlStr, `"id"`) || !strings.Contains(sqlStr, "name") {
		t.Fatalf("合法列名应正常拼进 SQL，实际 %s", sqlStr)
	}
}
