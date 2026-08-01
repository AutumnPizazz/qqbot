package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestE2EOneBotDown 验证 OneBot 掉线后：动作 API 返回 503、状态变为重连中。
func TestE2EOneBotDown(t *testing.T) {
	env := newE2E(t)
	cookie, csrf := env.setupAndLogin()
	env.initConfig(cookie, csrf)
	env.startBot()
	env.oneBot.waitConnected(3 * time.Second)

	// 模拟 OneBot 服务端断开连接
	env.oneBot.dropConn()

	// 等待 Manager 感知断线（readLoop 退出 → reconnecting/disconnected）
	deadline := time.Now().Add(10 * time.Second)
	got503 := false
	for time.Now().Before(deadline) {
		resp := env.doH(http.MethodPost, "/api/v1/groups/123456789/actions/unmute",
			map[string]any{"target_id": 1}, cookie, csrf,
			map[string]string{"Idempotency-Key": "e2e-down-1"})
		if resp.Code == http.StatusServiceUnavailable {
			got503 = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !got503 {
		t.Fatal("OneBot 掉线后动作应返回 503")
	}
	// 状态接口应反映非 connected
	var st struct {
		OneBot struct {
			Status string `json:"status"`
		} `json:"onebot"`
	}
	resp := env.do(http.MethodGet, "/api/v1/status", nil, cookie, csrf)
	if resp.Code == http.StatusOK {
		_ = json.Unmarshal(resp.Body.Bytes(), &st)
		if st.OneBot.Status == "connected" {
			t.Fatal("OneBot 掉线后状态不应为 connected")
		}
	}
	// 掉线期间不应写动作审计（动作未执行）
	auditResp := env.do(http.MethodGet, "/api/v1/audit?limit=50", nil, cookie, csrf)
	if strings.Contains(auditResp.Body.String(), "e2e-down-1") {
		t.Fatal("未执行的动作不应写入审计")
	}
}
