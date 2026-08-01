package state

import (
	"bytes"
	"errors"
	"fmt"
)

// 错误类型约定（对应 REST API 状态码，见设计文档 14 节）：
//   - ErrRevisionConflict  → 409 revision 冲突
//   - *ValidationError     → 422 业务校验失败
//   - *SchemaError         → 500 control.json schema 不兼容
//   - 其他                 → 500 写盘/IO 等内部错误

// ErrRevisionConflict 表示配置版本冲突：调用方持有的 revision 已过期。
var ErrRevisionConflict = errors.New("配置版本冲突，请重新加载")

// ValidationError 是业务校验失败，Fields 携带字段路径 → 错误原因。
type ValidationError struct {
	Fields  map[string]string `json:"fields,omitempty"`
	Message string            `json:"message"`
}

func (e *ValidationError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "配置校验失败"
}

func (e *ValidationError) add(field, msg string) {
	if e.Fields == nil {
		e.Fields = map[string]string{}
	}
	if _, dup := e.Fields[field]; !dup {
		e.Fields[field] = msg
	}
}

// SchemaError 表示 control.json 的 schema 版本与程序不兼容。
type SchemaError struct {
	Version int
}

func (e *SchemaError) Error() string {
	return fmt.Sprintf("control.json schema 版本 %d 不受支持（当前 %d）", e.Version, SchemaVersion)
}

// newValidationError 构建带单条错误的校验错误。
func newValidationError(field, msg string) *ValidationError {
	return &ValidationError{Fields: map[string]string{field: msg}}
}

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
