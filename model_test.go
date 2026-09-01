package orm

import (
	"reflect"
	"strings"
	"testing"
)

// TestParseMetaQuotedDbTagOK 验证：标准引号格式能正确解析 pk / autoincrement。
func TestParseMetaQuotedDbTagOK(t *testing.T) {
	type goodTag struct {
		Id   int64  `db:"id,pk,autoincrement"`
		Name string `db:"name"`
	}
	m := parseMeta(reflect.TypeOf(goodTag{}))
	if m.fields[0].goName != "Id" {
		t.Fatalf("期望首字段为 Id，实际 %s", m.fields[0].goName)
	}
	if !m.fields[0].pk {
		t.Fatalf("期望 Id 字段 pk=true，实际 false")
	}
	if !m.fields[0].autoInc {
		t.Fatalf("期望 Id 字段 autoInc=true，实际 false（自增主键会被写入 0 值的根因）")
	}
	if m.fields[1].colName != "name" {
		t.Fatalf("期望 Name 列名为 name，实际 %q", m.fields[1].colName)
	}
}

// TestParseMetaNoTagFallsBackToSnake 验证：无 db tag 时退化为字段名 snake_case，
// 且名为 Id / ID 的字段被识别为主键（但不带 autoincrement）。
func TestParseMetaNoTagFallsBackToSnake(t *testing.T) {
	type plain struct {
		Id   int64
		Name string
	}
	m := parseMeta(reflect.TypeOf(plain{}))
	if m.fields[0].colName != "id" {
		t.Fatalf("期望列名 id，实际 %q", m.fields[0].colName)
	}
	if !m.fields[0].pk {
		t.Fatalf("期望 Id 被识别为主键，实际 pk=false")
	}
	if m.fields[0].autoInc {
		t.Fatalf("无 tag 时不应推断 autoincrement，实际 autoInc=true")
	}
}

// TestValidateDbTagUnquotedPanics 验证：无引号 db tag 必须 panic（核心防呆逻辑）。
// 直接用 validateDbTag 验证，避免测试源码里写字面错误 tag 触发 go vet 的静态拦截。
func TestValidateDbTagUnquotedPanics(t *testing.T) {
	cases := []string{
		`db:id,pk,autoincrement`,
		`db:name`,
		`json:"x" db:col,pk`,
	}
	for _, raw := range cases {
		func() {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("raw=%q 期望 panic，但实际没有", raw)
				}
				msg := ""
				switch v := r.(type) {
				case error:
					msg = v.Error()
				case string:
					msg = v
				}
				if !strings.Contains(msg, "db tag 格式错误") {
					t.Fatalf("raw=%q panic 信息不符合预期: %v", raw, r)
				}
			}()
			validateDbTag(raw, "User", "Id")
		}()
	}
}

// TestValidateDbTagQuotedOrEmptyOK 验证：引号格式或空 tag 不触发 panic。
func TestValidateDbTagQuotedOrEmptyOK(t *testing.T) {
	ok := []string{
		``,
		`db:"id,pk,autoincrement"`,
		`db:"name"`,
		`json:"profile,json"`,
	}
	for _, raw := range ok {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("raw=%q 不应 panic，但发生了: %v", raw, r)
				}
			}()
			validateDbTag(raw, "User", "Field")
		}()
	}
}
