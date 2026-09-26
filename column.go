package orm

import (
	"fmt"
	"reflect"
	"strings"
)

// ColExpr 字段选择器解析后的列表达式。调用点不会出现任何字符串列名。
type ColExpr struct {
	name string
}

// Name 返回解析后的列名（只读）。聚合 / 投影这类「需要列名原值」的场景用它，
// 调试时也可以直接打印表达式看清楚到底选中了哪一列。
func (c ColExpr) Name() string { return c.name }

// TColExpr 带字段类型的列表达式：既携带列名，也携带字段的 Go 类型 F。
//
// 它解决的问题是：Col 的返回类型把 F 擦除了，于是 Pluck / MaxOf 这类
// 「结果类型取决于列」的函数无法从参数推导出 F，调用方被迫手写全部类型参数
// （orm.Pluck[User, int64](...)）。用 TCol 则全部类型可从闭包字面量推导：
//
//	ids, err := orm.Pluck(ctx, db, q, orm.TCol(func(u *User) *int64 { return &u.ID }))
type TColExpr[T any, F any] struct{ ColExpr }

// TCol 同时解析列名与字段类型（Col 只解析列名）。
// 列名的推导规则与 Col 完全一致，因此同样受「列名只来自结构体 db tag」的约束。
func TCol[T any, F any](picker func(*T) *F) TColExpr[T, F] {
	return TColExpr[T, F]{ColExpr: Col[T, F](picker)}
}

// Col 把一个「返回字段指针的闭包」解析成数据库列名。
//
// 用法：
//
//	orm.Col[User](func(u *User) *string { return &u.Name })
//
// 列名从结构体字段的 `db` tag 自动推断，永不手敲，写错字段会在编译期（类型不对）
// 或运行期（tag 拼错）立刻暴露，而不是生成一条错误 SQL。
func Col[T any, F any](picker func(*T) *F) ColExpr {
	return ColExpr{name: resolveColumn(picker)}
}

// ColOf 按结构体字段名直接取列名表达式。用于代码生成场景：生成器把字段名
// 写成结构体字段，调用点仍然是 `UserCols.Age` 这种类型安全的形式，但底层
// 不再依赖手写闭包。
//
// 匹配规则：优先 Go 字段名（大小写敏感），其次 db tag 中的列名。未匹配会 panic。
func ColOf[T any](fieldName string) ColExpr {
	meta := getMeta[T]()
	for i := range meta.fields {
		f := meta.fields[i]
		if f.goName == fieldName {
			return ColExpr{name: f.colName}
		}
	}
	for i := range meta.fields {
		f := meta.fields[i]
		if f.colName == fieldName {
			return ColExpr{name: f.colName}
		}
	}
	panic(fmt.Sprintf("orm: 类型 %s 中不存在字段 %q", meta.table, fieldName))
}

// resolveColumn 用反射从 picker 闭包反查其指向的结构体字段，读出 db tag。
func resolveColumn[T any, F any](picker func(*T) *F) string {
	t := new(T)
	tv := reflect.ValueOf(t).Elem()          // 可寻址的结构体
	ptr := picker(t)                         // *F，指向 t 的某个字段
	target := reflect.ValueOf(ptr).Pointer() // 该指针持有的地址（即字段地址）

	rt := tv.Type()
	if col, ok := resolveColumnIn(tv, rt, target); ok {
		return col
	}
	panic("orm: 无法从 picker 闭包解析出字段，请确认闭包返回的是该结构体的字段指针")
}

// resolveColumnIn 在结构体 v（类型 t）中按字段地址反查列名，找不到时 ok=false。
//
// 必须递归进匿名嵌入结构体：嵌入的字段是 **promoted** 的，`&t.CreatedAt` 拿到的地址
// 属于内嵌的 Base，外层 t 的直接字段里根本没有它，只扫一层必然漏。反过来，嵌入字段
// 与外层结构体可能同地址（偏移 0），只扫一层还会把「选了嵌入里的第一个字段」误判成
// 「选了整个嵌入字段」，返回伪列名 "base" —— 这正是扁平化之前 Col 静默返回错误列名的原因。
// 嵌入字段自身不是列（已被展开），故不参与匹配。
func resolveColumnIn(v reflect.Value, t reflect.Type, target uintptr) (string, bool) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		fv := v.Field(i)
		if !fv.CanAddr() {
			continue
		}
		if f.Anonymous && f.Type.Kind() == reflect.Struct && f.Tag.Get("db") != "-" {
			if col, ok := resolveColumnIn(fv, f.Type, target); ok {
				return col, true
			}
			continue
		}
		if fv.Addr().Pointer() == target {
			return columnName(f), true
		}
	}
	return "", false
}

func columnName(f reflect.StructField) string {
	if tag := f.Tag.Get("db"); tag != "" {
		return strings.Split(tag, ",")[0]
	}
	return toSnake(f.Name)
}

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
