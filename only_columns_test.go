package orm

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ---- OnlyColumns 写入白名单 ----
//
// 背景：UpdateById / Update 默认写入**全部**可写列，实体上未赋值的字段会被一并写回。
// 典型事故是 time.Time 的零值 —— bench 里实测过：payload 没给 created_at，
// 每迭代一次就把该行的 created_at 抹成 0001-01-01。OnlyColumns 提供静态白名单来避免。
// 这组用例锁住它的行为与边界（尤其是「不静默忽略」）。

// onlyColsLogicUser 仅用于本文件的「逻辑删除列」校验，名字取得独特点以免与其它测试文件重名。
type onlyColsLogicUser struct {
	Id        int64      `db:"id,pk,autoincrement"`
	Name      string     `db:"name"`
	DeletedAt *time.Time `db:"deleted_at,logic"`
}

// TestOnlyColumnsUpdateById 白名单只放 name 时，SET 子句里不得出现 age。
func TestOnlyColumnsUpdateById(t *testing.T) {
	ctx := context.Background()
	db := newMockDB(t)

	if err := UpdateById(ctx, db, &User{Id: 7, Name: "neo", Age: 30}, OnlyColumns("name")); err != nil {
		t.Fatalf("UpdateById 失败：%v", err)
	}
	want := `UPDATE "users" SET "name" = ? WHERE "id" = ?`
	if recQuery != want {
		t.Fatalf("OnlyColumns(\"name\") 应只更新 name：\n 实际 %s\n 期望 %s", recQuery, want)
	}
	if len(recArgs) != 2 {
		t.Fatalf("应只有 2 个参数（SET 的 name + WHERE 的 id），实际 %v", recArgs)
	}
	if fmt.Sprint(recArgs[0]) != "neo" || fmt.Sprint(recArgs[1]) != "7" {
		t.Fatalf("参数应为 [neo 7]（SET 在前、WHERE 在后），实际 %v", recArgs)
	}
}

// TestOnlyColumnsMultiColumn 多列白名单按传入顺序进入 SET 子句。
func TestOnlyColumnsMultiColumn(t *testing.T) {
	ctx := context.Background()
	db := newMockDB(t)

	if err := UpdateById(ctx, db, &User{Id: 7, Name: "neo", Age: 30}, OnlyColumns("age", "name")); err != nil {
		t.Fatalf("UpdateById 失败：%v", err)
	}
	if !strings.Contains(recQuery, `SET "age" = ?, "name" = ?`) {
		t.Fatalf("应按白名单传入顺序生成 SET：%s", recQuery)
	}
}

// TestOnlyColumnsWithOmitZero 与 OmitZero 叠加：先取白名单，再剔除其中的零值列。
func TestOnlyColumnsWithOmitZero(t *testing.T) {
	ctx := context.Background()
	db := newMockDB(t)

	// age 留零值：白名单放行 name+age，OmitZero 再把 age 剔掉
	if err := UpdateById(ctx, db, &User{Id: 7, Name: "neo"}, OnlyColumns("name", "age"), OmitZero()); err != nil {
		t.Fatalf("UpdateById 失败：%v", err)
	}
	if !strings.Contains(recQuery, `SET "name" = ?`) {
		t.Fatalf("name 应保留在 SET 中：%s", recQuery)
	}
	if strings.Contains(recQuery, `"age"`) {
		t.Fatalf("零值 age 应被 OmitZero 剔除：%s", recQuery)
	}
}

// TestOnlyColumnsRejectsPK 白名单点名主键列必须报错。
// 主键是 WHERE 条件而非被更新列 —— 静默丢弃会让调用方以为「id 也参与更新了」。
func TestOnlyColumnsRejectsPK(t *testing.T) {
	ctx := context.Background()
	db := newMockDB(t)

	err := UpdateById(ctx, db, &User{Id: 7, Name: "neo"}, OnlyColumns("id", "name"))
	if err == nil {
		t.Fatal("白名单含主键列时应报错，实际通过")
	}
	if !strings.Contains(err.Error(), "id") || !strings.Contains(err.Error(), "主键") {
		t.Fatalf("错误信息应点名 id 并说明原因，实际：%v", err)
	}
}

// TestOnlyColumnsRejectsAutoInc 自增主键在 Insert 路径上的原因应是「由数据库发号」，
// 而不是「WHERE 条件」—— 两者同时成立时取更贴合场景的说法。
func TestOnlyColumnsRejectsAutoInc(t *testing.T) {
	ctx := context.Background()
	db := newMockDB(t)

	err := Insert(ctx, db, &User{Name: "neo", Age: 30}, OnlyColumns("id", "name"))
	if err == nil {
		t.Fatal("Insert 的白名单含自增主键时应报错，实际通过")
	}
	if !strings.Contains(err.Error(), "发号") {
		t.Fatalf("自增主键应给出「由数据库发号」的原因，实际：%v", err)
	}
}

// TestOnlyColumnsRejectsUnknownCol 拼错列名必须报错（而不是静默生成少一列的 SET）。
func TestOnlyColumnsRejectsUnknownCol(t *testing.T) {
	ctx := context.Background()
	db := newMockDB(t)

	err := UpdateById(ctx, db, &User{Id: 7, Name: "neo"}, OnlyColumns("nmae"))
	if err == nil {
		t.Fatal("白名单含不存在的列时应报错（拼写防呆），实际通过")
	}
	if !strings.Contains(err.Error(), "nmae") {
		t.Fatalf("错误信息应点名拼错的列，实际：%v", err)
	}
}

// TestOnlyColumnsRejectsLogicCol 逻辑删除列必须走 DeleteById / Delete，不能经白名单直接改。
func TestOnlyColumnsRejectsLogicCol(t *testing.T) {
	ctx := context.Background()
	db := newMockDB(t)

	now := time.Now()
	err := UpdateById(ctx, db, &onlyColsLogicUser{Id: 7, Name: "neo", DeletedAt: &now}, OnlyColumns("deleted_at"))
	if err == nil {
		t.Fatal("白名单含逻辑删除列时应报错，实际通过")
	}
	if !strings.Contains(err.Error(), "逻辑删除") {
		t.Fatalf("错误信息应说明逻辑删除列的处理方式，实际：%v", err)
	}
}

// TestOnlyColumnsEmpty 空白名单要报「白名单为空」，不能发出没有 SET 子句的 SQL，
// 也不能把原因误报成 OmitZero（会把排查方向带偏）。
func TestOnlyColumnsEmpty(t *testing.T) {
	ctx := context.Background()
	db := newMockDB(t)

	err := UpdateById(ctx, db, &User{Id: 7, Name: "neo"}, OnlyColumns())
	if err == nil {
		t.Fatal("空白名单应报错，实际通过")
	}
	if !strings.Contains(err.Error(), "白名单为空") {
		t.Fatalf("空白名单应提示白名单为空，实际：%v", err)
	}
	if strings.Contains(err.Error(), "OmitZero") {
		t.Fatalf("未使用 OmitZero 时不应把原因归到它头上，实际：%v", err)
	}
}

// TestOnlyColumnsDuplicateTolerated 白名单重复项去重而不是报错（无害，不该打扰调用方）。
func TestOnlyColumnsDuplicateTolerated(t *testing.T) {
	ctx := context.Background()
	db := newMockDB(t)

	if err := UpdateById(ctx, db, &User{Id: 7, Name: "neo", Age: 30}, OnlyColumns("name", "name")); err != nil {
		t.Fatalf("重复列名不应报错：%v", err)
	}
	if got := strings.Count(recQuery, `"name" = ?`); got != 1 {
		t.Fatalf("重复列名应去重为一次，实际出现 %d 次：%s", got, recQuery)
	}
}
