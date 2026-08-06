// Package admin 实现网页管理后台：鉴权（setup/Argon2id/session/CSRF/限流）、
// REST API 与嵌入式前端（阶段 4）。
//
// 架构约束（设计文档 5 节）：
//   - admin 只负责编排与 HTTP 语义，不直接修改状态文件，不直接调用底层 WebSocket；
//   - 配置修改只经 ConfigService.Update；
//   - 人工动作只经 ActionService；
//   - HTTP 层不持有/返回解密后的长期 secret（GET 只返回 configured 布尔值）。
package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"qqbot/internal/bot"
	"qqbot/internal/mailer"
	"qqbot/internal/napcat"
	"qqbot/internal/onebot"
	"qqbot/internal/state"
	"qqbot/internal/watchdog"
)

// Options 是 Server 的依赖注入。
type Options struct {
	DataDir        string
	Service        *state.ConfigService
	Keys           *state.MasterKey
	Manager        *onebot.Manager
	Actions        *bot.ActionService
	QueryAudit     func(bot.AuditQuery) bot.AuditPage
	RecentMessages func(groupID int64, limit int) []bot.MessageRecord
	Counters       func(groupID int64) map[string]map[int64]int // 规则计数器状态（可 nil）
	EmailSender    mailer.Sender                                // 邮箱验证码发送器（nil=按生效配置构建；测试注入）
	SecureCookies  bool                                         // Cookie Secure 标志（默认 HTTPS/反代场景 true）
	TrustedProxies []string                                     // 可信反向代理 IP（仅这些来源允许提供 X-Forwarded-For）
}

// Server 是管理后台 HTTP 服务。
// 业务组件（Manager/Actions/审计/消息源）可在运行中注入：
// 初始化模式下启动时为空，配置首次提交成功后由 main 注入（无需重启）。
type Server struct {
	opts     Options
	auth     *authStore
	sessions *sessionStore
	loginRL  *rateLimiter
	started  time.Time
	idem     *idemStore

	compsMu        sync.RWMutex
	mgr            *onebot.Manager
	actions        *bot.ActionService
	queryAudit     func(bot.AuditQuery) bot.AuditPage
	recent         func(int64, int) []bot.MessageRecord
	counters       func(int64) map[string]map[int64]int // 规则计数器状态（可 nil）
	trustedProxies []string                             // 可信反向代理 IP（QQBOT_TRUST_PROXY）

	napMu     sync.Mutex
	napClient *napcat.Client // 懒构建；配置变更后重建

	mfa          *mfaStore // 邮箱两步验证票据（纯内存）
	mailMu       sync.Mutex
	mailOverride mailer.Sender // 测试注入的发信器（nil=按配置构建）
}

// SetComponents 注入业务组件（初始化完成后调用，幂等）。
func (s *Server) SetComponents(mgr *onebot.Manager, actions *bot.ActionService,
	queryAudit func(bot.AuditQuery) bot.AuditPage, recent func(int64, int) []bot.MessageRecord,
	counters func(int64) map[string]map[int64]int) {
	s.compsMu.Lock()
	defer s.compsMu.Unlock()
	if mgr != nil {
		s.mgr = mgr
	}
	if actions != nil {
		s.actions = actions
	}
	if queryAudit != nil {
		s.queryAudit = queryAudit
	}
	if recent != nil {
		s.recent = recent
	}
	if counters != nil {
		s.counters = counters
	}
}

func (s *Server) manager() *onebot.Manager {
	s.compsMu.RLock()
	defer s.compsMu.RUnlock()
	return s.mgr
}

func (s *Server) actionService() *bot.ActionService {
	s.compsMu.RLock()
	defer s.compsMu.RUnlock()
	return s.actions
}

func (s *Server) queryAuditFn() func(bot.AuditQuery) bot.AuditPage {
	s.compsMu.RLock()
	defer s.compsMu.RUnlock()
	return s.queryAudit
}

func (s *Server) recentFn() func(int64, int) []bot.MessageRecord {
	s.compsMu.RLock()
	defer s.compsMu.RUnlock()
	return s.recent
}

// New 创建管理后台。
func New(opts Options) (*Server, error) {
	auth, err := openAuthStore(opts.DataDir)
	if err != nil {
		return nil, err
	}
	s := &Server{
		opts:           opts,
		auth:           auth,
		sessions:       newSessionStore(),
		loginRL:        newRateLimiter(15*time.Minute, 10, 100),
		started:        time.Now(),
		idem:           newIdemStore(10*time.Minute, 1000),
		mgr:            opts.Manager,
		actions:        opts.Actions,
		queryAudit:     opts.QueryAudit,
		recent:         opts.RecentMessages,
		counters:       opts.Counters,
		trustedProxies: opts.TrustedProxies,
		mfa:            newMFAStore(),
		mailOverride:   opts.EmailSender,
	}
	// 配置变更时：NapCat 客户端重建（token/URL 可能变化）
	opts.Service.Subscribe(func() {
		s.napMu.Lock()
		s.napClient = nil
		s.napMu.Unlock()
	})
	return s, nil
}

// SetupRequired 判断是否处于待初始化状态（未设置管理员密码）。
func (s *Server) SetupRequired() bool { return s.auth.SetupRequired() }

// EnsureSetupToken 生成并返回一次性 setup token（明文只输出到日志一次）。
// 已存在未消费 token 时返回错误（重启不会重新生成）。
func (s *Server) EnsureSetupToken() (string, error) { return s.auth.EnsureSetupToken() }

// Handler 返回完整的 HTTP 路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})

	// 认证（无需登录）
	mux.HandleFunc("POST /api/v1/auth/setup", s.handleSetup)
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/verify", s.handleVerify)
	mux.HandleFunc("POST /api/v1/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/v1/auth/session", s.handleSession)

	// 业务 API（需要登录 + CSRF + 初始化门控）
	api := s.apiRoutes()
	mux.Handle("/api/v1/", s.withSession(s.gateSetup(s.withCSRF(api))))

	// 前端（阶段 4 嵌入；当前返回占位页）
	mux.HandleFunc("/", s.handleIndex)

	return s.securityHeaders(mux)
}

// apiRoutes 业务路由（登录后可用）。
func (s *Server) apiRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/v1/auth/password", s.handlePassword)

	mux.HandleFunc("GET /api/v1/status", s.handleStatus)

	mux.HandleFunc("GET /api/v1/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/v1/settings", s.handlePutSettings)
	mux.HandleFunc("POST /api/v1/settings/test-onebot", s.handleTestOneBot)
	mux.HandleFunc("POST /api/v1/settings/test-napcat", s.handleTestNapCat)
	mux.HandleFunc("POST /api/v1/settings/test-email", s.handleTestEmail)
	mux.HandleFunc("POST /api/v1/settings/test-watchdog", s.handleTestWatchdog)

	mux.HandleFunc("GET /api/v1/config/history", s.handleHistory)
	mux.HandleFunc("POST /api/v1/config/history/{revision}/restore", s.handleRestore)

	mux.HandleFunc("GET /api/v1/groups", s.handleListGroups)
	mux.HandleFunc("POST /api/v1/groups", s.handleCreateGroup)
	mux.HandleFunc("GET /api/v1/groups/{group_id}", s.handleGetGroup)
	mux.HandleFunc("PUT /api/v1/groups/{group_id}", s.handlePutGroup)
	mux.HandleFunc("DELETE /api/v1/groups/{group_id}", s.handleDeleteGroup)
	mux.HandleFunc("POST /api/v1/groups/{group_id}/reset", s.handleResetGroup)
	mux.HandleFunc("GET /api/v1/groups/{group_id}/counters", s.handleGroupCounters)
	mux.HandleFunc("GET /api/v1/rule-meta", s.handleRuleMeta)

	mux.HandleFunc("GET /api/v1/groups/{group_id}/members", s.handleMembers)
	mux.HandleFunc("GET /api/v1/groups/{group_id}/messages", s.handleMessages)

	mux.HandleFunc("POST /api/v1/groups/{group_id}/actions/mute", s.handleAction("mute"))
	mux.HandleFunc("POST /api/v1/groups/{group_id}/actions/unmute", s.handleAction("unmute"))
	mux.HandleFunc("POST /api/v1/groups/{group_id}/actions/kick", s.handleAction("kick"))
	mux.HandleFunc("POST /api/v1/groups/{group_id}/actions/whole-ban", s.handleAction("whole-ban"))
	mux.HandleFunc("POST /api/v1/groups/{group_id}/actions/recall", s.handleAction("recall"))
	mux.HandleFunc("POST /api/v1/groups/{group_id}/actions/card", s.handleAction("card"))

	mux.HandleFunc("GET /api/v1/audit", s.handleAudit)

	mux.HandleFunc("GET /api/v1/napcat/status", s.handleNapCatStatus)
	mux.HandleFunc("GET /api/v1/napcat/qrcode.png", s.handleNapCatQRCode)
	mux.HandleFunc("POST /api/v1/napcat/qrcode/refresh", s.handleNapCatRefresh)

	return mux
}

// securityHeaders 设置安全响应头（文档 13.3）。
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// clientIP 获取客户端真实 IP：仅当请求来自显式配置的可信代理（QQBOT_TRUST_PROXY）时
// 才信任 X-Forwarded-For，否则一律取 RemoteAddr（文档 13.4：只有显式配置的代理
// 地址才允许提供 X-Forwarded-*）。
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if s.trustProxy(host) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if first, _, ok := strings.Cut(xff, ","); ok {
				return strings.TrimSpace(first)
			}
			return strings.TrimSpace(xff)
		}
	}
	return host
}

// trustProxy 判断来源地址是否在可信代理列表中（支持精确 IP 与 CIDR）。
func (s *Server) trustProxy(host string) bool {
	if len(s.trustedProxies) == 0 {
		return false
	}
	ip := net.ParseIP(host)
	for _, p := range s.trustedProxies {
		if p == host {
			return true
		}
		if ip != nil {
			if _, cidr, err := net.ParseCIDR(p); err == nil && cidr.Contains(ip) {
				return true
			}
		}
	}
	return false
}

// requestLog 记录 API 访问（不含 body）。
func (s *Server) requestLog(r *http.Request, status int) {
	slog.Debug("api", "method", r.Method, "path", r.URL.Path, "status", status, "ip", s.clientIP(r))
}

// napcatClient 返回当前 NapCat 客户端（懒构建，token 变更后重建）。
func (s *Server) napcatClient() (*napcat.Client, error) {
	s.napMu.Lock()
	defer s.napMu.Unlock()
	if s.napClient != nil {
		return s.napClient, nil
	}
	cur := s.opts.Service.Current()
	if cur.System.NapCat.WebUIURL == "" {
		return nil, fmt.Errorf("未配置 NapCat WebUI 地址")
	}
	token, err := s.opts.Keys.Decrypt(cur.System.NapCat.WebUIToken, napcatFieldToken)
	if err != nil {
		return nil, fmt.Errorf("解密 NapCat token 失败: %w", err)
	}
	c := napcat.New(napcat.Config{WebUIURL: cur.System.NapCat.WebUIURL, Token: token})
	s.napClient = c
	return c, nil
}

// configHash 计算配置摘要（幂等键的一部分）。
func configHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
}

var _ = strings.TrimSpace

// StartNapCatWatchdog 启动 NapCat 掉线监控（watchdog 包），并订阅配置变更热生效：
// 保存系统设置后自动按新配置启停，无需重启进程。
//
// 高度可配置（control.json system.watchdog，后台系统设置可调）：
//   - enabled：总开关，默认关闭——不启用时完全不影响程序运行；
//   - interval_minutes：检测间隔（分钟，默认 10）；
//   - email_to：提醒收件人，空则回退到系统邮箱收件人（Email.To）。
//
// 依赖（缺失时仅记录日志说明原因，不阻塞主程序）：
//   - NapCat WebUI 配置（检测前提）；
//   - SMTP 配置完整（发信前提，复用登录验证码通道，授权码来自 control.json 加密存储）。
// 非阻塞：监控 goroutine 随 ctx 取消而退出。
func (s *Server) StartNapCatWatchdog(ctx context.Context) {
	var mu sync.Mutex
	var cancel context.CancelFunc

	start := func() {
		wd := s.opts.Service.Effective().Watchdog
		if !wd.Enabled {
			return // 默认关闭：静默跳过，不影响任何功能
		}
		cur := s.opts.Service.Current()
		if cur == nil || cur.System.NapCat.WebUIURL == "" {
			slog.Warn("NapCat 掉线监控已开启但未配置 NapCat WebUI 地址，监控未运行")
			return
		}
		if wd.EmailTo == "" {
			slog.Warn("NapCat 掉线监控已开启但无提醒收件人（请配置 watchdog.email_to 或系统邮箱收件人），监控未运行")
			return
		}
		e := s.opts.Service.Effective().Email
		if e.SMTPHost == "" || e.SMTPPort <= 0 || e.SMTPUser == "" || e.SMTPPassword == "" {
			slog.Warn("NapCat 掉线监控已开启但 SMTP 未配置完整（可在系统设置中配置，不影响其他功能），监控未运行")
			return
		}
		// 检测：复用 napcat.Client 的登录状态查询（含短时缓存，分钟级间隔无压力）
		check := func(ctx context.Context) (bool, error) {
			client, err := s.napcatClient()
			if err != nil {
				return false, err
			}
			st, err := client.CheckLogin(ctx)
			if err != nil {
				return false, err
			}
			return st.IsLogin, nil
		}
		// 发信：复用登录验证码的 SMTP 通道（授权码解密自 control.json）
		send := func(to, subject, body string) error {
			sender := s.emailSender()
			if sender == nil {
				return fmt.Errorf("邮箱发送组件不可用")
			}
			return sender.Send(to, subject, body)
		}
		wctx, wcancel := context.WithCancel(ctx)
		cancel = wcancel
		go watchdog.Watch(wctx, check, send, watchdog.Options{
			Interval: wd.Interval,
			To:       wd.EmailTo,
			Logger:   slog.Default(),
		})
		slog.Info("NapCat 掉线监控已启动", "interval", wd.Interval, "to", wd.EmailTo)
	}
	stop := func() {
		if cancel != nil {
			cancel()
			cancel = nil
			slog.Info("NapCat 掉线监控已停止（配置变更）")
		}
	}

	start() // 首次按当前配置启动
	// 配置热生效：保存系统设置后自动重启监控
	s.opts.Service.Subscribe(func() {
		mu.Lock()
		defer mu.Unlock()
		stop()
		start()
	})
}
