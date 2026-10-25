package orm

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// 本文件固定第二轮代码审查里确认的两个问题：
//
//  1. UpdateSets / UpdatePartial 能直接把乐观锁版本列写成任意值 ——
//     版本列被抹平，后续所有 `WHERE version = ?` 的并发检测失效，且不报错。
//  2. Upsert / BatchUpsert 的 conflictCols 完全没校验 —— 列名拼错时
//     PG/SQLite 由数据库报错（指不回调用处），MySQL 更是**直接忽略冲突键**、
//     静默按表上另一个唯一索引执行，跨方言行为分裂。
//
// 两条都属于「不报错但做错事」，所以测试断言的重点是：**必须报错，且不下发 SQL**。

// TestUpdateSetsRejectsVersionColumn 覆盖 UpdateSets 与 UpdatePartial 两个入口。
//
// 旧行为：`Set("version", 99)` 会被原样拼成 `SET "version" = $N` 发出去，
// 命中的每一行版本都变成 99 —— 别的会话拿着真实版本再 UpdateById 就永远匹配不上。
// 之所以不能统一在 checkSetCols 里拦：UpdateByIdSets 依赖 sets 带版本值来触发
// 单行乐观锁，那里是合法用法。
func TestUpdateSetsRejectsVersionColumn(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name string
		call func(db *DB) (int64, error)
	}{
		{
			name: "UpdateSets",
			call: func(db *DB) (int64, error) {
				return UpdateSets[verUser](ctx, db, NewQuery[verUser]().
					Eq(Col[verUser](func(u *verUser) *int64 { return &u.ID }), int64(1)).
					Set(ColOf[verUser]("Name"), "x").
					Set(ColOf[verUser]("Version"), 99))
			},
		},
		{
			name: "UpdatePartial",
			call: func(db *DB) (int64, error) {
				return UpdatePartial[verUser](ctx, db, NewQuery[verUser]().
					Eq(Col[verUser](func(u *verUser) *int64 { return &u.ID }), int64(1)),
					map[string]any{"name": "x", "version": 99})
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := mockDB(t, PG)
			recQuery = ""

			n, err := c.call(db)
			if err == nil {
				t.Fatalf("%s 写版本列必须报错，实际成功（影响 %d 行）", c.name, n)
			}
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("%s 的错误应可被 errors.Is(ErrInvalidArgument) 判别，实际：%v", c.name, err)
			}
			if !strings.Contains(err.Error(), "版本列") {
				t.Fatalf("%s 的错误文案应点明版本列，实际：%v", c.name, err)
			}
			if recQuery != "" {
				t.Fatalf("%s 被拒后不应下发任何 SQL，实际：%s", c.name, recQuery)
			}
		})
	}
}

// TestUpdateSetsWithoutVersionStillWorks 反向断言：没有版本列的模型照常更新，
// 别让闸门误伤正常调用（避免「加校验 = 打挂一片」）。
func TestUpdateSetsWithoutVersionStillWorks(t *testing.T) {
	ctx := context.Background()
	db := mockDB(t, PG)
	recQuery = ""

	n, err := UpdateSets[aggUser](ctx, db, NewQuery[aggUser]().
		Eq(Col[aggUser](func(u *aggUser) *int64 { return &u.Id }), int64(1)).
		Set(ColOf[aggUser]("Name"), "x"))
	if err != nil {
		t.Fatalf("无版本列的 UpdateSets 不该失败：%v", err)
	}
	if n != 1 {
		t.Fatalf("受影响行数应为 1，实际 %d", n)
	}
	if !strings.Contains(recQuery, `UPDATE "agg_users" SET "name" = $1`) {
		t.Fatalf("SQL 形状不符：%s", recQuery)
	}
	if strings.Contains(recQuery, `"version"`) {
		t.Fatalf("无版本列模型不该出现 version 片段：%s", recQuery)
	}
}

// TestUpsertRejectsInvalidConflictCols 三个非法形态：不存在列 / 空串 / 重复。
// 每一条都必须在下发 SQL 前失败 —— 交给数据库报的话，MySQL 那条根本不会报。
func TestUpsertRejectsInvalidConflictCols(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name string
		cc   []string
		want string // 错误文案里必须出现的片段
	}{
		{"未知列", []string{"emial"}, "不是模型中的列"},
		{"空列名", []string{""}, "空列名"},
		{"重复列", []string{"name", "name"}, "重复"},
	}

	for _, c := range cases {
		t.Run("Upsert/"+c.name, func(t *testing.T) {
			db := mockDB(t, PG)
			recQuery = ""

			err := Upsert[verUser](ctx, db, &verUser{ID: 1, Name: "a"}, c.cc)
			if err == nil {
				t.Fatalf("冲突键 %v 必须被拒绝", c.cc)
			}
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("错误应可被 errors.Is(ErrInvalidArgument) 判别，实际：%v", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("错误文案应包含 %q，实际：%v", c.want, err)
			}
			if recQuery != "" {
				t.Fatalf("被拒后不应下发任何 SQL，实际：%s", recQuery)
			}
		})

		t.Run("BatchUpsert/"+c.name, func(t *testing.T) {
			db := mockDB(t, PG)
			recQuery = ""

			err := BatchUpsert[verUser](ctx, db, []verUser{{ID: 1, Name: "a"}}, c.cc)
			if err == nil {
				t.Fatalf("冲突键 %v 必须被拒绝", c.cc)
			}
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("错误应可被 errors.Is(ErrInvalidArgument) 判别，实际：%v", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("错误文案应包含 %q，实际：%v", c.want, err)
			}
			if recQuery != "" {
				t.Fatalf("被拒后不应下发任何 SQL，实际：%s", recQuery)
			}
		})
	}
}

// TestUpsertAcceptsValidConflictCol 合法冲突键照常生成 SQL（PG 形态）。
func TestUpsertAcceptsValidConflictCol(t *testing.T) {
	ctx := context.Background()
	db := mockDB(t, PG)
	recQuery = ""

	if err := Upsert[verUser](ctx, db, &verUser{ID: 1, Name: "a"}, []string{"name"}); err != nil {
		t.Fatalf("合法冲突键不该失败：%v", err)
	}
	if !strings.Contains(recQuery, `ON CONFLICT ("name") DO UPDATE SET`) {
		t.Fatalf("PG upsert 后缀形状不符：%s", recQuery)
	}
}

// TestUpsertMySQLIgnoresConflictColsRealButSilent 记录一个**真实但无解**的方言差异：
// MySQL 的 ON DUPLICATE KEY UPDATE 语法里没有冲突键位置，冲突键不会出现在 SQL 中。
// 这里固定住现状（不报错、SQL 里没有 name），避免日后误以为「MySQL 也按冲突键走」。
func TestUpsertMySQLIgnoresConflictColsRealButSilent(t *testing.T) {
	ctx := context.Background()
	db := mockDB(t, MySQL)
	recQuery = ""

	if err := Upsert[verUser](ctx, db, &verUser{ID: 1, Name: "a"}, []string{"name"}); err != nil {
		t.Fatalf("MySQL upsert 不该失败：%v", err)
	}
	if strings.Contains(recQuery, "ON CONFLICT") {
		t.Fatalf("MySQL 不该出现 ON CONFLICT：%s", recQuery)
	}
	if !strings.Contains(recQuery, "ON DUPLICATE KEY UPDATE") {
		t.Fatalf("MySQL 应走 ON DUPLICATE KEY UPDATE：%s", recQuery)
	}
}
