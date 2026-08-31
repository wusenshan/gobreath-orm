package orm

import (
	"context"
	"fmt"
	"reflect"
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

		// 加载子对象（复用 scanStruct，按列 IN 查询）
		var children []reflect.Value
		if kind == "belongs_to" {
			rows, err := queryListReflect(ctx, db, childElemType, childMeta, childMeta.pk.colName, parentKeyValues)
			if err != nil {
				return err
			}
			children = rows
		} else {
			rows, err := queryListReflect(ctx, db, childElemType, childMeta, childFKCol, parentKeyValues)
			if err != nil {
				return err
			}
			children = rows
		}

		// 把子对象按外键值挂回到每个父对象
		for i := 0; i < slice.Len(); i++ {
			ev := derefValue(slice.Index(i))
			field := ev.FieldByName(rel)
			if !field.IsValid() || !field.CanSet() {
				continue
			}
			switch kind {
			case "belongs_to":
				pv := fieldByColName(ev, parentMeta, parentFKCol)
				matched := findChild(children, childMeta.pk.colName, pv)
				setChild(field, matched)
			default: // has_many / has_one：子表外键 == 父主键
				pv := fieldByColName(ev, parentMeta, parentMeta.pk.colName)
				if kind == "has_many" {
					setChildren(field, children, childMeta, childFKCol, pv)
				} else {
					matched := findChild(children, childFKCol, pv)
					setChild(field, matched)
				}
			}
		}
	}
	return nil
}

// queryListReflect 按「col IN (vals)」查询并返回反射值切片（每个为 *T），复用 scanStruct 扫描整行。
func queryListReflect(ctx context.Context, db *DB, typ reflect.Type, meta *modelMeta, col string, vals []any) ([]reflect.Value, error) {
	if len(vals) == 0 {
		return nil, nil
	}
	d := db.dialect
	phs := make([]string, len(vals))
	args := make([]any, len(vals))
	for i, v := range vals {
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
	defer rows.Close()
	out := make([]reflect.Value, 0, len(vals))
	for rows.Next() {
		ptr := reflect.New(typ)
		if err := scanStruct(rows, ptr.Interface()); err != nil {
			return nil, err
		}
		out = append(out, ptr)
	}
	return out, rows.Err()
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

// findChild 在 children 中找「col 列值 == key」的首个匹配；未命中返回零值 reflect.Value。
func findChild(children []reflect.Value, col string, key any) reflect.Value {
	for _, c := range children {
		cv := derefValue(c)
		if sameKey(fieldByColName(cv, getMetaOf(c), col), key) {
			return c
		}
	}
	return reflect.Value{}
}

// setChildren 把 children 中「childFKCol == key」的子对象 append 进 has_many 切片字段。
func setChildren(field reflect.Value, children []reflect.Value, childMeta *modelMeta, childFKCol string, key any) {
	target := reflect.MakeSlice(field.Type(), 0, len(children))
	for _, c := range children {
		cv := fieldByColName(derefValue(c), childMeta, childFKCol)
		if sameKey(cv, key) {
			target = reflect.Append(target, normalizeChild(c, field.Type().Elem()))
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

// sameKey 比较两个键是否相等（标量主键/外键值）。
func sameKey(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return reflect.DeepEqual(a, b)
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
