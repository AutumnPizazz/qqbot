package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"qqbot/internal/onebot"
	"qqbot/internal/rules"
	"qqbot/internal/state"
)

// groupRequest 是群配置的写请求：revision + 完整群 DTO（规则/白名单一次提交）。
type groupRequest struct {
	Revision int64             `json:"revision"`
	Group    state.GroupConfig `json:"group"`
}

// groupResponse 返回群配置与提交后的新 revision。
type groupResponse struct {
	Group    state.GroupConfig `json:"group"`
	Revision int64             `json:"revision"`
}

// handleListGroups 群列表。
// GET /api/v1/groups
func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	cur := s.opts.Service.Current()
	groups := cur.Groups
	if groups == nil {
		groups = []state.GroupConfig{} // 保持 JSON 数组，避免前端 null 崩溃
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"groups":   groups,
		"revision": cur.Revision,
	})
}

// handleCreateGroup 新增群。
// POST /api/v1/groups {revision?, group: {...}}
func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	var req groupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorResponse(w, http.StatusBadRequest, CodeBadRequest, "请求体无效", nil)
		return
	}
	if req.Revision == 0 {
		req.Revision = s.opts.Service.Revision()
	}
	newGroup := req.Group
	// 新群注入默认规则（豁免 + 邀请审批），保证默认行为安全
	if len(newGroup.Rules) == 0 {
		var seq int
		newGroup.Rules = rules.InitialRules(&seq)
	}
	_, err := s.opts.Service.Update("admin", req.Revision, func(c *state.Control) error {
		for _, g := range c.Groups {
			if g.GroupID == newGroup.GroupID {
				return errDuplicateGroup
			}
		}
		c.Groups = append(c.Groups, newGroup)
		return nil
	}, "新增群 "+strconv.FormatInt(newGroup.GroupID, 10))
	switch {
	case errors.Is(err, errDuplicateGroup):
		errorResponse(w, http.StatusUnprocessableEntity, CodeValidationFailed, "群号已存在", nil)
	case err != nil:
		respondUpdate(w, err)
	default:
		writeJSON(w, http.StatusOK, groupResponse{Group: newGroup, Revision: s.opts.Service.Revision()})
	}
}

// findGroup 从 Control 中查找群（返回索引）。
func findGroup(c *state.Control, gid int64) (int, *state.GroupConfig) {
	for i := range c.Groups {
		if c.Groups[i].GroupID == gid {
			return i, &c.Groups[i]
		}
	}
	return -1, nil
}

// handleGetGroup 获取单个群。
// GET /api/v1/groups/{group_id}
func (s *Server) handleGetGroup(w http.ResponseWriter, r *http.Request) {
	gid, err := strconv.ParseInt(r.PathValue("group_id"), 10, 64)
	if err != nil || gid <= 0 {
		errorResponse(w, http.StatusBadRequest, CodeBadRequest, "group_id 无效", nil)
		return
	}
	cur := s.opts.Service.Current()
	if _, g := findGroup(cur, gid); g != nil {
		writeJSON(w, http.StatusOK, groupResponse{Group: *g, Revision: cur.Revision})
		return
	}
	errorResponse(w, http.StatusNotFound, CodeNotFound, "群不存在", nil)
}

// handlePutGroup 全量更新群配置。
// PUT /api/v1/groups/{group_id} {revision, group: {...}}
func (s *Server) handlePutGroup(w http.ResponseWriter, r *http.Request) {
	gid, err := strconv.ParseInt(r.PathValue("group_id"), 10, 64)
	if err != nil || gid <= 0 {
		errorResponse(w, http.StatusBadRequest, CodeBadRequest, "group_id 无效", nil)
		return
	}
	var req groupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorResponse(w, http.StatusBadRequest, CodeBadRequest, "请求体无效", nil)
		return
	}
	if req.Group.GroupID != gid {
		errorResponse(w, http.StatusBadRequest, CodeBadRequest, "路径 group_id 与请求体不一致", nil)
		return
	}
	newGroup := req.Group
	_, err = s.opts.Service.Update("admin", req.Revision, func(c *state.Control) error {
		idx, _ := findGroup(c, gid)
		if idx < 0 {
			return errNotFound
		}
		c.Groups[idx] = newGroup
		return nil
	}, "更新群 "+strconv.FormatInt(gid, 10))
	if errors.Is(err, errNotFound) {
		errorResponse(w, http.StatusNotFound, CodeNotFound, "群不存在", nil)
		return
	}
	if err != nil {
		respondUpdate(w, err)
		return
	}
	writeJSON(w, http.StatusOK, groupResponse{Group: newGroup, Revision: s.opts.Service.Revision()})
}

// errNotFound 是 mutate 内"资源不存在"信号。
var errNotFound = errors.New("群不存在")

// errDuplicateGroup 是 mutate 内"群号已存在"信号。
var errDuplicateGroup = errors.New("群号已存在")

// handleDeleteGroup 删除群。
// DELETE /api/v1/groups/{group_id}
func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	gid, err := strconv.ParseInt(r.PathValue("group_id"), 10, 64)
	if err != nil || gid <= 0 {
		errorResponse(w, http.StatusBadRequest, CodeBadRequest, "group_id 无效", nil)
		return
	}
	_, err = s.opts.Service.Update("admin", s.opts.Service.Revision(), func(c *state.Control) error {
		idx, _ := findGroup(c, gid)
		if idx < 0 {
			return errNotFound
		}
		c.Groups = append(c.Groups[:idx], c.Groups[idx+1:]...)
		return nil
	}, "删除群 "+strconv.FormatInt(gid, 10))
	if errors.Is(err, errNotFound) {
		errorResponse(w, http.StatusNotFound, CodeNotFound, "群不存在", nil)
		return
	}
	respondUpdate(w, err)
}

// handleResetGroup 重置群为默认配置（保留 group_id/remark，清除功能配置）。
// POST /api/v1/groups/{group_id}/reset
func (s *Server) handleResetGroup(w http.ResponseWriter, r *http.Request) {
	gid, err := strconv.ParseInt(r.PathValue("group_id"), 10, 64)
	if err != nil || gid <= 0 {
		errorResponse(w, http.StatusBadRequest, CodeBadRequest, "group_id 无效", nil)
		return
	}
	_, err = s.opts.Service.Update("admin", s.opts.Service.Revision(), func(c *state.Control) error {
		idx, _ := findGroup(c, gid)
		if idx < 0 {
			return errNotFound
		}
		// 重置为初始规则（默认豁免 + 邀请审批），保留群备注
		var seq int
		ng := state.GroupConfig{GroupID: gid, Enabled: false, Remark: c.Groups[idx].Remark}
		ng.Rules = rules.InitialRules(&seq)
		c.Groups[idx] = ng
		return nil
	}, "重置群 "+strconv.FormatInt(gid, 10))
	if errors.Is(err, errNotFound) {
		errorResponse(w, http.StatusNotFound, CodeNotFound, "群不存在", nil)
		return
	}
	respondUpdate(w, err)
}

// handleMembers 群成员列表（OneBot get_group_member_list）。
// GET /api/v1/groups/{group_id}/members
func (s *Server) handleMembers(w http.ResponseWriter, r *http.Request) {
	gid, err := strconv.ParseInt(r.PathValue("group_id"), 10, 64)
	if err != nil || gid <= 0 {
		errorResponse(w, http.StatusBadRequest, CodeBadRequest, "group_id 无效", nil)
		return
	}
	if s.manager() == nil {
		errorResponse(w, http.StatusServiceUnavailable, CodeOneBotDisconnected, "OneBot 未启动（配置未初始化）", nil)
		return
	}
	data, err := s.manager().Call("get_group_member_list", map[string]any{"group_id": gid})
	if err != nil {
		if errors.Is(err, onebot.ErrDisconnected) || errors.Is(err, onebot.ErrGenerationClosed) {
			errorResponse(w, http.StatusServiceUnavailable, CodeOneBotDisconnected, "OneBot 未连接", nil)
			return
		}
		errorResponse(w, http.StatusBadGateway, CodeOneBotFailed, cleanErr(err), nil)
		return
	}
	var members []json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		errorResponse(w, http.StatusBadGateway, CodeOneBotFailed, "成员数据解析失败", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": members})
}

// handleMessages 最近群消息（机器人内存环形缓冲）。
// GET /api/v1/groups/{group_id}/messages?limit=
func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	gid, err := strconv.ParseInt(r.PathValue("group_id"), 10, 64)
	if err != nil || gid <= 0 {
		errorResponse(w, http.StatusBadRequest, CodeBadRequest, "group_id 无效", nil)
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	if s.recentFn() == nil {
		writeJSON(w, http.StatusOK, map[string]any{"messages": []any{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": s.recentFn()(gid, limit)})
}
