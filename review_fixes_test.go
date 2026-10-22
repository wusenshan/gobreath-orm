package orm

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// setOrderProbe 用于验证 UpdateByIdSets 的 SET 列顺序：4 个非主键列，
// 未排序时 map 遍历顺序随机，重复执行必然产出不同的 SQL 文本。
type setOrderProbe struct {
	Id    int64  `db:"id,pk,autoincrement"`
	Alpha string `db:"alpha"`
	Beta  string `db:"beta"`
	Gamma string `db:"gamma"`
	Delta string `db:"delta"`
}

func (setOrderProbe) TableName() string { return "set_order_probe" }

// TestUpdateByIdSetsColumnOrderDeterministic 单行 UpdateByIdSets 的 SET 列必须按列名
// 排序后拼接。
//
// v0.1.13 的 CHANGELOG 声称「UpdateSets / UpdateByIdSets / UpdatePartial 遍历 map 拼
// SET 的随机顺序」已修，实际只有 updateSetsImpl 与批量版 UpdateByIdsSets 排了序，
// 单行 UpdateByIdSets 仍是 `for col, val := range sets`。后果：同一份 sets 每次生成
// 不同的 SQL 文本 —— prepared statement 缓存命中率归零、慢查询日志与逐字断言不稳定。
func TestUpdateByIdSetsColumnOrderDeterministic(t *testing.T) {
	const rounds = 30
	sets := map[string]any{"gamma": "g", "alpha": "a", "delta": "d", "beta": "b"}

	var first string
	for i := 0; i < rounds; i++ {
		exec := &auditExecutor{}
		db := NewDB(exec, PG)
		if _, err := UpdateByIdSets[setOrderProbe](context.Background(), db, 1, sets); err != nil {
			// auditExecutor 的 Exec 结果为 0 行，不当作失败；这里只关心 SQL 形态。
			_ = err
		}
		if i == 0 {
			first = exec.query
			continue
		}
		if exec.query != first {
			t.Fatalf("第 %d 次生成的 SQL 与首次不同 —— SET 列顺序仍在随机：\n首次: %s\n本次: %s", i+1, first, exec.query)
		}
	}

	// 排序口径：alpha → beta → delta → gamma（列名字典序）
	idx := func(col string) int { return strings.Index(first, `"`+col+`" =`) }
	order := []string{"alpha", "beta", "delta", "gamma"}
	prev := -1
	for _, col := range order {
		at := idx(col)
		if at < 0 {
			t.Fatalf("SQL 缺少列 %q：%s", col, first)
		}
		if at < prev {
			t.Fatalf("SET 列未按列名排序（%q 出现在前一列之前）：%s", col, first)
		}
		prev = at
	}
}

// TestIntrospectionRejectsInvalidTableName 自省 API 的表名必须过标识符白名单。
//
// 表名在系统表查询里走字面量而非占位符，而 sqlLiteral 只做单引号双写：
// MySQL 默认不开 NO_BACKSLASH_ESCAPES，反斜杠是转义字符，表名以反斜杠结尾时
// 字面量不会闭合，后面的 SQL 结构会被改写。AutoMigrate 路径的表名来自模型元数据，
// 公开 API 的表名则由调用方直接给、全程不过 quoteTable —— 必须在这里堵。
func TestIntrospectionRejectsInvalidTableName(t *testing.T) {
	ctx := context.Background()
	db := NewDB(&auditExecutor{}, PG)

	bad := []string{
		"",            // 空
		"users;drop",  // 分隔符
		`users"`,      // 引号
		`users\`,      // 反斜杠结尾（MySQL 转义下字面量不闭合）
		"bad name",    // 空格
		"1users",      // 数字开头
		"a.b.c",       // 三段（只支持 schema.table）
		"users--",     // 注释符
		"' OR 1=1 --", // 注入样式
	}
	for _, name := range bad {
		t.Run("bad:"+name, func(t *testing.T) {
			if _, err := TableExists(ctx, db, name); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("TableExists(%q) 应返回 ErrInvalidArgument，实际 %v", name, err)
			}
			if _, err := ColumnExists(ctx, db, name, "id"); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("ColumnExists(%q) 应返回 ErrInvalidArgument，实际 %v", name, err)
			}
			if _, err := DescribeTable(ctx, db, name); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("DescribeTable(%q) 应返回 ErrInvalidArgument，实际 %v", name, err)
			}
		})
	}

	// 合法表名（含 schema.table 两段）必须放行到查询阶段：错误可以是驱动层的，
	// 但绝不能是 ErrInvalidArgument —— 否则等于把正常用法也拦了。
	for _, name := range []string{"users", "public.users"} {
		t.Run("ok:"+name, func(t *testing.T) {
			if _, err := TableExists(ctx, db, name); errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("TableExists(%q) 被误判为非法表名：%v", name, err)
			}
			if _, err := DescribeTable(ctx, db, name); errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("DescribeTable(%q) 被误判为非法表名：%v", name, err)
			}
		})
	}
}
