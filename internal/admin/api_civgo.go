package admin

import (
	"encoding/json"
	"io"
	"net/http"
)

// CivgoAdmin civgo 配置管理接口（main 注入实现；nil = 未接入，前端隐藏）。
// 保持 admin 不依赖 civgo 包：配置读写语义全部由实现方负责。
type CivgoAdmin interface {
	// Get 返回脱敏配置 DTO 与是否已配置。
	Get() (map[string]any, bool)
	// Save 校验并保存配置；返回是否立即生效（false = 需重启进程）。
	Save(raw json.RawMessage) (effective bool, err error)
	// Status 返回运行状态。
	Status() map[string]any
	// TestAI 测试 AI 网关（function calling 自检）。
	TestAI() error
}

// handleGetCivgo civgo 配置与状态（脱敏）。
// GET /api/v1/civgo
func (s *Server) handleGetCivgo(w http.ResponseWriter, r *http.Request) {
	if s.opts.Civgo == nil {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false, "enabled": false, "status": map[string]any{}})
		return
	}
	cfg, ok := s.opts.Civgo.Get()
	status := s.opts.Civgo.Status()
	enabled := false
	if v, has := cfg["enabled"].(bool); has {
		enabled = v
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"configured": ok,
		"enabled":    enabled,
		"config":     cfg,
		"status":     status,
	})
}

// handlePutCivgo 保存 civgo 配置（校验失败 422；写盘失败 500）。
// PUT /api/v1/civgo
func (s *Server) handlePutCivgo(w http.ResponseWriter, r *http.Request) {
	if s.opts.Civgo == nil {
		errorResponse(w, http.StatusUnprocessableEntity, CodeValidationFailed, "civgo 管理未接入", nil)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		errorResponse(w, http.StatusBadRequest, CodeBadRequest, "请求体无效", nil)
		return
	}
	effective, err := s.opts.Civgo.Save(raw)
	if err != nil {
		errorResponse(w, http.StatusUnprocessableEntity, CodeValidationFailed, err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "effective": effective})
}

// handleTestCivgoAI 测试 AI 网关连接（function calling 自检）。
// POST /api/v1/civgo/test-ai
func (s *Server) handleTestCivgoAI(w http.ResponseWriter, r *http.Request) {
	if s.opts.Civgo == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "detail": "civgo 管理未接入"})
		return
	}
	if err := s.opts.Civgo.TestAI(); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "detail": cleanErr(err)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "detail": "AI 网关可用（function calling 支持）"})
}
