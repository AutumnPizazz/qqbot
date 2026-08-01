package admin

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"qqbot/internal/mailer"
)

// handleSetup 首次初始化：校验 setup token → 设置管理员密码 → 建立会话。
// POST /api/v1/auth/setup {setup_token, password}
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if !s.auth.SetupRequired() {
		errorResponse(w, http.StatusForbidden, CodeForbidden, "管理员密码已设置，setup 不可重复执行", nil)
		return
	}
	var req struct {
		SetupToken string `json:"setup_token"`
		Password   string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorResponse(w, http.StatusBadRequest, CodeBadRequest, "请求体无效", nil)
		return
	}
	if !s.auth.VerifySetupToken(req.SetupToken) {
		s.loginRL.fail(s.clientIP(r))
		errorResponse(w, http.StatusForbidden, CodeForbidden, "setup token 无效或已消费", nil)
		return
	}
	if err := s.auth.SetPassword(req.Password); err != nil {
		errorResponse(w, http.StatusUnprocessableEntity, CodeValidationFailed, err.Error(), nil)
		return
	}
	if err := s.auth.ConsumeSetupToken(); err != nil {
		errorResponse(w, http.StatusInternalServerError, CodeInternal, "保存认证状态失败", nil)
		return
	}
	s.sessions.invalidateAll() // setup 后清理旧会话（理论为空）
	s.startSession(w, r)
}

// handleLogin 登录：密码校验 + 限流；两步验证开启时返回票据等待验证码。
// POST /api/v1/auth/login {password}
// 返回：{authenticated:true}（未开启两步）或 {step:"verify", ticket, email, resend_after}。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorResponse(w, http.StatusBadRequest, CodeBadRequest, "请求体无效", nil)
		return
	}
	ip := s.clientIP(r)
	if !s.loginRL.allow(ip) {
		errorResponse(w, http.StatusTooManyRequests, CodeRateLimited, "登录尝试过于频繁，请稍后再试", nil)
		return
	}
	if !s.auth.VerifyPassword(req.Password) {
		s.loginRL.fail(ip)
		errorResponse(w, http.StatusUnauthorized, CodeUnauthorized, "密码错误", nil)
		return
	}
	// 邮箱两步验证：密码正确 → 发验证码 → 等 /auth/verify
	if s.twoStepEnabled() {
		email := s.opts.Service.Effective().Email.To
		ticket, code, wait, err := s.mfa.create(ip, email)
		if err != nil {
			errorResponse(w, http.StatusTooManyRequests, CodeRateLimited, "验证码发送过于频繁，请稍后再试", nil)
			return
		}
		sender := s.emailSender()
		if sender == nil {
			errorResponse(w, http.StatusInternalServerError, CodeInternal, "邮箱发送组件不可用", nil)
			return
		}
		body := fmt.Sprintf("你的 QQBot 管理后台登录验证码是：%s\n\n验证码 %d 分钟内有效，如非本人操作请忽略。", code, int(mfaCodeTTL.Minutes()))
		if err := sender.Send(email, "【QQBot 管理后台】登录验证码", body); err != nil {
			s.mfa.delete(ticket) // 发信失败：销毁票据，避免无效重试
			slog.Warn("登录验证码发送失败", "err", err)
			errorResponse(w, http.StatusInternalServerError, CodeInternal, "验证码发送失败，请检查系统设置里的邮箱配置", nil)
			return
		}
		slog.Info("已发送登录验证码", "ip", ip, "email", email)
		writeJSON(w, http.StatusOK, map[string]any{
			"step":         "verify",
			"ticket":       ticket,
			"email":        maskEmail(email),
			"resend_after": int(wait.Seconds()),
		})
		return
	}
	s.startSession(w, r)
}

// handleVerify 验证码校验：成功后建立会话。
// POST /api/v1/auth/verify {ticket, code}
func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ticket string `json:"ticket"`
		Code   string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Ticket == "" || req.Code == "" {
		errorResponse(w, http.StatusBadRequest, CodeBadRequest, "请求体无效", nil)
		return
	}
	ip := s.clientIP(r)
	if !s.loginRL.allow(ip) {
		errorResponse(w, http.StatusTooManyRequests, CodeRateLimited, "尝试过于频繁，请稍后再试", nil)
		return
	}
	if _, ok := s.mfa.verify(req.Ticket, req.Code); !ok {
		s.loginRL.fail(ip)
		errorResponse(w, http.StatusUnauthorized, CodeUnauthorized, "验证码错误或已过期", nil)
		return
	}
	s.startSession(w, r)
}

// twoStepEnabled 两步验证是否开启且配置完整。
func (s *Server) twoStepEnabled() bool {
	eff := s.opts.Service.Effective()
	if eff == nil {
		return false
	}
	e := eff.Email
	return e.Enabled && e.SMTPHost != "" && e.SMTPPort > 0 && e.SMTPUser != "" && e.SMTPPassword != "" && e.To != ""
}

// emailSender 返回发信器：测试注入优先，否则按生效配置构建。
func (s *Server) emailSender() mailer.Sender {
	s.mailMu.Lock()
	defer s.mailMu.Unlock()
	if s.mailOverride != nil {
		return s.mailOverride
	}
	eff := s.opts.Service.Effective()
	if eff == nil {
		return nil
	}
	e := eff.Email
	if e.SMTPHost == "" || e.To == "" {
		return nil
	}
	return mailer.New(mailer.Config{
		Host: e.SMTPHost, Port: e.SMTPPort,
		User: e.SMTPUser, Password: e.SMTPPassword, From: e.SMTPUser,
	})
}

// maskEmail 脱敏展示收件邮箱：a***@example.com。
func maskEmail(email string) string {
	at := strings.IndexByte(email, '@')
	if at <= 1 {
		return "***@" + email[at+1:]
	}
	return email[:1] + "***" + email[at:]
}

// startSession 建立会话并下发 Cookie（HttpOnly/Secure/SameSite=Strict）。
func (s *Server) startSession(w http.ResponseWriter, r *http.Request) {
	sess := s.sessions.create()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sess.ID,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.opts.SecureCookies,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   7 * 24 * 3600,
	})
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true})
}

// handleLogout 退出登录。
// POST /api/v1/auth/logout
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		s.sessions.delete(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/",
		HttpOnly: true, Secure: s.opts.SecureCookies,
		SameSite: http.SameSiteStrictMode, MaxAge: -1,
	})
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
}

// handleSession 返回当前会话状态与 CSRF token（前端仅内存持有，不落 localStorage）。
// GET /api/v1/auth/session
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"authenticated":  false,
			"setup_required": s.auth.SetupRequired(),
		})
		return
	}
	sess, ok := s.sessions.get(cookie.Value)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{
			"authenticated":  false,
			"setup_required": s.auth.SetupRequired(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated":  true,
		"setup_required": false,
		"csrf_token":     sess.CSRF,
		"created_at":     sess.CreatedAt,
		"expires_at":     sess.ExpiresAt,
	})
}

// handlePassword 修改密码：校验当前密码，成功后使全部 session 失效。
// PUT /api/v1/auth/password {current_password, new_password}
func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorResponse(w, http.StatusBadRequest, CodeBadRequest, "请求体无效", nil)
		return
	}
	ok, err := s.auth.ChangePassword(req.CurrentPassword, req.NewPassword)
	if err != nil {
		errorResponse(w, http.StatusUnprocessableEntity, CodeValidationFailed, err.Error(), nil)
		return
	}
	if !ok {
		errorResponse(w, http.StatusForbidden, CodeForbidden, "当前密码错误", nil)
		return
	}
	n := s.sessions.invalidateAll()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sessions_invalidated": n})
}
