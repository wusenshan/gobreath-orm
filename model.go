package orm

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	vectorDim int   // 向量维度（db tag 含 ",vector(N)" 时解析出 N；0 表示未指定维度）
	logic   bool    // true 表示该字段是逻辑删除列（db tag 含 ",logic"）
	nologic bool    // true 表示显式退出约定软删除匹配（db tag 含 ",nologic"）
	version bool    // true 表示该字段是乐观锁版本列（db tag 含 ",version"）
	unique  bool    // true 表示该列需唯一约束（db tag 含 ",unique"），仅 AutoMigrate 使用
	index   bool    // true 表示该列需二级索引（db tag 含 ",index"），仅 AutoMigrate 使用
	typ     reflect.Type
	rawTag  string  // 原始 struct tag 字符串（仅用于 StrictTagCheck 模式下的格式校验）
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
	tagChecked    bool       // 是否已在 StrictTagCheck 模式下校验过 tag 格式（避免重复校验）

	// 行映射计划缓存（按结果集列签名索引）。惰性初始化，首次查询时填充。
	scanMu        sync.RWMutex
	scanPlans     map[uint64][]*scanPlan
	scanPlanCount int // 已缓存计划总数，用于封顶（见 maxScanPlansPerModel）
}

var metaCache sync.Map

// strictTagCheck 为 true 时，模型解析阶段会对 db tag 做严格格式校验
// （无引号的 `db:col,pk` 会直接 panic）。默认 false（兼容旧行为，不校验）。
// 仅增不减：只要进程内任一 Open 开启了 StrictTagCheck，全局即进入严格模式
// （越严格越安全，且避免缓存命中的模型漏校验）。
var strictTagCheck atomic.Bool

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
		m := v.(*modelMeta)
		// 严格模式开启后，若此前以非严格模式缓存过（未校验），补校验一次。
		if strictTagCheck.Load() && !m.tagChecked {
			for _, f := range m.fields {
				if f.rawTag != "" {
					validateDbTag(f.rawTag, typ.Name(), f.goName)
				}
			}
			m.tagChecked = true
		}
		return m
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
		raw := string(f.Tag)
		// guard（仅 StrictTagCheck 模式）：db tag 必须用引号包裹（标准 struct tag 格式
		// `db:"col,pk"`）。写成 `db:col,pk`（无引号）时 reflect 读不到 key，Tag.Get("db")
		// 返回空，字段会退化成「仅按字段名映射」，pk/autoincrement 等全部丢失，导致自增主键
		// 被当成普通列写入 0 值 —— 这类问题很难排查。默认关闭以兼容旧行为；开启后启动即 panic。
		if strictTagCheck.Load() {
			validateDbTag(raw, typ.Name(), f.Name)
		}
		if tag == "-" {
			m.fields = append(m.fields, fieldInfo{goName: f.Name, ignore: true, rawTag: raw})
			continue
		}
		fi := fieldInfo{goName: f.Name, typ: f.Type, rawTag: raw}
		if tag != "" {
			parts := strings.Split(tag, ",")
			fi.colName = parts[0]
			for _, p := range parts[1:] {
				if strings.HasPrefix(p, "vector") {
					fi.vector = true
					if n := parseVectorDim(p); n > 0 {
						fi.vectorDim = n
					}
					continue
				}
				switch p {
				case "pk":
					fi.pk = true
				case "autoincrement", "autoinc", "auto_increment":
					fi.autoInc = true
				case "json":
					fi.json = true
				case "logic":
					fi.logic = true
				case "nologic":
					fi.nologic = true
				case "version":
					fi.version = true
				case "unique":
					fi.unique = true
				case "index":
					fi.index = true
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
	m.tagChecked = strictTagCheck.Load()
	return m
}

// validateDbTag 检查 struct tag 原始字符串里是否出现了「无引号的 db tag」笔误。
// 标准 struct tag 格式要求 `db:"col,pk"`（双引号包裹）；写成 `db:col,pk`（无引号）
// 时 reflect 读不到 db key，Tag.Get("db") 返回空，pk/autoincrement 等修饰符全部
// 丢失，自增主键会被当成普通列写入零值，且不会报任何错。这里在模型解析阶段直接 panic，
// 把问题暴露在启动第一时间（go vet 也能静态拦这类笔误，但很多项目并不跑 vet）。
func validateDbTag(raw, typeName, fieldName string) {
	if raw == "" || !strings.Contains(raw, "db:") || strings.Contains(raw, `db:"`) {
		return
	}
	panic(fmt.Errorf("orm: 结构体 %s 字段 %s 的 db tag 格式错误：缺少引号，正确写法是 `db:\"col,pk,autoincrement\"`，而不是 `db:col,pk`",
		typeName, fieldName))
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
//
// 列映射在列表查询的热路径上：50 行 × 9 列要建 100 个 map、做 900 次 map 查找、
// 并调 50 次 rows.Columns()。但这些开销只与「列集合」有关，与「行」无关 ——
// 同一个查询的每一行，列序与目标字段的对应关系完全相同。
// 所以把它按「结果集列签名」缓存到模型元数据上：每个查询解析一次，而不是每行一次。

// scanSetter 把一个已扫描出的驱动值写入目标字段。
// 由构建计划时按字段类型选定，省掉逐行的类型 switch。
type scanSetter func(fv reflect.Value, val any) error

// scanPlan 一次查询的「结果列 → 字段」映射计划。构建完成后即视为不可变，
// 可被多个并发查询共享。
type scanPlan struct {
	cols   []string // 构建时的列清单，用于哈希碰撞时逐项复核
	fields []int32  // 结果列下标 → meta.fields 下标；-1 表示该列不映射到任何字段
	set    []scanSetter
}

func (p *scanPlan) matches(cols []string) bool {
	if len(p.cols) != len(cols) {
		return false
	}
	for i := range cols {
		if p.cols[i] != cols[i] {
			return false
		}
	}
	return true
}

// hashCols 对列清单做 FNV-1a。
// 用哈希而非拼接后的字符串做键，是为了避开每次查询一次 key 字符串分配。
func hashCols(cols []string) uint64 {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	h := uint64(offset64)
	for _, c := range cols {
		for i := 0; i < len(c); i++ {
			h ^= uint64(c[i])
			h *= prime64
		}
		h ^= 0 // 分隔符：避免 ["ab","c"] 与 ["a","bc"] 撞成同一哈希
		h *= prime64
	}
	return h
}

// maxScanPlansPerModel 单个模型缓存的映射计划上限。
//
// 列签名由 SQL 决定，正常程序里是有限个（结构体列集合就那么几种）；
// 但 Select(cols...) 允许拼动态列清单，极端情况下组合数可能失控。
// 到顶后不再缓存（退化成「每次查询重建一次计划」，即改动前的开销），
// 用轻微的性能回退换内存不无限增长。
const maxScanPlansPerModel = 128

// scanPlanFor 取（必要时构建）列清单对应的映射计划。
func (m *modelMeta) scanPlanFor(cols []string) *scanPlan {
	h := hashCols(cols)
	m.scanMu.RLock()
	for _, p := range m.scanPlans[h] {
		if p.matches(cols) {
			m.scanMu.RUnlock()
			return p
		}
	}
	m.scanMu.RUnlock()

	p := m.buildScanPlan(cols)
	m.scanMu.Lock()
	if m.scanPlans == nil {
		m.scanPlans = make(map[uint64][]*scanPlan, 1)
	}
	// 并发构建时可能已有人写入等价计划：不追加重复项。
	for _, old := range m.scanPlans[h] {
		if old.matches(cols) {
			m.scanMu.Unlock()
			return old
		}
	}
	if m.scanPlanCount >= maxScanPlansPerModel {
		m.scanMu.Unlock()
		return p
	}
	m.scanPlans[h] = append(m.scanPlans[h], p)
	m.scanPlanCount++
	m.scanMu.Unlock()
	return p
}

func (m *modelMeta) buildScanPlan(cols []string) *scanPlan {
	p := &scanPlan{
		cols:   append([]string(nil), cols...),
		fields: make([]int32, len(cols)),
		set:    make([]scanSetter, len(cols)),
	}
	for i, c := range cols {
		p.fields[i] = -1
		for j := range m.fields {
			f := &m.fields[j]
			if f.ignore || f.colName != c {
				continue
			}
			// 与旧实现一致：同名列取**最后**一个匹配字段。
			p.fields[i] = int32(j)
			p.set[i] = setterFor(f)
		}
	}
	return p
}

// setterFor 按字段类型选定赋值函数。
//
// 只为标量（字符串 / 整数 / 无符号 / 浮点 / 布尔，含其指针形式）做特化；
// 其余类型（结构体、切片、map，以及 JSON 列）一律回落到通用的 setField，
// 行为与未特化时完全一致。
func setterFor(f *fieldInfo) scanSetter {
	if f.json {
		return jsonUnmarshalVal
	}
	t := f.typ
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return setStringField
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return setIntField
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return setUintField
	case reflect.Float32, reflect.Float64:
		return setFloatField
	case reflect.Bool:
		return setBoolField
	}
	return setField
}

// rowScanner 绑定一次结果集：复用扫描缓冲，逐行写入目标结构体。
//
// 每个**查询**构造一次（而不是每行一次）是关键 —— 缓冲与映射计划都与行无关。
// 注意它持有可变状态（vals），不可跨 goroutine 共享。
type rowScanner struct {
	plan *scanPlan
	vals []any
	ptrs []any
}

// newRowScanner 构造行扫描器。sample 只用于取模型元数据，传 *T 或值为 nil 的 *T 均可。
func newRowScanner(rows *sql.Rows, sample any) (*rowScanner, error) {
	return newRowScannerMeta(rows, getMetaOf(reflect.ValueOf(sample)))
}

// newRowScannerMeta 与 newRowScanner 同义，只是直接给定模型元数据。
func newRowScannerMeta(rows *sql.Rows, meta *modelMeta) (*rowScanner, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	plan := meta.scanPlanFor(cols)
	s := &rowScanner{
		plan: plan,
		vals: make([]any, len(cols)),
		ptrs: make([]any, len(cols)),
	}
	for i := range s.vals {
		s.ptrs[i] = &s.vals[i]
	}
	return s, nil
}

// scanInto 扫描当前行并写入 dest（*T）；调用前须已 rows.Next()。
func (s *rowScanner) scanInto(rows *sql.Rows, dest any) error {
	if err := rows.Scan(s.ptrs...); err != nil {
		return err
	}
	rv := reflect.ValueOf(dest).Elem()
	fields, setters, vals := s.plan.fields, s.plan.set, s.vals
	for i, fi := range fields {
		if fi < 0 {
			continue
		}
		if err := setters[i](rv.Field(int(fi)), vals[i]); err != nil {
			return err
		}
	}
	return nil
}

// scanStruct 扫描当前行为单个结构体。
// 列表场景请改用 newRowScanner 复用扫描器（省掉逐行的缓冲分配与计划查找）。
func scanStruct(rows *sql.Rows, dest any) error {
	s, err := newRowScanner(rows, dest)
	if err != nil {
		return err
	}
	return s.scanInto(rows, dest)
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

// prepareTarget 处理 NULL 与指针字段，返回真正应当被写入的目标值。
// ok=false 表示值为 NULL 且已处理完毕（指针字段置 nil、非指针保持零值），调用方直接返回。
func prepareTarget(fv reflect.Value, val any) (reflect.Value, bool) {
	if val == nil {
		if fv.Kind() == reflect.Ptr {
			fv.Set(reflect.Zero(fv.Type()))
		}
		return reflect.Value{}, false
	}
	if fv.Kind() == reflect.Ptr {
		if fv.IsNil() {
			fv.Set(reflect.New(fv.Type().Elem()))
		}
		return fv.Elem(), true
	}
	return fv, true
}

// ---- 按类别赋值。target 必须已是解引用后、非 NULL 的目标值。----

func assignString(target reflect.Value, val any) error {
	switch v := val.(type) {
	case string:
		target.SetString(v)
	case []byte:
		target.SetString(string(v))
	default:
		target.SetString(fmt.Sprint(v))
	}
	return nil
}

func assignInt(target reflect.Value, val any) error {
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
	return nil
}

func assignUint(target reflect.Value, val any) error {
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
	return nil
}

func assignFloat(target reflect.Value, val any) error {
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
	return nil
}

func assignBool(target reflect.Value, val any) error {
	switch v := val.(type) {
	case bool:
		target.SetBool(v)
	case int64:
		target.SetBool(v != 0)
	default:
		return fmt.Errorf("orm: 无法把 %T 赋给 bool 字段", val)
	}
	return nil
}

// assignGeneric 兜底：按可赋值 / 可转换处理（结构体、time.Time、自定义类型等）。
func assignGeneric(target reflect.Value, val any) error {
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

// setField 把数据库驱动返回的原始值（int64/string/[]byte/float64/time.Time 等）
// 安全地赋给结构体字段，处理常见的类型不匹配。
//
// 字段类型已知时，映射计划会直接选到下面那组特化函数，省掉这里的 Kind 判断；
// 本函数是「类型不在特化范围内」时的通用入口，行为与特化前完全一致。
func setField(fv reflect.Value, val any) error {
	target, ok := prepareTarget(fv, val)
	if !ok {
		return nil
	}
	switch target.Kind() {
	case reflect.String:
		return assignString(target, val)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return assignInt(target, val)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return assignUint(target, val)
	case reflect.Float32, reflect.Float64:
		return assignFloat(target, val)
	case reflect.Bool:
		return assignBool(target, val)
	default:
		return assignGeneric(target, val)
	}
}

// ---- 特化 setter：与 setField 的各分支完全同义，只是省掉了逐行的 Kind 判断。----

func setStringField(fv reflect.Value, val any) error {
	target, ok := prepareTarget(fv, val)
	if !ok {
		return nil
	}
	return assignString(target, val)
}

func setIntField(fv reflect.Value, val any) error {
	target, ok := prepareTarget(fv, val)
	if !ok {
		return nil
	}
	return assignInt(target, val)
}

func setUintField(fv reflect.Value, val any) error {
	target, ok := prepareTarget(fv, val)
	if !ok {
		return nil
	}
	return assignUint(target, val)
}

func setFloatField(fv reflect.Value, val any) error {
	target, ok := prepareTarget(fv, val)
	if !ok {
		return nil
	}
	return assignFloat(target, val)
}

func setBoolField(fv reflect.Value, val any) error {
	target, ok := prepareTarget(fv, val)
	if !ok {
		return nil
	}
	return assignBool(target, val)
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

// parseVectorDim 从 db tag 的分段（如 "vector(1536)"）解析向量维度；
// 无括号或非数字时返回 0（表示未指定维度）。
func parseVectorDim(seg string) int {
	seg = strings.TrimSpace(seg)
	open := strings.Index(seg, "(")
	close := strings.Index(seg, ")")
	if open < 0 || close < open+1 {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(seg[open+1 : close]))
	if err != nil || n <= 0 {
		return 0
	}
	return n
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
