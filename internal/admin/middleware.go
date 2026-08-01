package admin

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const sessionCookieName = "qqbot_sid"

type ctxKey int

const sessionCtxKey ctxKey = 1

func withSessionCtx(r *http.Request, sess *session) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), sessionCtxKey, sess))
}

func sessionFromCtx(r *http.Request) *session {
	sess, _ := r.Context().Value(sessionCtxKey).(*session)
	return sess
}

// withSession 校验登录态：Cookie 中的 opaque session ID → 内存 session。
// 未登录/过期 → 401。通过后注入 session 到 context。
func (s *Server) withSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			errorResponse(w, http.StatusUnauthorized, CodeUnauthorized, "未登录或会话已过期", nil)
			return
		}
		sess, ok := s.sessions.get(cookie.Value)
		if !ok {
			errorResponse(w, http.StatusUnauthorized, CodeUnauthorized, "未登录或会话已过期", nil)
			return
		}
		next.ServeHTTP(w, withSessionCtx(r, sess))
	})
}

// withCSRF 校验写请求：X-CSRF-Token 必须与会话绑定 token 一致；
// 存在 Origin 头时还必须同源（默认关闭 CORS，无需 Access-Control-*）。
func (s *Server) withCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		sess := sessionFromCtx(r)
		if sess == nil {
			errorResponse(w, http.StatusUnauthorized, CodeUnauthorized, "未登录", nil)
			return
		}
		if r.Header.Get("X-CSRF-Token") != sess.CSRF {
			errorResponse(w, http.StatusForbidden, CodeForbidden, "CSRF 校验失败", nil)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(origin, r) {
			errorResponse(w, http.StatusForbidden, CodeForbidden, "请求来源不被允许", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin 校验 Origin 与请求 Host 同源。
func sameOrigin(origin string, r *http.Request) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	// 只信任 http/https 与匹配的 Host（含端口）
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host == r.Host
}

// gateSetup 初始化门控：业务配置未初始化（revision 0）时，
// 仅允许系统设置/测试/建群等 setup 流程端点，其余返回 503 setup_required。
func (s *Server) gateSetup(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.opts.Service.Initialized() || setupAllowed(r) {
			next.ServeHTTP(w, r)
			return
		}
		errorResponse(w, http.StatusServiceUnavailable, CodeSetupRequired,
			"配置尚未初始化，请先完成 setup 流程", nil)
	})
}

// setupAllowed 判断端点是否属于初始化流程白名单。
func setupAllowed(r *http.Request) bool {
	p := r.URL.Path
	switch {
	case p == "/api/v1/status":
		return true
	case p == "/api/v1/settings" && (r.Method == http.MethodGet || r.Method == http.MethodPut):
		return true
	case strings.HasPrefix(p, "/api/v1/settings/test-"):
		return true
	case p == "/api/v1/groups" && r.Method == http.MethodPost:
		return true
	case strings.HasPrefix(p, "/api/v1/auth/"):
		return true
	}
	return false
}

// idemStore 是动作幂等缓存：按 (管理员, 动作, 参数摘要) 短期缓存结果，
// 防止浏览器重复提交危险动作（设计文档 12 节）。
type idemStore struct {
	mu    sync.Mutex
	ttl   time.Duration
	max   int
	byKey map[string]idemEntry
	order []string // FIFO 顺序（用于超限淘汰）
}

type idemEntry struct {
	at     time.Time
	status int
	body   any
}

func newIdemStore(ttl time.Duration, max int) *idemStore {
	return &idemStore{
		ttl:   ttl,
		max:   max,
		byKey: map[string]idemEntry{},
	}
}

// get 返回缓存结果（含命中标记）。
func (s *idemStore) get(key string) (idemEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked()
	e, ok := s.byKey[key]
	if !ok || time.Since(e.at) > s.ttl {
		delete(s.byKey, key)
		return idemEntry{}, false
	}
	return e, true
}

// put 缓存结果；超限时淘汰最旧条目。
func (s *idemStore) put(key string, status int, body any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked()
	if _, exists := s.byKey[key]; !exists {
		s.order = append(s.order, key)
	}
	s.byKey[key] = idemEntry{at: time.Now(), status: status, body: body}
	for len(s.order) > s.max {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.byKey, oldest)
	}
}

// gcLocked 清理过期条目（调用方持锁）。
func (s *idemStore) gcLocked() {
	now := time.Now()
	keep := s.order[:0]
	for _, k := range s.order {
		e, ok := s.byKey[k]
		if !ok || now.Sub(e.at) > s.ttl {
			delete(s.byKey, k)
			continue
		}
		keep = append(keep, k)
	}
	s.order = keep
}
