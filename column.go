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
	for i := 0; i < rt.NumField(); i++ {
		f := tv.Field(i)
		if f.CanAddr() && f.Addr().Pointer() == target {
			return columnName(rt.Field(i))
		}
	}
	panic("orm: 无法从 picker 闭包解析出字段，请确认闭包返回的是该结构体的字段指针")
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
