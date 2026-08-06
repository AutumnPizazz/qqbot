package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"qqbot/internal/bot"
	"qqbot/internal/onebot"
	"qqbot/internal/state"
)

// fieldPaths 是加密字段的 AAD 路径（与 state 包约定一致）。
const (
	fieldOneBotToken = "system.onebot.access_token"
	napcatFieldToken = "system.napcat.webui_token"
	fieldEmailPass   = "system.email.smtp_password"
)

// --- GET /api/v1/status ---

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	cur := s.opts.Service.Current()
	status, lastErr, lastErrAt := onebot.StatusDisconnected, (error)(nil), time.Time{}
	if s.manager() != nil {
		status, lastErr, lastErrAt = s.manager().Status()
	}

	// 最近失败/unknown 动作（审计）
	page := bot.AuditPage{}
	if s.queryAuditFn() != nil {
		page = s.queryAuditFn()(bot.AuditQuery{Limit: 5})
	}
	recent := make([]map[string]any, 0, 5)
	for _, e := range page.Entries {
		if e.ActionStatus == "failed" || e.ActionStatus == "unknown" {
			recent = append(recent, map[string]any{
				"time": e.Time, "group_id": e.GroupID, "action": e.Action,
				"result": e.ActionStatus, "detail": e.Detail,
			})
		}
	}

	total, enabled := 0, 0
	for _, g := range cur.Groups {
		total++
		if g.Enabled {
			enabled++
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"setup_required":  s.auth.SetupRequired(),
		"initialized":     s.opts.Service.Initialized(),
		"config_revision": cur.Revision,
		"uptime_seconds":  int64(time.Since(s.started).Seconds()),
		"onebot": map[string]any{
			"status":        status,
			"last_error":    safeErrMsg(lastErr),
			"last_error_at": lastErrAt,
			"ws_url":        cur.System.OneBot.WSURL,
		},
		"napcat": map[string]any{
			"configured": cur.System.NapCat.WebUIURL != "" && cur.System.NapCat.WebUIToken.Configured(),
		},
		"groups":                 map[string]any{"total": total, "enabled": enabled},
		"recent_action_failures": recent,
	})
}

// safeErrMsg 清洗错误信息（不包含 URL/凭据）。
func safeErrMsg(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// --- GET /api/v1/settings ---

// settingsDTO 是 GET /settings 的响应（敏感字段只暴露 configured）。
type settingsDTO struct {
	Revision int64 `json:"revision"`
	System   struct {
		BotName  string `json:"bot_name"`
		Owner    int64  `json:"owner"`
		Timezone string `json:"timezone,omitempty"`
		OneBot   struct {
			WSURL        string `json:"ws_url"`
			APITimeoutMs int    `json:"api_timeout_ms,omitempty"`
			AccessToken  struct {
				Configured bool `json:"configured"`
			} `json:"access_token"`
		} `json:"onebot"`
		NapCat struct {
			WebUIURL   string `json:"webui_url,omitempty"`
			WebUIToken struct {
				Configured bool `json:"configured"`
			} `json:"webui_token"`
		} `json:"napcat"`
		Email struct {
			Enabled      bool   `json:"enabled"`
			SMTPHost     string `json:"smtp_host"`
			SMTPPort     int    `json:"smtp_port"`
			SMTPUser     string `json:"smtp_user"`
			SMTPPassword struct {
				Configured bool `json:"configured"`
			} `json:"smtp_password"`
			To string `json:"to"`
		} `json:"email"`
		Watchdog struct {
			Enabled         bool   `json:"enabled"`
			IntervalMinutes int    `json:"interval_minutes"`
			EmailTo         string `json:"email_to"`
		} `json:"watchdog"`
	} `json:"system"`
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	cur := s.opts.Service.Current()
	var dto settingsDTO
	dto.Revision = cur.Revision
	dto.System.BotName = cur.System.BotName
	dto.System.Owner = cur.System.Owner
	dto.System.Timezone = cur.System.Timezone
	dto.System.OneBot.WSURL = cur.System.OneBot.WSURL
	dto.System.OneBot.APITimeoutMs = cur.System.OneBot.APITimeoutMs
	dto.System.OneBot.AccessToken.Configured = cur.System.OneBot.AccessToken.Configured()
	dto.System.NapCat.WebUIURL = cur.System.NapCat.WebUIURL
	dto.System.NapCat.WebUIToken.Configured = cur.System.NapCat.WebUIToken.Configured()
	dto.System.Email.Enabled = cur.System.Email.Enabled
	dto.System.Email.SMTPHost = cur.System.Email.SMTPHost
	dto.System.Email.SMTPPort = cur.System.Email.SMTPPort
	dto.System.Email.SMTPUser = cur.System.Email.SMTPUser
	dto.System.Email.SMTPPassword.Configured = cur.System.Email.SMTPPassword.Configured()
	dto.System.Email.To = cur.System.Email.To
	dto.System.Watchdog.Enabled = cur.System.Watchdog.Enabled
	dto.System.Watchdog.IntervalMinutes = cur.System.Watchdog.IntervalMinutes
	dto.System.Watchdog.EmailTo = cur.System.Watchdog.EmailTo
	writeJSON(w, http.StatusOK, dto)
}

// tokenUpdate 是敏感字段的更新语义：缺失=保持；clear=true=清除。
// 字符串值通过 raw 分支处理。
type tokenUpdate struct {
	Clear bool `json:"clear,omitempty"`
}

// --- PUT /api/v1/settings ---

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Revision int64 `json:"revision"`
		System   struct {
			BotName  *string `json:"bot_name"`
			Owner    *int64  `json:"owner"`
			Timezone *string `json:"timezone"`
			OneBot   *struct {
				WSURL        *string         `json:"ws_url"`
				APITimeoutMs *int            `json:"api_timeout_ms"`
				AccessToken  json.RawMessage `json:"access_token"`
			} `json:"onebot"`
			NapCat *struct {
				WebUIURL   *string         `json:"webui_url"`
				WebUIToken json.RawMessage `json:"webui_token"`
			} `json:"napcat"`
			Email *struct {
				Enabled      *bool           `json:"enabled"`
				SMTPHost     *string         `json:"smtp_host"`
				SMTPPort     *int            `json:"smtp_port"`
				SMTPUser     *string         `json:"smtp_user"`
				SMTPPassword json.RawMessage `json:"smtp_password"`
				To           *string         `json:"to"`
			} `json:"email"`
			Watchdog *struct {
				Enabled         *bool   `json:"enabled"`
				IntervalMinutes *int    `json:"interval_minutes"`
				EmailTo         *string `json:"email_to"`
			} `json:"watchdog"`
		} `json:"system"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorResponse(w, http.StatusBadRequest, CodeBadRequest, "请求体无效", nil)
		return
	}
	_, err := s.opts.Service.Update("admin", req.Revision, func(c *state.Control) error {
		if req.System.BotName != nil {
			c.System.BotName = *req.System.BotName
		}
		if req.System.Owner != nil {
			c.System.Owner = *req.System.Owner
		}
		if req.System.Timezone != nil {
			c.System.Timezone = *req.System.Timezone
		}
		if ob := req.System.OneBot; ob != nil {
			if ob.WSURL != nil {
				c.System.OneBot.WSURL = *ob.WSURL
			}
			if ob.APITimeoutMs != nil {
				c.System.OneBot.APITimeoutMs = *ob.APITimeoutMs
			}
			if ob.AccessToken != nil {
				enc, err := resolveTokenUpdate(s.opts.Keys, ob.AccessToken, fieldOneBotToken)
				if err != nil {
					return err
				}
				c.System.OneBot.AccessToken = enc
			}
		}
		if nc := req.System.NapCat; nc != nil {
			if nc.WebUIURL != nil {
				c.System.NapCat.WebUIURL = *nc.WebUIURL
			}
			if nc.WebUIToken != nil {
				enc, err := resolveTokenUpdate(s.opts.Keys, nc.WebUIToken, napcatFieldToken)
				if err != nil {
					return err
				}
				c.System.NapCat.WebUIToken = enc
			}
		}
		if em := req.System.Email; em != nil {
			if em.Enabled != nil {
				c.System.Email.Enabled = *em.Enabled
			}
			if em.SMTPHost != nil {
				c.System.Email.SMTPHost = *em.SMTPHost
			}
			if em.SMTPPort != nil {
				c.System.Email.SMTPPort = *em.SMTPPort
			}
			if em.SMTPUser != nil {
				c.System.Email.SMTPUser = *em.SMTPUser
			}
			if em.SMTPPassword != nil {
				enc, err := resolveTokenUpdate(s.opts.Keys, em.SMTPPassword, fieldEmailPass)
				if err != nil {
					return err
				}
				c.System.Email.SMTPPassword = enc
			}
			if em.To != nil {
				c.System.Email.To = *em.To
			}
		}
		if wd := req.System.Watchdog; wd != nil {
			if wd.Enabled != nil {
				c.System.Watchdog.Enabled = *wd.Enabled
			}
			if wd.IntervalMinutes != nil {
				c.System.Watchdog.IntervalMinutes = *wd.IntervalMinutes
			}
			if wd.EmailTo != nil {
				c.System.Watchdog.EmailTo = *wd.EmailTo
			}
		}
		return nil
	}, "更新系统设置")
	respondUpdate(w, err)
}

// resolveTokenUpdate 解析敏感字段更新：
//   - 字符串 → 加密替换
//   - {"clear": true} → nil（清除）
//   - 其他 → 错误
func resolveTokenUpdate(keys *state.MasterKey, raw json.RawMessage, fieldPath string) (*state.EncryptedValue, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return keys.Encrypt(s, fieldPath)
	}
	var tu tokenUpdate
	if err := json.Unmarshal(raw, &tu); err == nil && tu.Clear {
		return nil, nil
	}
	return nil, fmt.Errorf("字段 %s 格式无效（须为字符串或 {\"clear\":true}）", fieldPath)
}

// respondUpdate 统一处理 ConfigService.Update 的返回。
func respondUpdate(w http.ResponseWriter, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	switch {
	case errors.Is(err, state.ErrRevisionConflict):
		errorResponse(w, http.StatusConflict, CodeRevisionConflict, err.Error(), nil)
	default:
		var ve *state.ValidationError
		if errors.As(err, &ve) {
			errorResponse(w, http.StatusUnprocessableEntity, CodeValidationFailed, ve.Message, ve.Fields)
			return
		}
		errorResponse(w, http.StatusInternalServerError, CodeInternal, err.Error(), nil)
	}
}

// --- 连接测试 ---

// handleTestOneBot 测试 OneBot 连接（get_login_info：NapCat 通用，返回登录 QQ 信息）。
// POST /api/v1/settings/test-onebot
func (s *Server) handleTestOneBot(w http.ResponseWriter, r *http.Request) {
	cur := s.opts.Service.Current()
	if cur.System.OneBot.WSURL == "" {
		errorResponse(w, http.StatusUnprocessableEntity, CodeValidationFailed, "请先配置 OneBot 地址", nil)
		return
	}
	if s.manager() == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "detail": "OneBot 尚未启动（配置未初始化）"})
		return
	}
	data, err := s.manager().Call("get_login_info", nil)
	if err != nil {
		if errors.Is(err, onebot.ErrDisconnected) || errors.Is(err, onebot.ErrGenerationClosed) {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "detail": "OneBot 未连接（连接中或断线）"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "detail": cleanErr(err)})
		return
	}
	var info struct {
		UserID   int64  `json:"user_id"`
		Nickname string `json:"nickname"`
	}
	if err := json.Unmarshal(data, &info); err != nil || info.UserID == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "detail": "响应解析失败：" + string(data)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "detail": fmt.Sprintf("已连接，登录账号 %s (%d)", info.Nickname, info.UserID),
	})
}

// handleTestNapCat 测试 NapCat WebUI 连通性。
// POST /api/v1/settings/test-napcat
func (s *Server) handleTestNapCat(w http.ResponseWriter, r *http.Request) {
	client, err := s.napcatClient()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "detail": cleanErr(err)})
		return
	}
	st, err := client.CheckLogin(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "detail": cleanErr(err)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "is_login": st.IsLogin,
		"detail": map[bool]string{true: "已登录", false: "未登录（二维码可获取）"}[st.IsLogin],
	})
}

// handleTestEmail 发送测试邮件验证 SMTP 配置（使用当前生效配置）。
// POST /api/v1/settings/test-email
func (s *Server) handleTestEmail(w http.ResponseWriter, r *http.Request) {
	eff := s.opts.Service.Effective()
	if eff == nil || eff.Email.SMTPHost == "" || eff.Email.To == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "detail": "邮箱配置不完整（需 SMTP 服务器/账号/收件邮箱）"})
		return
	}
	sender := s.emailSender()
	if sender == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "detail": "邮箱发送组件不可用"})
		return
	}
	err := sender.Send(eff.Email.To, "【QQBot 管理后台】测试邮件",
		"这是一封测试邮件，SMTP 配置正常。")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "detail": cleanErr(err)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "detail": "测试邮件已发送至 " + eff.Email.To})
}

// handleTestWatchdog 发送一封掉线提醒测试邮件（验证监控发信通路）。
// POST /api/v1/settings/test-watchdog
func (s *Server) handleTestWatchdog(w http.ResponseWriter, r *http.Request) {
	eff := s.opts.Service.Effective()
	if eff == nil || eff.Email.SMTPHost == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "detail": "SMTP 未配置完整（需 SMTP 服务器/账号）"})
		return
	}
	to := eff.Watchdog.EmailTo
	if to == "" {
		to = eff.Email.To
	}
	if to == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "detail": "无提醒收件人（watchdog.email_to 与系统邮箱收件人均为空）"})
		return
	}
	sender := s.emailSender()
	if sender == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "detail": "邮箱发送组件不可用"})
		return
	}
	err := sender.Send(to, "【QQBot】NapCat 掉线监控测试",
		"这是一封掉线监控测试邮件，发信通路正常。")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "detail": cleanErr(err)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "detail": "测试提醒已发送至 " + to})
}

// cleanErr 清洗错误信息（去除 URL/凭据细节）。
func cleanErr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// --- 配置历史 ---

// handleHistory 配置历史列表（不含快照内容，避免大响应）。
// GET /api/v1/config/history
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	cur := s.opts.Service.Current()
	items := make([]map[string]any, 0, len(cur.History))
	for _, h := range cur.History {
		items = append(items, map[string]any{
			"revision": h.Revision, "time": h.Time, "actor": h.Actor,
			"summary": h.Summary, "hash": h.Hash,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": items})
}

// handleRestore 恢复到指定历史 revision。
// POST /api/v1/config/history/{revision}/restore
func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	rev, err := strconv.ParseInt(r.PathValue("revision"), 10, 64)
	if err != nil || rev <= 0 {
		errorResponse(w, http.StatusBadRequest, CodeBadRequest, "revision 无效", nil)
		return
	}
	_, err = s.opts.Service.Restore("admin", rev)
	if err != nil {
		errorResponse(w, http.StatusNotFound, CodeNotFound, err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restored_revision": rev})
}
