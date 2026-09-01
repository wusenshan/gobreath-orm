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

// newTaggedType 用 reflect.StructOf 在运行时构造带任意 struct tag 的类型，
// 绕过源码里字面无引号 tag 会触发的 go vet 静态拦截，从而能测试严格校验路径。
func newTaggedType(idTag, nameTag string) reflect.Type {
	return reflect.StructOf([]reflect.StructField{
		{Name: "Id", Type: reflect.TypeOf(int64(0)), Tag: reflect.StructTag(idTag)},
		{Name: "Name", Type: reflect.TypeOf(""), Tag: reflect.StructTag(nameTag)},
	})
}

// TestStrictTagCheckDefaultOffNoPanic 验证：默认（StrictTagCheck=false）下，
// 即便 db tag 无引号，parseMeta 也不校验、不 panic（兼容旧行为）。
func TestStrictTagCheckDefaultOffNoPanic(t *testing.T) {
	strictTagCheck.Store(false)
	defer strictTagCheck.Store(false)
	typ := newTaggedType(`db:id,pk,autoincrement`, `db:name`)
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("默认关闭严格校验时不应 panic，但发生了: %v", r)
			}
		}()
		_ = getMetaByType(typ)
	}()
}

// TestStrictTagCheckOnUnquotedPanics 验证：开启后，无引号 db tag 在模型解析时 panic。
func TestStrictTagCheckOnUnquotedPanics(t *testing.T) {
	strictTagCheck.Store(true)
	defer strictTagCheck.Store(false)
	typ := newTaggedType(`db:id,pk,autoincrement`, `db:name`)
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatalf("开启严格校验时，无引号 db tag 应 panic")
			}
		}()
		_ = getMetaByType(typ)
	}()
}

// TestStrictTagCheckOnQuotedOK 验证：开启后，引号正确的 db tag 不 panic 且能正确解析。
func TestStrictTagCheckOnQuotedOK(t *testing.T) {
	strictTagCheck.Store(true)
	defer strictTagCheck.Store(false)
	typ := newTaggedType(`db:"id,pk,autoincrement"`, `db:"name"`)
	var m *modelMeta
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("开启严格校验时，引号正确 db tag 不应 panic: %v", r)
			}
		}()
		m = getMetaByType(typ)
	}()
	if !m.fields[0].pk || !m.fields[0].autoInc {
		t.Fatalf("开启严格校验后字段仍应正确解析 pk/autoInc")
	}
}

// TestStrictTagCheckCacheBackfill 验证：先以非严格模式缓存模型，再开启严格模式，
// 命中缓存时应补校验（此前未校验的模型不会漏检）。
func TestStrictTagCheckCacheBackfill(t *testing.T) {
	strictTagCheck.Store(false)
	typ := newTaggedType(`db:id,pk,autoincrement`, `db:name`)
	_ = getMetaByType(typ) // 非严格缓存，不校验
	strictTagCheck.Store(true)
	defer strictTagCheck.Store(false)
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatalf("缓存后开启严格模式，无引号 tag 应补校验并 panic")
			}
		}()
		_ = getMetaByType(typ) // 命中缓存，应触发补校验 panic
	}()
}

