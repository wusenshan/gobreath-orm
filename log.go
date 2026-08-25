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
func DefaultLogger(w io.Writer) LogFunc {
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
		msg := fmt.Sprintf("%s (  %8s) %s args=%v", tag, dur, query, args)
		if err != nil {
			msg += " err=" + err.Error()
		}
		fmt.Fprintf(w, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), msg)
	}
}
