package admin

// 端到端集成测试：使用真实 ConfigService / onebot.Manager / bot.Bot / admin.Server，
// 配合 mock OneBot WebSocket 与 mock NapCat HTTP 服务，验证前四个阶段的完整生命周期：
// setup → 登录 → 初始化配置 → 建群 → OneBot 连接 → Bot 事件响应（欢迎/关键词处罚）→
// 人工动作 → 审计 → 二维码 → 配置热重连 → 历史恢复 → 改密。
//
// 运行：go test ./internal/admin/ -run TestE2E -v

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"qqbot/internal/bot"
	"qqbot/internal/config"
	"qqbot/internal/onebot"
	"qqbot/internal/state"
)

// ---------- mock OneBot WebSocket ----------

type e2eOneBot struct {
	t       *testing.T
	srv     *httptest.Server
	url     string
	mu      sync.Mutex
	writeMu sync.Mutex // 串行化写帧（gorilla 单写者约束：回包 + 推事件）
	conn    *websocket.Conn
	acts    []e2eAction // 收到的动作（按到达顺序）
	seen    int         // 已消费的动作数
}

type e2eAction struct {
	Action string
	Params map[string]any
	Echo   json.RawMessage
}

func newE2EOneBot(t *testing.T) *e2eOneBot {
	t.Helper()
	e := &e2eOneBot{t: t}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		e.mu.Lock()
		e.conn = ws
		e.mu.Unlock()
		ws.SetReadDeadline(time.Now().Add(120 * time.Second))
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				e.mu.Lock()
				if e.conn == ws {
					e.conn = nil
				}
				e.mu.Unlock()
				_ = ws.Close()
				return
			}
			e.handleFrame(ws, data)
		}
	}))
	e.url = "ws://" + strings.TrimPrefix(e.srv.URL, "http://")
	t.Cleanup(e.srv.Close)
	return e
}

func (e *e2eOneBot) handleFrame(ws *websocket.Conn, data []byte) {
	var req struct {
		Action string          `json:"action"`
		Params map[string]any  `json:"params"`
		Echo   json.RawMessage `json:"echo"`
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return
	}
	e.mu.Lock()
	e.acts = append(e.acts, e2eAction{Action: req.Action, Params: req.Params, Echo: req.Echo})
	e.mu.Unlock()

	var data2 map[string]any
	switch req.Action {
	case "get_login_info":
		data2 = map[string]any{"user_id": 10001, "nickname": "验收机器人"}
	case "get_group_member_info":
		uid, _ := req.Params["user_id"].(float64)
		data2 = map[string]any{"user_id": int64(uid), "nickname": "新人小张", "card": "", "role": "member"}
	}
	resp := map[string]any{"status": "ok", "retcode": 0, "data": data2, "echo": req.Echo}
	out, _ := json.Marshal(resp)
	e.writeMu.Lock()
	_ = ws.WriteMessage(websocket.TextMessage, out)
	e.writeMu.Unlock()
}

// dropConn 从服务端关闭当前连接（模拟 OneBot 掉线）。
func (e *e2eOneBot) dropConn() {
	e.mu.Lock()
	ws := e.conn
	e.mu.Unlock()
	if ws != nil {
		_ = ws.Close()
	}
}

// push 向当前连接推送一条事件（连接建立后调用）。
func (e *e2eOneBot) push(raw string) {
	e.mu.Lock()
	ws := e.conn
	e.mu.Unlock()
	if ws == nil {
		e.t.Fatalf("push 时 OneBot 未连接")
	}
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	if err := ws.WriteMessage(websocket.TextMessage, []byte(raw)); err != nil {
		e.t.Fatalf("push 事件失败: %v", err)
	}
}

// waitAction 等待指定动作到达（默认 3 秒超时），返回该动作。
func (e *e2eOneBot) waitAction(want string, timeout time.Duration) e2eAction {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		e.mu.Lock()
		for i := e.seen; i < len(e.acts); i++ {
			if e.acts[i].Action == want {
				a := e.acts[i]
				e.seen = i + 1
				e.mu.Unlock()
				return a
			}
		}
		e.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	e.mu.Lock()
	acts := make([]string, 0, len(e.acts))
	for _, a := range e.acts {
		acts = append(acts, a.Action)
	}
	e.mu.Unlock()
	e.t.Fatalf("等待动作 %s 超时（已收到: %v）", want, acts)
	return e2eAction{}
}

// waitConnected 等待客户端连入。
func (e *e2eOneBot) waitConnected(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		e.mu.Lock()
		ok := e.conn != nil
		e.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatal("等待 OneBot 客户端连接超时")
}

// connCount 当前是否有活跃连接。
func (e *e2eOneBot) connected() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.conn != nil
}

// ---------- mock NapCat WebUI ----------

type e2eNapCat struct {
	mu       sync.Mutex
	token    string
	isLogin  bool
	qrURL    string
	refresh  int
	loginCnt int
}

func newE2ENapCat(t *testing.T, token string) (*e2eNapCat, *httptest.Server) {
	t.Helper()
	m := &e2eNapCat{token: token}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		write := func(code int, msg string, data any) {
			w.Header().Set("Content-Type", "application/json")
			resp := map[string]any{"code": code, "message": msg}
			if data != nil {
				resp["data"] = data
			}
			json.NewEncoder(w).Encode(resp)
		}
		switch r.URL.Path {
		case "/api/auth/login":
			m.mu.Lock()
			m.loginCnt++
			m.mu.Unlock()
			var body struct {
				Hash string `json:"hash"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			sum := sha256.Sum256([]byte(m.token + ".napcat"))
			if !strings.EqualFold(body.Hash, hex.EncodeToString(sum[:])) {
				write(-1, "token is invalid", nil)
				return
			}
			cred := base64.StdEncoding.EncodeToString([]byte(`{"Data":{"HashEncoded":"` + body.Hash + `"},"Hmac":"x"}`))
			write(0, "success", map[string]string{"Credential": cred})
		case "/api/QQLogin/CheckLoginStatus":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				write(-1, "Unauthorized", nil)
				return
			}
			m.mu.Lock()
			write(0, "success", map[string]any{
				"isLogin": m.isLogin, "isOffline": false,
				"qrcodeurl": m.qrURL, "loginError": "",
			})
			m.mu.Unlock()
		case "/api/QQLogin/GetQQLoginQrcode":
			m.mu.Lock()
			write(0, "success", map[string]string{"qrcode": m.qrURL})
			m.mu.Unlock()
		case "/api/QQLogin/RefreshQRcode":
			m.mu.Lock()
			m.refresh++
			m.qrURL = strings.Replace(m.qrURL, "?v=1", "?v=2", 1)
			m.mu.Unlock()
			write(0, "success", nil)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	m.qrURL = srv.URL + "/qr.png?v=1"
	return m, srv
}

// ---------- E2E 环境 ----------

type e2eEnv struct {
	t       *testing.T
	dir     string
	keys    *state.MasterKey
	svc     *state.ConfigService
	mgr     *onebot.Manager
	bot     *bot.Bot
	s       *Server
	ts      *httptest.Server
	oneBot  *e2eOneBot
	oneBot2 *e2eOneBot
	napcat  *e2eNapCat
	cancel  context.CancelFunc
}

// newE2E 搭建完整环境：未初始化状态（等价全新部署，等待网页 setup）。
func newE2E(t *testing.T) *e2eEnv {
	t.Helper()
	env := &e2eEnv{t: t}
	env.oneBot = newE2EOneBot(t)
	env.oneBot2 = newE2EOneBot(t)
	env.napcat, _ = newE2ENapCat(t, "napcat-secret")

	env.dir = t.TempDir()
	env.keys = state.NewTestMasterKey()
	svc, err := state.Open(env.dir, env.keys)
	if err != nil {
		t.Fatal(err)
	}
	env.svc = svc

	// Manager 初始 endpoint 为空（初始化模式：不连接 OneBot）
	ctx, cancel := context.WithCancel(context.Background())
	env.cancel = cancel
	env.mgr = onebot.NewManager(onebot.Endpoint{URL: "", Timeout: time.Second})
	go env.mgr.Run(ctx)

	s, err := New(Options{
		DataDir: env.dir,
		Service: svc,
		Keys:    env.keys,
		Manager: env.mgr,
		// Actions/QueryAudit/RecentMessages 在 startBot 后注入
	})
	if err != nil {
		t.Fatal(err)
	}
	env.s = s
	env.ts = httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		cancel()
		env.ts.Close()
	})
	return env
}

// startBot 在配置初始化后启动 Bot（等价 main.go 的网页化路径启动 + 热生效联动）。
// 顺序要求：先注册事件 handlers（b.Start），再触发连接（Reconfigure），
// 否则新 generation 创建时 handler 快照为空（main.go 同样遵守此顺序）。
func (e *e2eEnv) startBot() {
	e.t.Helper()
	eff := e.svc.Effective()
	e.bot = bot.NewWithDir(eff, e.mgr, e.dir)
	e.svc.Subscribe(func() {
		next := e.svc.Effective()
		e.bot.SetExternalConfigSource(func() *config.Config { return next })
		e.mgr.Reconfigure(onebot.Endpoint{
			URL:         next.OneBot.WSURL,
			AccessToken: next.OneBot.AccessToken,
			Timeout:     time.Second,
		})
	})
	e.bot.Start()
	e.mgr.Reconfigure(onebot.Endpoint{
		URL:         eff.OneBot.WSURL,
		AccessToken: eff.OneBot.AccessToken,
		Timeout:     time.Second,
	})
	e.s.SetComponents(e.mgr, e.bot.Actions(), e.bot.QueryAudit, e.bot.RecentMessages, e.bot.CountersByGroup)
}

func (e *e2eEnv) do(method, path string, body any, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	return doRequest(e.ts, method, path, body, cookie, csrf)
}

// doH 带额外请求头（如 Idempotency-Key）。
func (e *e2eEnv) doH(method, path string, body any, cookie *http.Cookie, csrf string, headers map[string]string) *httptest.ResponseRecorder {
	var rd *strings.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rd = strings.NewReader(string(data))
	} else {
		rd = strings.NewReader("")
	}
	req := httptest.NewRequest(method, e.ts.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.ts.Config.Handler.ServeHTTP(w, req)
	return w
}

// setupAndLogin 完成首次 setup 并返回会话。
func (e *e2eEnv) setupAndLogin() (*http.Cookie, string) {
	e.t.Helper()
	if !e.s.SetupRequired() {
		e.t.Fatal("应处于 setup 状态")
	}
	tok, err := e.s.EnsureSetupToken()
	if err != nil {
		e.t.Fatal(err)
	}
	resp := postJSON(e.ts, "/api/v1/auth/setup", map[string]any{
		"setup_token": tok, "password": "password123",
	}, nil)
	if resp.Code != http.StatusOK {
		e.t.Fatalf("setup 失败: %d %s", resp.Code, resp.Body.String())
	}
	cs := resp.Result().Cookies()
	if len(cs) == 0 {
		e.t.Fatal("setup 未下发 Cookie")
	}
	return cs[0], getCSRF(e.t, e.ts, cs[0])
}

// initConfig 通过 API 完成基础配置：OneBot/NapCat 地址 + token + 第一个群。
func (e *e2eEnv) initConfig(cookie *http.Cookie, csrf string) {
	e.t.Helper()
	resp := e.do(http.MethodPut, "/api/v1/settings", map[string]any{
		"revision": 0,
		"system": map[string]any{
			"bot_name": "群管小助手", "owner": 10001, "timezone": "Asia/Shanghai",
			"onebot": map[string]any{
				"ws_url": e.oneBot.url, "api_timeout_ms": 3000,
				"access_token": "onebot-secret",
			},
			"napcat": map[string]any{
				"webui_url": e.napcatSrvURL(), "webui_token": "napcat-secret",
			},
		},
	}, cookie, csrf)
	if resp.Code != http.StatusOK {
		e.t.Fatalf("初始化系统设置失败: %d %s", resp.Code, resp.Body.String())
	}
	resp = e.do(http.MethodPost, "/api/v1/groups", map[string]any{
		"group": map[string]any{
			"group_id": 123456789, "enabled": true, "remark": "测试群",
			"rules": []map[string]any{
				{
					"id": "r1", "name": "新人欢迎", "event": "group_increase", "enabled": true,
					"then": []map[string]any{{"type": "send_message", "params": map[string]any{
						"message": "欢迎 {nickname} 加入本群", "at": true,
					}}},
					"break": true,
				},
				{
					"id": "r2", "name": "广告", "event": "message", "enabled": true,
					"when": []map[string]any{{"type": "text_contains", "params": map[string]any{"text": "广告"}}},
					"then": []map[string]any{{"type": "warn", "params": map[string]any{"reason": "禁止广告"}}},
					"break": true,
				},
				{
					"id": "r3", "name": "开挂", "event": "message", "enabled": true,
					"when": []map[string]any{{"type": "text_contains", "params": map[string]any{"text": "开挂"}}},
					"then": []map[string]any{{"type": "mute", "params": map[string]any{
						"minutes": 10, "reason": "发布违规内容：开挂",
					}}},
					"break": true,
				},
			},
		},
	}, cookie, csrf)
	if resp.Code != http.StatusOK {
		e.t.Fatalf("建群失败: %d %s", resp.Code, resp.Body.String())
	}
}

// napcatSrvURL 返回 mock NapCat 的地址（从 e2eNapCat 的 QR URL 推导）。
func (e *e2eEnv) napcatSrvURL() string {
	e.napcat.mu.Lock()
	defer e.napcat.mu.Unlock()
	return strings.TrimSuffix(e.napcat.qrURL, "/qr.png?v=1")
}
