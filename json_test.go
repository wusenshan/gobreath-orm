package orm

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"strings"
	"testing"
)

// Doc 含一个 JSON 列（Meta），用于测试存储映射与查询。
type Doc struct {
	Id   int64           `db:"id,pk,autoincrement"`
	Meta map[string]any `db:"meta,json"`
}

func (Doc) TableName() string { return "docs" }

func TestJsonPathBuildAllDialects(t *testing.T) {
	cases := []struct {
		d     Dialect
		want  string
	}{
		{PG, `"meta"->'a'->>'b' = $1`},
		{MySQL, "JSON_EXTRACT(`meta`, '$.a.b') = ?"},
		{SQLite, `json_extract("meta", '$.a.b') = ?`},
	}
	for _, c := range cases {
		q := NewQuery[Doc]().WithDialect(c.d).
			Json(Col[Doc](func(d *Doc) *map[string]any { return &d.Meta }), "a.b", "=", "x")
		sqlStr, _ := q.Build()
		if !strings.Contains(sqlStr, c.want) {
			t.Fatalf("[%s] JSON 路径查询错误: %s（期望含 %q）", c.d, sqlStr, c.want)
		}
	}
}

func TestJsonContainsBuildAllDialects(t *testing.T) {
	cases := []struct {
		d    Dialect
		want string
	}{
		{PG, `"meta" @> $1::jsonb`},
		{MySQL, "JSON_CONTAINS(`meta`, ?)"},
		{SQLite, `json_contains("meta", ?)`},
	}
	for _, c := range cases {
		q := NewQuery[Doc]().WithDialect(c.d).
			JsonContains(Col[Doc](func(d *Doc) *map[string]any { return &d.Meta }), map[string]any{"status": "active"})
		sqlStr, args := q.Build()
		if !strings.Contains(sqlStr, c.want) {
			t.Fatalf("[%s] JSON 包含查询错误: %s（期望含 %q）", c.d, sqlStr, c.want)
		}
		if len(args) != 1 {
			t.Fatalf("[%s] JsonContains 应有 1 个参数，实际 %d", c.d, len(args))
		}
		// 参数应为可解析的 JSON 文本
		if _, err := json.Marshal(args[0]); err != nil {
			t.Fatalf("[%s] 参数不是合法 JSON: %v", c.d, err)
		}
	}
}

func TestJSONStorageRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := newMockDB(t)

	// 写入：Meta 应被 marshal 成 JSON 字节
	_ = Insert(ctx, db, &Doc{Id: 1, Meta: map[string]any{"a": float64(1), "b": "hello"}})
	insArg := recArgs[0]
	raw, ok := insArg.([]byte)
	if !ok {
		t.Fatalf("JSON 字段写入参数应为 []byte，实际 %T", insArg)
	}
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("写入的不是合法 JSON: %v (原文 %s)", err, string(raw))
	}
	if probe["b"] != "hello" {
		t.Fatalf("写入 JSON 内容错误: %s", string(raw))
	}
	if !strings.Contains(recQuery, `INSERT INTO "docs" ("meta") VALUES (?)`) {
		t.Fatalf("Insert SQL 错误: %s", recQuery)
	}

	// 读取：返回的 []byte 应被反序列化回 map
	mockRegistry["docs"] = &mockRows{
		cols: []string{"id", "meta"},
		data: [][]driver.Value{{int64(1), []byte(`{"a":1,"b":"hello"}`)}},
	}
	got, err := SelectById[Doc](ctx, db, 1)
	if err != nil {
		t.Fatalf("SelectById 失败: %v", err)
	}
	if got.Meta["b"] != "hello" || got.Meta["a"] != float64(1) {
		t.Fatalf("JSON 反序列化结果错误: %+v", got.Meta)
	}
}
