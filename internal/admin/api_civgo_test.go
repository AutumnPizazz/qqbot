package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"qqbot/internal/state"
)

// fakeCivgoAdmin 测试用 CivgoAdmin 实现。
type fakeCivgoAdmin struct {
	cfg        map[string]any
	configured bool
	status     map[string]any
	saved      json.RawMessage
	saveErr    error
	effective  bool
	testErr    error
}

func (f *fakeCivgoAdmin) Get() (map[string]any, bool) {
	return f.cfg, f.configured
}
func (f *fakeCivgoAdmin) Save(raw json.RawMessage) (bool, error) {
	f.saved = raw
	return f.effective, f.saveErr
}
func (f *fakeCivgoAdmin) Status() map[string]any { return f.status }
func (f *fakeCivgoAdmin) TestAI() error          { return f.testErr }

// testServerCivgo 组装带 civgo 接入的后台。
func testServerCivgo(t *testing.T, fake *fakeCivgoAdmin) (*httptest.Server, *Server) {
	t.Helper()
	ts, s, _ := testServer(t, func(c *state.Control) {
		c.System.BotName = "test"
		c.System.Owner = 10001
		c.System.OneBot.WSURL = "ws://127.0.0.1:3001"
	})
	s.opts.Civgo = fake
	return ts, s
}

func TestCivgoAPI(t *testing.T) {
	fake := &fakeCivgoAdmin{
		cfg:        map[string]any{"enabled": true, "groups": []any{111}},
		configured: true,
		status:     map[string]any{"docmap_files": 15},
	}
	ts, s := testServerCivgo(t, fake)
	c, _ := doSetup(t, ts, s)
	csrf := getCSRF(t, ts, c)

	// GET：脱敏配置 + 状态
	resp := doRequest(ts, http.MethodGet, "/api/v1/civgo", nil, c, csrf)
	if resp.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/civgo 应 200，got %d", resp.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["configured"] != true || body["enabled"] != true {
		t.Errorf("GET 响应错误: %v", body)
	}
	st, _ := body["status"].(map[string]any)
	if st["docmap_files"] != float64(15) {
		t.Errorf("status 应透传: %v", body["status"])
	}

	// PUT：保存透传
	putBody := map[string]any{"config": map[string]any{"enabled": true}}
	resp = doRequest(ts, http.MethodPut, "/api/v1/civgo", putBody, c, csrf)
	if resp.Code != http.StatusOK {
		t.Fatalf("PUT 应 200，got %d: %s", resp.Code, resp.Body.String())
	}
	if fake.saved == nil {
		t.Fatal("Save 应被调用")
	}

	// PUT 校验失败 → 422
	fake.saveErr = errors.New("repo.url 不能为空")
	resp = doRequest(ts, http.MethodPut, "/api/v1/civgo", putBody, c, csrf)
	if resp.Code != http.StatusUnprocessableEntity {
		t.Fatalf("校验失败应 422，got %d", resp.Code)
	}
	fake.saveErr = nil

	// 未配置：GET 返回 configured=false
	fake.configured = false
	resp = doRequest(ts, http.MethodGet, "/api/v1/civgo", nil, c, csrf)
	json.Unmarshal(resp.Body.Bytes(), &body)
	if body["configured"] != false {
		t.Errorf("未配置应返回 configured=false: %v", body)
	}
	fake.configured = true

	// test-ai：成功与失败
	resp = doRequest(ts, http.MethodPost, "/api/v1/civgo/test-ai", nil, c, csrf)
	var tr map[string]any
	json.Unmarshal(resp.Body.Bytes(), &tr)
	if tr["ok"] != true {
		t.Errorf("test-ai 应成功: %v", tr)
	}
	fake.testErr = errors.New("网关不支持")
	resp = doRequest(ts, http.MethodPost, "/api/v1/civgo/test-ai", nil, c, csrf)
	json.Unmarshal(resp.Body.Bytes(), &tr)
	if tr["ok"] != false || tr["detail"] != "网关不支持" {
		t.Errorf("test-ai 失败应透传: %v", tr)
	}
}
