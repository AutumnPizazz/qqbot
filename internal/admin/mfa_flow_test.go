package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"qqbot/internal/state"
)

// fakeMailer 记录发送请求（两步验证测试注入）。
type fakeMailer struct {
	mu   sync.Mutex
	sent []string // "to|subject|body"
	fail error
}

func (f *fakeMailer) Send(to, subject, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, to+"|"+subject+"|"+body)
	return f.fail
}

func (f *fakeMailer) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return ""
	}
	return f.sent[len(f.sent)-1]
}

// newMFAEnv 构建注入 fake 发信器的测试环境。
func newMFAEnv(t *testing.T) (*e2eEnv, *fakeMailer) {
	t.Helper()
	env := newE2E(t)
	mail := &fakeMailer{}
	env.s.mailOverride = mail
	return env, mail
}

// enableEmailMFA 直接修改配置开启邮箱两步验证。
func enableEmailMFA(t *testing.T, env *e2eEnv) {
	t.Helper()
	pass, err := env.keys.Encrypt("fake-smtp-pass", fieldEmailPass)
	if err != nil {
		t.Fatal(err)
	}
	_, err = env.svc.Update("admin", env.svc.Revision(), func(c *state.Control) error {
		c.System.Email = state.EmailConfig{
			Enabled: true, SMTPHost: "smtp.example.com", SMTPPort: 465,
			SMTPUser: "bot@example.com", SMTPPassword: pass, To: "admin@example.com",
		}
		return nil
	}, "开启邮箱验证")
	if err != nil {
		t.Fatal(err)
	}
}

// 两步登录全流程：密码 → 验证码（从 fake mailer 提取）→ 会话建立。
func TestLoginTwoStepFlow(t *testing.T) {
	env, mail := newMFAEnv(t)
	cookie, csrf := env.setupAndLogin()
	env.initConfig(cookie, csrf)
	enableEmailMFA(t, env)

	// 第一步：正确密码 → 返回 verify 票据（尚未建立会话）
	resp := env.do(http.MethodPost, "/api/v1/auth/login", map[string]any{
		"password": "password123",
	}, nil, "")
	if resp.Code != http.StatusOK {
		t.Fatalf("登录第一步失败: %d %s", resp.Code, resp.Body.String())
	}
	var step struct {
		Step   string `json:"step"`
		Ticket string `json:"ticket"`
		Email  string `json:"email"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &step); err != nil || step.Step != "verify" || step.Ticket == "" {
		t.Fatalf("响应错误: %v %s", err, resp.Body.String())
	}
	if !strings.Contains(step.Email, "***") {
		t.Fatalf("邮箱未脱敏: %q", step.Email)
	}

	// 从邮件提取验证码
	last := mail.last()
	if last == "" || !strings.Contains(last, "验证码") {
		t.Fatalf("未发送验证码邮件: %q", last)
	}
	code := extractCodeFromBody(last)

	// 错误验证码 → 401
	resp = env.do(http.MethodPost, "/api/v1/auth/verify", map[string]any{
		"ticket": step.Ticket, "code": "000000",
	}, nil, "")
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("错误验证码应 401: %d", resp.Code)
	}

	// 正确验证码 → 会话建立
	resp = env.do(http.MethodPost, "/api/v1/auth/verify", map[string]any{
		"ticket": step.Ticket, "code": code,
	}, nil, "")
	if resp.Code != http.StatusOK {
		t.Fatalf("验证失败: %d %s", resp.Code, resp.Body.String())
	}
	// 票据一次性：重复使用失败
	resp = env.do(http.MethodPost, "/api/v1/auth/verify", map[string]any{
		"ticket": step.Ticket, "code": code,
	}, nil, "")
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("重复使用票据应 401: %d", resp.Code)
	}
}

// 密码错误不触发验证码发送。
func TestLoginWrongPasswordNoEmail(t *testing.T) {
	env, mail := newMFAEnv(t)
	cookie, csrf := env.setupAndLogin()
	env.initConfig(cookie, csrf)
	enableEmailMFA(t, env)

	resp := env.do(http.MethodPost, "/api/v1/auth/login", map[string]any{
		"password": "wrong-password",
	}, nil, "")
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("错误密码应 401: %d", resp.Code)
	}
	if mail.last() != "" {
		t.Fatal("密码错误不应发送验证码")
	}
}

// 发信失败时不建立会话并销毁票据。
func TestLoginMailFailRejects(t *testing.T) {
	env, mail := newMFAEnv(t)
	cookie, csrf := env.setupAndLogin()
	env.initConfig(cookie, csrf)
	enableEmailMFA(t, env)
	mail.fail = errors.New("smtp down")

	resp := env.do(http.MethodPost, "/api/v1/auth/login", map[string]any{
		"password": "password123",
	}, nil, "")
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("发信失败应 500: %d %s", resp.Code, resp.Body.String())
	}
}

// 测试邮件发送接口。
func TestTestEmailEndpoint(t *testing.T) {
	env, mail := newMFAEnv(t)
	cookie, csrf := env.setupAndLogin()
	env.initConfig(cookie, csrf)
	enableEmailMFA(t, env)

	resp := env.do(http.MethodPost, "/api/v1/settings/test-email", map[string]any{}, cookie, csrf)
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"ok":true`) {
		t.Fatalf("测试邮件失败: %d %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(mail.last(), "测试邮件") {
		t.Fatal("测试邮件未发送")
	}
}

// extractCodeFromBody 从邮件正文提取 6 位数字验证码。
func extractCodeFromBody(raw string) string {
	parts := strings.SplitN(raw, "|", 3)
	if len(parts) != 3 {
		return ""
	}
	for _, tok := range strings.FieldsFunc(parts[2], func(r rune) bool {
		return r == ' ' || r == '\n' || r == '。' || r == '：' || r == ':' || r == '，'
	}) {
		if len(tok) == 6 && isDigits(tok) {
			return tok
		}
	}
	return ""
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
