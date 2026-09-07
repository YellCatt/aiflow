// Package out 定义 aiflow 与 shell 之间的统一输出契约。
// 约定：结构化 JSON 走 stdout，日志与进度走 stderr。
package out

import (
	"encoding/json"
	"io"
	"os"
)

// Writer 允许重定向输出目标，默认是 os.Stdout。
var Writer io.Writer = os.Stdout

// Envelope 是所有命令的统一返回外壳。
type Envelope struct {
	OK    bool        `json:"ok"`
	Data  interface{} `json:"data"`
	Error string      `json:"error,omitempty"`
}

// OK 输出成功结果，data 为任意可序列化负载。
func OK(data interface{}) {
	write(Envelope{OK: true, Data: data})
}

// Fail 输出失败结果。
func Fail(err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	write(Envelope{OK: false, Data: nil, Error: msg})
}

func write(e Envelope) {
	enc := json.NewEncoder(Writer)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(e)
}
