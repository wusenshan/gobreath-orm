package orm

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"
)

// ---- Remove 家族（带行级审计的物理删除）的 SQL 形状 / 参数 / 钩子 / 守卫测试 ----

// removeArticle 带时间型逻辑删除列的模型：验证「穿透软删」。
type removeArticle struct {
	ID        int64      `db:"id,pk,autoincrement"`
	Title     string     `db:"title"`
	DeletedAt *time.Time `db:"deleted_at,logic"`
}

func (removeArticle) TableName() string { return "remove_articles" }

// registerRemoveRows 注册一个 mock 结果集工厂，测试结束时清理，避免污染同包其它用例。
func registerRemoveRows(t *testing.T, key string, cols []string, data [][]driver.Value) {
	t.Helper()
	mockFactories[key] = func() driver.Rows {
		return &mockRows{cols: cols, data: data}
	}
	t.Cleanup(func() { delete(mockFactories, key) })
}

// collectRemoveSQL 经既有 Hook 收集执行过的全部 SQL（捕获 SELECT 与 DELETE 都能看到）。
func collectRemoveSQL(t *testing.T, db *DB) (*testHook, *DB) {
	t.Helper()
	hook := &testHook{}
	return hook, db.WithHooks(hook)
}

// TestRemoveEntitySQLShape 单个与多个实体：先 SELECT 捞行（穿透软删，不带逻辑删除条件），
// 再按捕获的主键 DELETE；钩子收到行快照与主键。
func TestRemoveEntitySQLShape(t *testing.T) {
	ctx := context.Background()
	registerRemoveRows(t, `WHERE "id" IN (?, ?)`, []string{"id", "name", "age"}, [][]driver.Value{
		{int64(1), "alice", int64(30)},
		{int64(2), "bob", int64(40)},
	})
	base := newMockDB(t)
	hook, db := collectRemoveSQL(t, base)

	var events []RemoveEvent
	db = db.WithRemoveListener(func(ev RemoveEvent) { events = append(events, ev) })

	n, err := Remove(ctx, db, &User{Id: 1, Name: "alice"}, &User{Id: 2, Name: "bob"})
	if err != nil {
		t.Fatalf("Remove 出错：%v", err)
	}
	// mock 驱动的 RowsAffected 固定为 1：2 个主键合在一条 DELETE 里，受影响行数即 1。
	if n != 1 {
		t.Fatalf("受影响行数应为 1（mock 每条语句固定报 1），实际 %d", n)
	}

	// SQL 序列：捕获 SELECT 在前、DELETE 在后；捕获 SELECT 不带逻辑删除过滤（穿透软删），
	// DELETE 是物理删除。
	var selSQL, delSQL string
	for _, e := range hook.events {
		if e.Phase != HookPhaseAfter {
			continue // before/after 各记一次，只取 after 去重
		}
		if strings.Contains(e.Query, "SELECT * FROM") {
			selSQL = e.Query
		}
		if strings.Contains(e.Query, "DELETE FROM") {
			delSQL = e.Query
		}
	}
	if selSQL == "" || delSQL == "" {
		t.Fatalf("应先 SELECT 捕获再 DELETE，实际 SQL 序列：%v", hook.events)
	}
	if !strings.Contains(selSQL, `WHERE "id" IN (?, ?)`) {
		t.Fatalf("捕获 SELECT 应按主键 IN 捞行：%s", selSQL)
	}
	if strings.Contains(selSQL, "deleted_at") {
		t.Fatalf("Remove 是物理删除，捕获 SELECT 不应带逻辑删除条件：%s", selSQL)
	}
	if !strings.HasPrefix(delSQL, `DELETE FROM "users" WHERE "id" IN (?, ?)`) {
		t.Fatalf("DELETE 应按捕获到的主键集合删除：%s", delSQL)
	}
	if len(delArgsOf(hook)) != 2 {
		t.Fatalf("DELETE 应绑定 2 个主键实参，实际 %v", delArgsOf(hook))
	}

	// 钩子快照：Table / Rows（动态类型 []*User）/ PKs。
	if len(events) != 1 {
		t.Fatalf("应发 1 个 RemoveEvent（单批），实际 %d", len(events))
	}
	ev := events[0]
	if ev.Table != "users" {
		t.Fatalf("事件 Table 应为 users，实际 %q", ev.Table)
	}
	rows, ok := ev.Rows.([]*User)
	if !ok || len(rows) != 2 || rows[0].Id != 1 || rows[1].Name != "bob" {
		t.Fatalf("事件 Rows 应为 [*User{id:1}, *User{id:2}]，实际 %#v", ev.Rows)
	}
	if len(ev.PKs) != 2 || ev.PKs[0] != "n:1" || ev.PKs[1] != "n:2" {
		t.Fatalf("事件 PKs 应为 [n:1 n:2]，实际 %v", ev.PKs)
	}
}

// delArgsOf 从 hook 事件里取 DELETE 语句的绑定参数。
func delArgsOf(hook *testHook) []any {
	for _, e := range hook.events {
		if strings.Contains(e.Query, "DELETE FROM") {
			return e.Args
		}
	}
	return nil
}

// TestRemoveSingleEntitySinglePlaceholder 单实体时 IN 只有一个占位符。
func TestRemoveSingleEntitySinglePlaceholder(t *testing.T) {
	ctx := context.Background()
	registerRemoveRows(t, `WHERE "id" IN (?)`, []string{"id", "name", "age"}, [][]driver.Value{
		{int64(7), "carol", int64(20)},
	})
	base := newMockDB(t)
	hook, db := collectRemoveSQL(t, base)
	if _, err := Remove(ctx, db, &User{Id: 7}); err != nil {
		t.Fatalf("Remove 出错：%v", err)
	}
	found := false
	for _, e := range hook.events {
		if strings.Contains(e.Query, `DELETE FROM "users" WHERE "id" IN (?)`) {
			if len(e.Args) != 1 || e.Args[0] != int64(7) {
				t.Fatalf("DELETE 应绑定主键 7，实际 %v", e.Args)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("未找到按主键的 DELETE：%v", hook.events)
	}
}

// TestRemovePenetratesSoftDelete 穿透软删：已被逻辑删除的行同样会被物理删除，
// 且钩子快照里带着它被删前的完整数据（含 deleted_at）。
func TestRemovePenetratesSoftDelete(t *testing.T) {
	ctx := context.Background()
	deletedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	registerRemoveRows(t, `FROM "remove_articles" WHERE "id" IN (?)`, []string{"id", "title", "deleted_at"}, [][]driver.Value{
		{int64(5), "gone", deletedAt},
	})
	base := newMockDB(t)
	hook, db := collectRemoveSQL(t, base)

	var events []RemoveEvent
	db = db.WithRemoveListener(func(ev RemoveEvent) { events = append(events, ev) })

	n, err := Remove(ctx, db, &removeArticle{ID: 5, DeletedAt: &deletedAt})
	if err != nil {
		t.Fatalf("Remove 出错：%v", err)
	}
	if n != 1 {
		t.Fatalf("受影响行数应为 1，实际 %d", n)
	}
	if len(events) != 1 {
		t.Fatalf("应发 1 个事件，实际 %d", len(events))
	}
	rows, ok := events[0].Rows.([]*removeArticle)
	if !ok || len(rows) != 1 || rows[0].DeletedAt == nil || !rows[0].DeletedAt.Equal(deletedAt) {
		t.Fatalf("快照应包含软删前的完整数据（deleted_at 不丢），实际 %#v", events[0].Rows)
	}
	// 捕获 SELECT 与 DELETE 都不应出现「未删除」过滤。
	for _, e := range hook.events {
		if strings.Contains(e.Query, "deleted_at") && strings.Contains(e.Query, "IS NULL") {
			t.Fatalf("穿透软删的 SQL 不应带 IS NULL 过滤：%s", e.Query)
		}
	}
}

// TestRemoveGuards 守卫：零主键 / 空列表 / nil 实体都在**任何 SQL 下发之前**失败。
func TestRemoveGuards(t *testing.T) {
	ctx := context.Background()
	base := newMockDB(t)
	hook, db := collectRemoveSQL(t, base)

	if _, err := Remove[User](ctx, db, &User{Name: "no-pk"}); err == nil {
		t.Fatal("主键为零值应报错，不静默跳过")
	}
	if _, err := Remove[User](ctx, db); err == nil || !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("空实体列表应报 ErrInvalidArgument，实际 %v", err)
	}
	if _, err := Remove[User](ctx, db, nil); err == nil {
		t.Fatal("nil 实体应报错")
	}
	for _, e := range hook.events {
		if strings.Contains(e.Query, "DELETE") {
			t.Fatalf("守卫失败时不应下发任何 DELETE：%s", e.Query)
		}
	}
}

// TestRemoveChunksOverPlaceholderLimit 分片：超过单条 DELETE 的主键上限时自动分片，
// 每片独立发事件，总数为各片之和。
func TestRemoveChunksOverPlaceholderLimit(t *testing.T) {
	ctx := context.Background()
	oldChunk := removeInChunk
	removeInChunk = 1
	t.Cleanup(func() { removeInChunk = oldChunk })

	registerRemoveRows(t, `WHERE "id" IN (?, ?, ?)`, []string{"id", "name", "age"}, [][]driver.Value{
		{int64(1), "a", int64(1)},
		{int64(2), "b", int64(2)},
		{int64(3), "c", int64(3)},
	})
	base := newMockDB(t)
	hook, db := collectRemoveSQL(t, base)

	var events []RemoveEvent
	db = db.WithRemoveListener(func(ev RemoveEvent) { events = append(events, ev) })

	n, err := Remove(ctx, db, &User{Id: 1}, &User{Id: 2}, &User{Id: 3})
	if err != nil {
		t.Fatalf("Remove 出错：%v", err)
	}
	if n != 3 {
		t.Fatalf("总分片行数应为 3，实际 %d", n)
	}
	deletes := 0
	for _, e := range hook.events {
		if e.Phase == HookPhaseAfter && strings.Contains(e.Query, "DELETE FROM") {
			deletes++
		}
	}
	if deletes != 3 {
		t.Fatalf("chunk=1 时应发 3 条 DELETE，实际 %d", deletes)
	}
	if len(events) != 3 {
		t.Fatalf("每片应各发一个事件，实际 %d", len(events))
	}
	for i, ev := range events {
		if len(ev.PKs) != 1 {
			t.Fatalf("第 %d 片应只含 1 个主键，实际 %v", i, ev.PKs)
		}
	}
}

// TestRemoveQueryShapeAndGuards 条件删除：捕获 SELECT 复用查询条件且穿透软删；
// 无条件 / Limit / Offset 一律显式失败；调用方的 Query 不被改动。
func TestRemoveQueryShapeAndGuards(t *testing.T) {
	ctx := context.Background()
	registerRemoveRows(t, `"age" > ?`, []string{"id", "name", "age"}, [][]driver.Value{
		{int64(9), "dave", int64(99)},
	})
	base := newMockDB(t)
	hook, db := collectRemoveSQL(t, base)

	q := NewQuery[User]().Gt(Col[User](func(u *User) *int { return &u.Age }), 30)
	rows, n, err := RemoveQuery(ctx, db, q)
	if err != nil {
		t.Fatalf("RemoveQuery 出错：%v", err)
	}
	if n != 1 || len(rows) != 1 || rows[0].Id != 9 {
		t.Fatalf("返回快照应为 1 行（id=9），实际 n=%d rows=%#v", n, rows)
	}
	var selSQL, delSQL string
	for _, e := range hook.events {
		if strings.Contains(e.Query, "SELECT") {
			selSQL = e.Query
		}
		if strings.Contains(e.Query, "DELETE FROM") {
			delSQL = e.Query
		}
	}
	if !strings.Contains(selSQL, `"age" > ?`) || strings.Contains(selSQL, "deleted_at") {
		t.Fatalf("捕获 SELECT 应复用条件且不带软删过滤：%s", selSQL)
	}
	if !strings.Contains(delSQL, `DELETE FROM "users" WHERE "id" IN (?)`) || len(delArgsOf(hook)) != 1 {
		t.Fatalf("DELETE 应按捕获主键删除：%s args=%v", delSQL, delArgsOf(hook))
	}
	// 调用方的 Query 不被改动。
	if q.unscoped {
		t.Fatal("RemoveQuery 不得修改调用方的 Query（unscoped 被污染）")
	}

	// 守卫。
	if _, _, err := RemoveQuery(ctx, db, NewQuery[User]()); err == nil || !strings.Contains(err.Error(), "必须有条件") {
		t.Fatalf("无条件应报错，实际 %v", err)
	}
	if _, _, err := RemoveQuery(ctx, db, NewQuery[User]().Gt(Col[User](func(u *User) *int { return &u.Age }), 1).Limit(5)); err == nil {
		t.Fatal("带 Limit 应报错（删前 N 条的语义一旦失效就是全表删除）")
	}
	if _, _, err := RemoveQuery(ctx, db, NewQuery[User]().Gt(Col[User](func(u *User) *int { return &u.Age }), 1).Offset(10)); err == nil {
		t.Fatal("带 Offset 应报错")
	}
	if _, _, err := RemoveQuery[User](ctx, db, nil); err == nil {
		t.Fatal("nil Query 应报错")
	}
}

// TestRemoveSQLValidation RemoveSQL：只接受 SELECT、表必须与实体一致、
// 主键缺失在任何 DELETE 之前失败。
func TestRemoveSQLValidation(t *testing.T) {
	ctx := context.Background()
	base := newMockDB(t)
	hook, db := collectRemoveSQL(t, base)

	if _, _, err := RemoveSQL[User](ctx, db, "DELETE FROM users WHERE id = 1"); err == nil {
		t.Fatal("DELETE 语句应被拒绝（盲删请用 RawExec）")
	}
	if _, _, err := RemoveSQL[User](ctx, db, ""); err == nil {
		t.Fatal("空 SQL 应被拒绝")
	}
	if _, _, err := RemoveSQL[User](ctx, db, "SELECT * FROM orders WHERE id = 1"); err == nil ||
		!strings.Contains(err.Error(), "不一致") {
		t.Fatalf("SELECT 表与实体表不一致应报错，实际 %v", err)
	}
	for _, e := range hook.events {
		if strings.Contains(e.Query, "DELETE") {
			t.Fatalf("校验失败时不应下发任何 DELETE：%s", e.Query)
		}
	}
}

// TestRemoveSQLHappyPath 合法路径：表名带 schema / 引号 / 前缀都能对上；
// 捞出的行按主键删除并进快照。
func TestRemoveSQLHappyPath(t *testing.T) {
	ctx := context.Background()
	registerRemoveRows(t, `WHERE last_login < ?`, []string{"id", "name", "age"}, [][]driver.Value{
		{int64(11), "eve", int64(31)},
		{int64(12), "frank", int64(32)},
	})
	base := newMockDB(t)
	_, db := collectRemoveSQL(t, base)

	var events []RemoveEvent
	db = db.WithRemoveListener(func(ev RemoveEvent) { events = append(events, ev) })

	// schema 限定 + 引号 + 别名：都应通过首表校验。
	rows, n, err := RemoveSQL[User](ctx, db, `SELECT u.* FROM main."users" u WHERE last_login < ?`, "2020-01-01")
	if err != nil {
		t.Fatalf("RemoveSQL 出错：%v", err)
	}
	// mock 驱动的 RowsAffected 固定为 1：2 个主键合在一条 DELETE 里，受影响行数即 1。
	if len(rows) != 2 || rows[0].Id != 11 || rows[1].Name != "frank" {
		t.Fatalf("快照应为 2 行，实际 rows=%#v", rows)
	}
	if n != 1 {
		t.Fatalf("受影响行数应为 1（mock 每条语句固定报 1），实际 %d", n)
	}
	if len(events) != 1 || len(events[0].PKs) != 2 || events[0].PKs[0] != "n:11" {
		t.Fatalf("事件快照/主键异常：%#v", events)
	}
}

// TestRemoveSQLMissingPKColumn SELECT 投影缺主键列时，在任何 DELETE 之前显式失败。
func TestRemoveSQLMissingPKColumn(t *testing.T) {
	ctx := context.Background()
	registerRemoveRows(t, `SELECT name FROM users WHERE age > ?`, []string{"name"}, [][]driver.Value{
		{"no-pk"},
	})
	base := newMockDB(t)
	hook, db := collectRemoveSQL(t, base)

	_, _, err := RemoveSQL[User](ctx, db, "SELECT name FROM users WHERE age > ?", 18)
	if err == nil || !strings.Contains(err.Error(), "主键为零值") {
		t.Fatalf("缺主键列应报错，实际 %v", err)
	}
	for _, e := range hook.events {
		if strings.Contains(e.Query, "DELETE") {
			t.Fatalf("主键缺失时不应下发 DELETE：%s", e.Query)
		}
	}
}

// removePrefixUser 不实现 TableName()：表名走自动推导（复数蛇形），前缀才会生效
// —— 显式表名按框架口径不叠前缀。
type removePrefixUser struct {
	ID   int64  `db:"id,pk,autoincrement"`
	Name string `db:"name"`
}

// TestRemoveListenerSurvivesDerivedDB 派生 DB（WithPrefix 等）必须继承监听器 ——
// 派生时丢监听器等于审计静默失效，属于框架级事故。
func TestRemoveListenerSurvivesDerivedDB(t *testing.T) {
	ctx := context.Background()
	registerRemoveRows(t, `FROM "t_remove_prefix_users" WHERE "id" IN (?)`, []string{"id", "name"}, [][]driver.Value{
		{int64(1), "alice"},
	})
	base := newMockDB(t)
	var events []RemoveEvent
	db := base.WithRemoveListener(func(ev RemoveEvent) { events = append(events, ev) }).WithPrefix("t_")

	if _, err := Remove(ctx, db, &removePrefixUser{ID: 1}); err != nil {
		t.Fatalf("Remove 出错：%v", err)
	}
	if len(events) != 1 || events[0].Table != "t_remove_prefix_users" {
		t.Fatalf("派生 DB 应继承监听器且表名带前缀，实际 %#v", events)
	}
}

// TestRemoveAuditLogger 开箱即用的审计日志监听器输出表名 / 行数 / 主键 / SQL。
func TestRemoveAuditLogger(t *testing.T) {
	ctx := context.Background()
	registerRemoveRows(t, `WHERE "id" IN (?)`, []string{"id", "name", "age"}, [][]driver.Value{
		{int64(3), "gina", int64(22)},
	})
	base := newMockDB(t)
	var sb strings.Builder
	db := base.WithRemoveListener(RemoveAuditLogger(&sb))
	if _, err := Remove(ctx, db, &User{Id: 3}); err != nil {
		t.Fatalf("Remove 出错：%v", err)
	}
	out := sb.String()
	for _, want := range []string{"remove users", "1 rows", "n:3", "DELETE FROM"} {
		if !strings.Contains(out, want) {
			t.Fatalf("审计日志应包含 %q，实际：\n%s", want, out)
		}
	}
}

// TestRemoveRepoBinding Repo 包装与包级函数行为一致。
func TestRemoveRepoBinding(t *testing.T) {
	ctx := context.Background()
	registerRemoveRows(t, `WHERE "id" IN (?)`, []string{"id", "name", "age"}, [][]driver.Value{
		{int64(4), "henry", int64(25)},
	})
	base := newMockDB(t)
	hook, db := collectRemoveSQL(t, base)
	repo := NewRepo[User](db)

	if _, err := repo.Remove(ctx, &User{Id: 4}); err != nil {
		t.Fatalf("repo.Remove 出错：%v", err)
	}
	if _, _, err := repo.RemoveQuery(ctx, NewQuery[User]()); err == nil {
		t.Fatal("repo.RemoveQuery 无条件应报错")
	}
	if _, _, err := repo.RemoveSQL(ctx, "DELETE FROM users"); err == nil {
		t.Fatal("repo.RemoveSQL 对 DELETE 应报错")
	}
	found := false
	for _, e := range hook.events {
		if strings.Contains(e.Query, `DELETE FROM "users" WHERE "id" IN (?)`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("repo.Remove 未按主键 DELETE：%v", hook.events)
	}
}
