package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"qqbot/internal/bot"
	"qqbot/internal/onebot"
)

// handleAction 统一处理六类人工动作：
// 鉴权（已由中间件完成）→ 参数校验 → Idempotency-Key 检查 → ActionService → 审计。
//
// 幂等语义（设计文档 12 节）：
//   - 请求必须携带 Idempotency-Key 头；同一 (管理员, 动作, 参数摘要) 在 TTL 内
//     返回首次结果，阻止浏览器重复提交危险动作。
//   - OneBot 超时返回 unknown：QQ 端可能已执行成功，不能提示安全重试。
func (s *Server) handleAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		gid, err := strconv.ParseInt(r.PathValue("group_id"), 10, 64)
		if err != nil || gid <= 0 {
			errorResponse(w, http.StatusBadRequest, CodeBadRequest, "group_id 无效", nil)
			return
		}
		sess := sessionFromCtx(r)
		if sess == nil {
			errorResponse(w, http.StatusUnauthorized, CodeUnauthorized, "未登录", nil)
			return
		}
		if s.actionService() == nil {
			errorResponse(w, http.StatusServiceUnavailable, CodeOneBotDisconnected, "OneBot 未启动（配置未初始化）", nil)
			return
		}
		// Idempotency-Key 必填
		idemKey := r.Header.Get("Idempotency-Key")
		if idemKey == "" {
			errorResponse(w, http.StatusBadRequest, CodeBadRequest, "缺少 Idempotency-Key 请求头", nil)
			return
		}
		// 参数解析
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			errorResponse(w, http.StatusBadRequest, CodeBadRequest, "请求体无效", nil)
			return
		}
		paramHash := configHash(action + "|" + gidStr(gid) + "|" + string(mustMarshal(body)))
		cacheKey := "act:" + sess.ID + ":" + paramHash + ":" + idemKey
		if prev, ok := s.idem.get(cacheKey); ok {
			writeJSON(w, prev.status, prev.body)
			return
		}

		req := bot.ActionRequest{
			RequestID:      idemKey,
			ActorID:        0, // 网页管理员：固定 admin（无 QQ 号）
			ActorType:      "admin",
			Source:         "api",
			SourceIP:       s.clientIP(r),
			GroupID:        gid,
			ConfigRevision: s.opts.Service.Revision(),
		}
		parseIntField := func(name string, def int) int {
			if raw, ok := body[name]; ok {
				var v int
				if err := json.Unmarshal(raw, &v); err == nil {
					return v
				}
			}
			return def
		}
		parseStringField := func(name, def string) string {
			if raw, ok := body[name]; ok {
				var v string
				if err := json.Unmarshal(raw, &v); err == nil {
					return v
				}
			}
			return def
		}
		parseBoolField := func(name string, def bool) bool {
			if raw, ok := body[name]; ok {
				var v bool
				if err := json.Unmarshal(raw, &v); err == nil {
					return v
				}
			}
			return def
		}

		var res bot.ActionResult
		var actErr error
		switch action {
		case "mute":
			target := parseIntField("target_id", 0)
			req.TargetID = int64(target)
			res, actErr = s.actionService().Mute(req, parseIntField("minutes", 30))
		case "unmute":
			req.TargetID = int64(parseIntField("target_id", 0))
			res, actErr = s.actionService().Unmute(req)
		case "kick":
			req.TargetID = int64(parseIntField("target_id", 0))
			res, actErr = s.actionService().Kick(req, parseStringField("reason", "违反群规"))
		case "whole-ban":
			res, actErr = s.actionService().WholeBan(req, parseBoolField("enable", true))
		case "recall":
			res, actErr = s.actionService().Recall(req, int32(parseIntField("message_id", 0)))
		case "card":
			req.TargetID = int64(parseIntField("target_id", 0))
			res, actErr = s.actionService().Card(req, parseStringField("card", ""))
		default:
			errorResponse(w, http.StatusNotFound, CodeNotFound, "未知动作", nil)
			return
		}

		if actErr != nil {
			if errors.Is(actErr, onebot.ErrDisconnected) || errors.Is(actErr, onebot.ErrGenerationClosed) {
				errorResponse(w, http.StatusServiceUnavailable, CodeOneBotDisconnected, "OneBot 未连接，请稍后重试", nil)
				return
			}
			// 参数校验等业务错误
			errorResponse(w, http.StatusUnprocessableEntity, CodeValidationFailed, actErr.Error(), nil)
			return
		}

		status := http.StatusOK
		if res.Result == "failed" {
			status = http.StatusBadGateway // 502：OneBot 明确失败
		}
		payload := map[string]any{
			"action_result": res,
			"audit_result":  map[string]any{"status": "ok"},
		}
		s.idem.put(cacheKey, status, payload)
		writeJSON(w, status, payload)
	}
}

func gidStr(g int64) string { return strconv.FormatInt(g, 10) }

// mustMarshal 序列化（用于幂等摘要；失败返回空对象）。
func mustMarshal(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return data
}

// --- 审计 ---

// handleAudit 审计分页查询。
// GET /api/v1/audit?group_id=&source=&result=&cursor=&limit=
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if s.queryAuditFn() == nil {
		writeJSON(w, http.StatusOK, bot.AuditPage{})
		return
	}
	q := bot.AuditQuery{}
	q.GroupID = queryInt64(r, "group_id", 0)
	q.Source = r.URL.Query().Get("source")
	q.Result = r.URL.Query().Get("result")
	q.Cursor = queryInt64(r, "cursor", 0)
	q.Limit = int(queryInt64(r, "limit", 20))
	page := s.queryAuditFn()(q)
	writeJSON(w, http.StatusOK, page)
}

func queryInt64(r *http.Request, name string, def int64) int64 {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}
