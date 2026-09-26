package orm

import (
	"fmt"
	"io"
	"os"
	"time"
)

// LogLevel 控制 SQL 日志的输出等级（阈值语义：只输出 >= 设定级别的事件）。
// 事件严重度：普通执行成功 = Info，慢查询 = Warn，执行出错 = Error。
type LogLevel int8

const (
	Silent LogLevel = iota // 不输出任何 SQL 日志（默认）
	Info                   // 输出全部 SQL
	Warn                   // 仅输出慢查询与错误
	Error                  // 仅输出执行错误
)

// LogFunc 是 SQL 日志的回调。框架在执行完每条 SQL 后调用它，
// 你可以把它接到 zap / logrus / slog 等任意日志库。
//   - level：本次事件等级
//   - query：最终 SQL（占位符已是方言形式，如 $1 / ?）
//   - args：绑定参数
//   - dur：执行耗时
//   - err：执行错误（成功为 nil）
type LogFunc func(level LogLevel, query string, args []any, dur time.Duration, err error)

// DefaultLogger 返回一个开箱即用的 LogFunc，把日志写到 w（为 nil 时写 os.Stderr）。
// 输出格式示例：
//
//	2026-08-24 17:20:00 INFO  (   1.2ms) SELECT * FROM users WHERE id = $1 args=[1]
//	2026-08-24 17:20:01 ERROR (   3.4ms) SELECT * FROM x: err=ERROR: relation "x" does not exist
//
// ⚠️ 安全提示：为便于本地排查，它会**原样打印绑定参数**（args=%v）。若 SQL 会带上
// 密码、Token、手机号、身份证号等敏感值（登录 / 支付 / 实名类写入很常见），这些值
// 会以明文落进日志文件。生产环境请改用 DefaultLoggerMasked 传入脱敏函数，或直接
// 自定义 LogFunc 接到你们的日志库。
func DefaultLogger(w io.Writer) LogFunc { return defaultLogger(w, nil) }

// MaskArgsFunc 是绑定参数的脱敏钩子，配合 DefaultLoggerMasked 使用。
// query 为本次执行的 SQL，返回一份用于打印的参数副本。
type MaskArgsFunc func(query string, args []any) []any

// DefaultLoggerMasked 与 DefaultLogger 行为一致，区别是打印前先经 mask 处理一份参数
// **副本**：脱敏只影响日志文本，真实执行用的参数不受任何影响。mask 为 nil 时等同
// DefaultLogger。
//
// 示例（长度超过 4 的字符串只留首尾两字符）：
//
//	logger := orm.DefaultLoggerMasked(os.Stdout, func(_ string, args []any) []any {
//		out := make([]any, len(args))
//		for i, a := range args {
//			if s, ok := a.(string); ok && len(s) > 4 {
//				out[i] = s[:2] + "***" + s[len(s)-2:]
//				continue
//			}
//			out[i] = a
//		}
//		return out
//	})
func DefaultLoggerMasked(w io.Writer, mask MaskArgsFunc) LogFunc { return defaultLogger(w, mask) }

func defaultLogger(w io.Writer, mask MaskArgsFunc) LogFunc {
	if w == nil {
		w = os.Stderr
	}
	return func(level LogLevel, query string, args []any, dur time.Duration, err error) {
		var tag string
		switch level {
		case Warn:
			tag = "WARN "
		case Error:
			tag = "ERROR"
		default:
			tag = "INFO "
		}
		logArgs := args
		if mask != nil {
			logArgs = mask(query, args)
		}
		msg := fmt.Sprintf("%s (  %8s) %s args=%v", tag, dur, query, logArgs)
		if err != nil {
			msg += " err=" + err.Error()
		}
		fmt.Fprintf(w, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), msg)
	}
}
