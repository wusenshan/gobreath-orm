package orm

import (
	"context"
	"strings"
	"testing"
	"time"
)

// ---- 约定软删除字段名（Config.SoftDeleteField）测试 ----

// ConvSoftTimeUser 无 ,logic tag；DeletedAt(*time.Time) 列名 "deleted_at" 与全局约定匹配 → 自动软删。
type ConvSoftTimeUser struct {
	Id        int64      `db:"id,pk,autoincrement"`
	Name      string     `db:"name"`
	DeletedAt *time.Time `db:"deleted_at"`
}

func (ConvSoftTimeUser) TableName() string { return "conv_time_users" }

// ConvSoftIntUser 无 ,logic tag；Go 字段名 Deleted 与全局约定 "Deleted" 匹配（列名为 del）→ 自动软删。
type ConvSoftIntUser struct {
	Id      int64  `db:"id,pk,autoincrement"`
	Title   string `db:"title"`
	Deleted int    `db:"del"`
}

func (ConvSoftIntUser) TableName() string { return "conv_int_users" }

// ConvSoftBoolUser 无 ,logic tag；IsDel(bool) 列名 "is_del" 与约定匹配 → 自动软删（= false / true）。
type ConvSoftBoolUser struct {
	Id    int64 `db:"id,pk,autoincrement"`
	Name  string `db:"name"`
	IsDel bool  `db:"is_del"`
}

func (ConvSoftBoolUser) TableName() string { return "conv_bool_users" }

// LogicBoolUser 用 ,logic tag 显式声明 bool 逻辑列。
type LogicBoolUser struct {
	Id    int64 `db:"id,pk,autoincrement"`
	Name  string `db:"name"`
	IsDel bool  `db:"is_del,logic"`
}

func (LogicBoolUser) TableName() string { return "logic_bool_users" }

// NoLogicUser 有 deleted_at 列但用 ,nologic 显式退出约定匹配 → 物理删除。
type NoLogicUser struct {
	Id        int64      `db:"id,pk,autoincrement"`
	Name      string     `db:"name"`
	DeletedAt *time.Time `db:"deleted_at,nologic"`
}

func (NoLogicUser) TableName() string { return "no_logic_users" }

// PriorityUser 同时有 ,logic(DeletedAt) 与约定字段(is_del)；,logic 优先。
type PriorityUser struct {
	Id        int64      `db:"id,pk,autoincrement"`
	Name      string     `db:"name"`
	DeletedAt *time.Time `db:"deleted_at,logic"`
	IsDel     bool       `db:"is_del"`
}

func (PriorityUser) TableName() string { return "priority_users" }

func TestConventionSoftDeleteTimeByColName(t *testing.T) {
	db := newMockDB(t).WithSoftDeleteField("deleted_at")
	ctx := context.Background()

	_, _ = SelectList[ConvSoftTimeUser](ctx, db, NewQuery[ConvSoftTimeUser]())
	if !strings.Contains(recQuery, `WHERE "deleted_at" IS NULL`) {
		t.Fatalf("约定(time,按列名) 未追加过滤: %s", recQuery)
	}

	_ = DeleteById[ConvSoftTimeUser](ctx, db, 1)
	if !strings.Contains(recQuery, `UPDATE "conv_time_users" SET "deleted_at" = ? WHERE "id" = ? AND "deleted_at" IS NULL`) {
		t.Fatalf("约定(time) 软删 SQL 不符合预期: %s", recQuery)
	}
	if _, ok := recArgs[0].(time.Time); !ok {
		t.Fatalf("约定(time) 软删首参应为 time.Time，实际 %T", recArgs[0])
	}
}

func TestConventionSoftDeleteIntByGoName(t *testing.T) {
	db := newMockDB(t).WithSoftDeleteField("Deleted")
	ctx := context.Background()

	_, _ = SelectList[ConvSoftIntUser](ctx, db, NewQuery[ConvSoftIntUser]())
	if !strings.Contains(recQuery, `WHERE "del" = 0`) {
		t.Fatalf("约定(int,按Go字段名) 过滤应为 = 0: %s", recQuery)
	}

	_ = Delete[ConvSoftIntUser](ctx, db, NewQuery[ConvSoftIntUser]().Eq(Col(func(o *ConvSoftIntUser) *int64 { return &o.Id }), 5))
	if !strings.Contains(recQuery, `UPDATE "conv_int_users" SET "del" = ? WHERE "id" = ? AND "del" = 0`) {
		t.Fatalf("约定(int) 软删 SQL 不符合预期: %s", recQuery)
	}
	if len(recArgs) != 2 || recArgs[1] != int64(1) {
		t.Fatalf("约定(int) 软删末参应为 1，实际 %v", recArgs)
	}
}

func TestConventionSoftDeleteBool(t *testing.T) {
	db := newMockDB(t).WithSoftDeleteField("is_del")
	ctx := context.Background()

	_, _ = SelectList[ConvSoftBoolUser](ctx, db, NewQuery[ConvSoftBoolUser]())
	if !strings.Contains(recQuery, `WHERE "is_del" = false`) {
		t.Fatalf("约定(bool) 过滤应为 = false: %s", recQuery)
	}

	_ = DeleteById[ConvSoftBoolUser](ctx, db, 1)
	if !strings.Contains(recQuery, `UPDATE "conv_bool_users" SET "is_del" = ? WHERE "id" = ? AND "is_del" = false`) {
		t.Fatalf("约定(bool) 软删 SQL 不符合预期: %s", recQuery)
	}
	if len(recArgs) != 2 || recArgs[0] != true {
		t.Fatalf("约定(bool) 软删首参应为 true，实际 %v", recArgs)
	}
}

func TestLogicTagBoolSoftDelete(t *testing.T) {
	db := newMockDB(t)
	ctx := context.Background()

	_, _ = SelectList[LogicBoolUser](ctx, db, NewQuery[LogicBoolUser]())
	if !strings.Contains(recQuery, `WHERE "is_del" = false`) {
		t.Fatalf(",logic(bool) 过滤应为 = false: %s", recQuery)
	}

	_ = DeleteById[LogicBoolUser](ctx, db, 1)
	if !strings.Contains(recQuery, `UPDATE "logic_bool_users" SET "is_del" = ? WHERE "id" = ? AND "is_del" = false`) {
		t.Fatalf(",logic(bool) 软删 SQL 不符合预期: %s", recQuery)
	}
	if len(recArgs) != 2 || recArgs[0] != true {
		t.Fatalf(",logic(bool) 软删首参应为 true，实际 %v", recArgs)
	}
}

func TestNoLogicOptOut(t *testing.T) {
	db := newMockDB(t).WithSoftDeleteField("deleted_at")
	ctx := context.Background()

	// ,nologic 退出约定 → 查询不加过滤
	_, _ = SelectList[NoLogicUser](ctx, db, NewQuery[NoLogicUser]())
	if strings.Contains(recQuery, "deleted_at") {
		t.Fatalf(",nologic 不应追加 deleted_at 过滤: %s", recQuery)
	}

	// 删除走物理删除
	_ = DeleteById[NoLogicUser](ctx, db, 1)
	if !strings.Contains(recQuery, `DELETE FROM "no_logic_users" WHERE "id" = ?`) {
		t.Fatalf(",nologic 删除应为物理删除: %s", recQuery)
	}
	if strings.Contains(recQuery, "deleted_at") {
		t.Fatalf(",nologic 物理删除不应含 deleted_at: %s", recQuery)
	}
}

func TestLogicTagBeatsConvention(t *testing.T) {
	// 模型同时声明 ,logic(DeletedAt) 与全局约定字段(is_del)；,logic 优先。
	db := newMockDB(t).WithSoftDeleteField("is_del")
	ctx := context.Background()

	_, _ = SelectList[PriorityUser](ctx, db, NewQuery[PriorityUser]())
	if !strings.Contains(recQuery, `"deleted_at" IS NULL`) {
		t.Fatalf(",logic 应优先于约定: %s", recQuery)
	}
	if strings.Contains(recQuery, `"is_del"`) {
		t.Fatalf(",logic 优先时不应使用约定字段 is_del: %s", recQuery)
	}
}
