package orm

import (
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// fieldInfo 结构体字段的元数据。
type fieldInfo struct {
	goName    string
	colName   string
	pk        bool
	autoInc   bool
	ignore    bool
	json      bool // true 表示字段以 JSON 形式读写（db tag 含 ",json"）
	vector    bool // true 表示字段为向量列（db tag 含 ",vector"），读写时序列化为文本 [..]
	vectorDim int  // 向量维度（db tag 含 ",vector(N)" 时解析出 N；0 表示未指定维度）
	logic     bool // true 表示该字段是逻辑删除列（db tag 含 ",logic"）
	nologic   bool // true 表示显式退出约定软删除匹配（db tag 含 ",nologic"）
	version   bool // true 表示该字段是乐观锁版本列（db tag 含 ",version"）
	unique    bool // true 表示该列需唯一约束（db tag 含 ",unique"），仅 AutoMigrate 使用
	index     bool // true 表示该列需二级索引（db tag 含 ",index"），仅 AutoMigrate 使用
	typ       reflect.Type
	rawTag    string // 原始 struct tag 字符串（仅用于 StrictTagCheck 模式下的格式校验）
	// idx 是该字段相对模型根结构体的 reflect 路径。普通字段为单级（[i]），
	// 匿名嵌入结构体被扁平化展开后为多级（[嵌入字段下标, 内层下标, ...]）。
	// 取值 / 赋值一律走 FieldByIndex，因此 meta.fields 的下标不再等于结构体字段下标。
	idx []int
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
	// 主键 / 逻辑列 / 版本列先收集字段下标，循环体结束后再统一取地址 ——
	// 循环里直接 `&m.fields[len(m.fields)-1]` 并不安全：后续 append 触发扩容时，
	// 已保存的指针会指向**旧底层数组**的副本，值当时是对的但与 m.fields 断开同步。
	var pkIdxs []int
	logicIdx, versionIdx := -1, -1
	// seen 记录已占用的列名 → 字段来源，用于检出列名冲突（含嵌入展开后与外层撞名）。
	// 静默让其中一个胜出会把「这一列到底是谁」变成猜谜，故解析期直接报错。
	seen := make(map[string]string)
	// register 把一个字段登记进元数据：追加到 fields、维护列清单，并收集
	// pk / logic / version 的下标。匿名嵌入展开出来的字段也走这里，
	// 因此它们与外层字段被同等对待（嵌入里的 ,pk / ,logic / ,version 同样生效）。
	register := func(fi fieldInfo) {
		m.fields = append(m.fields, fi)
		if fi.ignore {
			return
		}
		if prev, ok := seen[fi.colName]; ok {
			panic(fmt.Errorf("orm: 结构体 %s 的列 %q 被重复声明（%s 与 %s）："+
				"请用 db tag 给其中一个指定不同的列名，或用 `db:\"-\"` 跳过不需要的那个",
				typ.Name(), fi.colName, prev, fi.goName))
		}
		seen[fi.colName] = typ.Name() + "." + fi.goName
		m.columns = append(m.columns, fi.colName)
		if fi.pk {
			pkIdxs = append(pkIdxs, len(m.fields)-1)
		}
		if fi.logic {
			logicIdx = len(m.fields) - 1
			m.logicIsTime = isTimeType(fi.typ)
		}
		if fi.version {
			versionIdx = len(m.fields) - 1
		}
	}
	// 字段收集（含匿名嵌入结构体的扁平化展开）见 collectFields。
	for _, fi := range collectFields(typ, nil, 0) {
		register(fi)
	}
	// 未显式声明主键时，沿用「字段名 ID / Id」的约定推断。
	if len(pkIdxs) == 0 {
		for i := range m.fields {
			if m.fields[i].goName == "ID" || m.fields[i].goName == "Id" {
				m.fields[i].pk = true
				pkIdxs = append(pkIdxs, i)
				break
			}
		}
	}
	if len(pkIdxs) > 1 {
		cols := make([]string, 0, len(pkIdxs))
		for _, i := range pkIdxs {
			cols = append(cols, m.fields[i].colName)
		}
		panic(compositePKPanic(typ, cols))
	}
	if len(pkIdxs) == 1 {
		m.pk = &m.fields[pkIdxs[0]]
	}
	if logicIdx >= 0 {
		m.logicCol = &m.fields[logicIdx]
	}
	if versionIdx >= 0 {
		m.versionCol = &m.fields[versionIdx]
	}
	m.tagChecked = strictTagCheck.Load()
	return m
}

// maxEmbedDepth 限制嵌入结构体的展开层数。Go 的值类型嵌套本就不可能无限深
// （会编译失败），这里仍加一道闸，避免异常嵌套把解析拖死。
const maxEmbedDepth = 8

// collectFields 递归收集 typ 的可映射字段，顺序与声明顺序一致。
//
// **匿名嵌入的结构体会被扁平化展开**：内层字段与外层字段一起出现在列清单里
// （如 `Base` 里的 `CreatedAt` 直接成为模型的 created_at 列），取值路径记录在
// fieldInfo.idx —— 一条 reflect 的多级索引，读写与行映射据此定位到嵌入结构体里的字段。
//
// 在这之前框架把嵌入字段当成**一个普通列**：AutoMigrate 建出伪列（Base → `base TEXT`）、
// Insert 把整个结构体当参数绑定（驱动报 unsupported type）、Col 因匿名字段与外层结构体
// 同地址而返回伪列名 —— 最后这条最危险：不报错，却生成指向错误列的查询条件。
// 后来改成解析期 panic 以堵住静默错误，代价是这类模型直接不可用（升级即崩）。
// 扁平化才是用户真正想要的语义：GORM 的 `gorm:"embedded"` 也是这个方向。
//
// prefix 是到达 typ 的字段路径（最外层为 nil），depth 是当前嵌入层数。
func collectFields(typ reflect.Type, prefix []int, depth int) []fieldInfo {
	var out []fieldInfo
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
		idx := make([]int, 0, len(prefix)+1)
		idx = append(append(idx, prefix...), i)

		// 匿名嵌入：结构体就地展开；指针暂不支持（见下）；其它 Kind
		// （如匿名嵌入的命名基本类型 `type MyInt int`）按普通列处理，
		// 与 Go 提升字段的语义一致。
		if f.Anonymous && tag != "-" {
			switch f.Type.Kind() {
			case reflect.Struct:
				if depth >= maxEmbedDepth {
					panic(fmt.Errorf("orm: 结构体 %s 的嵌入层数超过 %d 层，请检查是否有异常嵌套",
						typ.Name(), maxEmbedDepth))
				}
				out = append(out, collectFields(f.Type, idx, depth+1)...)
				continue
			case reflect.Ptr:
				panic(fmt.Errorf("orm: 结构体 %s 匿名嵌入了指针类型 %s，暂不支持："+
					"行映射要在扫描时按需 new、取连接键要先判空，nil 指针的读写语义未定义；"+
					"请改为值嵌入 %s，或把需要的字段平铺声明在 %s 上",
					typ.Name(), f.Type.String(), f.Type.Elem().String(), typ.Name()))
			}
		}
		if tag == "-" {
			out = append(out, fieldInfo{goName: f.Name, ignore: true, rawTag: raw, idx: idx})
			continue
		}
		out = append(out, parseFieldInfo(f, idx))
	}
	return out
}

// parseFieldInfo 解析单个字段的 db tag（列名与 pk / autoincrement / json / vector /
// logic / version / unique / index 等修饰符）。idx 是该字段相对模型根结构体的路径。
func parseFieldInfo(f reflect.StructField, idx []int) fieldInfo {
	fi := fieldInfo{goName: f.Name, typ: f.Type, rawTag: string(f.Tag), idx: idx}
	tag := f.Tag.Get("db")
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
	return fi
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

// compositePKPanic 在模型声明了多个主键列时构造 panic 值。
//
// 复合主键尚未支持 —— 而在加上这道闸门之前，parseMeta 会让「最后一个 ,pk 列」静默胜出：
// 多主键模型能正常构造，但主键只认最后一列，于是 DeleteById / UpdateById / Upsert 生成的
// WHERE 少了一半条件，**可能命中并改写多行**；AutoMigrate 还会产出两个内联 PRIMARY KEY
// 的非法 DDL（MySQL 1068 / PG "multiple primary keys"）。「静默做错事」比直接失败危险得多，
// 因此在这里硬失败，把问题暴露在启动第一时间。
//
// 需要复合主键时，请先用显式条件（Eq / In / Where）代替按主键操作。
func compositePKPanic(typ reflect.Type, cols []string) error {
	name := typ.Name()
	if name == "" {
		name = typ.String() // 匿名结构体没有 Name()
	}
	return fmt.Errorf("orm: 结构体 %s 声明了 %d 个主键列（%s），但复合主键尚未支持；"+
		"请只保留一个 ,pk 列，或改用显式条件（Eq / In）代替按主键操作",
		name, len(cols), strings.Join(cols, ", "))
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
	// dupCols 记录「同名且都映射到字段」的重复列（按出现顺序去重）。
	// 非空表示结果集有歧义：第 2 个同名列会覆盖第 1 个的值，调用方必须报错，
	// 详见 newRowScannerMeta。
	dupCols []string
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
	// 复查重名列：JOIN 多表后 SELECT *（两张表都有 id / name）时，结果集会带两列同名，
	// 上面「取最后一个匹配字段」会让后一列静默覆盖前一列的值 —— 数据级错误且毫无提示。
	// 这里把「同名 + 至少映射到一个字段」的列记下来，交由 newRowScannerMeta 返回错误。
	// 只在确有字段映射时才记录：不映射到任何字段的重复列（如自定义投影里重复出现的
	// 计算列）不会造成错值，保持原有宽松行为。
	if len(cols) > 1 {
		count := make(map[string]int, len(cols))
		for _, c := range cols {
			count[c]++
		}
		seen := make(map[string]bool, len(cols))
		for i, c := range cols {
			if count[c] > 1 && p.fields[i] >= 0 && !seen[c] {
				seen[c] = true
				p.dupCols = append(p.dupCols, c)
			}
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
	if f.vector {
		// 向量列必须走专用 setter：驱动交回来的是 "[1,0,0]" 这样的**文本**，
		// 落到 []float32 / [3]float32 字段上，通用兜底只会报
		// "orm: 无法把 string 赋给 []float32 字段"。
		dim := f.vectorDim
		return func(fv reflect.Value, val any) error { return setVectorField(fv, val, dim) }
	}
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
	meta *modelMeta // 用于按 fieldInfo.idx 定位字段（嵌入字段的路径是多级的）
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
	// 结果集带重名列时立刻报错，而不是「最后一个同名列静默胜出」：
	// 后者会把不想要的那一列的值写进字段（如 JOIN 后 SELECT *，两表都有 id，
	// 结构体的 Id 拿到的是被 JOIN 表的值），既不报错也无任何提示。
	if len(plan.dupCols) > 0 {
		return nil, fmt.Errorf("orm: 结果集中列名 %s 重复出现，无法确定各自映射到哪个字段（多表 JOIN 后 SELECT * 时常见）："+
			"请用 Select 显式指定列，必要时用 AS 起唯一别名",
			"`"+strings.Join(plan.dupCols, "`, `")+"`")
	}
	s := &rowScanner{
		plan: plan,
		meta: meta,
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
		// 按 fieldInfo.idx 定位：嵌入字段被扁平化后路径是多级的，
		// fields 里的下标也不再等于结构体字段下标。
		if err := setters[i](rv.FieldByIndex(s.meta.fields[fi].idx), vals[i]); err != nil {
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

// ---- 向量列 ----

// parseVectorText 解析向量列的文本形式（pgvector 的 "[1,2,3]"、以及 MySQL
// STRING_TO_VECTOR / TO_VECTOR 的输出格式），返回各分量。
// 空向量 "[]" 返回空切片（不是错误）。
func parseVectorText(s string) ([]float64, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]float64, len(parts))
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return nil, fmt.Errorf("orm: 向量文本第 %d 个分量 %q 无法解析：%w", i, strings.TrimSpace(p), err)
		}
		out[i] = f
	}
	return out, nil
}

// looksLikeVectorText 判断驱动返回的字节串是文本形式（"[1,2,3]"）还是 MySQL 的二进制形式。
//
// 只看跳过前导空白后的第一个字节是不是 '['：PG 与 MySQL 的 VECTOR_TO_STRING / FROM_VECTOR
// 都产出列表文本；而 MySQL 的 VECTOR 列本身按 BLOB 返回二进制，首字节是某个 float32
// 的最低有效字节，是 '['（0x5B）的概率极低 —— 即便撞上，后续的维度校验也会把它拦下来。
func looksLikeVectorText(b []byte) bool {
	for _, c := range b {
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		}
		return c == '['
	}
	return false
}

// parseVectorBinary 解码 MySQL 的 VECTOR 二进制形式。
//
// MySQL 的 VECTOR 列底层是 BLOB：N 个小端序 IEEE-754 单精度浮点依次排布
// （官方 worklog 原文 "Field_vector : public Field_blob"，精度 = sizeof(float)）。
// 文档样例可逐字节核对：STRING_TO_VECTOR("[3.14,2024,18]") = 0xC3F548400000FD4400009041
//   - 3.14f = 0x4048F5C3 → 小端字节 C3 F5 48 40
//   - 2024f = 0x44FD0000 → 小端字节 00 00 FD 44
//   - 18f = 0x41900000 → 小端字节 00 00 90 41
//
// 也就是说 MySQL 下 `SELECT emb FROM t` 拿回的是裸字节、而不是 "[..]" 文本 —— 读路径必须
// 自己解码，否则向量列会「写得进去、读不回来」（PG 走文本、MySQL 走二进制，两条路都要通）。
func parseVectorBinary(b []byte, dim int) ([]float64, error) {
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("orm: 向量二进制长度 %d 不是 4 的倍数（MySQL 每个分量 4 字节单精度浮点）", len(b))
	}
	n := len(b) / 4
	if dim > 0 && n != dim {
		return nil, fmt.Errorf("orm: 向量二进制解出 %d 维，与字段声明的 %d 维不符（db tag 的 ,vector(%d)）", n, dim, dim)
	}
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		out[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:])))
	}
	return out, nil
}

// parseVectorPayload 按载荷形态分派：文本（"[1,2,3]"）走文本解析，其余按 MySQL 的
// VECTOR 二进制解码。空载荷返回空切片（真正的 SQL NULL 由 prepareTarget 在上层处理）。
func parseVectorPayload(b []byte, dim int) ([]float64, error) {
	if len(b) == 0 {
		return nil, nil
	}
	if looksLikeVectorText(b) {
		return parseVectorText(string(b))
	}
	return parseVectorBinary(b, dim)
}

// setVectorField 把驱动返回的向量值写回 []float32 / []float64 / 其定长数组字段。
//
// 背景：写方向的序列化（serializeVector：[]float32 → "[..]"）早就有了，读方向却一直
// 缺 —— 于是「向量列能写进去、读不回来」：PG 的 vector 列经驱动交回的是文本，落到
// []float32 字段上会报 `orm: 无法把 string 赋给 []float32 字段`。RAG 场景里检索结果
// 本身就要读向量列（拿去做二次召回 / 展示），这个缺口必须在框架层补上，
// 否则每个调用方都要退回 RawQuery。
//
// dim > 0（db tag 写了 ,vector(N)）时校验维度，不一致直接报错 —— 静默截断/补零
// 只会把问题推迟到更难定位的地方。
func setVectorField(fv reflect.Value, val any, dim int) error {
	target, ok := prepareTarget(fv, val)
	if !ok {
		return nil
	}
	var floats []float64
	switch v := val.(type) {
	case []float64:
		floats = v
	case []float32:
		floats = make([]float64, len(v))
		for i, x := range v {
			floats[i] = float64(x)
		}
	case string:
		parsed, err := parseVectorPayload([]byte(v), dim)
		if err != nil {
			return err
		}
		floats = parsed
	case []byte:
		parsed, err := parseVectorPayload(v, dim)
		if err != nil {
			return err
		}
		floats = parsed
	default:
		return fmt.Errorf("orm: 无法把 %T 解析为向量（期望 \"[..]\" 文本或浮点切片）", val)
	}
	if dim > 0 && len(floats) != dim {
		return fmt.Errorf("orm: 向量维度不匹配：字段声明为 %d 维（db tag 的 ,vector(%d)），数据库返回 %d 维",
			dim, dim, len(floats))
	}

	setElem := func(ev reflect.Value, f float64) {
		if ev.Kind() == reflect.Float32 {
			ev.SetFloat(float64(float32(f)))
			return
		}
		ev.SetFloat(f)
	}
	switch target.Kind() {
	case reflect.Slice:
		out := reflect.MakeSlice(target.Type(), len(floats), len(floats))
		for i, f := range floats {
			setElem(out.Index(i), f)
		}
		target.Set(out)
	case reflect.Array:
		if target.Len() != len(floats) {
			return fmt.Errorf("orm: 向量维度不匹配：字段是 [%d]，数据库返回 %d 维",
				target.Len(), len(floats))
		}
		for i, f := range floats {
			setElem(target.Index(i), f)
		}
	default:
		return fmt.Errorf("orm: 向量列的字段类型应为 []float32 / []float64 或其定长数组，实际 %s", target.Type())
	}
	return nil
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
			// 嵌入字段扁平化后路径是多级的，且 meta.fields 的下标不再等于结构体字段
			// 下标（忽略字段也占位），必须按记录的 idx 取。
			return ev.FieldByIndex(meta.fields[i].idx)
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
