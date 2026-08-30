package orm

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// VectorMetric 向量距离度量，决定 ORDER BY / 阈值过滤使用的运算符（PG）或函数（MySQL）。
//
// 零值 = L2（欧几里得），即 Nearest/WithinDistance 不指定度量时的默认行为，
// 与旧版本 `<->` 语法保持一致。文本语义相似度检索（如 RAG）首选 Cosine。
type VectorMetric int

const (
	// L2 欧几里得距离。PG `<->`；MySQL 'EUCLIDEAN'。
	L2 VectorMetric = iota
	// Cosine 余弦距离（文本嵌入相似度首选）。PG `<=>`；MySQL 'COSINE'。
	Cosine
	// InnerProduct 内积。PG `<#>`（返回负内积，ASC 即最相似）；MySQL 'DOT'。
	InnerProduct
	// L1 曼哈顿距离。PG `<+>`；MySQL 需 9.7+ 的 'MANHATTAN'，低版本会报函数不存在。
	L1
)

// serializeVector 把向量参数统一序列化为文本 `[1,2,3]`，供 PG（pgvector 文本格式）与
// MySQL（STRING_TO_VECTOR 入参）以同一形式参数化绑定，避免依赖 pgvector-go 等第三方包。
// 支持：string（原样透传，如已格式化的文本或 pgvector.Vector.String() 结果）、
// []float32、[]float64、定长数组、以及实现了 fmt.Stringer 的类型（如 pgvector.Vector）。
// 无法识别的类型原样返回（交由驱动处理）。
func serializeVector(vec any) any {
	switch v := vec.(type) {
	case string:
		return v
	case []float32:
		return floats32ToText(v)
	case []float64:
		return floats64ToText(v)
	}
	rv := reflect.ValueOf(vec)
	if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
		n := rv.Len()
		if n == 0 {
			return "[]"
		}
		parts := make([]string, n)
		for i := 0; i < n; i++ {
			parts[i] = formatFloatVal(rv.Index(i))
		}
		return "[" + strings.Join(parts, ",") + "]"
	}
	if s, ok := vec.(fmt.Stringer); ok {
		return s.String()
	}
	return vec
}

func floats32ToText(v []float32) string {
	parts := make([]string, len(v))
	for i, x := range v {
		parts[i] = strconv.FormatFloat(float64(x), 'f', -1, 32)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func floats64ToText(v []float64) string {
	parts := make([]string, len(v))
	for i, x := range v {
		parts[i] = strconv.FormatFloat(x, 'f', -1, 64)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// formatFloatVal 把单个数值（任意整/浮点种类）格式化为最短十进制文本，用于向量序列化。
func formatFloatVal(rv reflect.Value) string {
	switch rv.Kind() {
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(rv.Float(), 'f', -1, 64)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(rv.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(rv.Uint(), 10)
	default:
		return fmt.Sprintf("%v", rv.Interface())
	}
}
