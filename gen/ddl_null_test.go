package gen

import (
	"fmt"
	"regexp"
	"testing"
)

// TestDDLNullableGeneratesPointer 验证：ormgen 从 DDL 生成模型时，可空且无默认值的列应输出指针类型，
// 而 NOT NULL / 带 DEFAULT 的列保持值类型。
func TestDDLNullableGeneratesPointer(t *testing.T) {
	ddl := `CREATE TABLE product (
	id bigserial primary key,
	name varchar(100) not null,
	score integer,
	created_at timestamp
);`
	files, err := FromDDL(ddl, Options{Package: "model", Mode: SingleFile})
	if err != nil {
		t.Fatal(err)
	}
	src, ok := files["models_gen.go"]
	if !ok {
		t.Fatalf("缺少 models_gen.go，实际文件: %v", keysOf(files))
	}

	// 可空无默认：score / created_at 应为指针（gofmt 对齐，用正则容忍空白）
	if !regexp.MustCompile(`Score\s+\*int`).MatchString(src) {
		t.Fatalf("可空无默认列 score 应为 *int，实际:\n%s", src)
	}
	if !regexp.MustCompile(`CreatedAt\s+\*time\.Time`).MatchString(src) {
		t.Fatalf("可空无默认列 created_at 应为 *time.Time，实际:\n%s", src)
	}
	// NOT NULL：name 仍为值类型（整份源码不应出现 *string）
	if regexp.MustCompile(`\*string`).MatchString(src) {
		t.Fatalf("NOT NULL 列 name 不应为指针，实际:\n%s", src)
	}
	// 主键自增：id 值类型（不应为 *int64）
	if regexp.MustCompile(`Id\s+\*int64`).MatchString(src) {
		t.Fatalf("主键自增列 id 不应为指针，实际:\n%s", src)
	}
}

func keysOf(m map[string]string) string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return fmt.Sprintf("%v", ks)
}
