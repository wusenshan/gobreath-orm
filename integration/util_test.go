package integration

import "fmt"

// 真库返回的驱动原生类型在各方言间并不统一（PG 的 bigint 是 int64，
// SQLite 可能是 int64 也可能是 string），断言时统一成可比形态，
// 免得测试为了迁就驱动差异而写成一大坨类型 switch。
func toInt(v any) int {
	switch x := v.(type) {
	case int64:
		return int(x)
	case int:
		return x
	case float64:
		return int(x)
	case []byte:
		return atoiInt(string(x))
	case string:
		return atoiInt(x)
	default:
		panic(fmt.Sprintf("toInt: 不认识的类型 %T (%#v)", v, v))
	}
}

func toStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	default:
		return fmt.Sprintf("%v", x)
	}
}

func atoiInt(s string) int {
	n := 0
	neg := false
	for i, c := range s {
		if i == 0 && c == '-' {
			neg = true
			continue
		}
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	if neg {
		return -n
	}
	return n
}

// itoa64 用于拼少量取证用的 SQL（测试里不接受用户输入，故直接拼接）。
func itoa64(v int64) string { return fmt.Sprintf("%d", v) }
