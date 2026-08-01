package admin

import (
	"encoding/json"
	"net/http"
)

// 统一错误格式（设计文档 8 节）：
//
//	{"error": {"code": "validation_failed", "message": "...", "fields": {...}}}
//
// HTTP 状态约定（文档 14 节）：
//   - 400 请求结构错误；401 未登录/会话过期；403 CSRF/来源错误
//   - 404 资源不存在；409 revision 或幂等冲突；422 业务校验失败
//   - 502 NapCat/OneBot 返回异常；503 setup 未完成/OneBot 断线/组件不可用
type ErrorCode string

const (
	CodeBadRequest          ErrorCode = "bad_request"
	CodeValidationFailed    ErrorCode = "validation_failed"
	CodeRevisionConflict    ErrorCode = "revision_conflict"
	CodeUnauthorized        ErrorCode = "unauthorized"
	CodeForbidden           ErrorCode = "forbidden"
	CodeNotFound            ErrorCode = "not_found"
	CodeSetupRequired       ErrorCode = "setup_required"
	CodeOneBotDisconnected  ErrorCode = "onebot_disconnected"
	CodeOneBotFailed        ErrorCode = "onebot_failed"
	CodeNapCatUnavailable   ErrorCode = "napcat_unavailable"
	CodeRateLimited         ErrorCode = "rate_limited"
	CodeInternal            ErrorCode = "internal"
	CodeIdempotencyConflict ErrorCode = "idempotency_conflict"
)

// apiError 是统一错误响应体。
type apiError struct {
	Code    ErrorCode         `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
	Status  int               `json:"-"`
}

func (e *apiError) Error() string { return e.Message }

// errorResponse 写出统一错误。
func errorResponse(w http.ResponseWriter, status int, code ErrorCode, msg string, fields map[string]string) {
	writeJSON(w, status, map[string]any{
		"error": apiError{Code: code, Message: msg, Fields: fields, Status: status},
	})
}

// writeJSON 写出 JSON 响应（含安全响应头）。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
