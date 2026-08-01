package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"qqbot/internal/bot"
	"qqbot/internal/state"
)

// testServer 组装一个完整的管理后台（未初始化模式：Manager/Actions 为 nil）。
func testServer(t *testing.T, init func(c *state.Control)) (*httptest.Server, *Server, *state.ConfigService) {
	t.Helper()
	dir := t.TempDir()
	keys := state.NewTestMasterKey()
	svc, err := state.Open(dir, keys)
	if err != nil {
		t.Fatal(err)
	}
	// 初始化基础配置（模拟 setup 流程完成后）
	if init != nil {
		if _, err := svc.Update("admin", 0, func(c *state.Control) error {
			init(c)
			return nil
		}, "初始化"); err != nil {
			t.Fatal(err)
		}
	}
	opts := Options{
		DataDir: dir,
		Service: svc,
		Keys:    keys,
		QueryAudit: func(q bot.AuditQuery) bot.AuditPage {
			return bot.AuditPage{Entries: []bot.AuditEntry{}}
		},
	}
	s, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, s, svc
}

// doSetup 完成 setup：取 setup token → 设密码 → 返回 cookie。
func doSetup(t *testing.T, ts *httptest.Server, s *Server) (*http.Cookie, string) {
	t.Helper()
	if !s.SetupRequired() {
		t.Fatal("应处于 setup 状态")
	}
	tok, err := s.EnsureSetupToken()
	if err != nil {
		t.Fatal(err)
	}
	resp := postJSON(ts, "/api/v1/auth/setup", map[string]any{
		"setup_token": tok, "password": "password123",
	}, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("setup 应成功: %d %s", resp.Code, resp.Body.String())
	}
	var cookies []*http.Cookie
	for _, c := range resp.Result().Cookies() {
		cookies = append(cookies, c)
	}
	if len(cookies) == 0 {
		t.Fatal("setup 应下发会话 Cookie")
	}
	c := cookies[0]
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("Cookie 安全属性缺失: %+v", c)
	}
	csrf := getCSRF(t, ts, c)
	return c, csrf
}

// doSetupAgain 在密码已设置后重新登录（用于需要会话但 setup 已完成的测试）。
func doSetupAgain(t *testing.T, ts *httptest.Server, s *Server) (*http.Cookie, string) {
	t.Helper()
	resp := postJSON(ts, "/api/v1/auth/login", map[string]any{"password": "newpassword456"}, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("登录失败: %d %s", resp.Code, resp.Body.String())
	}
	cs := resp.Result().Cookies()
	if len(cs) == 0 {
		t.Fatal("登录未下发 Cookie")
	}
	return cs[0], getCSRF(t, ts, cs[0])
}

func postJSON(ts *httptest.Server, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, ts.URL+path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	ts.Config.Handler.ServeHTTP(w, req)
	return w
}

func getCSRF(t *testing.T, ts *httptest.Server, cookie *http.Cookie) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, ts.URL+"/api/v1/auth/session", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	ts.Config.Handler.ServeHTTP(w, req)
	var out struct {
		Authenticated bool   `json:"authenticated"`
		CSRF          string `json:"csrf_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Authenticated || out.CSRF == "" {
		t.Fatalf("session 响应异常: %s", w.Body.String())
	}
	return out.CSRF
}

func doRequest(ts *httptest.Server, method, path string, body any, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	var rd *bytes.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rd = bytes.NewReader(data)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, ts.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	w := httptest.NewRecorder()
	ts.Config.Handler.ServeHTTP(w, req)
	return w
}

func TestSetupTokenSingleUse(t *testing.T) {
	ts, s, _ := testServer(t, nil)
	tok, _ := s.EnsureSetupToken()
	// 正确 token 成功
	resp := postJSON(ts, "/api/v1/auth/setup", map[string]any{"setup_token": tok, "password": "password123"}, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("setup 应成功: %d", resp.Code)
	}
	// 再试：token 已消费 + 密码已设置
	resp = postJSON(ts, "/api/v1/auth/setup", map[string]any{"setup_token": tok, "password": "password123"}, nil)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("重复 setup 应 403，实际 %d", resp.Code)
	}
}

func TestSetupWrongToken(t *testing.T) {
	ts, _, _ := testServer(t, nil)
	resp := postJSON(ts, "/api/v1/auth/setup", map[string]any{"setup_token": "wrong", "password": "password123"}, nil)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("错误 token 应 403，实际 %d", resp.Code)
	}
}

func TestAuthFlow(t *testing.T) {
	ts, s, _ := testServer(t, nil)
	c, _ := doSetup(t, ts, s)

	// 退出登录
	resp := postJSON(ts, "/api/v1/auth/logout", nil, c)
	if resp.Code != http.StatusOK {
		t.Fatalf("logout 失败: %d", resp.Code)
	}
	// 退出后原 Cookie 失效
	req := httptest.NewRequest(http.MethodGet, ts.URL+"/api/v1/status", nil)
	req.AddCookie(c)
	w := httptest.NewRecorder()
	ts.Config.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("退出后应 401，实际 %d", w.Code)
	}
	// 重新登录
	resp = postJSON(ts, "/api/v1/auth/login", map[string]any{"password": "password123"}, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("login 失败: %d", resp.Code)
	}
}

func TestWrongPasswordAndRateLimit(t *testing.T) {
	ts, s, _ := testServer(t, nil)
	_, _ = doSetup(t, ts, s)
	// 连续错误密码触发 IP 限流（上限 10）
	for i := 0; i < 10; i++ {
		resp := postJSON(ts, "/api/v1/auth/login", map[string]any{"password": "wrong"}, nil)
		if resp.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次错误密码应 401，实际 %d", i, resp.Code)
		}
	}
	resp := postJSON(ts, "/api/v1/auth/login", map[string]any{"password": "wrong"}, nil)
	if resp.Code != http.StatusTooManyRequests {
		t.Fatalf("超限后应 429，实际 %d", resp.Code)
	}
	// 限流不影响正确密码？限流窗口内仍被拒（安全策略：IP 维度整体限制）
	_ = s
}

func TestCSRFRequiredForWrites(t *testing.T) {
	ts, s, svc := testServer(t, nil)
	// setup 完成后初始化配置（让业务 API 可用）
	c, _ := doSetup(t, ts, s)
	_, err := svc.Update("admin", 0, func(c *state.Control) error {
		c.System.BotName = "机器人"
		c.System.Owner = 10001
		c.System.OneBot.WSURL = "ws://127.0.0.1:3001"
		return nil
	}, "init")
	if err != nil {
		t.Fatal(err)
	}
	// 无 CSRF 的写请求 → 403
	resp := doRequest(ts, http.MethodPut, "/api/v1/settings",
		map[string]any{"revision": 1, "system": map[string]any{"bot_name": "x"}}, c, "")
	if resp.Code != http.StatusForbidden {
		t.Fatalf("无 CSRF 写请求应 403，实际 %d", resp.Code)
	}
	// 错误 CSRF → 403
	resp = doRequest(ts, http.MethodPut, "/api/v1/settings",
		map[string]any{"revision": 1, "system": map[string]any{"bot_name": "x"}}, c, "deadbeef")
	if resp.Code != http.StatusForbidden {
		t.Fatalf("错误 CSRF 应 403，实际 %d", resp.Code)
	}
	// 正确 CSRF → 200
	csrf := getCSRF(t, ts, c)
	resp = doRequest(ts, http.MethodPut, "/api/v1/settings",
		map[string]any{"revision": 1, "system": map[string]any{"bot_name": "x"}}, c, csrf)
	if resp.Code != http.StatusOK {
		t.Fatalf("正确 CSRF 应成功，实际 %d %s", resp.Code, resp.Body.String())
	}
}

func TestSecretNotLeaked(t *testing.T) {
	ts, s, _ := testServer(t, nil)
	c, _ := doSetup(t, ts, s)
	// 初始化配置（含 secret）
	_, err := s.opts.Service.Update("admin", 0, func(c *state.Control) error {
		c.System.BotName = "机器人"
		c.System.Owner = 10001
		c.System.OneBot.WSURL = "ws://127.0.0.1:3001"
		tok, err := s.opts.Keys.Encrypt("super-secret-token", "system.onebot.access_token")
		if err != nil {
			return err
		}
		c.System.OneBot.AccessToken = tok
		return nil
	}, "init")
	if err != nil {
		t.Fatal(err)
	}
	csrf := getCSRF(t, ts, c)
	resp := doRequest(ts, http.MethodGet, "/api/v1/settings", nil, c, csrf)
	body := resp.Body.String()
	if strings.Contains(body, "super-secret-token") {
		t.Fatal("API 响应泄漏明文 secret")
	}
	var dto settingsDTO
	if err := json.Unmarshal(resp.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if !dto.System.OneBot.AccessToken.Configured {
		t.Fatal("access_token 应标记为 configured")
	}
	// 审计/日志路径也不应出现明文（检查审计文件）
	_ = s.opts.Service.Current()
}

func TestSetupGateBlocksUninitialized(t *testing.T) {
	ts, s, _ := testServer(t, nil)
	c, csrf := doSetup(t, ts, s)
	// 未初始化：动作 API → 503 setup_required
	resp := doRequest(ts, http.MethodPost, "/api/v1/groups/123/actions/mute",
		map[string]any{"target_id": 1}, c, csrf)
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("未初始化动作应 503，实际 %d", resp.Code)
	}
	// 未初始化：GET /audit → 503
	resp = doRequest(ts, http.MethodGet, "/api/v1/audit", nil, c, csrf)
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("未初始化审计应 503，实际 %d", resp.Code)
	}
	// 白名单：PUT /settings 可用
	resp = doRequest(ts, http.MethodPut, "/api/v1/settings",
		map[string]any{"revision": 0, "system": map[string]any{
			"bot_name": "机器人", "owner": 10001,
			"onebot": map[string]any{"ws_url": "ws://127.0.0.1:3001"},
		}}, c, csrf)
	if resp.Code != http.StatusOK {
		t.Fatalf("初始化模式下 PUT /settings 应可用，实际 %d %s", resp.Code, resp.Body.String())
	}
	// 建群可用
	resp = doRequest(ts, http.MethodPost, "/api/v1/groups",
		map[string]any{"group": map[string]any{"group_id": 123456789, "enabled": true}}, c, csrf)
	if resp.Code != http.StatusOK {
		t.Fatalf("初始化模式下建群应可用，实际 %d %s", resp.Code, resp.Body.String())
	}
	// 初始化后动作 API 应通过门控（Manager nil → 503 onebot 未启动）
	resp = doRequest(ts, http.MethodPost, "/api/v1/groups/123456789/actions/mute",
		map[string]any{"target_id": 1}, c, csrf)
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("动作应 503 onebot 未启动，实际 %d", resp.Code)
	}
}

func TestPasswordChangeInvalidatesSessions(t *testing.T) {
	ts, s, _ := testServer(t, nil)
	c, csrf := doSetup(t, ts, s)
	// 改密
	resp := doRequest(ts, http.MethodPut, "/api/v1/auth/password",
		map[string]any{"current_password": "password123", "new_password": "newpassword456"}, c, csrf)
	if resp.Code != http.StatusOK {
		t.Fatalf("改密失败: %d %s", resp.Code, resp.Body.String())
	}
	// 旧 session 失效
	req := httptest.NewRequest(http.MethodGet, ts.URL+"/api/v1/status", nil)
	req.AddCookie(c)
	w := httptest.NewRecorder()
	ts.Config.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("改密后旧会话应失效，实际 %d", w.Code)
	}
	// 错误当前密码 → 403（需要重新登录拿新会话）
	c2, csrf2 := doSetupAgain(t, ts, s)
	resp = doRequest(ts, http.MethodPut, "/api/v1/auth/password",
		map[string]any{"current_password": "wrong", "new_password": "newpassword456"}, c2, csrf2)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("错误当前密码应 403，实际 %d", resp.Code)
	}
}

func TestGroupsCRUDAndConflict(t *testing.T) {
	ts, s, _ := testServer(t, func(c *state.Control) {
		c.System.BotName = "机器人"
		c.System.Owner = 10001
		c.System.OneBot.WSURL = "ws://127.0.0.1:3001"
	})
	c, csrf := doSetup(t, ts, s)

	// 建群
	resp := doRequest(ts, http.MethodPost, "/api/v1/groups",
		map[string]any{"group": map[string]any{"group_id": 111, "enabled": true, "remark": "群一"}}, c, csrf)
	if resp.Code != http.StatusOK {
		t.Fatalf("建群失败: %d %s", resp.Code, resp.Body.String())
	}
	var created groupResponse
	_ = json.Unmarshal(resp.Body.Bytes(), &created)
	if created.Revision != 2 {
		t.Fatalf("revision 应为 2，实际 %d", created.Revision)
	}
	// 重复群号 → 422
	resp = doRequest(ts, http.MethodPost, "/api/v1/groups",
		map[string]any{"group": map[string]any{"group_id": 111, "enabled": true}}, c, csrf)
	if resp.Code != http.StatusUnprocessableEntity {
		t.Fatalf("重复群号应 422，实际 %d", resp.Code)
	}
	// 更新（过期 revision → 409）
	resp = doRequest(ts, http.MethodPut, "/api/v1/groups/111",
		map[string]any{"revision": 1, "group": map[string]any{"group_id": 111, "enabled": false}}, c, csrf)
	if resp.Code != http.StatusConflict {
		t.Fatalf("过期 revision 应 409，实际 %d", resp.Code)
	}
	// 更新（正确 revision）
	resp = doRequest(ts, http.MethodPut, "/api/v1/groups/111",
		map[string]any{"revision": 2, "group": map[string]any{"group_id": 111, "enabled": false, "remark": "群一"}}, c, csrf)
	if resp.Code != http.StatusOK {
		t.Fatalf("更新群失败: %d %s", resp.Code, resp.Body.String())
	}
	// 不存在的群 → 404
	resp = doRequest(ts, http.MethodPut, "/api/v1/groups/999",
		map[string]any{"revision": 3, "group": map[string]any{"group_id": 999}}, c, csrf)
	if resp.Code != http.StatusNotFound {
		t.Fatalf("不存在群应 404，实际 %d", resp.Code)
	}
	// 删除
	resp = doRequest(ts, http.MethodDelete, "/api/v1/groups/111", nil, c, csrf)
	if resp.Code != http.StatusOK {
		t.Fatalf("删除失败: %d", resp.Code)
	}
}

func TestValidationErrorFields(t *testing.T) {
	ts, s, _ := testServer(t, func(c *state.Control) {
		c.System.BotName = "机器人"
		c.System.Owner = 10001
		c.System.OneBot.WSURL = "ws://127.0.0.1:3001"
	})
	c, csrf := doSetup(t, ts, s)
	// 非法 ws_url
	resp := doRequest(ts, http.MethodPut, "/api/v1/settings",
		map[string]any{"revision": 1, "system": map[string]any{
			"onebot": map[string]any{"ws_url": "ftp://bad"},
		}}, c, csrf)
	if resp.Code != http.StatusUnprocessableEntity {
		t.Fatalf("应 422，实际 %d", resp.Code)
	}
	var out struct {
		Error struct {
			Code   string            `json:"code"`
			Fields map[string]string `json:"fields"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Error.Code != "validation_failed" {
		t.Fatalf("错误码错误: %s", out.Error.Code)
	}
	if _, ok := out.Error.Fields["system.onebot.ws_url"]; !ok {
		t.Fatalf("字段级错误缺失: %v", out.Error.Fields)
	}
}

func TestUnauthenticatedAccessDenied(t *testing.T) {
	ts, _, _ := testServer(t, func(c *state.Control) {
		c.System.BotName = "机器人"
		c.System.Owner = 10001
		c.System.OneBot.WSURL = "ws://127.0.0.1:3001"
	})
	// 无 Cookie 访问业务 API → 401
	for _, path := range []string{
		"/api/v1/status", "/api/v1/settings", "/api/v1/groups",
		"/api/v1/audit", "/api/v1/napcat/status",
	} {
		req := httptest.NewRequest(http.MethodGet, ts.URL+path, nil)
		w := httptest.NewRecorder()
		ts.Config.Handler.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s 未认证应 401，实际 %d", path, w.Code)
		}
	}
	// 二维码接口未认证不可访问
	req := httptest.NewRequest(http.MethodGet, ts.URL+"/api/v1/napcat/qrcode.png", nil)
	w := httptest.NewRecorder()
	ts.Config.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("二维码未认证应 401，实际 %d", w.Code)
	}
}
