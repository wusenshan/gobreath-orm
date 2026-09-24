package orm

import (
	"context"
	"strings"
	"testing"
	"time"
)

// auditLogicUser 带时间型逻辑删除列，用于条件 Delete 的参数顺序断言。
type auditLogicUser struct {
	ID        int64      `db:"id,pk,autoincrement"`
	Name      string     `db:"name"`
	DeletedAt *time.Time `db:"deleted_at,logic"`
}

// TestDeleteArgsFollowPlaceholderOrder 是「条件逻辑删除在 MySQL / SQLite 上静默不生效」的回归测试。
//
// 这个 bug 的特殊之处：**参数个数完全正确**，只是顺序与占位符错位 ——
//
//	UPDATE "t" SET "deleted_at" = ? WHERE "name" = ?     args=[条件值, 时间值]
//	                               ^ 拿到条件值              ^ 拿到时间值
//
// 于是 WHERE 拿时间值去比字符串列必然命中 0 行，Delete 变成一次「不报错、不删、
// 返回 nil」的空操作；SET 侧那个非法值因为没有任何行被匹配到，连 MySQL 严格模式的
// 类型检查都不会触发。PG 因为 `$n` 自带序号而完全正确，所以只在位置型方言
// （MySQL / SQLite 的 `?`）上出问题。
//
// 因此：**只断言「参数个数 == 占位符个数」的用例永远抓不到它**（本仓库既有的向量
// 用例族正是那个口径），必须逐位断言 args[i] 与第 i 个占位符的对应关系。
func TestDeleteArgsFollowPlaceholderOrder(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		dialect Dialect
	}{
		{"PG", PG},
		{"MySQL", MySQL},
		{"SQLite", SQLite},
	}
	for _, c := range cases {
		exec := &auditExecutor{}
		db := NewDB(exec, c.dialect)

		before := time.Now()
		err := Delete[auditLogicUser](ctx, db, NewQuery[auditLogicUser]().Eq(
			Col[auditLogicUser](func(u *auditLogicUser) *string { return &u.Name }), "dave"))
		if err != nil {
			t.Fatalf("[%s] Delete 出错：%v", c.name, err)
		}

		// SQL 里 SET 出现在 WHERE 之前，所以 args 必须先是「删除时间值」、再是「条件值」。
		setAt, whereAt := strings.Index(exec.query, " SET "), strings.Index(exec.query, " WHERE ")
		if setAt < 0 || whereAt < 0 || setAt > whereAt {
			t.Fatalf("[%s] SQL 形状异常（SET 应在 WHERE 之前）：%s", c.name, exec.query)
		}
		if len(exec.args) != 2 {
			t.Fatalf("[%s] 参数个数 %d；期望 2（SQL=%s）", c.name, len(exec.args), exec.query)
		}
		delVal, ok := exec.args[0].(time.Time)
		if !ok || delVal.Before(before) {
			t.Fatalf("[%s] args[0] 应为删除时间值，实际 %#v —— SET 与 WHERE 的参数顺序错位（SQL=%s）",
				c.name, exec.args[0], exec.query)
		}
		if exec.args[1] != "dave" {
			t.Fatalf("[%s] args[1] 应为条件值 \"dave\"，实际 %#v（SQL=%s）",
				c.name, exec.args[1], exec.query)
		}
	}
}

// TestDeleteMultiConditionArgsOrder 多条件 + 逻辑删除时同样要逐位对齐。
// 条件个数越多，错位造成的「错值写入」越难在真库上被发现（可能命中别的行）。
func TestDeleteMultiConditionArgsOrder(t *testing.T) {
	exec := &auditExecutor{}
	db := NewDB(exec, MySQL)
	nameCol := Col[auditLogicUser](func(u *auditLogicUser) *string { return &u.Name })
	idCol := Col[auditLogicUser](func(u *auditLogicUser) *int64 { return &u.ID })

	err := Delete[auditLogicUser](ctxBackground(), db,
		NewQuery[auditLogicUser]().Eq(nameCol, "dave").Eq(idCol, int64(7)))
	if err != nil {
		t.Fatalf("Delete 出错：%v", err)
	}
	if len(exec.args) != 3 {
		t.Fatalf("参数个数 %d；期望 3（SQL=%s）", len(exec.args), exec.query)
	}
	if _, ok := exec.args[0].(time.Time); !ok {
		t.Fatalf("args[0] 应为删除时间值，实际 %#v（SQL=%s）", exec.args[0], exec.query)
	}
	if exec.args[1] != "dave" || exec.args[2] != int64(7) {
		t.Fatalf("args[1:] 应为 [dave 7]，实际 %#v（SQL=%s）", exec.args[1:], exec.query)
	}
}

// TestDeleteUnscopedStaysPhysical 确认修复没有改变 Unscoped 分支：它应生成物理 DELETE，
// 且参数只有条件值（没有删除时间值）。
func TestDeleteUnscopedStaysPhysical(t *testing.T) {
	exec := &auditExecutor{}
	db := NewDB(exec, MySQL)
	nameCol := Col[auditLogicUser](func(u *auditLogicUser) *string { return &u.Name })

	if err := Delete[auditLogicUser](ctxBackground(), db,
		NewQuery[auditLogicUser]().Unscoped().Eq(nameCol, "dave")); err != nil {
		t.Fatalf("Unscoped Delete 出错：%v", err)
	}
	if !strings.HasPrefix(exec.query, "DELETE FROM") {
		t.Fatalf("Unscoped 应生成物理 DELETE，实际 %s", exec.query)
	}
	if len(exec.args) != 1 || exec.args[0] != "dave" {
		t.Fatalf("物理删除的参数应只有条件值 [dave]，实际 %#v", exec.args)
	}
}

func ctxBackground() context.Context { return context.Background() }
