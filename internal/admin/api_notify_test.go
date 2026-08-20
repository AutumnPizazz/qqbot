package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// doNotify 发送 notify 请求（可选 Bearer token）。
func doNotify(t *testing.T, ts *httptest.Server, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, ts.URL+"/api/v1/notify", strings.NewReader(string(data)))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	ts.Config.Handler.ServeHTTP(w, req)
	return w
}

func TestNotifyRequiresTokenConfigured(t *testing.T) {
	// 未配置 QQBOT_NOTIFY_TOKEN → 503 功能未启用
	t.Setenv("QQBOT_NOTIFY_TOKEN", "")
	ts, _, _ := testServer(t, nil)
	w := doNotify(t, ts, "any-token", map[string]any{"to": 2170191481, "text": "hi"})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("未配置 token 时应返回 503，got %d: %s", w.Code, w.Body.String())
	}
}

func TestNotifyAuth(t *testing.T) {
	t.Setenv("QQBOT_NOTIFY_TOKEN", "secret-token-123")
	ts, _, _ := testServer(t, nil)

	// 无 token / 错误 token → 401
	for _, tok := range []string{"", "wrong", "Bearer wrong"} {
		w := doNotify(t, ts, tok, map[string]any{"to": 2170191481, "text": "hi"})
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("token=%q 应返回 401，got %d", tok, w.Code)
		}
	}
	// 正确 token → 通过鉴权，但 OneBot 未启动 → 503 onebot_disconnected
	w := doNotify(t, ts, "secret-token-123", map[string]any{"to": 2170191481, "text": "hi"})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("OneBot 未启动时应返回 503，got %d: %s", w.Code, w.Body.String())
	}
}

func TestNotifyValidation(t *testing.T) {
	t.Setenv("QQBOT_NOTIFY_TOKEN", "secret-token-123")
	ts, _, _ := testServer(t, nil)

	cases := []struct {
		name string
		body any
		want int
	}{
		{"缺目标", map[string]any{"text": "hi"}, http.StatusUnprocessableEntity},
		{"to+group 冲突", map[string]any{"to": 1, "group_id": 2, "text": "hi"}, http.StatusUnprocessableEntity},
		{"text 为空", map[string]any{"to": 1, "text": ""}, http.StatusUnprocessableEntity},
		{"非 JSON", "not-json", http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := doNotify(t, ts, "secret-token-123", c.body)
			if w.Code != c.want {
				t.Fatalf("应返回 %d，got %d: %s", c.want, w.Code, w.Body.String())
			}
		})
	}
}
