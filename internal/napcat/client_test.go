package napcat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mockNapcat 模拟 NapCat WebUI 登录相关 API。
type mockNapcat struct {
	token    string
	mu       sync.Mutex
	isLogin  bool
	qrURL    string
	loginErr string
	loginCnt atomic.Int64 // /api/auth/login 调用次数
	checkCnt atomic.Int64
}

func (m *mockNapcat) handler() http.Handler {
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, code int, msg string, data any) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{"code": code, "message": msg}
		if data != nil {
			resp["data"] = data
		}
		json.NewEncoder(w).Encode(resp)
	}
	mux.HandleFunc("/api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		m.loginCnt.Add(1)
		var body struct {
			Hash string `json:"hash"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		sum := sha256.Sum256([]byte(m.token + ".napcat"))
		if !strings.EqualFold(body.Hash, hex.EncodeToString(sum[:])) {
			write(w, -1, "token is invalid", nil)
			return
		}
		cred := base64.StdEncoding.EncodeToString([]byte(`{"Data":{"CreatedTime":1,"HashEncoded":"` + body.Hash + `"},"Hmac":"x"}`))
		write(w, 0, "success", map[string]string{"Credential": cred})
	})
	mux.HandleFunc("/api/QQLogin/CheckLoginStatus", func(w http.ResponseWriter, r *http.Request) {
		m.checkCnt.Add(1)
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			write(w, -1, "Unauthorized", nil)
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		write(w, 0, "success", map[string]any{
			"isLogin": m.isLogin, "isOffline": false,
			"qrcodeurl": m.qrURL, "loginError": m.loginErr,
		})
	})
	mux.HandleFunc("/api/QQLogin/GetQQLoginQrcode", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.qrURL == "" {
			write(w, -1, "QRCode Get Error", nil)
			return
		}
		write(w, 0, "success", map[string]string{"qrcode": m.qrURL})
	})
	mux.HandleFunc("/api/QQLogin/RefreshQRcode", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.qrURL = strings.Replace(m.qrURL, "/qr.png?v=1", "/qr.png?v=2", 1)
		m.loginErr = ""
		m.mu.Unlock()
		write(w, 0, "success", nil)
	})
	return mux
}

func startMock(t *testing.T) (*mockNapcat, *Client) {
	t.Helper()
	m := &mockNapcat{token: "test-token-123", isLogin: false}
	srv := httptest.NewServer(m.handler())
	t.Cleanup(srv.Close)
	m.qrURL = srv.URL + "/qr.png?v=1"
	return m, New(Config{WebUIURL: srv.URL, Token: m.token})
}

func TestCheckLoginAndQR(t *testing.T) {
	m, c := startMock(t)
	ctx := context.Background()
	st, err := c.CheckLogin(ctx)
	if err != nil {
		t.Fatalf("CheckLogin: %v", err)
	}
	if st.IsLogin || !strings.Contains(st.QRCodeURL, "/qr.png?v=1") {
		t.Fatalf("状态不符: %+v", st)
	}
	// 登录成功（等待状态缓存过期）
	m.mu.Lock()
	m.isLogin = true
	m.mu.Unlock()
	time.Sleep(statusCacheTTL + 50*time.Millisecond)
	st, err = c.CheckLogin(ctx)
	if err != nil || !st.IsLogin {
		t.Fatalf("登录后状态应更新: %+v err=%v", st, err)
	}
	// 二维码获取/刷新
	url, err := c.GetQRCode(ctx)
	if err != nil || !strings.Contains(url, "/qr.png?v=1") {
		t.Fatalf("GetQRCode: %v %s", err, url)
	}
	if err := c.RefreshQRCode(ctx); err != nil {
		t.Fatalf("RefreshQRCode: %v", err)
	}
	url, _ = c.GetQRCode(ctx)
	if !strings.Contains(url, "/qr.png?v=2") {
		t.Fatalf("刷新后二维码未更新: %s", url)
	}
}

func TestCredentialRefreshOnceOn401(t *testing.T) {
	// 凭证失效：请求返回 401 → 自动重新登录一次并重试成功
	m, c := startMock(t)
	ctx := context.Background()
	if _, err := c.CheckLogin(ctx); err != nil {
		t.Fatal(err)
	}
	loginsBefore := m.loginCnt.Load()
	c.InvalidateCredForTest()                        // 模拟 credential 过期
	time.Sleep(statusCacheTTL + 50*time.Millisecond) // 越过状态缓存
	if _, err := c.CheckLogin(ctx); err != nil {
		t.Fatalf("401 后应自动重认证: %v", err)
	}
	if m.loginCnt.Load() != loginsBefore+1 {
		t.Fatalf("应恰好重新登录一次（登录次数 %d -> %d）", loginsBefore, m.loginCnt.Load())
	}
}

func TestConcurrentCheckLoginSingleAuth(t *testing.T) {
	m, c := startMock(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.CheckLogin(ctx); err != nil {
				t.Errorf("并发 CheckLogin: %v", err)
			}
		}()
	}
	wg.Wait()
	// 并发首次调用只应触发 1 次 /api/auth/login（singleflight）
	if m.loginCnt.Load() != 1 {
		t.Fatalf("并发调用应只登录 1 次，实际 %d", m.loginCnt.Load())
	}
}

func TestStatusCacheHits(t *testing.T) {
	m, c := startMock(t)
	ctx := context.Background()
	// 状态缓存 1 秒：连续调用只发 1 次 HTTP 请求
	if _, err := c.CheckLogin(ctx); err != nil {
		t.Fatal(err)
	}
	cnt1 := m.checkCnt.Load()
	if _, err := c.CheckLogin(ctx); err != nil {
		t.Fatal(err)
	}
	if m.checkCnt.Load() != cnt1 {
		t.Fatal("缓存期内不应重复请求 CheckLoginStatus")
	}
	// 缓存过期后重新请求
	time.Sleep(statusCacheTTL + 50*time.Millisecond)
	if _, err := c.CheckLogin(ctx); err != nil {
		t.Fatal(err)
	}
	if m.checkCnt.Load() <= cnt1 {
		t.Fatal("缓存过期后应重新请求")
	}
}

func TestHTTPErrorTyped(t *testing.T) {
	// 不可达地址：应返回携带状态的 HTTPError 或网络错误
	c := New(Config{WebUIURL: "http://127.0.0.1:1", Token: "x"})
	_, err := c.CheckLogin(context.Background())
	if err == nil {
		t.Fatal("应报错")
	}
}

func TestWrongToken(t *testing.T) {
	srv := httptest.NewServer((&mockNapcat{token: "right"}).handler())
	t.Cleanup(srv.Close)
	c := New(Config{WebUIURL: srv.URL, Token: "wrong"})
	_, err := c.CheckLogin(context.Background())
	if err == nil || !strings.Contains(err.Error(), "WebUI 登录失败") {
		t.Fatalf("错误 token 应报 WebUI 登录失败: %v", err)
	}
}

func TestQRImageGeneration(t *testing.T) {
	// 内容 URL → 本地生成 PNG
	data, mime, err := QRImage(context.Background(), "https://txz.qq.com/p?k=abc*def&f=1600001615")
	if err != nil {
		t.Fatalf("QRImage: %v", err)
	}
	if mime != "image/png" || len(data) < 100 {
		t.Fatalf("PNG 生成异常: mime=%s len=%d", mime, len(data))
	}
	if _, err := png.Decode(bytes.NewReader(data)); err != nil {
		t.Fatalf("PNG 无法解码: %v", err)
	}
	// data URL 内联图片
	raw := "data:image/png;base64," + base64.StdEncoding.EncodeToString(testPNG)
	data, mime, err = QRImage(context.Background(), raw)
	if err != nil || mime != "image/png" || string(data) != string(testPNG) {
		t.Fatalf("data URL 解码异常: %v", err)
	}
	// 空内容
	if _, _, err := QRImage(context.Background(), "  "); err == nil {
		t.Fatal("空内容应报错")
	}
	// 非图片 data URL
	if _, _, err := QRImage(context.Background(), "data:text/plain;base64,aGk="); err == nil {
		t.Fatal("非图片 data URL 应报错")
	}
}

var testPNG = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x01, 0x02, 0x03}
