package admin

// notify 通知端点：AI agent / 外部系统通过 HTTPS 调用，复用已建立的
// OneBot 连接向指定 QQ（私聊）或群（群聊）发送消息。
//
//   POST /api/v1/notify
//   Authorization: Bearer <QQBOT_NOTIFY_TOKEN>
//   {"to": 2170191481, "text": "..."}          // 私聊
//   {"group_id": 675179266, "text": "..."}     // 群聊
//
// 鉴权：独立静态 token（环境变量 QQBOT_NOTIFY_TOKEN），不依赖浏览器 session
// （AI agent 无需登录）。未配置 token 时端点返回 503（功能未启用）。
// 安全说明：token 应足够长（建议 openssl rand -hex 32），仅配置在服务端
// 与可信调用方，勿入库、勿进前端。

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"

	"qqbot/internal/onebot"
)

const (
	notifyEnvToken  = "QQBOT_NOTIFY_TOKEN"
	notifyMaxRunes  = 5000 // 单条消息最大字符数（与群聊风控经验值对齐）
	notifyMaxBody   = 1 << 20
	notifyTokenHint = "notify 未启用：请配置环境变量 QQBOT_NOTIFY_TOKEN（openssl rand -hex 32 生成）"
)

// handleNotify 处理通知消息发送请求。
func (s *Server) handleNotify(w http.ResponseWriter, r *http.Request) {
	token := os.Getenv(notifyEnvToken)
	if token == "" {
		slog.Warn("notify 端点被调用但未配置 token", "ip", s.clientIP(r))
		errorResponse(w, http.StatusServiceUnavailable, CodeInternal, notifyTokenHint, nil)
		return
	}
	// Bearer token 校验（常量时间比较，防时序侧信道）
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if auth == "" || subtle.ConstantTimeCompare([]byte(auth), []byte(token)) != 1 {
		errorResponse(w, http.StatusUnauthorized, CodeUnauthorized, "notify token 无效", nil)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, notifyMaxBody))
	if err != nil {
		errorResponse(w, http.StatusBadRequest, CodeBadRequest, "读取请求体失败", nil)
		return
	}
	var req struct {
		To      int64  `json:"to"`
		GroupID int64  `json:"group_id"`
		Text    string `json:"text"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		errorResponse(w, http.StatusBadRequest, CodeBadRequest, "请求体必须是 JSON", nil)
		return
	}
	if req.To == 0 && req.GroupID == 0 {
		errorResponse(w, http.StatusUnprocessableEntity, CodeValidationFailed, "to 与 group_id 必须提供其一", nil)
		return
	}
	if req.To != 0 && req.GroupID != 0 {
		errorResponse(w, http.StatusUnprocessableEntity, CodeValidationFailed, "to 与 group_id 只能提供其一", nil)
		return
	}
	if req.Text == "" {
		errorResponse(w, http.StatusUnprocessableEntity, CodeValidationFailed, "text 不能为空", nil)
		return
	}
	if len([]rune(req.Text)) > notifyMaxRunes {
		errorResponse(w, http.StatusUnprocessableEntity, CodeValidationFailed,
			"text 过长（最多 "+strconv.Itoa(notifyMaxRunes)+" 字符）", nil)
		return
	}

	mgr := s.manager()
	if mgr == nil {
		errorResponse(w, http.StatusServiceUnavailable, CodeOneBotDisconnected, "OneBot 尚未启动", nil)
		return
	}

	var action string
	var params map[string]any
	if req.To != 0 {
		action = "send_private_msg"
		params = map[string]any{"user_id": req.To, "message": req.Text}
	} else {
		action = "send_group_msg"
		params = map[string]any{"group_id": req.GroupID, "message": req.Text}
	}
	// 用 Call 同步等待 OneBot echo 回包，确认真实发送结果（含 message_id）
	data, err := mgr.Call(action, params)
	if err != nil {
		switch {
		case errors.Is(err, onebot.ErrDisconnected) || errors.Is(err, onebot.ErrGenerationClosed):
			errorResponse(w, http.StatusServiceUnavailable, CodeOneBotDisconnected, "OneBot 未连接（机器人离线）", nil)
		case strings.Contains(err.Error(), "超时"):
			errorResponse(w, http.StatusBadGateway, CodeOneBotFailed, "发送超时（QQ 端可能已收到）", nil)
		default:
			errorResponse(w, http.StatusBadGateway, CodeOneBotFailed, "发送失败: "+cleanErr(err), nil)
		}
		return
	}
	var resp struct {
		MessageID int64 `json:"message_id"`
	}
	_ = json.Unmarshal(data, &resp)
	slog.Info("notify 发送成功", "action", action, "target", req.To+req.GroupID, "message_id", resp.MessageID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "action": action, "message_id": resp.MessageID})
}
