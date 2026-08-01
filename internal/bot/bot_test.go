package bot

import (
	"testing"
	"time"

	"qqbot/internal/config"
	"qqbot/internal/onebot"
	"qqbot/internal/rules"
)

func TestExtractText(t *testing.T) {
	m := onebot.GroupMessage{
		RawMessage: "[CQ:at,qq=1] 你好",
		Message: []onebot.CQMsg{
			{Type: "at", Data: map[string]any{"qq": "1"}},
			{Type: "text", Data: map[string]any{"text": " 你好"}},
		},
	}
	if got := extractText(m); got != " 你好" {
		t.Fatalf("extractText 应剔除 CQ 码，得到 %q", got)
	}
	// 纯文本不受影响
	m2 := onebot.GroupMessage{RawMessage: "欢迎加入 123456789 群"}
	if got := extractText(m2); got != "欢迎加入 123456789 群" {
		t.Fatalf("纯文本应原样保留，得到 %q", got)
	}
}

func TestExtractMsgTypes(t *testing.T) {
	m := onebot.GroupMessage{
		RawMessage: "[CQ:at,qq=1][CQ:image,file=x.png] 看图 https://example.com/x",
		Message: []onebot.CQMsg{
			{Type: "at", Data: map[string]any{"qq": "1"}},
			{Type: "image", Data: map[string]any{"file": "x.png"}},
			{Type: "text", Data: map[string]any{"text": " 看图 https://example.com/x"}},
		},
	}
	got := extractMsgTypes(m)
	for _, want := range []string{"at", "image", "text", "url"} {
		found := false
		for _, g := range got {
			if g == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("缺少类型 %s: %v", want, got)
		}
	}
	// 纯文本无 CQ 类型
	if got := extractMsgTypes(onebot.GroupMessage{RawMessage: "普通消息"}); len(got) != 1 || got[0] != "text" {
		t.Fatalf("纯文本类型错误: %v", got)
	}
	// 链接归类为 url
	if got := extractMsgTypes(onebot.GroupMessage{RawMessage: "看 http://x.com"}); len(got) != 2 {
		t.Fatalf("链接应归类 url: %v", got)
	}
}

// 规则命中 warn：审计应记录（发送失败不阻断审计，与旧行为一致）。
func TestKeywordWarnViaEngine(t *testing.T) {
	b := newTestBotWithRules(t, config.GroupConfig{
		GroupID: 123456789, Enabled: true,
		Rules: []rules.Rule{warnRule("r1", "广告", "加我领取", "禁止广告")},
	})
	raw := rawJSON(onebot.GroupMessage{
		MessageType: "group", GroupID: 123456789, UserID: 55555, MessageID: 1,
		RawMessage: "加我领取福利", SelfID: 100,
		Sender: onebot.Sender{UserID: 55555, Nickname: "测试", Role: "member"},
	})
	if err := b.onMessage(raw); err != nil {
		t.Fatalf("群消息处理失败: %v", err)
	}
	page := b.QueryAudit(AuditQuery{GroupID: 123456789, Limit: 10})
	if len(page.Entries) == 0 {
		t.Fatal("应有审计记录")
	}
	e := page.Entries[0]
	if e.Action != "警告" || e.ActorType != "automatic" || e.TargetID != 55555 {
		t.Fatalf("审计记录错误: %+v", e)
	}
	if e.Detail == "" || e.Detail == "规则「广告」" {
		t.Fatalf("审计应带规则名: %q", e.Detail)
	}
	// 未命中关键词：无新审计
	before := len(page.Entries)
	raw2 := rawJSON(onebot.GroupMessage{
		MessageType: "group", GroupID: 123456789, UserID: 55555, MessageID: 2,
		RawMessage: "正常聊天", SelfID: 100,
		Sender: onebot.Sender{UserID: 55555, Role: "member"},
	})
	if err := b.onMessage(raw2); err != nil {
		t.Fatal(err)
	}
	if after := len(b.QueryAudit(AuditQuery{GroupID: 123456789, Limit: 10}).Entries); after != before {
		t.Fatal("未命中不应产生审计")
	}
}

// 豁免规则（is_exempt）置顶：管理员/群主/白名单/owner 不受处罚。
func TestExemptRulesViaEngine(t *testing.T) {
	b := newTestBotWithRules(t, config.GroupConfig{
		GroupID: 123456789, Enabled: true,
		Whitelist: []int64{777},
		Rules: []rules.Rule{
			exemptRule("r1"),
			warnRule("r2", "广告", "加我领取", "禁止广告"),
		},
	})
	send := func(userID int64, role string) {
		t.Helper()
		raw := rawJSON(onebot.GroupMessage{
			MessageType: "group", GroupID: 123456789, UserID: userID, MessageID: 1,
			RawMessage: "加我领取福利", SelfID: 100,
			Sender: onebot.Sender{UserID: userID, Role: role},
		})
		if err := b.onMessage(raw); err != nil {
			t.Fatalf("处理失败: %v", err)
		}
	}
	// 群主 / 管理员 / 白名单 / owner 全部豁免
	for _, tc := range []struct{ uid int64; role string }{
		{111, "owner"}, {222, "admin"}, {777, "member"}, {10001, "member"},
	} {
		send(tc.uid, tc.role)
		if n := len(b.QueryAudit(AuditQuery{GroupID: 123456789, Limit: 10}).Entries); n != 0 {
			t.Fatalf("用户 %d（%s）应被豁免: %+v", tc.uid, tc.role, b.QueryAudit(AuditQuery{Limit: 5}).Entries)
		}
	}
	// 普通成员触发
	send(55555, "member")
	if n := len(b.QueryAudit(AuditQuery{GroupID: 123456789, Limit: 10}).Entries); n != 1 {
		t.Fatal("普通成员应被处罚")
	}
}

// 新人入群：规则引擎发送欢迎（无连接时静默失败，不 panic）。
func TestWelcomeOnJoin(t *testing.T) {
	b := newTestBotWithRules(t, config.GroupConfig{
		GroupID: 123456789, Enabled: true,
		Rules: []rules.Rule{{
			ID: "r1", Name: "新人欢迎", Enabled: true, Event: rules.EventGroupIncrease,
			Then: []rules.Action{{Type: "send_message", Params: mustJSONParams(rules.SendMessageParams{
				Message: "欢迎 {nickname}", At: true,
			})}},
			Break: true,
		}},
	})
	raw := rawJSON(map[string]any{
		"post_type": "notice", "notice_type": "group_increase",
		"group_id": 123456789, "user_id": 8888, "time": time.Now().Unix(),
	})
	if err := b.onNotice(raw); err != nil {
		t.Fatalf("入群通知处理失败: %v", err)
	}
	// 非 group_increase 通知忽略
	raw = rawJSON(map[string]any{"post_type": "notice", "notice_type": "group_decrease", "group_id": 123456789})
	if err := b.onNotice(raw); err != nil {
		t.Fatal(err)
	}
}

// 加群申请：规则引擎审批（无连接时审计仍记录）。
func TestJoinRequestViaEngine(t *testing.T) {
	b := newTestBotWithRules(t, config.GroupConfig{
		GroupID: 123456789, Enabled: true,
		Rules: []rules.Rule{
			{
				ID: "r1", Name: "拒绝中介", Enabled: true, Event: rules.EventGroupRequest,
				When: []rules.Condition{{Type: "request_contains", Params: mustJSONParams(rules.TextContainsParams{Text: "中介"})}},
				Then: []rules.Action{{Type: "reject_join", Params: mustJSONParams(rules.RejectJoinParams{Reason: "不允许中介"})}},
				Break: true,
			},
			{
				ID: "r2", Name: "自动同意", Enabled: true, Event: rules.EventGroupRequest,
				When: []rules.Condition{{Type: "subtype_is", Params: mustJSONParams(rules.SubtypeParams{Subtype: "add"})}},
				Then: []rules.Action{{Type: "approve_join"}},
				Break: true,
			},
		},
	})
	req := func(comment string) []byte {
		return rawJSON(onebot.GroupRequest{
			RequestType: "group", GroupID: 123456789, UserID: 9999,
			Comment: comment, Flag: "f1", SubType: "add", Time: time.Now().Unix(),
		})
	}
	if err := b.onRequest(req("想加群")); err != nil {
		t.Fatal(err)
	}
	if err := b.onRequest(req("我是中介")); err != nil {
		t.Fatal(err)
	}
	page := b.QueryAudit(AuditQuery{GroupID: 123456789, Limit: 10})
	if len(page.Entries) != 2 {
		t.Fatalf("应有 2 条审计: %+v", page.Entries)
	}
	// 拒绝在前（倒序）
	if page.Entries[0].Action != "拒绝入群申请" || page.Entries[1].Action != "同意入群申请" {
		t.Fatalf("审计动作错误: %+v", page.Entries)
	}
}

// 邀请机器人入群：默认规则仅 owner 同意、其余拒绝（无连接时审计记录）。
func TestInviteViaEngine(t *testing.T) {
	b := newTestBotWithRules(t, config.GroupConfig{
		GroupID: 123456789, Enabled: true,
		Rules: []rules.Rule{
			{
				ID: "r1", Name: "同意邀请", Enabled: true, Event: rules.EventGroupRequest,
				When: []rules.Condition{
					{Type: "subtype_is", Params: mustJSONParams(rules.SubtypeParams{Subtype: "invite"})},
					{Type: "is_owner"},
				},
				Then: []rules.Action{{Type: "approve_join"}}, Break: true,
			},
			{
				ID: "r2", Name: "拒绝邀请", Enabled: true, Event: rules.EventGroupRequest,
				When: []rules.Condition{{Type: "subtype_is", Params: mustJSONParams(rules.SubtypeParams{Subtype: "invite"})}},
				Then: []rules.Action{{Type: "reject_join", Params: mustJSONParams(rules.RejectJoinParams{Reason: "机器人不接受邀请"})}},
				Break: true,
			},
		},
	})
	invite := func(userID int64) []byte {
		return rawJSON(onebot.GroupRequest{
			RequestType: "group", GroupID: 123456789, UserID: userID,
			Comment: "", Flag: "f2", SubType: "invite", Time: time.Now().Unix(),
		})
	}
	if err := b.onRequest(invite(10001)); err != nil { // owner
		t.Fatal(err)
	}
	if err := b.onRequest(invite(6666)); err != nil { // 非 owner
		t.Fatal(err)
	}
	page := b.QueryAudit(AuditQuery{GroupID: 123456789, Limit: 10})
	if len(page.Entries) != 2 {
		t.Fatalf("应有 2 条审计: %+v", page.Entries)
	}
	if page.Entries[0].Action != "拒绝机器人入群邀请" || page.Entries[1].Action != "接受机器人入群邀请" {
		t.Fatalf("邀请审批审计错误: %+v", page.Entries)
	}
}

// 未配置的群：事件一律忽略。
func TestUnconfiguredGroupIgnored(t *testing.T) {
	b := newTestBot(t)
	raw := rawJSON(onebot.GroupMessage{
		MessageType: "group", GroupID: 999999, UserID: 55555,
		RawMessage: "加我领取", SelfID: 100,
		Sender: onebot.Sender{UserID: 55555, Role: "member"},
	})
	if err := b.onMessage(raw); err != nil {
		t.Fatal(err)
	}
	if n := len(b.QueryAudit(AuditQuery{Limit: 10}).Entries); n != 0 {
		t.Fatal("未配置群不应产生任何副作用")
	}
}

func TestConfigValidation(t *testing.T) {
	cfg, err := config.Load("../../config.example.yaml")
	if err != nil {
		t.Fatalf("示例配置应通过校验: %v", err)
	}
	if len(cfg.Groups) != 2 {
		t.Fatalf("示例配置应有 2 个群，得到 %d", len(cfg.Groups))
	}
}
