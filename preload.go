package orm

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
)

// Preload 把 parents（*[]T）上声明的关联关系一次性加载并赋值，避免 N+1 查询。
// relations 为父类型 T 的 Go 字段名列表；支持三类关系：
//   - has_many  ：父持有 N 个子对象（字段为切片），按「子表外键列 = 父表主键」批量查询后挂回；
//   - has_one   ：父持有 1 个子对象（字段为结构体/指针），规则同 has_many，只是每个父最多一个；
//   - belongs_to：父持有指向子表主键的外键列（字段为结构体/指针），按「父外键值 IN 子主键」加载。
//
// 外键列约定：
//   - has_many / has_one 默认 <父类型名>ID 的 snake 形式（如 User → user_id）；
//   - belongs_to       默认 <子类型名>ID 的 snake 形式（如 Article → article_id，即父表上的外键列名）。
//
// 可用关联字段的 `orm` tag 覆盖关系种类与外键列，例如：
//
//	type User struct {
//	    Id       int64     `db:"id,pk,autoincrement"`
//	    Articles []Article `db:"-" orm:"has_many"`                 // 子表含 user_id 列
//	    Profile  *Profile  `db:"-" orm:"has_one;fk:user_id"`        // 显式指定外键列
//	}
//	type Article struct {
//	    Id     int64 `db:"id,pk,autoincrement"`
//	    UserId int64 `db:"user_id"`
//	}
//
// ⚠️ 关联字段务必用 db:"-" 标记（避免被当作普通列），否则会同时参与 CRUD 而报错。
// 软删除过滤对该子查询同样生效（不会加载已逻辑删除的子对象）。
func Preload[T any](ctx context.Context, db *DB, parents *[]T, relations ...string) error {
	if parents == nil || *parents == nil {
		return nil
	}
	return preloadReflect(ctx, db, reflect.ValueOf(parents).Elem(), relations)
}

// PreloadOne 是 Preload 的单对象版本，dst 为 *T；关系加载规则与 Preload 完全一致。
func PreloadOne[T any](ctx context.Context, db *DB, dst *T, relations ...string) error {
	if dst == nil {
		return nil
	}
	elemType := reflect.TypeOf(*dst)
	slice := reflect.MakeSlice(reflect.SliceOf(elemType), 1, 1)
	slice.Index(0).Set(reflect.ValueOf(*dst))
	if err := preloadReflect(ctx, db, slice, relations); err != nil {
		return err
	}
	*dst = slice.Index(0).Interface().(T)
	return nil
}

// preloadReflect 反射实现关联加载：slice 必须是可寻址的 []T（T 为结构体或指针结构体）。
func preloadReflect(ctx context.Context, db *DB, slice reflect.Value, relations []string) error {
	if slice.Kind() != reflect.Slice || slice.Len() == 0 {
		return nil
	}
	elemType := slice.Type().Elem()
	parentMeta := getMetaByType(derefType(elemType))
	parentTypeName := elemName(elemType)

	for _, rel := range relations {
		sf, ok := derefType(elemType).FieldByName(rel)
		if !ok {
			return fmt.Errorf("orm: Preload 关系 %q 在类型 %s 上不存在", rel, parentMeta.table)
		}
		kind, fk := parseRelTag(sf)
		if kind == "" {
			if sf.Type.Kind() == reflect.Slice {
				kind = "has_many"
			} else {
				kind = "has_one"
			}
		}

		childElemType := derefType(sf.Type)
		childMeta := getMetaByType(childElemType)
		if childMeta.pk == nil {
			return fmt.Errorf("orm: Preload 子类型 %s 无主键，无法关联", childMeta.table)
		}

		var childFKCol, parentFKCol string
		var parentKeyValues []any // has_many/has_one：父主键集合；belongs_to：父外键值集合
		switch kind {
		case "belongs_to":
			parentFKCol = fk
			if parentFKCol == "" {
				parentFKCol = toSnake(elemName(childElemType)) + "_id"
			}
			parentKeyValues = collectColumn(slice, parentMeta, parentFKCol)
		default: // has_many / has_one：子表持有指向父主键的外键
			childFKCol = fk
			if childFKCol == "" {
				childFKCol = toSnake(parentTypeName) + "_id"
			}
			parentKeyValues = collectPK(slice, parentMeta)
		}

		// 加载子对象（复用 scanStruct，按列 IN 查询）。两侧的连接列随关系方向不同：
		// belongs_to 用子表主键对父表外键；has_one/has_many 用子表外键对父表主键。
		matchCol := childFKCol
		if kind == "belongs_to" {
			matchCol = childMeta.pk.colName
		}
		// 先滤掉 nil / 零值键再查询：`IN (0)` 会把「外键恰好为 0」的脏数据挂到尚未落库的父对象上，
		// 而这类父子关系并不存在。零值键在匹配阶段同样被判为「不关联」（见 keyOf），语义一致。
		keys := validKeys(parentKeyValues)
		var children []reflect.Value
		if len(keys) > 0 {
			rows, err := queryListReflect(ctx, db, childElemType, childMeta, matchCol, keys)
			if err != nil {
				return err
			}
			children = rows
		}

		// 把子对象按连接列挂回到每个父对象。
		// 先给子对象建「连接列值 → 子对象」索引再匹配：此前是「每个父对象 × 每个子对象」的
		// 双重反射比较，1000 个父对象配上 1000 个子对象就是 100 万次比较。
		idx := indexChildren(children, childMeta, matchCol)

		for i := 0; i < slice.Len(); i++ {
			ev := derefValue(slice.Index(i))
			field := ev.FieldByName(rel)
			if !field.IsValid() || !field.CanSet() {
				continue
			}
			var pv any
			if kind == "belongs_to" {
				pv = fieldByColName(ev, parentMeta, parentFKCol)
			} else {
				pv = fieldByColName(ev, parentMeta, parentMeta.pk.colName)
			}
			if kind == "has_many" {
				setChildren(field, idx, pv)
			} else { // has_one / belongs_to：取首个匹配（与历史行为一致）
				setChild(field, idx.first(pv))
			}
		}
	}
	return nil
}

// preloadInChunk 单条 IN 查询携带的最大键数量。
//
// 大批量 Preload（例如一次加载 5 万个父对象）会把 5 万个占位符塞进一条 SQL：
// MySQL 的 max_allowed_packet、PG 的 65535 个绑定参数上限、SQLite 的变量数上限
// （默认 999）都会直接报错 —— SQLite 甚至只有 999，几千行数据就挂。
// 这里按固定大小切片，逐个分片查询再合并结果，行为与单条查询一致（顺序不变）。
const preloadInChunk = 500

// queryListReflect 按「col IN (vals)」查询并返回反射值切片（每个为 *T），复用 scanStruct 扫描整行。
// 键数量超过 preloadInChunk 时自动分片查询后合并，避免撞上各数据库的绑定参数上限。
func queryListReflect(ctx context.Context, db *DB, typ reflect.Type, meta *modelMeta, col string, vals []any) ([]reflect.Value, error) {
	if len(vals) == 0 {
		return nil, nil
	}
	d := db.dialect
	out := make([]reflect.Value, 0, len(vals))
	for start := 0; start < len(vals); start += preloadInChunk {
		end := start + preloadInChunk
		if end > len(vals) {
			end = len(vals)
		}
		chunk := vals[start:end]
		phs := make([]string, len(chunk))
		args := make([]any, len(chunk))
		for i, v := range chunk {
			args[i] = v
			phs[i] = d.Placeholder(i + 1)
		}
		sqlStr := fmt.Sprintf("SELECT * FROM %s WHERE %s IN (%s)",
			quoteTable(meta.finalTable(db.prefix), d), d.QuoteIdent(col), strings.Join(phs, ", "))
		if s := logicSuffix(resolveLogic(meta, db), d, false); s != "" {
			sqlStr += " AND " + s
		}
		rows, err := db.queryContext(ctx, sqlStr, args...)
		if err != nil {
			return nil, err
		}
		// 与 SelectList 同理：扫描器按查询构造一次，不在逐行里重建映射计划与缓冲。
		sc, err := newRowScannerMeta(rows, meta)
		if err != nil {
			rows.Close()
			return nil, err
		}
		for rows.Next() {
			ptr := reflect.New(typ)
			if err := sc.scanInto(rows, ptr.Interface()); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, ptr)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ---- 反射辅助 ----

func derefType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Ptr || t.Kind() == reflect.Slice {
		t = t.Elem()
	}
	return t
}

// elemName 返回类型（含指针）最终结构体类型的名称，用于推导默认外键列名。
func elemName(t reflect.Type) string {
	return derefType(t).Name()
}

// parseRelTag 解析关联字段的 `orm` tag，返回关系种类（has_many/has_one/belongs_to）与外键列名覆盖。
func parseRelTag(f reflect.StructField) (kind, fk string) {
	tag := f.Tag.Get("orm")
	if tag == "" {
		return "", ""
	}
	for _, p := range strings.Split(tag, ";") {
		p = strings.TrimSpace(p)
		switch {
		case p == "has_many" || p == "has_one" || p == "belongs_to":
			kind = p
		case strings.HasPrefix(p, "fk:"):
			fk = strings.TrimSpace(strings.TrimPrefix(p, "fk:"))
		}
	}
	return
}

// collectPK 收集切片每个元素的父主键列值（[]any）。
func collectPK(slice reflect.Value, meta *modelMeta) []any {
	out := make([]any, 0, slice.Len())
	for i := 0; i < slice.Len(); i++ {
		ev := derefValue(slice.Index(i))
		out = append(out, fieldByColName(ev, meta, meta.pk.colName))
	}
	return out
}

// collectColumn 收集切片每个元素指定列的值（[]any），用于 belongs_to 收集父外键值。
func collectColumn(slice reflect.Value, meta *modelMeta, col string) []any {
	out := make([]any, 0, slice.Len())
	for i := 0; i < slice.Len(); i++ {
		ev := derefValue(slice.Index(i))
		out = append(out, fieldByColName(ev, meta, col))
	}
	return out
}

// fieldByColName 返回结构体（或指针）上某 db 列名对应的字段值。
func fieldByColName(ev reflect.Value, meta *modelMeta, col string) any {
	fi := fieldInfoForCol(meta, col)
	if fi == nil {
		return nil
	}
	fv := ev.FieldByName(fi.goName)
	if !fv.IsValid() {
		return nil
	}
	return fv.Interface()
}

// childIndex 子对象按连接列建好的索引：规范化键 → 子对象下标列表（保持查询返回顺序）。
//
// 有了它，把子对象挂回父对象只需按父侧键做一次 map 查找（O(1)），
// 不再对每个父对象遍历整个子对象列表做反射比较。
type childIndex struct {
	children []reflect.Value
	byKey    map[string][]int
}

// indexChildren 按 meta 的 col 列给子对象建索引。
// 键为 nil / 零值的子对象不入索引：这类行没有有效的连接值，不该被当成任何一个父对象的关联。
func indexChildren(children []reflect.Value, meta *modelMeta, col string) childIndex {
	idx := childIndex{children: children, byKey: make(map[string][]int, len(children))}
	for i, c := range children {
		k, ok := keyOf(fieldByColName(derefValue(c), meta, col))
		if !ok {
			continue
		}
		idx.byKey[k] = append(idx.byKey[k], i)
	}
	return idx
}

// first 返回键匹配的首个子对象；未命中返回零值 reflect.Value。
func (x childIndex) first(key any) reflect.Value {
	k, ok := keyOf(key)
	if !ok {
		return reflect.Value{}
	}
	list := x.byKey[k]
	if len(list) == 0 {
		return reflect.Value{}
	}
	return x.children[list[0]]
}

// setChildren 把键匹配的子对象（可能有多个）append 进 has_many 切片字段。
func setChildren(field reflect.Value, idx childIndex, key any) {
	target := reflect.MakeSlice(field.Type(), 0, len(idx.children))
	if k, ok := keyOf(key); ok {
		for _, i := range idx.byKey[k] {
			target = reflect.Append(target, normalizeChild(idx.children[i], field.Type().Elem()))
		}
	}
	field.Set(target)
}

// setChild 把匹配的子对象（或 nil）赋给 has_one / belongs_to 字段（支持结构体与指针字段）。
func setChild(field reflect.Value, child reflect.Value) {
	if !child.IsValid() {
		field.Set(reflect.Zero(field.Type()))
		return
	}
	field.Set(normalizeChild(child, field.Type()))
}

// normalizeChild 把反射得到的子对象（*T 或 T）归一为目标字段元素类型（结构体或指针结构体）。
func normalizeChild(child reflect.Value, targetType reflect.Type) reflect.Value {
	if targetType.Kind() == reflect.Ptr {
		if child.Kind() == reflect.Ptr {
			return child
		}
		cp := reflect.New(child.Type())
		cp.Elem().Set(child)
		return cp
	}
	if child.Kind() == reflect.Ptr {
		return child.Elem()
	}
	return child
}

// keyOf 把主键/外键值归一成可直接比较的字符串键。
//
// 归一化的必要性：这两个值往往来自不同的地方 —— 数据库扫回来的是驱动给的 int64/[]byte，
// 调用方在 Go 里手填的可能是 int/uint/int32。此前用 reflect.DeepEqual 比较，int64(7) 与
// int(7) 判为不等，Preload 会把**明明存在的关联**静默留空（父对象字段全零，不报错、
// 不提示，只能靠人工比对数据才发现）。这里统一按「数值 → 十进制数字」「字符串 → 原文」
// 归一，跨整型宽度/有无符号也能正确匹配。
//
// 返回 ok=false 表示该值不能作为连接键：nil、数字 0、空字符串。
// 这类值意味着父对象尚未落库、或外键为空，不应与任何子行关联：
// 旧实现的 `IN (0)` 会把外键恰为 0 的脏数据挂到未保存的父对象上。
func keyOf(v any) (string, bool) {
	if v == nil {
		return "", false
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface:
		if rv.IsNil() {
			return "", false
		}
		return keyOf(rv.Elem().Interface())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n := rv.Int()
		if n == 0 {
			return "", false
		}
		return "n:" + strconv.FormatInt(n, 10), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		n := rv.Uint()
		if n == 0 {
			return "", false
		}
		return "n:" + strconv.FormatUint(n, 10), true
	case reflect.Float32, reflect.Float64:
		f := rv.Float()
		if f == 0 {
			return "", false
		}
		// 整数值的浮点（1.0）与整型（1）也算同一个键：主外键类型不一致是常见用法。
		if f == math.Trunc(f) && f >= -9.007199254740992e15 && f <= 9.007199254740992e15 {
			return "n:" + strconv.FormatInt(int64(f), 10), true
		}
		return "f:" + strconv.FormatFloat(f, 'g', -1, 64), true
	case reflect.Bool:
		return "b:" + strconv.FormatBool(rv.Bool()), true
	case reflect.String:
		s := rv.String()
		if s == "" {
			return "", false
		}
		return "s:" + s, true
	case reflect.Slice:
		// 少数驱动/字段类型会以 []byte 交出主键值：数字按数字归一，其余按文本。
		if b, isBytes := v.([]byte); isBytes {
			if len(b) == 0 {
				return "", false
			}
			if n, err := strconv.ParseInt(string(b), 10, 64); err == nil && n != 0 {
				return "n:" + strconv.FormatInt(n, 10), true
			}
			s := string(b)
			if s == "" {
				return "", false
			}
			return "s:" + s, true
		}
	}
	return fmt.Sprintf("v:%v", v), true
}

// validKeys 过滤掉 nil / 零值键，并去重（保留首次出现顺序），用于构造 IN 查询的参数。
func validKeys(vals []any) []any {
	out := make([]any, 0, len(vals))
	seen := make(map[string]bool, len(vals))
	for _, v := range vals {
		k, ok := keyOf(v)
		if !ok || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, v)
	}
	return out
}

func derefValue(v reflect.Value) reflect.Value {
	for v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return v
		}
		v = v.Elem()
	}
	return v
}
