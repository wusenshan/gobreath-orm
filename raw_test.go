package orm

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"
)

func rawTestDB(t *testing.T) *DB {
	t.Helper()
	db := newMockDB(t)
	// 快照并清空全局注册表，避免其他测试遗留的 key 随机抢先匹配；测试后恢复。
	old := mockRegistry
	mockRegistry = map[string]*mockRows{}
	t.Cleanup(func() { mockRegistry = old })
	return db
}

// DTO 结构体：无 db tag、无 TableName，列名按字段名 snake_case 匹配；
// 结果集多余列（extra）应被忽略；别名 user_name / dept_name 应命中字段。
func TestRawQueryDTO(t *testing.T) {
	db := rawTestDB(t)
	ctx := context.Background()
	mockRegistry["FROM users u JOIN"] = &mockRows{
		cols: []string{"user_name", "dept_name", "extra"},
		data: [][]driver.Value{
			{"alice", "dev", "ignored"},
			{"bob", "ops", "ignored"},
		},
	}

	type UserDeptVO struct {
		UserName string
		DeptName string
	}
	list, err := RawQuery[UserDeptVO](ctx, db,
		"SELECT u.name AS user_name, d.name AS dept_name, 1 AS extra FROM users u JOIN dept d ON d.id = u.dept_id WHERE u.age > ?", 18)
	if err != nil {
		t.Fatalf("RawQuery DTO 失败: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("应返回 2 行，实际 %d", len(list))
	}
	if list[0].UserName != "alice" || list[0].DeptName != "dev" {
		t.Fatalf("首行映射错误: %+v", list[0])
	}
	if list[1].UserName != "bob" || list[1].DeptName != "ops" {
		t.Fatalf("次行映射错误: %+v", list[1])
	}
	// 参数应原样透传绑定
	if len(recArgs) != 1 || recArgs[0] != int64(18) {
		t.Fatalf("参数应透传 [18]，实际 %v", recArgs)
	}
}

// 标量类型：RawQuery[T] 单列扫描为切片。
func TestRawQueryScalar(t *testing.T) {
	db := rawTestDB(t)
	ctx := context.Background()
	mockRegistry["SELECT name FROM users ORDER"] = &mockRows{
		cols: []string{"name"},
		data: [][]driver.Value{{"alice"}, {"bob"}},
	}
	names, err := RawQuery[string](ctx, db, "SELECT name FROM users ORDER BY id")
	if err != nil {
		t.Fatalf("RawQuery 标量失败: %v", err)
	}
	if len(names) != 2 || names[0] != "alice" || names[1] != "bob" {
		t.Fatalf("标量结果错误: %v", names)
	}
}

func TestRawOneScalarCount(t *testing.T) {
	db := rawTestDB(t)
	ctx := context.Background()
	mockRegistry["COUNT(*)"] = &mockRows{
		cols: []string{"cnt"},
		data: [][]driver.Value{{int64(42)}},
	}
	n, err := RawOne[int64](ctx, db, "SELECT COUNT(*) FROM users WHERE age > ?", 18)
	if err != nil {
		t.Fatalf("RawOne 标量失败: %v", err)
	}
	if n != 42 {
		t.Fatalf("COUNT 应为 42，实际 %d", n)
	}
}

func TestRawOneNotFound(t *testing.T) {
	db := rawTestDB(t)
	ctx := context.Background()
	// 未注册的查询返回空结果集 → ErrNotFound
	if _, err := RawOne[int64](ctx, db, "SELECT id FROM users WHERE id = ?", 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("空结果应返回 ErrNotFound，实际 %v", err)
	}
}

func TestRawExec(t *testing.T) {
	db := rawTestDB(t)
	ctx := context.Background()
	res, err := RawExec(ctx, db, "UPDATE users SET status = ? WHERE id IN (?, ?)", 1, 7, 9)
	if err != nil {
		t.Fatalf("RawExec 失败: %v", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("RowsAffected 应为 1，实际 %d", n)
	}
	if len(recArgs) != 3 || recArgs[0] != int64(1) || recArgs[1] != int64(7) || recArgs[2] != int64(9) {
		t.Fatalf("参数应透传 [1 7 9]，实际 %v", recArgs)
	}
}

func TestRepoRawPassthrough(t *testing.T) {
	db := rawTestDB(t)
	ctx := context.Background()
	mockRegistry["FROM users WHERE age >"] = &mockRows{
		cols: []string{"id", "name", "age"},
		data: [][]driver.Value{
			{int64(1), "alice", int64(20)},
			{int64(2), "bob", int64(30)},
		},
	}
	repo := NewRepo[User](db)
	list, err := repo.RawQuery(ctx, "SELECT id, name, age FROM users WHERE age > ?", 18)
	if err != nil {
		t.Fatalf("Repo.RawQuery 失败: %v", err)
	}
	if len(list) != 2 || list[0].Name != "alice" || list[1].Age != 30 {
		t.Fatalf("Repo.RawQuery 结果错误: %+v", list)
	}
	if _, err := repo.RawExec(ctx, "UPDATE users SET age = ? WHERE id = ?", 21, 1); err != nil {
		t.Fatalf("Repo.RawExec 失败: %v", err)
	}
}
