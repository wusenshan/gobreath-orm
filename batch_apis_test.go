package orm

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"
)

// ---- 本文件专用测试模型 ----

// batchSoftUser 带时间型逻辑删除列，用于 DeleteByIds 软删分支的参数顺序断言。
type batchSoftUser struct {
	Id        int64      `db:"id,pk,autoincrement"`
	Name      string     `db:"name"`
	DeletedAt *time.Time `db:"deleted_at,logic"`
}

func (batchSoftUser) TableName() string { return "batch_soft_users" }

// batchVerUser 带乐观锁版本列，用于 UpdateByIdsSets 的版本列拒绝断言。
type batchVerUser struct {
	Id      int64  `db:"id,pk,autoincrement"`
	Name    string `db:"name"`
	Version int    `db:"version,version"`
}

func (batchVerUser) TableName() string { return "batch_ver_users" }

// batchNoPK 无主键模型。字段名刻意避开 ID/Id —— 解析器有「未显式声明主键时
// 字段名 ID/Id 推断为主键」的约定（model.go），叫 Id 会被静默推断成有主键，
// 让「无主键应报错」的断言变成假绿。
type batchNoPK struct {
	NoKey int64  `db:"no_key"`
	Name  string `db:"name"`
}

// unknownDialect 既非内置方言类型、也不实现 DialectKinder —— 嵌入的是 Dialect
// 接口值本身，而内置方言结构体没有可被 promotion 的 DialectKind 方法，
// 所以 dialectKindKnown 应对它返回 not known（自省 API 据此报错而不是瞎猜 SQLite）。
type unknownDialect struct{ Dialect }

// inExpect 生成 `IN (?, ?, ?)` / `IN ($2, $3, $4)` 形态的期望串（占位符从 start 起）。
func inExpect(d Dialect, start, n int) string {
	phs := make([]string, n)
	for i := range phs {
		phs[i] = d.Placeholder(start + i)
	}
	return "IN (" + strings.Join(phs, ", ") + ")"
}

// ---- SelectByIds ----

// TestSelectByIdsSQLAndArgs 断言三方言的 IN 占位符与实参逐位对齐：
// 位置型方言（MySQL / SQLite 的 ?）按出现次序消耗实参，ids 顺序即占位符顺序；
// PG 的 $n 自带序号，从 $1 连续编到 $N。
func TestSelectByIdsSQLAndArgs(t *testing.T) {
	ctx := context.Background()
	ids := []any{int64(1), int64(2), int64(7)}
	for _, c := range []struct {
		name    string
		dialect Dialect
	}{
		{"PG", PG}, {"MySQL", MySQL}, {"SQLite", SQLite},
	} {
		exec := &auditExecutor{}
		db := NewDB(exec, c.dialect)
		_, _ = SelectByIds[User](ctx, db, ids...)
		want := "SELECT * FROM " + c.dialect.QuoteIdent("users") +
			" WHERE " + c.dialect.QuoteIdent("id") + " " + inExpect(c.dialect, 1, 3)
		if !strings.Contains(exec.query, want) {
			t.Fatalf("[%s] SQL 缺少期望片段。\n want: %s\n  got: %s", c.name, want, exec.query)
		}
		// User 无逻辑删除列：IN 之后不应再追加任何条件。
		if strings.Contains(exec.query, "deleted_at") {
			t.Fatalf("[%s] 无 logic 列的模型不应出现逻辑删除条件（SQL=%s）", c.name, exec.query)
		}
		for i, id := range ids {
			if exec.args[i] != id {
				t.Fatalf("[%s] args[%d] = %#v，期望 %#v —— IN 实参顺序与占位符错位（SQL=%s）",
					c.name, i, exec.args[i], id, exec.query)
			}
		}
	}
}

// TestSelectByIdsSoftDeleteSuffix 带逻辑删除列的模型批量查应自动过滤已删除行。
func TestSelectByIdsSoftDeleteSuffix(t *testing.T) {
	exec := &auditExecutor{}
	db := NewDB(exec, SQLite)
	_, _ = SelectByIds[batchSoftUser](context.Background(), db, int64(1))
	want := "SELECT * FROM " + SQLite.QuoteIdent("batch_soft_users") +
		" WHERE " + SQLite.QuoteIdent("id") + " IN (?) AND " +
		SQLite.QuoteIdent("deleted_at") + " IS NULL"
	if !strings.Contains(exec.query, want) {
		t.Fatalf("SQL 缺少软删除过滤。\n want: %s\n  got: %s", want, exec.query)
	}
}

// TestSelectByIdsGuards 空 ID 列表必须显式报错（ErrInvalidArgument），
// 而不是折叠成恒真条件静默查全表；无主键模型同理。
func TestSelectByIdsGuards(t *testing.T) {
	ctx := context.Background()
	db := NewDB(&auditExecutor{}, SQLite)

	_, err := SelectByIds[User](ctx, db)
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("空 ID 列表应返回 ErrInvalidArgument，实际 %v", err)
	}
	_, err = SelectByIds[batchNoPK](ctx, db, int64(1))
	if err == nil || strings.Contains(err.Error(), "参数不合法") {
		t.Fatalf("无主键模型应报「无主键」，实际 %v", err)
	}

	// DeleteByIds / ForceDeleteByIds / UpdateByIdsSets 的空列表守卫同口径。
	if _, err := DeleteByIds[User](ctx, db); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("DeleteByIds 空 ID 列表应返回 ErrInvalidArgument，实际 %v", err)
	}
	if _, err := ForceDeleteByIds[User](ctx, db); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("ForceDeleteByIds 空 ID 列表应返回 ErrInvalidArgument，实际 %v", err)
	}
	if _, err := UpdateByIdsSets[User](ctx, db, nil, map[string]any{"name": "x"}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("UpdateByIdsSets 空 ID 列表应返回 ErrInvalidArgument，实际 %v", err)
	}
	if _, err := UpdateByIdsSets[User](ctx, db, []any{int64(1)}, nil); err == nil || !strings.Contains(err.Error(), "至少需要一个字段") {
		t.Fatalf("UpdateByIdsSets 空 sets 应报错，实际 %v", err)
	}
}

// ---- DeleteByIds / ForceDeleteByIds ----

// TestDeleteByIdsSoftDeleteArgOrder 是「批量软删参数错位 → 静默 0 行」的预防性用例：
// SET 在 SQL 文本中先于 WHERE（IN）出现，所以删除值必须占第一个占位符、
// IN 列表从第二个起。位置型方言上顺序颠倒会把删除时间值绑给条件列，
// 匹配 0 行、不报错、不删除 —— 参数个数完全正确，mock 的 NumInput()=-1 也看不见，
// 必须逐位断言（与 TestDeleteArgsFollowPlaceholderOrder 同口径）。
func TestDeleteByIdsSoftDeleteArgOrder(t *testing.T) {
	ctx := context.Background()
	ids := []any{int64(3), int64(9)}
	for _, c := range []struct {
		name    string
		dialect Dialect
	}{
		{"PG", PG}, {"MySQL", MySQL}, {"SQLite", SQLite},
	} {
		exec := &auditExecutor{}
		db := NewDB(exec, c.dialect)
		// before 必须取在调用**之前**：删除时间在 DeleteByIds 内部生成，
		// 晚于此刻。曾经把它写在调用之后，断言「删除时间早于调用后时刻」——
		// 逻辑上恒真，只因 Windows 时钟粒度粗（两次读数落在同一 tick）
		// 才一直假绿，Linux 的纳秒精度立刻转红。
		before := time.Now()
		n, err := DeleteByIds[batchSoftUser](ctx, db, ids...)
		if err != nil {
			t.Fatalf("[%s] DeleteByIds 出错：%v", c.name, err)
		}
		if n != 1 { // auditExecutor 的 auditResult 恒为 1
			t.Fatalf("[%s] RowsAffected = %d，期望 1", c.name, n)
		}
		if !strings.HasPrefix(exec.query, "UPDATE "+c.dialect.QuoteIdent("batch_soft_users")) {
			// 软删分支必须是 UPDATE（保持 DeleteById 的同语义），绝不出现物理 DELETE。
			t.Fatalf("[%s] 软删分支应生成 UPDATE，实际 %s", c.name, exec.query)
		}
		if strings.Contains(exec.query, "DELETE FROM") {
			t.Fatalf("[%s] 软删分支不应生成物理 DELETE，实际 %s", c.name, exec.query)
		}
		setAt, whereAt := strings.Index(exec.query, " SET "), strings.Index(exec.query, " WHERE ")
		if setAt < 0 || whereAt < 0 || setAt > whereAt {
			t.Fatalf("[%s] SQL 形状异常（SET 应在 WHERE 之前）：%s", c.name, exec.query)
		}
		// IN 占位符从第 2 个起（第 1 个被删除值占用）。
		if !strings.Contains(exec.query, c.dialect.QuoteIdent("id")+" "+inExpect(c.dialect, 2, 2)) {
			t.Fatalf("[%s] IN 占位符应从第 2 个起。\n want: %s\n  got: %s",
				c.name, inExpect(c.dialect, 2, 2), exec.query)
		}
		delVal, ok := exec.args[0].(time.Time)
		if !ok || delVal.Before(before) {
			t.Fatalf("[%s] args[0] 应为删除时间值，实际 %#v —— SET 与 IN 的实参顺序错位（SQL=%s）",
				c.name, exec.args[0], exec.query)
		}
		for i, id := range ids {
			if exec.args[i+1] != id {
				t.Fatalf("[%s] args[%d] = %#v，期望 %#v（SQL=%s）", c.name, i+1, exec.args[i+1], id, exec.query)
			}
		}
	}
}

// TestDeleteByIdsHardDelete 无逻辑删除列的模型走物理 DELETE，参数即 ids。
func TestDeleteByIdsHardDelete(t *testing.T) {
	exec := &auditExecutor{}
	db := NewDB(exec, MySQL)
	n, err := DeleteByIds[User](context.Background(), db, int64(4), int64(5))
	if err != nil || n != 1 {
		t.Fatalf("DeleteByIds：n=%d err=%v", n, err)
	}
	want := "DELETE FROM " + MySQL.QuoteIdent("users") + " WHERE " + MySQL.QuoteIdent("id") + " IN (?, ?)"
	if !strings.HasPrefix(exec.query, want) {
		t.Fatalf("应生成物理 DELETE + IN。\n want: %s\n  got: %s", want, exec.query)
	}
	if len(exec.args) != 2 || exec.args[0] != int64(4) || exec.args[1] != int64(5) {
		t.Fatalf("args 应为 [4 5]，实际 %#v", exec.args)
	}
}

// TestForceDeleteByIdsIgnoresLogic 带逻辑删除列的模型 ForceDeleteByIds 必须物理删除。
func TestForceDeleteByIdsIgnoresLogic(t *testing.T) {
	exec := &auditExecutor{}
	db := NewDB(exec, SQLite)
	n, err := ForceDeleteByIds[batchSoftUser](context.Background(), db, int64(6))
	if err != nil || n != 1 {
		t.Fatalf("ForceDeleteByIds：n=%d err=%v", n, err)
	}
	want := "DELETE FROM " + SQLite.QuoteIdent("batch_soft_users") + " WHERE " + SQLite.QuoteIdent("id") + " IN (?)"
	if !strings.HasPrefix(exec.query, want) {
		t.Fatalf("应生成物理 DELETE。\n want: %s\n  got: %s", want, exec.query)
	}
	if len(exec.args) != 1 || exec.args[0] != int64(6) {
		t.Fatalf("args 应为 [6]，实际 %#v", exec.args)
	}
}

// ---- UpdateByIdsSets ----

// TestUpdateByIdsSetsOrderAndArgs 断言 SET 列按名称排序（同一份输入 SQL 恒定）
// 且实参顺序 = SET 值在前、ids 在后，与占位符出现顺序逐位一致。
func TestUpdateByIdsSetsOrderAndArgs(t *testing.T) {
	ctx := context.Background()
	ids := []any{int64(1), int64(2)}
	for _, c := range []struct {
		name    string
		dialect Dialect
	}{
		{"PG", PG}, {"MySQL", MySQL}, {"SQLite", SQLite},
	} {
		exec := &auditExecutor{}
		db := NewDB(exec, c.dialect)
		n, err := UpdateByIdsSets[User](ctx, db, ids, map[string]any{"name": "x", "age": 5})
		if err != nil || n != 1 {
			t.Fatalf("[%s] UpdateByIdsSets：n=%d err=%v", c.name, n, err)
		}
		// 列按字母序：age 在 name 前。占位符：SET 占 1..2，IN 占 3..4。
		want := "UPDATE " + c.dialect.QuoteIdent("users") +
			" SET " + c.dialect.QuoteIdent("age") + " = " + c.dialect.Placeholder(1) +
			", " + c.dialect.QuoteIdent("name") + " = " + c.dialect.Placeholder(2) +
			" WHERE " + c.dialect.QuoteIdent("id") + " " + inExpect(c.dialect, 3, 2)
		if !strings.Contains(exec.query, want) {
			t.Fatalf("[%s] SQL 不符。\n want: %s\n  got: %s", c.name, want, exec.query)
		}
		if exec.args[0] != 5 || exec.args[1] != "x" {
			t.Fatalf("[%s] SET 值应为 [5 x]，实际 %v %v（SQL=%s）", c.name, exec.args[0], exec.args[1], exec.query)
		}
		if exec.args[2] != int64(1) || exec.args[3] != int64(2) {
			t.Fatalf("[%s] IN 实参应为 [1 2]，实际 %#v —— SET 与 IN 的实参顺序错位（SQL=%s）",
				c.name, exec.args[2:], exec.query)
		}
	}
}

// TestUpdateByIdsSetsSoftDeleteSuffix 批量更新同样跳过已逻辑删除的行。
func TestUpdateByIdsSetsSoftDeleteSuffix(t *testing.T) {
	exec := &auditExecutor{}
	db := NewDB(exec, MySQL)
	_, err := UpdateByIdsSets[batchSoftUser](context.Background(), db,
		[]any{int64(1)}, map[string]any{"name": "x"})
	if err != nil {
		t.Fatalf("UpdateByIdsSets 出错：%v", err)
	}
	want := "AND " + MySQL.QuoteIdent("deleted_at") + " IS NULL"
	if !strings.Contains(exec.query, want) {
		t.Fatalf("应自动追加软删除过滤。\n want: %s\n  got: %s", want, exec.query)
	}
}

// TestUpdateByIdsSetsRejectsVersionColumn 批量形态无法做多行单版本冲突检测，
// sets 含版本列必须报错而不是静默写出只对个别行成立的条件。
func TestUpdateByIdsSetsRejectsVersionColumn(t *testing.T) {
	db := NewDB(&auditExecutor{}, MySQL)
	_, err := UpdateByIdsSets[batchVerUser](context.Background(), db,
		[]any{int64(1)}, map[string]any{"name": "x", "version": 3})
	if err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("sets 含版本列应报错（提及 version），实际 %v", err)
	}
	// 版本列不在 sets 里时不应误伤普通批量更新，且 SQL 不应触及版本列。
	exec := &auditExecutor{}
	db2 := NewDB(exec, MySQL)
	if _, err := UpdateByIdsSets[batchVerUser](context.Background(), db2,
		[]any{int64(1)}, map[string]any{"name": "x"}); err != nil {
		t.Fatalf("不含版本列的普通批量更新不应报错：%v", err)
	}
	if strings.Contains(exec.query, "version") {
		t.Fatalf("普通列更新不应触及版本列，实际 SQL：%s", exec.query)
	}
}

// ---- Save ----

// TestSaveInsertsWhenPKZero 主键为零值 → Insert。
func TestSaveInsertsWhenPKZero(t *testing.T) {
	exec := &auditExecutor{}
	db := NewDB(exec, MySQL) // MySQL 走 Exec + LastInsertId，便于断言
	err := Save[User](context.Background(), db, &User{Id: 0, Name: "n", Age: 3})
	if err != nil {
		t.Fatalf("Save 出错：%v", err)
	}
	want := "INSERT INTO " + MySQL.QuoteIdent("users")
	if !strings.Contains(exec.query, want) {
		t.Fatalf("主键零值应生成 INSERT。\n want: %s\n  got: %s", want, exec.query)
	}
	if strings.Contains(exec.query, "UPDATE") {
		t.Fatalf("主键零值不应生成 UPDATE，实际 %s", exec.query)
	}
}

// TestSaveUpdatesWhenPKSet 主键非零 → 全列更新（UpdateById 口径）。
func TestSaveUpdatesWhenPKSet(t *testing.T) {
	exec := &auditExecutor{}
	db := NewDB(exec, MySQL)
	err := Save[User](context.Background(), db, &User{Id: 5, Name: "n", Age: 3})
	if err != nil {
		t.Fatalf("Save 出错：%v", err)
	}
	want := "UPDATE " + MySQL.QuoteIdent("users") + " SET"
	if !strings.HasPrefix(exec.query, want) {
		t.Fatalf("主键非零应生成 UPDATE。\n want: %s\n  got: %s", want, exec.query)
	}
	// 逐位断言：最后一个实参是主键值（WHERE id = ?）。
	if exec.args[len(exec.args)-1] != int64(5) {
		t.Fatalf("末位实参应为主键 5，实际 %#v（SQL=%s）", exec.args[len(exec.args)-1], exec.query)
	}
}

// TestSaveZeroRowsIsNotFound 更新命中 0 行（记录不存在或已被逻辑删除）必须
// 返回 ErrNotFound —— 不静默成功、更不退回 Insert：拿着非零主键却插入新行，
// 通常意味着主键被意外清零或并发删除。
func TestSaveZeroRowsIsNotFound(t *testing.T) {
	db := NewDB(zeroAffectedExecutor{}, SQLite)
	err := Save[User](context.Background(), db, &User{Id: 5, Name: "n", Age: 3})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("更新命中 0 行应返回 ErrNotFound，实际 %v", err)
	}
}

// TestSaveNilEntity nil 实体显式报错；无主键模型无法判断插入还是更新，同样报错。
func TestSaveNilEntity(t *testing.T) {
	db := NewDB(&auditExecutor{}, SQLite)
	err := Save[User](context.Background(), db, nil)
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("nil 实体应返回 ErrInvalidArgument，实际 %v", err)
	}
	err = Save[batchNoPK](context.Background(), db, &batchNoPK{NoKey: 1, Name: "n"})
	if err == nil || !strings.Contains(err.Error(), "无主键") {
		t.Fatalf("无主键模型应报「无主键」，实际 %v", err)
	}
}

// ---- 自省（TableExists / ColumnExists / DescribeTable）----

// TestDescribeTableSQLPerDialect 三方言生成同构 6 列的自省 SQL；
// 重点核对 LIKE 里的 % 不被 Sprintf 吃掉、schema 字面量、以及各自的系统表。
func TestDescribeTableSQLPerDialect(t *testing.T) {
	cases := []struct {
		name    string
		dialect Dialect
		must    []string
	}{
		{"PG", PG, []string{
			"FROM information_schema.columns c", "current_schema()",
			"LIKE 'nextval%'", "ORDER BY c.ordinal_position",
			"information_schema.table_constraints", "PRIMARY KEY",
		}},
		{"MySQL", MySQL, []string{
			"FROM information_schema.columns", "DATABASE()",
			"column_key = 'PRI'", "LIKE '%auto_increment%'", "ORDER BY ordinal_position",
		}},
		{"SQLite", SQLite, []string{
			"FROM pragma_table_info('t')", `CASE WHEN "notnull" = 0 THEN 'YES' ELSE 'NO' END`,
			"instr(upper(type), 'INTEGER')", `ORDER BY "cid"`,
		}},
	}
	for _, c := range cases {
		q, ok := tableColumnsDetailSQL(c.dialect, "", "t")
		if !ok {
			t.Fatalf("[%s] 内置方言应支持自省", c.name)
		}
		for _, m := range c.must {
			if !strings.Contains(q, m) {
				t.Fatalf("[%s] SQL 缺少 %q。\n got: %s", c.name, m, q)
			}
		}
	}
	// 显式 schema：PG / MySQL 用字面量，SQLite 走双参 pragma。
	q, _ := tableColumnsDetailSQL(PG, "mysch", "t")
	if !strings.Contains(q, "'mysch'") {
		t.Fatalf("PG 显式 schema 应成字面量，实际 %s", q)
	}
	if strings.Contains(q, "current_schema()") {
		t.Fatalf("PG 显式 schema 不应再用 current_schema()，实际 %s", q)
	}
	q, _ = tableColumnsDetailSQL(SQLite, "att", "t")
	if !strings.Contains(q, `pragma_table_info('t', 'att')`) {
		t.Fatalf("SQLite 附属库应走双参 pragma，实际 %s", q)
	}
}

// TestTableExistsAndColumnExists 走 AutoMigrate 同一条自省查询。
// 注意必须用 mockFactories（每次查询现造 rows）：mockRegistry 里的实例被
// 第一次查询消费后 pos 已到 EOF，同一用例里的第二次查询会拿到空结果集
// —— TableExists 的断言就会「碰巧通过、后面全假阴」。
func TestTableExistsAndColumnExists(t *testing.T) {
	ctx := context.Background()
	db := newMockDB(t)
	mockFactories[`pragma_table_info('users')`] = func() driver.Rows {
		return &mockRows{
			cols: []string{"name"},
			data: [][]driver.Value{{"id"}, {"name"}},
		}
	}
	t.Cleanup(func() { delete(mockFactories, `pragma_table_info('users')`) })

	ok, err := TableExists(ctx, db, "users")
	if err != nil || !ok {
		t.Fatalf("TableExists 应为 true，got %v err=%v", ok, err)
	}
	ok, err = ColumnExists(ctx, db, "users", "ID") // 大小写不敏感
	if err != nil || !ok {
		t.Fatalf("ColumnExists(大写) 应命中归一后的列，got %v err=%v", ok, err)
	}
	ok, err = ColumnExists(ctx, db, "users", "nope")
	if err != nil || ok {
		t.Fatalf("不存在的列应返回 false，got %v err=%v", ok, err)
	}

	// 表不存在的场景：自省查询返回空结果集。
	mockFactories[`pragma_table_info('missing')`] = func() driver.Rows {
		return &mockRows{cols: []string{"name"}}
	}
	t.Cleanup(func() { delete(mockFactories, `pragma_table_info('missing')`) })
	ok, err = TableExists(ctx, db, "missing")
	if err != nil || ok {
		t.Fatalf("不存在的表应返回 false，got %v err=%v", ok, err)
	}
}

// TestIntrospectionRejectsUnknownDialect 未知方言必须报错而不是返回 false：
// 「不知道」伪装成「不存在」会让调用方据此重复建表。
func TestIntrospectionRejectsUnknownDialect(t *testing.T) {
	ctx := context.Background()
	db := NewDB(&auditExecutor{}, unknownDialect{})
	if _, err := TableExists(ctx, db, "users"); err == nil {
		t.Fatalf("未知方言的 TableExists 应报错")
	}
	if _, err := ColumnExists(ctx, db, "users", "id"); err == nil {
		t.Fatalf("未知方言的 ColumnExists 应报错")
	}
	if _, err := DescribeTable(ctx, db, "users"); err == nil {
		t.Fatalf("未知方言的 DescribeTable 应报错")
	}
	if _, err := DescribeTable(ctx, db, ""); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("空表名应返回 ErrInvalidArgument，实际 %v", err)
	}
}
