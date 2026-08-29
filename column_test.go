package orm

import (
	"strings"
	"testing"
)

func TestColOfByGoName(t *testing.T) {
	age := ColOf[User]("Age")
	if age.name != "age" {
		t.Fatalf("ColOf[User](\"Age\") 应返回列名 age，实际 %q", age.name)
	}
	name := ColOf[User]("Name")
	if name.name != "name" {
		t.Fatalf("ColOf[User](\"Name\") 应返回列名 name，实际 %q", name.name)
	}
}

func TestColOfByColumnNameFallback(t *testing.T) {
	// db tag 列名也可作为 ColOf 的查询 key（db tag 与 Go 字段名不同时）
	if ColOf[User]("age").name != "age" {
		t.Fatal("ColOf 按 db tag 列名回退失败")
	}
}

func TestColOfInQuery(t *testing.T) {
	sql, args := NewQuery[User]().
		WithDialect(SQLite).
		Ge(ColOf[User]("Age"), 10).
		Le(ColOf[User]("Age"), 20).
		Build()
	lower := strings.ToLower(sql)
	lower = strings.ReplaceAll(lower, "\"", "")
	if !strings.Contains(lower, "age >= ?") || !strings.Contains(lower, "age <= ?") {
		t.Fatalf("ColOf 生成的 SQL 错误: %s", sql)
	}
	if len(args) != 2 || args[0] != 10 || args[1] != 20 {
		t.Fatalf("参数错误: %v", args)
	}
}

func TestColOfPanicOnMissing(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("ColOf 字段不存在时应 panic")
		}
	}()
	_ = ColOf[User]("NotExist")
}
