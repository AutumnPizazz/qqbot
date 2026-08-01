package admin

import (
	"net/http"
	"strconv"

	"qqbot/internal/rules"
)

// handleRuleMeta 规则引擎注册表元数据（前端动态表单渲染）。
// GET /api/v1/rule-meta
func (s *Server) handleRuleMeta(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, rules.RuleMeta())
}

// handleGroupCounters 指定群的计数器实时状态（排障面板）。
// GET /api/v1/groups/{group_id}/counters
func (s *Server) handleGroupCounters(w http.ResponseWriter, r *http.Request) {
	gid, err := strconv.ParseInt(r.PathValue("group_id"), 10, 64)
	if err != nil || gid <= 0 {
		errorResponse(w, http.StatusBadRequest, CodeBadRequest, "group_id 无效", nil)
		return
	}
	if _, g := findGroup(s.opts.Service.Current(), gid); g == nil {
		errorResponse(w, http.StatusNotFound, CodeNotFound, "群不存在", nil)
		return
	}
	s.compsMu.RLock()
	counters := s.counters
	s.compsMu.RUnlock()
	out := map[string]map[int64]int{}
	if counters != nil {
		out = counters(gid)
	}
	writeJSON(w, http.StatusOK, map[string]any{"counters": out})
}
