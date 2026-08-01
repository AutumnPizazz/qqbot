// Package napcat 是 NapCat WebUI 的独立客户端：登录状态查询、二维码获取/
// 刷新/PNG 生成。
//
// 安全约束（设计文档 11 节）：
//   - credential 使用互斥 + singleflight 防止并发重复刷新；
//   - 401 时只重新认证一次；typed HTTP error 携带状态码，不靠字符串判断；
//   - 状态查询短时缓存，避免页面轮询击穿 NapCat；
//   - 二维码由后端生成 PNG，NapCat token 不进入浏览器；
//   - 二维码内容、credential、URL 参数不得进入审计或普通日志。
package napcat

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	qrcode "github.com/skip2/go-qrcode"
)

// apiTimeout 是单次 NapCat HTTP 请求超时。
const apiTimeout = 8 * time.Second

// statusCacheTTL 是登录状态查询的缓存时长（防页面轮询击穿）。
const statusCacheTTL = 1 * time.Second

// Config 是 NapCat WebUI 连接配置。
type Config struct {
	WebUIURL string // 如 http://127.0.0.1:6099
	Token    string // WebUI 密码（解密后的明文，仅进程内存持有）
	Timeout  time.Duration
}

// LoginStatus 是 CheckLoginStatus 接口的响应数据。
type LoginStatus struct {
	IsLogin    bool   `json:"isLogin"`
	IsOffline  bool   `json:"isOffline"`
	QRCodeURL  string `json:"qrcodeurl"`
	LoginError string `json:"loginError"`
}

// HTTPError 携带状态码的 typed HTTP 错误。
type HTTPError struct {
	StatusCode int
	Path       string
	Body       string // 已截断清洗的响应体
}

func (e *HTTPError) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("%s 返回 HTTP %d: %s", e.Path, e.StatusCode, e.Body)
	}
	return fmt.Sprintf("%s 返回 HTTP %d", e.Path, e.StatusCode)
}

// IsUnauthorized 判断是否 401（触发凭据重认证）。
func (e *HTTPError) IsUnauthorized() bool { return e.StatusCode == http.StatusUnauthorized }

// Client 是 NapCat WebUI 的轻量 API 客户端。
type Client struct {
	base  string
	token string
	http  *http.Client

	sf      singleflight.Group // 防并发重复刷新 credential
	credMu  sync.Mutex
	cred    string
	credExp time.Time

	statusMu   sync.Mutex
	status     *LoginStatus
	statusTime time.Time
}

// New 创建客户端。token 为空时跳过 WebUI 登录（部分环境允许免鉴权调用）。
func New(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = apiTimeout
	}
	return &Client{
		base:  strings.TrimRight(cfg.WebUIURL, "/"),
		token: cfg.Token,
		http:  &http.Client{Timeout: timeout},
	}
}

// CheckLogin 查询 QQ 登录状态（短时缓存，防轮询击穿）。
func (c *Client) CheckLogin(ctx context.Context) (*LoginStatus, error) {
	c.statusMu.Lock()
	if c.status != nil && time.Since(c.statusTime) < statusCacheTTL {
		st := *c.status
		c.statusMu.Unlock()
		return &st, nil
	}
	c.statusMu.Unlock()
	return c.checkLoginFresh(ctx)
}

// checkLoginFresh 强制刷新登录状态（供二维码刷新轮询使用）。
func (c *Client) checkLoginFresh(ctx context.Context) (*LoginStatus, error) {
	var out struct {
		Code int          `json:"code"`
		Data *LoginStatus `json:"data"`
	}
	if err := c.call(ctx, "POST", "/api/QQLogin/CheckLoginStatus", nil, &out); err != nil {
		return nil, err
	}
	if out.Code != 0 || out.Data == nil {
		return nil, fmt.Errorf("CheckLoginStatus 返回异常: code=%d", out.Code)
	}
	c.statusMu.Lock()
	st := *out.Data
	c.status = &st
	c.statusTime = time.Now()
	c.statusMu.Unlock()
	return out.Data, nil
}

// GetQRCode 获取当前登录二维码 URL（等价于前端 GetQQLoginQrcode）。
func (c *Client) GetQRCode(ctx context.Context) (string, error) {
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"message"`
		Data *struct {
			QRCode string `json:"qrcode"`
		} `json:"data"`
	}
	if err := c.call(ctx, "POST", "/api/QQLogin/GetQQLoginQrcode", nil, &out); err != nil {
		return "", err
	}
	if out.Code != 0 || out.Data == nil {
		return "", fmt.Errorf("GetQQLoginQrcode 失败: %s", out.Msg)
	}
	return out.Data.QRCode, nil
}

// RefreshQRCode 让 NapCat 重新生成登录二维码（原二维码过期后调用）。
func (c *Client) RefreshQRCode(ctx context.Context) error {
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"message"`
	}
	if err := c.call(ctx, "POST", "/api/QQLogin/RefreshQRcode", nil, &out); err != nil {
		return err
	}
	if out.Code != 0 {
		return fmt.Errorf("RefreshQRcode 失败: %s", out.Msg)
	}
	return nil
}

// call 发起带鉴权的 API 请求；401 时重新认证一次后重试。
func (c *Client) call(ctx context.Context, method, path string, body any, out any) error {
	if err := c.ensureCred(ctx); err != nil {
		return err
	}
	err := c.do(ctx, method, path, body, out)
	if he, ok := err.(*HTTPError); ok && he.IsUnauthorized() {
		// credential 过期：重新认证一次
		c.invalidateCred()
		if cerr := c.ensureCred(ctx); cerr != nil {
			return cerr
		}
		return c.do(ctx, method, path, body, out)
	}
	return err
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = strings.NewReader(string(data))
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.credMu.Lock()
	cred := c.cred
	c.credMu.Unlock()
	if cred != "" {
		req.Header.Set("Authorization", "Bearer "+cred)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("请求 %s 失败: %w", path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return &HTTPError{StatusCode: resp.StatusCode, Path: path, Body: truncate(string(data), 200)}
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("%s 响应解析失败: %w", path, err)
		}
	}
	return nil
}

func (c *Client) invalidateCred() {
	c.credMu.Lock()
	c.cred = ""
	c.credExp = time.Time{}
	c.credMu.Unlock()
}

// ensureCred 用 WebUI 密码换取 Bearer 凭证（有效期 1 小时，缓存复用）。
// singleflight 保证并发调用只触发一次登录请求。
func (c *Client) ensureCred(ctx context.Context) error {
	c.credMu.Lock()
	valid := c.cred != "" && time.Now().Before(c.credExp)
	c.credMu.Unlock()
	if valid {
		return nil
	}
	_, err, _ := c.sf.Do("login", func() (any, error) {
		// 双重检查：singleflight 并发等待者可能拿到已刷新的凭证
		c.credMu.Lock()
		valid := c.cred != "" && time.Now().Before(c.credExp)
		c.credMu.Unlock()
		if valid {
			return nil, nil
		}
		return nil, c.doLogin(ctx)
	})
	return err
}

// doLogin 执行一次 WebUI 登录并缓存凭证。
func (c *Client) doLogin(ctx context.Context) error {
	if c.token == "" {
		return fmt.Errorf("未配置 WebUI token，无法认证")
	}
	hash := sha256.Sum256([]byte(c.token + ".napcat"))
	reqBody := map[string]string{"hash": hex.EncodeToString(hash[:])}
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"message"`
		Data *struct {
			Credential string `json:"Credential"`
		} `json:"data"`
	}
	if err := c.do(ctx, "POST", "/api/auth/login", reqBody, &out); err != nil {
		return fmt.Errorf("WebUI 登录失败: %w", err)
	}
	if out.Code != 0 || out.Data == nil || out.Data.Credential == "" {
		return fmt.Errorf("WebUI 登录失败: %s（请检查 WebUI token 是否为当前密码）", out.Msg)
	}
	c.credMu.Lock()
	c.cred = out.Data.Credential
	c.credExp = time.Now().Add(50 * time.Minute)
	c.credMu.Unlock()
	return nil
}

// QRImage 生成二维码图片字节（PNG）与 MIME：
//   - 内容是 data:image/...;base64 内联图片 → 直接解码；
//   - 其余情况（如 https://txz.qq.com/p?k=...）是二维码**内容**而非图片地址，
//     NapCat WebUI 前端（qrcode.react）正是把它本地渲染成二维码，这里用 go-qrcode 等价生成。
func QRImage(ctx context.Context, content string) ([]byte, string, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, "", fmt.Errorf("二维码内容为空")
	}
	if strings.HasPrefix(content, "data:") {
		return decodeDataURL(content)
	}
	png, err := qrcode.Encode(content, qrcode.Medium, 256)
	if err != nil {
		return nil, "", fmt.Errorf("生成二维码失败: %w", err)
	}
	return png, "image/png", nil
}

// decodeDataURL 解析 data:image/png;base64,xxx 形式的二维码数据。
func decodeDataURL(raw string) ([]byte, string, error) {
	rest := raw[len("data:"):]
	meta, payload, ok := strings.Cut(rest, ",")
	if !ok {
		return nil, "", fmt.Errorf("data URL 格式无效")
	}
	meta = strings.ToLower(meta)
	if !strings.HasPrefix(meta, "image/") || !strings.Contains(meta, ";base64") {
		return nil, "", fmt.Errorf("data URL 不是 base64 图片: %s", truncate(meta, 80))
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(payload))
	if err != nil {
		return nil, "", fmt.Errorf("二维码 base64 解码失败: %w", err)
	}
	mime := strings.SplitN(meta, ";", 2)[0]
	return data, mime, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// InvalidateCredForTest 使缓存的 credential 失效（仅测试使用）。
func (c *Client) InvalidateCredForTest() {
	c.invalidateCred()
}
