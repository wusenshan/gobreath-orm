package gen

import "testing"

func TestParseJSONSample_ORM(t *testing.T) {
	src := `{"product_id":1,"title":"x","price":9.9,"in_stock":true,"tags":["a","b"],"created_at":"2024-01-15T10:30:00Z","attrs":{"color":"b"}}`
	tables, err := ParseJSONSample(src)
	if err != nil {
		t.Fatalf("ParseJSONSample: %v", err)
	}
	if len(tables) != 1 {
		t.Fatalf("want 1 table, got %d", len(tables))
	}
	cols := map[string]string{}
	for _, c := range tables[0].Columns {
		cols[c.GoName] = c.GoType
	}
	want := map[string]string{
		"ProductId": "int64",
		"Title":     "string",
		"Price":     "float64",
		"InStock":   "bool",
		"Tags":      "[]string",
		"CreatedAt": "time.Time",
		"Attrs":     "map[string]any",
	}
	for k, v := range want {
		if cols[k] != v {
			t.Errorf("col %s = %q, want %q", k, cols[k], v)
		}
	}
	if !tables[0].HasTime {
		t.Errorf("HasTime should be true for time.Time column")
	}

	// 数组顶层取首个元素
	arr := `[{"id":2,"name":"y"},{"id":3,"name":"z"}]`
	tables, err = ParseJSONSample(arr)
	if err != nil {
		t.Fatalf("ParseJSONSample(array): %v", err)
	}
	if len(tables) != 1 || tables[0].Columns[0].GoName != "Id" {
		t.Errorf("array sample should pick first element, got %+v", tables)
	}
}
