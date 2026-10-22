package orm

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
)

// iterRowsSpy 注册「FROM users」命中的固定结果集（工厂每次现造，可重复查询），
// last 持有最近一次构造的 mockRows，用于断言提前停止时的读取位置。
type iterRowsSpy struct{ last *mockRows }

func (s *iterRowsSpy) register(t *testing.T, cols []string, data [][]driver.Value) {
	t.Helper()
	// 键带引号：SQLite 方言生成 FROM "users"，裸写 FROM users 不会被子串命中
	// （这里全部用 SQLite 跑 mock，其他方言的 SQL 形态由 auditExecutor 用例覆盖）。
	mockFactories[`FROM "users"`] = func() driver.Rows {
		s.last = &mockRows{cols: cols, data: data}
		return s.last
	}
	t.Cleanup(func() { delete(mockFactories, `FROM "users"`) })
}

// TestQueryIterRowsInOrder 逐行产出的实体值与结果集行序一致，
// 空结果集产出 0 行、无错误 —— 与 SelectList 返回空切片同语义。
// 行映射走 newRowScanner（与 SelectList 共用列映射计划），方言无关，单方言验证。
func TestQueryIterRowsInOrder(t *testing.T) {
	ctx := context.Background()
	db := newMockDB(t)
	spy := &iterRowsSpy{}
	spy.register(t, []string{"id", "name", "age"}, [][]driver.Value{
		{driver.Value(int64(1)), driver.Value("alice"), driver.Value(int64(20))},
		{driver.Value(int64(2)), driver.Value("bob"), driver.Value(int64(21))},
		{driver.Value(int64(3)), driver.Value("carol"), driver.Value(int64(22))},
	})
	var got []User
	for u, err := range QueryIter[User](ctx, db, NewQuery[User]()) {
		if err != nil {
			t.Fatalf("不应报错: %v", err)
		}
		got = append(got, *u)
	}
	if len(got) != 3 {
		t.Fatalf("产出行数 = %d，期望 3", len(got))
	}
	if got[0].Name != "alice" || got[1].Name != "bob" || got[2].Name != "carol" {
		t.Fatalf("行序错乱: %v", got)
	}

	// 空结果集：0 行无错误。
	spy2 := &iterRowsSpy{}
	spy2.register(t, []string{"id", "name", "age"}, nil)
	n := 0
	for _, err := range QueryIter[User](ctx, db, NewQuery[User]()) {
		if err != nil {
			t.Fatalf("空结果集不应报错: %v", err)
		}
		n++
	}
	if n != 0 {
		t.Fatalf("空结果集行数 = %d，期望 0", n)
	}
}

// TestQueryIterEarlyBreak 消费方 break 后游标必须立即停止：
// 结果集有 5 行，break 在第 2 行之后 —— mockRows.pos 不允许推进到 3 以后。
func TestQueryIterEarlyBreak(t *testing.T) {
	ctx := context.Background()
	db := newMockDB(t)
	spy := &iterRowsSpy{}
	spy.register(t, []string{"id", "name", "age"}, [][]driver.Value{
		{driver.Value(int64(1)), driver.Value("u1"), driver.Value(int64(1))},
		{driver.Value(int64(2)), driver.Value("u2"), driver.Value(int64(2))},
		{driver.Value(int64(3)), driver.Value("u3"), driver.Value(int64(3))},
		{driver.Value(int64(4)), driver.Value("u4"), driver.Value(int64(4))},
		{driver.Value(int64(5)), driver.Value("u5"), driver.Value(int64(5))},
	})
	consumed := 0
	for range QueryIter[User](ctx, db, NewQuery[User]()) {
		consumed++
		if consumed == 2 {
			break
		}
	}
	if consumed != 2 {
		t.Fatalf("消耗行数 = %d，期望 2", consumed)
	}
	if spy.last.pos > 2 {
		t.Fatalf("break 后游标仍推进到第 %d 行 —— 期望立即停止读取", spy.last.pos)
	}
}

// TestQueryIterLogicDelete 与 SelectList 一致的逻辑删除口径：软删模型的
// SQL 必须带逻辑删除条件，Unscoped 才能去掉。三方言各跑一次 SQL 形态
// （auditExecutor 只捕获 SQL 不产出行，正好够用）。
func TestQueryIterLogicDelete(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name    string
		dialect Dialect
	}{
		{"PG", PG}, {"MySQL", MySQL}, {"SQLite", SQLite},
	} {
		wantCond := c.dialect.QuoteIdent("deleted_at") + " IS NULL"

		exec := &auditExecutor{}
		db := NewDB(exec, c.dialect)
		for range QueryIter[SoftUser](ctx, db, NewQuery[SoftUser]()) {
		}
		if !strings.Contains(exec.query, wantCond) {
			t.Fatalf("[%s] 软删模型缺少逻辑删除条件 %s（SQL=%s）", c.name, wantCond, exec.query)
		}

		exec2 := &auditExecutor{}
		db2 := NewDB(exec2, c.dialect)
		for range QueryIter[SoftUser](ctx, db2, NewQuery[SoftUser]().Unscoped()) {
		}
		if strings.Contains(exec2.query, "deleted_at") {
			t.Fatalf("[%s] Unscoped 不应再有逻辑删除条件（SQL=%s）", c.name, exec2.query)
		}
	}
}

// TestQueryIterYieldError 行扫描失败必须走（nil, err）通道一次后终止，
// 而不是 panic 或静默丢行：第一行正常产出，第二行 age 列给了字符串，
// int 转换报错 —— 产出统计应为 行=1 错误=1。
func TestQueryIterYieldError(t *testing.T) {
	ctx := context.Background()
	db := newMockDB(t)
	spy := &iterRowsSpy{}
	spy.register(t, []string{"id", "name", "age"}, [][]driver.Value{
		{driver.Value(int64(1)), driver.Value("ok"), driver.Value(int64(1))},
		{driver.Value(int64(2)), driver.Value("bad"), driver.Value("oops")},
	})
	seen, errs := 0, 0
	for _, err := range QueryIter[User](ctx, db, NewQuery[User]()) {
		if err != nil {
			errs++
		} else {
			seen++
		}
	}
	if seen != 1 || errs != 1 {
		t.Fatalf("产出统计: 行=%d 错误=%d，期望 行=1 错误=1", seen, errs)
	}
}

// TestQueryIterRepoBinding Repo 包装与包级函数同语义。
func TestQueryIterRepoBinding(t *testing.T) {
	ctx := context.Background()
	db := newMockDB(t)
	spy := &iterRowsSpy{}
	spy.register(t, []string{"id", "name", "age"}, [][]driver.Value{
		{driver.Value(int64(1)), driver.Value("a"), driver.Value(int64(1))},
		{driver.Value(int64(2)), driver.Value("b"), driver.Value(int64(2))},
	})
	repo := NewRepo[User](db)
	n := 0
	for range repo.QueryIter(ctx, NewQuery[User]()) {
		n++
	}
	if n != 2 {
		t.Fatalf("Repo.QueryIter 行数 = %d，期望 2", n)
	}
}

// TestErrNotSupportedDescribeTable 未知方言的自省错误必须可被 errors.Is
// 判别为 ErrNotSupported ——「不知道」要显式失败，调用方按错误类型分流，
// 而不是靠字符串匹配。
func TestErrNotSupportedDescribeTable(t *testing.T) {
	ctx := context.Background()
	db := NewDB(&auditExecutor{}, unknownDialect{Dialect: SQLite})
	if _, err := DescribeTable(ctx, db, "users"); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("未知方言 DescribeTable 应返回 ErrNotSupported，实际: %v", err)
	}
	if _, err := TableExists(ctx, db, "users"); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("未知方言 TableExists 应返回 ErrNotSupported，实际: %v", err)
	}
	if _, err := ColumnExists(ctx, db, "users", "id"); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("未知方言 ColumnExists 应返回 ErrNotSupported，实际: %v", err)
	}
}
