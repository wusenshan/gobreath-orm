package orm

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
)

// fieldInfo 结构体字段的元数据。
type fieldInfo struct {
	goName  string
	colName string
	pk      bool
	autoInc bool
	ignore  bool
	json    bool    // true 表示字段以 JSON 形式读写（db tag 含 ",json"）
	vector  bool    // true 表示字段为向量列（db tag 含 ",vector"），读写时序列化为文本 [..]
	logic   bool    // true 表示该字段是逻辑删除列（db tag 含 ",logic"）
	nologic bool    // true 表示显式退出约定软删除匹配（db tag 含 ",nologic"）
	version bool    // true 表示该字段是乐观锁版本列（db tag 含 ",version"）
	typ     reflect.Type
}

// modelMeta 一张表的模型元数据（字段、列、主键）。
type modelMeta struct {
	table         string
	explicitTable bool // true 表示表名来自 TableName() 显式指定，前缀不再叠加
	fields        []fieldInfo
	columns       []string // 所有非忽略列
	pk            *fieldInfo
	logicCol      *fieldInfo // 逻辑删除列（db tag 含 ",logic" 时非空）
	logicIsTime   bool       // true 表示逻辑列是 time.Time/*time.Time，未删除判定为 IS NULL；否则 = 0
	versionCol    *fieldInfo // 乐观锁版本列（db tag 含 ",version" 时非空）
}

var metaCache sync.Map

type tableNamer interface{ TableName() string }

// getMeta 解析类型 T 的模型元数据（带缓存）。
func getMeta[T any]() *modelMeta {
	typ := reflect.TypeOf((*T)(nil)).Elem()
	return getMetaByType(typ)
}

func getMetaOf(v reflect.Value) *modelMeta {
	typ := v.Type()
	for typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	return getMetaByType(typ)
}

func getMetaByType(typ reflect.Type) *modelMeta {
	if v, ok := metaCache.Load(typ); ok {
		return v.(*modelMeta)
	}
	m := parseMeta(typ)
	metaCache.Store(typ, m)
	return m
}

func parseMeta(typ reflect.Type) *modelMeta {
	tbl, explicit := resolveTable(typ)
	m := &modelMeta{table: tbl, explicitTable: explicit}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.PkgPath != "" { // 非导出字段
			continue
		}
		tag := f.Tag.Get("db")
		if tag == "-" {
			m.fields = append(m.fields, fieldInfo{goName: f.Name, ignore: true})
			continue
		}
		fi := fieldInfo{goName: f.Name, typ: f.Type}
		if tag != "" {
			parts := strings.Split(tag, ",")
			fi.colName = parts[0]
			for _, p := range parts[1:] {
				switch p {
				case "pk":
					fi.pk = true
				case "autoincrement", "autoinc", "auto_increment":
					fi.autoInc = true
				case "json":
					fi.json = true
				case "vector":
					fi.vector = true
				case "logic":
					fi.logic = true
				case "nologic":
					fi.nologic = true
				case "version":
					fi.version = true
				}
			}
		}
		if fi.colName == "" {
			fi.colName = toSnake(f.Name)
		}
		m.fields = append(m.fields, fi)
		m.columns = append(m.columns, fi.colName)
		if fi.pk {
			m.pk = &m.fields[len(m.fields)-1]
		}
		if fi.logic {
			m.logicCol = &m.fields[len(m.fields)-1]
			m.logicIsTime = isTimeType(f.Type)
		}
		if fi.version {
			m.versionCol = &m.fields[len(m.fields)-1]
		}
	}
	if m.pk == nil {
		for i := range m.fields {
			if m.fields[i].goName == "ID" || m.fields[i].goName == "Id" {
				m.fields[i].pk = true
				m.pk = &m.fields[i]
				break
			}
		}
	}
	return m
}

// resolveTable 返回逻辑表名，以及该表名是否来自 TableName() 显式指定。
// 显式指定的表名视为物理全名，后续前缀不再叠加；自动推导的才需要加前缀。
func resolveTable(typ reflect.Type) (string, bool) {
	v := reflect.New(typ)
	if tn, ok := v.Interface().(tableNamer); ok {
		return tn.TableName(), true
	}
	if tn, ok := v.Elem().Interface().(tableNamer); ok {
		return tn.TableName(), true
	}
	return plural(toSnake(typ.Name())), false
}

// applyPrefix 仅对「自动推导且未显式指定」的表名加前缀；显式名或空前缀原样返回。
// 兼容 schema 限定（如 public.users → public.t_users），避免在 schema 上错误拼接。
func applyPrefix(name, prefix string, explicit bool) string {
	if explicit || prefix == "" {
		return name
	}
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[:i+1] + prefix + name[i+1:]
	}
	return prefix + name
}

// finalTable 结合 DB 前缀得到最终物理表名。
func (m *modelMeta) finalTable(prefix string) string {
	return applyPrefix(m.table, prefix, m.explicitTable)
}

func plural(s string) string {
	if s == "" {
		return s
	}
	if strings.HasSuffix(s, "y") &&
		!strings.HasSuffix(s, "ay") && !strings.HasSuffix(s, "ey") &&
		!strings.HasSuffix(s, "oy") && !strings.HasSuffix(s, "uy") {
		return s[:len(s)-1] + "ies"
	}
	switch {
	case strings.HasSuffix(s, "s"), strings.HasSuffix(s, "x"),
		strings.HasSuffix(s, "ch"), strings.HasSuffix(s, "sh"):
		return s + "es"
	}
	return s + "s"
}

// ---- 结构体 ↔ 行 映射 ----

func scanStruct(rows *sql.Rows, dest any) error {
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return err
	}
	return assignStruct(dest, cols, vals, getMetaOf(reflect.ValueOf(dest)))
}

func assignStruct(dest any, cols []string, vals []any, meta *modelMeta) error {
	rv := reflect.ValueOf(dest).Elem()
	idx := make(map[string]int, len(meta.fields))
	jsonCols := make(map[string]bool, len(meta.fields))
	for i := range meta.fields {
		f := meta.fields[i]
		if f.ignore {
			continue
		}
		idx[f.colName] = i
		if f.json {
			jsonCols[f.colName] = true
		}
	}
	for i, c := range cols {
		fi, ok := idx[c]
		if !ok {
			continue
		}
		fv := rv.Field(fi)
		if jsonCols[c] {
			if err := jsonUnmarshalVal(fv, vals[i]); err != nil {
				return err
			}
			continue
		}
		if err := setField(fv, vals[i]); err != nil {
			return err
		}
	}
	return nil
}

// jsonUnmarshalVal 把驱动返回的 JSON 文本（[]byte/string）反序列化到结构体字段；
// 字段为指针时自动分配底层对象。
func jsonUnmarshalVal(fv reflect.Value, val any) error {
	var data []byte
	switch v := val.(type) {
	case nil:
		if fv.Kind() == reflect.Ptr {
			fv.Set(reflect.Zero(fv.Type()))
		}
		return nil
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("orm: 无法序列化 JSON 值: %w", err)
		}
		data = b
	}
	target := fv
	if fv.Kind() == reflect.Ptr {
		if fv.IsNil() {
			fv.Set(reflect.New(fv.Type().Elem()))
		}
		target = fv.Elem()
	}
	return json.Unmarshal(data, target.Addr().Interface())
}

// setField 把数据库驱动返回的原始值（int64/string/[]byte/float64/time.Time 等）
// 安全地赋给结构体字段，处理常见的类型不匹配。
func setField(fv reflect.Value, val any) error {
	if val == nil {
		if fv.Kind() == reflect.Ptr {
			fv.Set(reflect.Zero(fv.Type()))
		}
		return nil
	}
	target := fv
	if fv.Kind() == reflect.Ptr {
		if fv.IsNil() {
			fv.Set(reflect.New(fv.Type().Elem()))
		}
		target = fv.Elem()
	}
	switch target.Kind() {
	case reflect.String:
		switch v := val.(type) {
		case string:
			target.SetString(v)
		case []byte:
			target.SetString(string(v))
		default:
			target.SetString(fmt.Sprint(v))
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		switch v := val.(type) {
		case int64:
			target.SetInt(v)
		case int:
			target.SetInt(int64(v))
		case float64:
			target.SetInt(int64(v))
		case []byte:
			n, _ := strconv.ParseInt(string(v), 10, 64)
			target.SetInt(n)
		case string:
			n, _ := strconv.ParseInt(v, 10, 64)
			target.SetInt(n)
		default:
			return fmt.Errorf("orm: 无法把 %T 赋给 int 字段", val)
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		switch v := val.(type) {
		case int64:
			target.SetUint(uint64(v))
		case uint64:
			target.SetUint(v)
		case float64:
			target.SetUint(uint64(v))
		case []byte:
			n, _ := strconv.ParseUint(string(v), 10, 64)
			target.SetUint(n)
		case string:
			n, _ := strconv.ParseUint(v, 10, 64)
			target.SetUint(n)
		default:
			return fmt.Errorf("orm: 无法把 %T 赋给 uint 字段", val)
		}
	case reflect.Float32, reflect.Float64:
		switch v := val.(type) {
		case float64:
			target.SetFloat(v)
		case int64:
			target.SetFloat(float64(v))
		case []byte:
			f, _ := strconv.ParseFloat(string(v), 64)
			target.SetFloat(f)
		case string:
			f, _ := strconv.ParseFloat(v, 64)
			target.SetFloat(f)
		default:
			return fmt.Errorf("orm: 无法把 %T 赋给 float 字段", val)
		}
	case reflect.Bool:
		switch v := val.(type) {
		case bool:
			target.SetBool(v)
		case int64:
			target.SetBool(v != 0)
		default:
			return fmt.Errorf("orm: 无法把 %T 赋给 bool 字段", val)
		}
	default:
		rv2 := reflect.ValueOf(val)
		if rv2.Type().AssignableTo(target.Type()) {
			target.Set(rv2)
			return nil
		}
		if rv2.Type().ConvertibleTo(target.Type()) {
			target.Set(rv2.Convert(target.Type()))
			return nil
		}
		return fmt.Errorf("orm: 无法把 %T 赋给 %s 字段", val, target.Type())
	}
	return nil
}

// ---- 元数据辅助 ----

func fieldByCol(ev reflect.Value, meta *modelMeta, col string) reflect.Value {
	for i := range meta.fields {
		if meta.fields[i].colName == col {
			return ev.Field(i)
		}
	}
	return reflect.Value{}
}

// fieldInfoForCol 返回某列对应的字段元数据（用于判断是否为 JSON 字段）。
func fieldInfoForCol(meta *modelMeta, col string) *fieldInfo {
	for i := range meta.fields {
		if meta.fields[i].colName == col {
			return &meta.fields[i]
		}
	}
	return nil
}

// argFor 返回字段在写入（INSERT/UPDATE）时应绑定的参数值。
// JSON 字段先 marshal 成 []byte，再交由驱动以 JSON 文本写入。
func argFor(meta *modelMeta, ev reflect.Value, col string) (any, error) {
	fi := fieldInfoForCol(meta, col)
	fv := fieldByCol(ev, meta, col)
	if fi != nil && fi.json {
		return json.Marshal(fv.Interface())
	}
	if fi != nil && fi.vector {
		return serializeVector(fv.Interface()), nil
	}
	return fv.Interface(), nil
}

func writableCols(meta *modelMeta) []string {
	var cols []string
	for i := range meta.fields {
		f := meta.fields[i]
		if f.ignore || f.autoInc || f.logic {
			continue
		}
		cols = append(cols, f.colName)
	}
	return cols
}

func updateCols(meta *modelMeta) []string {
	var cols []string
	for i := range meta.fields {
		f := meta.fields[i]
		if f.ignore || f.pk || f.autoInc || f.logic {
			continue
		}
		cols = append(cols, f.colName)
	}
	return cols
}

// isTimeType 判断字段类型是否为 time.Time（含指针形式），用于决定逻辑删除的「未删除」判定。
func isTimeType(t reflect.Type) bool {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t == reflect.TypeOf(time.Time{})
}

// isBoolType 判断字段类型是否为 bool（含指针形式）。
func isBoolType(t reflect.Type) bool {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t.Kind() == reflect.Bool
}

// isIntType 判断字段类型是否为整数（含指针形式）。
func isIntType(t reflect.Type) bool {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	}
	return false
}

func quoteCols(cols []string, d Dialect) string {
	q := make([]string, len(cols))
	for i, c := range cols {
		q[i] = d.QuoteIdent(c)
	}
	return strings.Join(q, ", ")
}

// setFieldValue 把自增主键（LastInsertId 返回 int64）回填到字段。
func setFieldValue(fv reflect.Value, id int64) {
	if !fv.IsValid() {
		return
	}
	if fv.Kind() == reflect.Ptr {
		if fv.IsNil() {
			fv.Set(reflect.New(fv.Type().Elem()))
		}
		fv = fv.Elem()
	}
	switch fv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		fv.SetInt(id)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		fv.SetUint(uint64(id))
	}
}
