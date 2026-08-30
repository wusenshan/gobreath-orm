package gen

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// collapseSpaces 把多个空白折叠成一个空格，避免 gofmt 对齐导致字段前后空格数量不确定。
func collapseSpaces(s string) string {
	return strings.TrimSpace(regexp.MustCompile(`\s+`).ReplaceAllString(s, " "))
}

func TestGenerate(t *testing.T) {
	dir := t.TempDir()
	model := `package example

type User struct {
	Id   int64  ` + "`db:\"id,pk,autoincrement\"`" + `
	Name string
	Age  int
	Internal string ` + "`db:\"-\"`" + `
}

type Order struct {
	OrderId int64
}
`
	if err := os.WriteFile(filepath.Join(dir, "model.go"), []byte(model), 0644); err != nil {
		t.Fatal(err)
	}

	if err := Generate(dir, []string{"User", "Order"}, "cols.go"); err != nil {
		t.Fatalf("Generate 失败: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "cols.go"))
	if err != nil {
		t.Fatal(err)
	}
	out := string(got)
	collapsed := collapseSpaces(out)

	// 包名正确
	if !strings.Contains(collapsed, "package example") {
		t.Error("输出应包含 package example")
	}
	// 导入 orm
	if !strings.Contains(collapsed, `import orm "github.com/wusenshan/gobreath-orm"`) {
		t.Error("输出应导入 orm")
	}
	// 生成 UserColumnSet
	if !strings.Contains(collapsed, "type UserColumnSet struct") {
		t.Error("应生成 UserColumnSet")
	}
	if !strings.Contains(collapsed, "var UserCols = UserColumnSet") {
		t.Error("应生成 UserCols 变量")
	}
	// 字段包含导出字段，不含 Internal（db:"-")
	if !strings.Contains(collapsed, "Id orm.ColExpr") {
		t.Error("应包含 Id 字段")
	}
	if !strings.Contains(collapsed, "Name orm.ColExpr") {
		t.Error("应包含 Name 字段")
	}
	if !strings.Contains(collapsed, "Age orm.ColExpr") {
		t.Error("应包含 Age 字段")
	}
	if strings.Contains(collapsed, "Internal orm.ColExpr") {
		t.Error("不应包含 db:\"-\" 字段 Internal")
	}
	// 初始化语句
	if !strings.Contains(collapsed, `orm.ColOf[User]("Id")`) {
		t.Error("UserCols 初始化应使用 ColOf[User]")
	}
	// Order 也生成
	if !strings.Contains(collapsed, "type OrderColumnSet struct") {
		t.Error("应生成 OrderColumnSet")
	}
}
