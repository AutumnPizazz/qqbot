package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestE2EFullLifecycle 端到端主流程：setup → 初始化 → Bot 事件响应 → 人工动作 →
// 审计 → 二维码 → 热重连 → 历史恢复 → 改密。
func TestE2EFullLifecycle(t *testing.T) {
	env := newE2E(t)
	cookie, csrf := env.setupAndLogin()

	// 1. 初始化配置（OneBot/NapCat + 第一个群）
	env.initConfig(cookie, csrf)

	// 2. 未初始化检查已过；启动 Bot（模拟 main.go 网页化启动）
	env.startBot()

	// 3. OneBot 连接建立
	env.oneBot.waitConnected(3 * time.Second)
	waitStatus := func(want string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			var st struct {
				OneBot struct {
					Status string `json:"status"`
				} `json:"onebot"`
			}
			resp := env.do(http.MethodGet, "/api/v1/status", nil, cookie, csrf)
			if resp.Code == http.StatusOK {
				_ = json.Unmarshal(resp.Body.Bytes(), &st)
				if st.OneBot.Status == want {
					return
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("OneBot 状态未达到 %s", want)
	}
	waitStatus("connected")

	// 4. 连接测试 API
	resp := env.do(http.MethodPost, "/api/v1/settings/test-onebot", map[string]any{}, cookie, csrf)
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "验收机器人") {
		t.Fatalf("test-onebot 失败: %d %s", resp.Code, resp.Body.String())
	}
	resp = env.do(http.MethodPost, "/api/v1/settings/test-napcat", map[string]any{}, cookie, csrf)
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"ok":true`) {
		t.Fatalf("test-napcat 失败: %d %s", resp.Code, resp.Body.String())
	}

	// 5. 新人入群 → Bot 欢迎语（get_group_member_info + send_group_msg）
	env.oneBot.push(`{"post_type":"notice","notice_type":"group_increase","group_id":123456789,"user_id":222222,"operator_id":0,"self_id":100}`)
	member := env.oneBot.waitAction("get_group_member_info", 3*time.Second)
	if int64(member.Params["user_id"].(float64)) != 222222 {
		t.Fatalf("get_group_member_info 参数错误: %v", member.Params)
	}
	welcome := env.oneBot.waitAction("send_group_msg", 3*time.Second)
	msgText, _ := welcome.Params["message"].(string)
	if !strings.Contains(msgText, "新人小张") || !strings.Contains(msgText, "CQ:at,qq=222222") {
		t.Fatalf("欢迎语未包含昵称/@: %s", msgText)
	}

	// 6. 关键词 warn（群内发"广告" → @警告）
	env.oneBot.push(`{"post_type":"message","message_type":"group","group_id":123456789,"user_id":333333,"message_id":2001,"raw_message":"来点广告看看","message":[{"type":"text","data":{"text":"来点广告看看"}}],"sender":{"user_id":333333,"nickname":"张三","role":"member"},"self_id":100}`)
	warn := env.oneBot.waitAction("send_group_msg", 3*time.Second)
	if !strings.Contains(warn.Params["message"].(string), "广告") {
		t.Fatalf("警告消息未含关键词: %v", warn.Params["message"])
	}

	// 7. 关键词 mute（发"开挂" → set_group_ban 600s + 群通知）
	env.oneBot.push(`{"post_type":"message","message_type":"group","group_id":123456789,"user_id":444444,"message_id":2002,"raw_message":"开挂的人别来","message":[{"type":"text","data":{"text":"开挂的人别来"}}],"sender":{"user_id":444444,"nickname":"李四","role":"member"},"self_id":100}`)
	ban := env.oneBot.waitAction("set_group_ban", 3*time.Second)
	if int64(ban.Params["duration"].(float64)) != 600 {
		t.Fatalf("禁言时长应为 600s: %v", ban.Params)
	}
	if int64(ban.Params["group_id"].(float64)) != 123456789 {
		t.Fatalf("禁言群参数错误: %v", ban.Params)
	}

	// 8. 消息环形缓冲（网页撤回目标）
	resp = env.do(http.MethodGet, "/api/v1/groups/123456789/messages?limit=10", nil, cookie, csrf)
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "开挂的人别来") {
		t.Fatalf("messages 未记录: %d %s", resp.Code, resp.Body.String())
	}

	// 9. 人工动作 API（mute）：先不带幂等 key 检查 400
	resp = env.do(http.MethodPost, "/api/v1/groups/123456789/actions/mute",
		map[string]any{"target_id": 555555, "minutes": 30}, cookie, csrf)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("缺幂等 key 应 400，实际 %d", resp.Code)
	}
	// 带幂等 key 执行
	resp = env.doH(http.MethodPost, "/api/v1/groups/123456789/actions/mute",
		map[string]any{"target_id": 555555, "minutes": 30}, cookie, csrf,
		map[string]string{"Idempotency-Key": "e2e-mute-1"})
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"result":"ok"`) {
		t.Fatalf("人工禁言失败: %d %s", resp.Code, resp.Body.String())
	}
	act := env.oneBot.waitAction("set_group_ban", 3*time.Second)
	if int64(act.Params["user_id"].(float64)) != 555555 {
		t.Fatalf("人工禁言目标错误: %v", act.Params)
	}
	// 同一幂等 key 重放 → 命中缓存，不再执行动作
	before := len(env.oneBot.acts)
	resp = env.doH(http.MethodPost, "/api/v1/groups/123456789/actions/mute",
		map[string]any{"target_id": 555555, "minutes": 30}, cookie, csrf,
		map[string]string{"Idempotency-Key": "e2e-mute-1"})
	if resp.Code != http.StatusOK {
		t.Fatalf("幂等重放失败: %d", resp.Code)
	}
	if len(env.oneBot.acts) != before {
		t.Fatal("幂等 key 重放不应再次执行动作")
	}

	// 10. 审计记录（自动 + 人工）
	resp = env.do(http.MethodGet, "/api/v1/audit?limit=50", nil, cookie, csrf)
	if resp.Code != http.StatusOK {
		t.Fatalf("audit 失败: %d", resp.Code)
	}
	var ap struct {
		Entries []struct {
			Action       string `json:"action"`
			ActionStatus string `json:"action_status"`
			Source       string `json:"source"`
			RequestID    string `json:"request_id"`
		} `json:"entries"`
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &ap)
	if len(ap.Entries) < 3 {
		t.Fatalf("审计记录过少: %d\n%s", len(ap.Entries), resp.Body.String())
	}
	var hasAuto, hasManual, hasUnknown bool
	for _, e := range ap.Entries {
		if e.Source == "automatic" && e.ActionStatus == "ok" {
			hasAuto = true
		}
		if e.Source == "api" && e.ActionStatus == "ok" && e.RequestID == "e2e-mute-1" {
			hasManual = true
		}
	}
	if !hasAuto || !hasManual {
		t.Fatalf("审计缺少自动/人工记录: %+v", ap.Entries)
	}
	_ = hasUnknown

	// 11. NapCat 状态与二维码
	resp = env.do(http.MethodGet, "/api/v1/napcat/status", nil, cookie, csrf)
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"is_login":false`) {
		t.Fatalf("napcat 状态异常: %d %s", resp.Code, resp.Body.String())
	}
	qrResp := env.do(http.MethodGet, "/api/v1/napcat/qrcode.png", nil, cookie, csrf)
	if qrResp.Code != http.StatusOK {
		t.Fatalf("二维码接口失败: %d", qrResp.Code)
	}
	if !strings.HasPrefix(qrResp.Body.String(), "\x89PNG") {
		t.Fatalf("二维码不是 PNG: %q", qrResp.Body.String()[:8])
	}
	if cc := qrResp.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("二维码应禁止缓存: %s", cc)
	}
	// 刷新二维码
	resp = env.do(http.MethodPost, "/api/v1/napcat/qrcode/refresh", map[string]any{}, cookie, csrf)
	if resp.Code != http.StatusOK {
		t.Fatalf("刷新二维码失败: %d", resp.Code)
	}
	env.napcat.mu.Lock()
	refreshed := env.napcat.refresh
	env.napcat.mu.Unlock()
	if refreshed != 1 {
		t.Fatalf("NapCat 应收到 1 次刷新，实际 %d", refreshed)
	}

	// 12. 配置热重连：修改 ws_url → 切换连接到第二个 mock
	rev := env.svc.Revision()
	resp = env.do(http.MethodPut, "/api/v1/settings", map[string]any{
		"revision": rev,
		"system": map[string]any{
			"onebot": map[string]any{"ws_url": env.oneBot2.url},
		},
	}, cookie, csrf)
	if resp.Code != http.StatusOK {
		t.Fatalf("热重连配置失败: %d %s", resp.Code, resp.Body.String())
	}
	env.oneBot2.waitConnected(5 * time.Second)
	// 旧连接应已断开
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && env.oneBot.connected() {
		time.Sleep(50 * time.Millisecond)
	}
	if env.oneBot.connected() {
		t.Fatal("旧 OneBot 连接未关闭")
	}
	waitStatus("connected")
	// 新连接上动作可用
	resp = env.do(http.MethodPost, "/api/v1/settings/test-onebot", map[string]any{}, cookie, csrf)
	if !strings.Contains(resp.Body.String(), "验收机器人") {
		t.Fatalf("热重连后 test-onebot 失败: %s", resp.Body.String())
	}

	// 13. 历史恢复：恢复到 revision 1（初始系统设置，ws_url 回 oneBot）
	resp = env.do(http.MethodPost, "/api/v1/config/history/1/restore", map[string]any{}, cookie, csrf)
	if resp.Code != http.StatusOK {
		t.Fatalf("历史恢复失败: %d %s", resp.Code, resp.Body.String())
	}
	cur := env.svc.Current()
	if cur.System.OneBot.WSURL != env.oneBot.url {
		t.Fatalf("恢复后 ws_url 应回到初始值: %s", cur.System.OneBot.WSURL)
	}
	env.oneBot.waitConnected(5 * time.Second)

	// 14. 修改密码 → 全部 session 失效
	resp = env.do(http.MethodPut, "/api/v1/auth/password", map[string]any{
		"current_password": "password123", "new_password": "newpassword456",
	}, cookie, csrf)
	if resp.Code != http.StatusOK {
		t.Fatalf("改密失败: %d %s", resp.Code, resp.Body.String())
	}
	resp = env.do(http.MethodGet, "/api/v1/status", nil, cookie, csrf)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("改密后旧会话应 401，实际 %d", resp.Code)
	}
	// 新密码登录
	resp = postJSON(env.ts, "/api/v1/auth/login", map[string]any{"password": "newpassword456"}, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("新密码登录失败: %d", resp.Code)
	}
}
