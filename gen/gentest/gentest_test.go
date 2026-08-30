package gentest

import (
	"strings"
	"testing"

	"github.com/wusenshan/gobreath-orm"
)

func TestGeneratedCols(t *testing.T) {
	sql, args := orm.NewQuery[User]().
		WithDialect(orm.SQLite).
		Ge(UserCols.Age, 10).
		Le(UserCols.Age, 20).
		Build()
	lower := strings.ToLower(sql)
	lower = strings.ReplaceAll(lower, "\"", "")
	if !strings.Contains(lower, "age >= ?") || !strings.Contains(lower, "age <= ?") {
		t.Fatalf("生成列名用法生成的 SQL 错误: %s", sql)
	}
	if len(args) != 2 || args[0] != 10 || args[1] != 20 {
		t.Fatalf("参数错误: %v", args)
	}
}
