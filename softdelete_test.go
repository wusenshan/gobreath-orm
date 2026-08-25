package orm

import (
	"context"
	"strings"
	"testing"
	"time"
)

// ---- 逻辑删除测试模型 ----

// SoftUser 使用 time 类型逻辑删除列（deleted_at），未删除判定为 IS NULL。
type SoftUser struct {
	Id        int64      `db:"id,pk,autoincrement"`
	Name      string     `db:"name"`
	DeletedAt *time.Time `db:"deleted_at,logic"`
}

func (SoftUser) TableName() string { return "users" }

// SoftOrder 使用 int 类型逻辑删除列（deleted），未删除判定为 = 0。
type SoftOrder struct {
	Id      int64  `db:"id,pk,autoincrement"`
	Title   string `db:"title"`
	Deleted int    `db:"deleted,logic"`
}

func (SoftOrder) TableName() string { return "orders" }

func TestSoftDeleteTimeSelectFilter(t *testing.T) {
	db := newMockDB(t)
	ctx := context.Background()

	// SelectById 自动过滤已删除
	_, _ = SelectById[SoftUser](ctx, db, 1)
	if !strings.Contains(recQuery, `WHERE "id" = ? AND "deleted_at" IS NULL`) {
		t.Fatalf("SelectById 未追加逻辑删除过滤: %s", recQuery)
	}

	// SelectList 自动过滤
	_, _ = SelectList[SoftUser](ctx, db, NewQuery[SoftUser]())
	if !strings.Contains(recQuery, `WHERE "deleted_at" IS NULL`) {
		t.Fatalf("SelectList 未追加逻辑删除过滤: %s", recQuery)
	}

	// Count 自动过滤
	_, _ = Count[SoftUser](ctx, db, NewQuery[SoftUser]())
	if !strings.Contains(recQuery, `WHERE "deleted_at" IS NULL`) {
		t.Fatalf("Count 未追加逻辑删除过滤: %s", recQuery)
	}

	// Unscoped 关闭过滤
	_, _ = SelectList[SoftUser](ctx, db, NewQuery[SoftUser]().Unscoped())
	if strings.Contains(recQuery, `deleted_at`) {
		t.Fatalf("Unscoped 仍追加了逻辑删除过滤: %s", recQuery)
	}
}

func TestSoftDeleteTimeDeleteById(t *testing.T) {
	db := newMockDB(t)
	ctx := context.Background()

	// 有逻辑列 → 软删（UPDATE ... SET deleted_at = ?）
	_ = DeleteById[SoftUser](ctx, db, 1)
	if !strings.Contains(recQuery, `UPDATE "users" SET "deleted_at" = ? WHERE "id" = ? AND "deleted_at" IS NULL`) {
		t.Fatalf("DeleteById 软删 SQL 不符合预期: %s", recQuery)
	}
	if len(recArgs) != 2 {
		t.Fatalf("DeleteById 软删参数数量错误: %v", recArgs)
	}
	if _, ok := recArgs[0].(time.Time); !ok {
		t.Fatalf("DeleteById 软删首参应为 time.Time，实际 %T", recArgs[0])
	}

	// ForceDeleteById → 物理删除，无过滤
	_ = ForceDeleteById[SoftUser](ctx, db, 1)
	if !strings.Contains(recQuery, `DELETE FROM "users" WHERE "id" = ?`) {
		t.Fatalf("ForceDeleteById SQL 不符合预期: %s", recQuery)
	}
	if strings.Contains(recQuery, `deleted_at`) {
		t.Fatalf("ForceDeleteById 不应出现 deleted_at: %s", recQuery)
	}
}

func TestSoftDeleteIntColumn(t *testing.T) {
	db := newMockDB(t)
	ctx := context.Background()

	// int 逻辑列：未删除判定 = 0
	_, _ = SelectList[SoftOrder](ctx, db, NewQuery[SoftOrder]())
	if !strings.Contains(recQuery, `WHERE "deleted" = 0`) {
		t.Fatalf("int 逻辑列过滤应为 = 0: %s", recQuery)
	}

	// 软删写入 1
	_ = Delete[SoftOrder](ctx, db, NewQuery[SoftOrder]().Eq(Col(func(o *SoftOrder) *int64 { return &o.Id }), 5))
	if !strings.Contains(recQuery, `UPDATE "orders" SET "deleted" = ? WHERE "id" = ? AND "deleted" = 0`) {
		t.Fatalf("int 列软删 SQL 不符合预期: %s", recQuery)
	}
	// 参数顺序：WHERE 的 5 先入参，逻辑列值 1 后入参（驱动将 int 转 int64）
	if len(recArgs) != 2 || recArgs[1] != int64(1) {
		t.Fatalf("int 列软删末参应为 1，实际 %v", recArgs)
	}

	// Unscoped 后 Delete 变物理删除
	_ = Delete[SoftOrder](ctx, db, NewQuery[SoftOrder]().Unscoped().Eq(Col(func(o *SoftOrder) *int64 { return &o.Id }), 5))
	if !strings.Contains(recQuery, `DELETE FROM "orders" WHERE "id" = ?`) {
		t.Fatalf("Unscoped Delete 应为物理删除: %s", recQuery)
	}
}

func TestSoftDeleteRepoDelegation(t *testing.T) {
	db := newMockDB(t)
	ctx := context.Background()
	repo := NewRepo[SoftUser](db)

	// Repo.DeleteById 同样软删
	_ = repo.DeleteById(ctx, 1)
	if !strings.Contains(recQuery, `UPDATE "users" SET "deleted_at" = ? WHERE "id" = ? AND "deleted_at" IS NULL`) {
		t.Fatalf("Repo.DeleteById 未走软删: %s", recQuery)
	}

	// Repo.ForceDeleteById 物理删除
	_ = repo.ForceDeleteById(ctx, 1)
	if !strings.Contains(recQuery, `DELETE FROM "users" WHERE "id" = ?`) {
		t.Fatalf("Repo.ForceDeleteById 应为物理删除: %s", recQuery)
	}
}
