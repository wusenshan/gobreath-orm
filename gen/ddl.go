package gen

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// DDLType 表示目标数据库方言。
type DDLType int

const (
	TypeAuto DDLType = iota
	TypePostgres
	TypeMySQL
	TypeSQLite
)

// OutputMode 决定生成文件的组织方式。
type OutputMode int

const (
	// PerType 每表两个文件：<struct>.go（模型） + <struct>_cols.go（列闭包）。
	PerType OutputMode = iota
	// TwoFiles 合并为 models.go（全部模型） + columns.go（全部列闭包）。
	TwoFiles
	// SingleFile 所有模型与列闭包合并进一个 models_gen.go。
	SingleFile
)

// Options 控制 DDL 生成的产物形态。
type Options struct {
	Package     string    // 生成代码的包名，默认 model
	Dialect     DDLType   // 显式指定方言；TypeAuto 时按内容嗅探
	Mode        OutputMode // 输出文件组织方式
	TablePrefix string    // 表前缀，仅作用于 TableName() 返回的物理表名
}

// Column 是单列的解析结果。
type Column struct {
	GoName    string // 导出字段名，如 Id
	ColName   string // 数据库列名，如 id
	GoType    string // Go 类型，如 int / string / time.Time / []float32
	IsPK      bool
	IsAutoInc bool
	IsVector  bool
	VectorDim int
}

// Table 是单张表的解析结果。
type Table struct {
	Name       string
	StructName string
	Columns    []Column
	HasTime    bool
}

// constraintRe 识别列定义中的约束关键字，用于从列定义中切出类型部分。
// 注意：不含 "time"（否则会把 time 类型本身截断），unsigned 单独用 Contains 处理。
var constraintRe = regexp.MustCompile(`(?i)\b(primary|key|not|null|default|unique|references|auto_increment|autoincrement|check|collate|generated|comment|on)\b`)

// DetectDialect 按内容嗅探 DDL 所属方言（不依赖扩展名）。
func DetectDialect(ddl string) DDLType {
	low := strings.ToLower(ddl)
	switch {
	case strings.Contains(low, "auto_increment"):
		return TypeMySQL
	case strings.Contains(low, "autoincrement"):
		return TypeSQLite
	case strings.Contains(low, "serial") || strings.Contains(low, "vector(") || strings.Contains(low, "::"):
		return TypePostgres
	case strings.Contains(low, "engine="):
		return TypeMySQL
	default:
		return TypePostgres
	}
}

// lineOf 返回 src 中字符索引 idx 所在的行号（从 1 开始），用于解析错误精确定位。
func lineOf(src string, idx int) int {
	if idx < 0 {
		idx = 0
	}
	if idx > len(src) {
		idx = len(src)
	}
	return strings.Count(src[:idx], "\n") + 1
}

// ParseDDL 解析一段 DDL 文本，返回其中所有 CREATE TABLE 对应的表结构。
// 支持 IF NOT EXISTS、schema 限定名（public.users）、引号标识符、多条 CREATE TABLE。
func ParseDDL(ddl string, dt DDLType) ([]Table, error) {
	src := ddl
	n := len(src)
	var tables []Table
	i := 0
	for i < n {
		idx := strings.Index(strings.ToLower(src[i:]), "create table")
		if idx < 0 {
			break
		}
		i += idx
		j := i + len("create table")
		// 跳过 IF NOT EXISTS
		restLow := strings.ToLower(src[j:])
		if strings.HasPrefix(strings.TrimSpace(restLow), "if not exists") {
			k := strings.Index(restLow, "exists")
			j += k + len("exists")
		}
		// 跳过空白，读取表名（可能带引号 " ` [）
		for j < n && (src[j] == ' ' || src[j] == '\t' || src[j] == '\n' || src[j] == '\r') {
			j++
		}
		var rawName string
		if j < n && (src[j] == '"' || src[j] == '`' || src[j] == '[') {
			q := src[j]
			close := q
			if q == '[' {
				close = ']'
			}
			j++
			k := j
			for k < n && src[k] != close {
				k++
			}
			rawName = src[j:k]
			j = k + 1
		} else {
			k := j
			for k < n && src[k] != '(' && src[k] != ' ' && src[k] != '\n' && src[k] != '\t' && src[k] != ';' {
				k++
			}
			rawName = src[j:k]
			j = k
		}
		// 跳过空白到 '('
		for j < n && src[j] != '(' {
			j++
		}
		if j >= n {
			break
		}
		// 匹配成对的 ')'
		depth := 0
		bodyStart := j + 1
		k := j
		for k < n {
			if src[k] == '(' {
				depth++
			} else if src[k] == ')' {
				depth--
				if depth == 0 {
					break
				}
			}
			k++
		}
		body := src[bodyStart:k]
		j = k + 1

		tbl, err := parseTableBody(rawName, body, src, dt, bodyStart)
		if err != nil {
			return nil, err
		}
		tables = append(tables, tbl)
		i = j
	}
	if len(tables) == 0 {
		return nil, fmt.Errorf("ormgen: DDL 中未找到 CREATE TABLE 语句")
	}
	return tables, nil
}

func parseTableBody(rawName, body, src string, dt DDLType, base int) (Table, error) {
	name := stripSchema(rawName)
	tbl := Table{Name: name, StructName: toGoName(name)}
	defs := splitTopLevel(body)
	pkCols := map[string]bool{}
	seen := map[string]bool{}
	scanned := 0
	for _, d := range defs {
		raw := d
		d = strings.TrimSpace(d)
		if d == "" {
			scanned += len(raw)
			continue
		}
		low := strings.ToLower(d)
		if strings.HasPrefix(low, "primary key") {
			for _, c := range pkColumnsFromDef(d) {
				pkCols[strings.ToLower(c)] = true
			}
			continue
		}
		if strings.HasPrefix(low, "constraint") || strings.HasPrefix(low, "foreign key") ||
			strings.HasPrefix(low, "unique") || strings.HasPrefix(low, "key") ||
			strings.HasPrefix(low, "index") || strings.HasPrefix(low, "check") {
			continue
		}
		off := strings.Index(body[scanned:], d)
		line := lineOf(src, base+scanned+off)
		col, err := parseColumn(d, dt)
		if err != nil {
			return tbl, fmt.Errorf("第 %d 行: %v", line, err)
		}
		scanned += off + len(d)
		// 保证 Go 字段名在表内唯一
		gn := col.GoName
		if seen[gn] {
			for i := 2; ; i++ {
				cand := gn + strconv.Itoa(i)
				if !seen[cand] {
					gn = cand
					break
				}
			}
			col.GoName = gn
		}
		seen[gn] = true
		tbl.Columns = append(tbl.Columns, col)
	}
	for idx := range tbl.Columns {
		if pkCols[strings.ToLower(tbl.Columns[idx].ColName)] {
			tbl.Columns[idx].IsPK = true
		}
	}
	if len(tbl.Columns) == 0 {
		return tbl, fmt.Errorf("ormgen: 表 %s 没有解析到列（约第 %d 行）", rawName, lineOf(src, base))
	}
	for _, c := range tbl.Columns {
		if c.GoType == "time.Time" {
			tbl.HasTime = true
			break
		}
	}
	return tbl, nil
}

func parseColumn(def string, dt DDLType) (Column, error) {
	def = strings.TrimSpace(def)
	var colName, rest string
	if len(def) > 0 && (def[0] == '"' || def[0] == '`' || def[0] == '[') {
		q := def[0]
		close := q
		if q == '[' {
			close = ']'
		}
		end := strings.IndexByte(def[1:], close)
		if end < 0 {
			return Column{}, fmt.Errorf("ormgen: 无法解析列定义: %s", def)
		}
		colName = def[1 : 1+end]
		rest = def[1+end+1:]
	} else {
		k := 0
		for k < len(def) && def[k] != ' ' && def[k] != '\t' && def[k] != '(' && def[k] != ',' && def[k] != '\n' {
			k++
		}
		colName = def[:k]
		rest = def[k:]
	}
	rest = strings.TrimSpace(rest)
	low := strings.ToLower(rest)

	c := Column{GoName: toGoName(colName), ColName: colName}
	c.IsPK = strings.Contains(low, "primary key")

	typePart := typePartOf(rest)
	base, params := splitType(typePart)
	goType, isVec, dim := mapType(base, params, dt)
	if strings.Contains(strings.ToLower(base), "serial") {
		c.IsAutoInc = true
	} else {
		c.IsAutoInc = strings.Contains(low, "auto_increment") || strings.Contains(low, "autoincrement")
	}
	if strings.Contains(low, "unsigned") {
		goType = toUnsigned(goType)
	}
	c.GoType = goType
	c.IsVector = isVec
	c.VectorDim = dim
	return c, nil
}

func typePartOf(rest string) string {
	loc := constraintRe.FindStringIndex(strings.ToLower(rest))
	if loc == nil {
		return strings.TrimSpace(rest)
	}
	return strings.TrimSpace(rest[:loc[0]])
}

func splitType(s string) (base, params string) {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "("); i >= 0 && strings.HasSuffix(s, ")") {
		return s[:i], s[i+1 : len(s)-1]
	}
	return s, ""
}

func mapType(base, params string, dt DDLType) (goType string, isVector bool, dim int) {
	b := strings.ToLower(strings.TrimSpace(base))
	switch {
	case strings.HasPrefix(b, "serial") || strings.HasPrefix(b, "bigserial") || strings.HasPrefix(b, "smallserial"):
		switch {
		case strings.HasPrefix(b, "bigserial"):
			return "int64", false, 0
		case strings.HasPrefix(b, "smallserial"):
			return "int16", false, 0
		default:
			return "int", false, 0
		}
	case b == "integer", b == "int", b == "int4", b == "int2", b == "int8",
		b == "smallint", b == "tinyint", b == "mediumint", b == "bigint":
		switch {
		case b == "bigint", b == "int8":
			return "int64", false, 0
		case b == "smallint", b == "int2", b == "tinyint", b == "mediumint":
			return "int16", false, 0
		default:
			return "int", false, 0
		}
	case b == "varchar", b == "character varying", b == "char", b == "text",
		b == "bpchar", b == "name", b == "citext", b == "string":
		return "string", false, 0
	case b == "bool", b == "boolean":
		return "bool", false, 0
	case b == "real", b == "float4":
		return "float32", false, 0
	case b == "double", b == "double precision", b == "float8", b == "float",
		b == "numeric", b == "decimal", b == "money":
		return "float64", false, 0
	case b == "timestamp", b == "timestamptz", b == "timestamp with time zone",
		b == "timestamp without time zone", b == "date", b == "time",
		b == "time with time zone", b == "time without time zone",
		strings.HasPrefix(b, "datetime"):
		return "time.Time", false, 0
	case b == "json", b == "jsonb":
		return "string", false, 0
	case b == "uuid":
		return "string", false, 0
	case b == "bytea", b == "blob", b == "binary", b == "varbinary", b == "byte":
		return "[]byte", false, 0
	case strings.HasPrefix(b, "vector"):
		d := 0
		if params != "" {
			if n, err := strconv.Atoi(strings.TrimSpace(params)); err == nil {
				d = n
			}
		}
		return "[]float32", true, d
	default:
		return "string", false, 0
	}
}

func toUnsigned(goType string) string {
	switch goType {
	case "int":
		return "uint"
	case "int8":
		return "uint8"
	case "int16":
		return "uint16"
	case "int32":
		return "uint32"
	case "int64":
		return "uint64"
	default:
		return goType
	}
}

func dbTag(c Column) string {
	parts := []string{c.ColName}
	if c.IsPK {
		parts = append(parts, "pk")
	}
	if c.IsAutoInc {
		parts = append(parts, "autoincrement")
	}
	if c.IsVector {
		if c.VectorDim > 0 {
			parts = append(parts, fmt.Sprintf("vector(%d)", c.VectorDim))
		} else {
			parts = append(parts, "vector")
		}
	}
	return strings.Join(parts, ",")
}

func stripSchema(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
	return name
}

// toGoName 把数据库列名 / 表名转为导出的 Go 标识符（PascalCase）。
func toGoName(s string) string {
	if s == "" {
		return "Field"
	}
	var b strings.Builder
	upper := true
	for _, r := range s {
		if r == '_' || r == '-' || r == ' ' || r == '.' {
			upper = true
			continue
		}
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			if upper {
				b.WriteRune(unicode.ToUpper(r))
				upper = false
			} else {
				b.WriteRune(r)
			}
		}
	}
	out := b.String()
	if out == "" {
		return "Field"
	}
	if out[0] >= '0' && out[0] <= '9' {
		out = "F" + out
	}
	return out
}

// toSnake 是 model.go 中同款实现的本地副本（gen 包不依赖 orm 包）。
func toSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r - 'A' + 'a')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// splitTopLevel 按顶层逗号切分（忽略括号内的逗号与引号内的逗号）。
func splitTopLevel(s string) []string {
	var out []string
	depth := 0
	inStr := byte(0)
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr != 0 {
			if c == inStr {
				inStr = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			inStr = c
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// pkColumnsFromDef 从 "PRIMARY KEY (a, b)" 之类的表级约束中提取列名。
func pkColumnsFromDef(def string) []string {
	open := strings.Index(def, "(")
	close := strings.Index(def, ")")
	if open < 0 || close < open {
		return nil
	}
	inner := def[open+1 : close]
	var cols []string
	for _, p := range strings.Split(inner, ",") {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, "\"`[]")
		if p != "" {
			cols = append(cols, p)
		}
	}
	return cols
}

// FromDDL 解析 DDL 并生成文件内容（filename -> 源码）。调用方负责落盘。
func FromDDL(ddl string, opts Options) (map[string]string, error) {
	dt := opts.Dialect
	if dt == TypeAuto {
		dt = DetectDialect(ddl)
	}
	tables, err := ParseDDL(ddl, dt)
	if err != nil {
		return nil, err
	}
	pkg := opts.Package
	if pkg == "" {
		pkg = "model"
	}
	names := make([]string, len(tables))
	for i, t := range tables {
		if opts.TablePrefix != "" {
			names[i] = opts.TablePrefix + t.Name
		} else {
			names[i] = t.Name
		}
	}

	files := map[string]string{}
	switch opts.Mode {
	case PerType:
		for i, t := range tables {
			m, err := generateModel(t, pkg, names[i])
			if err != nil {
				return nil, err
			}
			c, err := generateColumns(t, pkg)
			if err != nil {
				return nil, err
			}
			base := strings.ToLower(toSnake(t.StructName))
			files[base+".go"] = m
			files[base+"_cols.go"] = c
		}
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

func generateModel(t Table, pkg, tableName string) (string, error) {
	buf := &bytes.Buffer{}
	fmt.Fprintf(buf, "// Code generated by gobreath-orm/cmd/ormgen from DDL. DO NOT EDIT.\n\n")
	fmt.Fprintf(buf, "package %s\n\n", pkg)
	if t.HasTime {
		fmt.Fprintf(buf, "import \"time\"\n\n")
	}
	fmt.Fprintf(buf, "// %s 由 ormgen 从 DDL 自动生成，对应表 %q。\n", t.StructName, t.Name)
	fmt.Fprintf(buf, "type %s struct {\n", t.StructName)
	for _, c := range t.Columns {
		fmt.Fprintf(buf, "\t%s %s %s\n", c.GoName, c.GoType, fmt.Sprintf("`db:%q`", dbTag(c)))
	}
	fmt.Fprintf(buf, "}\n\n")
	fmt.Fprintf(buf, "// TableName 返回该模型对应的物理表名（与 DDL 保持一致）。\n")
	fmt.Fprintf(buf, "func (%s) TableName() string { return %q }\n", t.StructName, tableName)
	return formatBuf(buf)
}

func generateColumns(t Table, pkg string) (string, error) {
	buf := &bytes.Buffer{}
	fmt.Fprintf(buf, "// Code generated by gobreath-orm/cmd/ormgen. DO NOT EDIT.\n\n")
	fmt.Fprintf(buf, "package %s\n\n", pkg)
	fmt.Fprintf(buf, "import orm \"github.com/wusenshan/gobreath-orm\"\n\n")
	setName := t.StructName + "ColumnSet"
	varName := t.StructName + "Cols"
	fmt.Fprintf(buf, "// %s 是 %s 的预计算列名集合，字段与 %s 的导出字段一一对应。\n", setName, t.StructName, t.StructName)
	fmt.Fprintf(buf, "type %s struct {\n", setName)
	for _, c := range t.Columns {
		fmt.Fprintf(buf, "\t%s orm.ColExpr\n", c.GoName)
	}
	fmt.Fprintf(buf, "}\n\n")
	fmt.Fprintf(buf, "// %s 提供了 %s 所有导出字段的 ColExpr，可直接用于查询构造器。\n", varName, t.StructName)
	fmt.Fprintf(buf, "var %s = %s{\n", varName, setName)
	for _, c := range t.Columns {
		fmt.Fprintf(buf, "\t%s: orm.ColOf[%s](\"%s\"),\n", c.GoName, t.StructName, c.GoName)
	}
	fmt.Fprintf(buf, "}\n")
	return formatBuf(buf)
}

func generateModelsOnly(tables []Table, pkg string, names []string) (string, error) {
	hasTime := false
	for _, t := range tables {
		if t.HasTime {
			hasTime = true
			break
		}
	}
	buf := &bytes.Buffer{}
	fmt.Fprintf(buf, "// Code generated by gobreath-orm/cmd/ormgen from DDL. DO NOT EDIT.\n\n")
	fmt.Fprintf(buf, "package %s\n\n", pkg)
	if hasTime {
		fmt.Fprintf(buf, "import \"time\"\n\n")
	}
	for i, t := range tables {
		fmt.Fprintf(buf, "// %s 由 ormgen 从 DDL 自动生成，对应表 %q。\n", t.StructName, t.Name)
		fmt.Fprintf(buf, "type %s struct {\n", t.StructName)
		for _, c := range t.Columns {
			fmt.Fprintf(buf, "\t%s %s %s\n", c.GoName, c.GoType, fmt.Sprintf("`db:%q`", dbTag(c)))
		}
		fmt.Fprintf(buf, "}\n\n")
		fmt.Fprintf(buf, "func (%s) TableName() string { return %q }\n\n", t.StructName, names[i])
	}
	return formatBuf(buf)
}

func generateColumnsOnly(tables []Table, pkg string) (string, error) {
	buf := &bytes.Buffer{}
	fmt.Fprintf(buf, "// Code generated by gobreath-orm/cmd/ormgen. DO NOT EDIT.\n\n")
	fmt.Fprintf(buf, "package %s\n\n", pkg)
	fmt.Fprintf(buf, "import orm \"github.com/wusenshan/gobreath-orm\"\n\n")
	for _, t := range tables {
		setName := t.StructName + "ColumnSet"
		varName := t.StructName + "Cols"
		fmt.Fprintf(buf, "type %s struct {\n", setName)
		for _, c := range t.Columns {
			fmt.Fprintf(buf, "\t%s orm.ColExpr\n", c.GoName)
		}
		fmt.Fprintf(buf, "}\n\n")
		fmt.Fprintf(buf, "var %s = %s{\n", varName, setName)
		for _, c := range t.Columns {
			fmt.Fprintf(buf, "\t%s: orm.ColOf[%s](\"%s\"),\n", c.GoName, t.StructName, c.GoName)
		}
		fmt.Fprintf(buf, "}\n\n")
	}
	return formatBuf(buf)
}

func generateCombined(tables []Table, pkg string, names []string) (string, error) {
	hasTime := false
	for _, t := range tables {
		if t.HasTime {
			hasTime = true
			break
		}
	}
	buf := &bytes.Buffer{}
	fmt.Fprintf(buf, "// Code generated by gobreath-orm/cmd/ormgen from DDL. DO NOT EDIT.\n\n")
	fmt.Fprintf(buf, "package %s\n\n", pkg)
	if hasTime {
		fmt.Fprintf(buf, "import \"time\"\n\n")
	}
	fmt.Fprintf(buf, "import orm \"github.com/wusenshan/gobreath-orm\"\n\n")
	for i, t := range tables {
		fmt.Fprintf(buf, "// %s 由 ormgen 从 DDL 自动生成，对应表 %q。\n", t.StructName, t.Name)
		fmt.Fprintf(buf, "type %s struct {\n", t.StructName)
		for _, c := range t.Columns {
			fmt.Fprintf(buf, "\t%s %s %s\n", c.GoName, c.GoType, fmt.Sprintf("`db:%q`", dbTag(c)))
		}
		fmt.Fprintf(buf, "}\n\n")
		fmt.Fprintf(buf, "func (%s) TableName() string { return %q }\n\n", t.StructName, names[i])
	}
	for _, t := range tables {
		setName := t.StructName + "ColumnSet"
		varName := t.StructName + "Cols"
		fmt.Fprintf(buf, "type %s struct {\n", setName)
		for _, c := range t.Columns {
			fmt.Fprintf(buf, "\t%s orm.ColExpr\n", c.GoName)
		}
		fmt.Fprintf(buf, "}\n\n")
		fmt.Fprintf(buf, "var %s = %s{\n", varName, setName)
		for _, c := range t.Columns {
			fmt.Fprintf(buf, "\t%s: orm.ColOf[%s](\"%s\"),\n", c.GoName, t.StructName, c.GoName)
		}
		fmt.Fprintf(buf, "}\n\n")
	}
	return formatBuf(buf)
}

func formatBuf(buf *bytes.Buffer) (string, error) {
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return "", fmt.Errorf("ormgen: 格式化生成代码: %w\n---\n%s", err, buf.String())
	}
	return string(formatted), nil
}

// StructNames 从一段 Go 源码中收集所有导出的结构体类型名，供 struct 模式批量生成列闭包。
func StructNames(src string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, 0)
	if err != nil {
		return nil, fmt.Errorf("ormgen: 解析 Go 源码: %w", err)
	}
	var names []string
	for _, decl := range f.Decls {
		ts, ok := decl.(*ast.GenDecl)
		if !ok || ts.Tok != token.TYPE {
			continue
		}
		for _, spec := range ts.Specs {
			t, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if _, ok := t.Type.(*ast.StructType); ok && ast.IsExported(t.Name.Name) {
				names = append(names, t.Name.Name)
			}
		}
	}
	sort.Strings(names)
	return names, nil
}
