package gen

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ParseJSONSample 从 JSON 文档样例推断 ORM 模型。
// 关系型模型是扁平的：嵌套对象退化为 map[string]any，数组退化为 []ElemType。
// 顶层若为数组，取第一个元素作为样本。
func ParseJSONSample(jsonStr string) ([]Table, error) {
	var raw any
	if err := json.Unmarshal([]byte(jsonStr), &raw); err != nil {
		if se, ok := err.(*json.SyntaxError); ok {
			line := strings.Count(jsonStr[:se.Offset], "\n") + 1
			return nil, fmt.Errorf("ormgen: 解析 JSON 样例第 %d 行: %s", line, err.Error())
		}
		return nil, fmt.Errorf("ormgen: 解析 JSON 样例: %w", err)
	}
	if arr, ok := raw.([]any); ok {
		if len(arr) == 0 {
			return nil, fmt.Errorf("ormgen: JSON 数组为空，无法推断结构")
		}
		raw = arr[0]
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("ormgen: JSON 样例顶层需为对象（或对象数组）")
	}
	structName := "Doc"
	t := Table{Name: toSnake(structName), StructName: structName}
	for k, v := range obj {
		t.Columns = append(t.Columns, inferORMColumn(k, v))
	}
	for _, c := range t.Columns {
		if c.GoType == "time.Time" {
			t.HasTime = true
		}
	}
	return []Table{t}, nil
}

func inferORMColumn(jsonName string, val any) Column {
	c := Column{GoName: toGoName(jsonName), ColName: toSnake(jsonName)}
	switch v := val.(type) {
	case map[string]any:
		c.GoType = "map[string]any"
	case []any:
		c.GoType = "[]" + inferORMElem(v)
	case float64:
		if v == float64(int64(v)) && !strings.ContainsAny(fmt.Sprintf("%v", v), ".eE") {
			c.GoType = "int64"
		} else {
			c.GoType = "float64"
		}
	case bool:
		c.GoType = "bool"
	case string:
		if isTimeStrORM(v) {
			c.GoType = "time.Time"
		} else {
			c.GoType = "string"
		}
	case nil:
		c.GoType = "any"
	default:
		c.GoType = "string"
	}
	return c
}

func inferORMElem(arr []any) string {
	if len(arr) == 0 {
		return "any"
	}
	switch arr[0].(type) {
	case map[string]any:
		return "map[string]any"
	case float64:
		return "float64"
	case bool:
		return "bool"
	case string:
		return "string"
	default:
		return "any"
	}
}

func isTimeStrORM(s string) bool {
	if _, err := time.Parse(time.RFC3339, s); err == nil {
		return true
	}
	if _, err := time.Parse("2006-01-02", s); err == nil {
		return true
	}
	return false
}

// FromJSON 从 JSON 文档样例生成文件内容，复用 DDL 的渲染管线。
func FromJSON(jsonStr string, opts Options) (map[string]string, error) {
	tables, err := ParseJSONSample(jsonStr)
	if err != nil {
		return nil, err
	}
	pkg := opts.Package
	if pkg == "" {
		pkg = "model"
	}
	if opts.StructName != "" {
		tables[0].StructName = opts.StructName
		tables[0].Name = toSnake(opts.StructName)
	}
	names := []string{tables[0].Name}
	files := map[string]string{}
	switch opts.Mode {
	case PerType:
		m, err := generateModel(tables[0], pkg, names[0])
		if err != nil {
			return nil, err
		}
		c, err := generateColumns(tables[0], pkg)
		if err != nil {
			return nil, err
		}
		base := strings.ToLower(toSnake(tables[0].StructName))
		files[base+".go"] = m
		files[base+"_cols.go"] = c
	case TwoFiles:
		m, err := generateModelsOnly(tables, pkg, names)
		if err != nil {
			return nil, err
		}
		c, err := generateColumnsOnly(tables, pkg)
		if err != nil {
			return nil, err
		}
		files["models.go"] = m
		files["columns.go"] = c
	case SingleFile:
		combined, err := generateCombined(tables, pkg, names)
		if err != nil {
			return nil, err
		}
		files["models_gen.go"] = combined
	}
	return files, nil
}
