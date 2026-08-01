package state

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"qqbot/internal/rules"
)

// baseControl 构造一个合法配置。
func baseControl() *Control {
	return &Control{
		SchemaVersion: SchemaVersion,
		System: SystemConfig{
			BotName: "机器人",
			Owner:   10001,
			OneBot: OneBotConfig{
				WSURL:        "ws://127.0.0.1:3001",
				APITimeoutMs: 5000,
			},
			NapCat: NapCatConfig{WebUIURL: "http://127.0.0.1:6099"},
		},
		Groups: []GroupConfig{
			{GroupID: 111, Enabled: true, Remark: "主群"},
		},
	}
}

func checkFields(t *testing.T, err error, want ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("应校验失败，缺少: %v", want)
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("应为 ValidationError，实际 %v", err)
	}
	for _, f := range want {
		if _, ok := ve.Fields[f]; !ok {
			t.Fatalf("缺少字段错误 %q，现有: %v", f, ve.Fields)
		}
	}
}

func TestValidateBasics(t *testing.T) {
	// 合法配置通过
	if err := normalizeAndValidate(baseControl()); err != nil {
		t.Fatalf("合法配置不应失败: %v", err)
	}
	// owner 必须为正数
	c := baseControl()
	c.System.Owner = 0
	checkFields(t, normalizeAndValidate(c), "system.owner")
	// ws_url 必须 ws/wss
	c = baseControl()
	c.System.OneBot.WSURL = "http://127.0.0.1:3001"
	checkFields(t, normalizeAndValidate(c), "system.onebot.ws_url")
	// 空 ws_url
	c = baseControl()
	c.System.OneBot.WSURL = ""
	checkFields(t, normalizeAndValidate(c), "system.onebot.ws_url")
	// api_timeout 越界
	c = baseControl()
	c.System.OneBot.APITimeoutMs = 999999
	checkFields(t, normalizeAndValidate(c), "system.onebot.api_timeout_ms")
}

func TestValidateNapCatURL(t *testing.T) {
	// userinfo 拒绝
	c := baseControl()
	c.System.NapCat.WebUIURL = "http://user:pass@127.0.0.1:6099"
	checkFields(t, normalizeAndValidate(c), "system.napcat.webui_url")
	// 非法 scheme
	c = baseControl()
	c.System.NapCat.WebUIURL = "ws://127.0.0.1:6099"
	checkFields(t, normalizeAndValidate(c), "system.napcat.webui_url")
	// 空 = 可选
	c = baseControl()
	c.System.NapCat.WebUIURL = ""
	if err := normalizeAndValidate(c); err != nil {
		t.Fatalf("NapCat 可选: %v", err)
	}
}

func TestValidateGroups(t *testing.T) {
	// 群号重复
	c := baseControl()
	c.Groups = append(c.Groups, GroupConfig{GroupID: 111, Enabled: false})
	checkFields(t, normalizeAndValidate(c), "groups.1.group_id")
	// 群号非正数
	c = baseControl()
	c.Groups[0].GroupID = -5
	checkFields(t, normalizeAndValidate(c), "groups.0.group_id")
	// 备注纯数字
	c = baseControl()
	c.Groups[0].Remark = "12345"
	checkFields(t, normalizeAndValidate(c), "groups.0.remark")
	// 备注重复
	c = baseControl()
	c.Groups = append(c.Groups, GroupConfig{GroupID: 222, Remark: "主群"})
	checkFields(t, normalizeAndValidate(c), "groups.1.remark")
	// 备注过长
	c = baseControl()
	c.Groups[0].Remark = strings.Repeat("长", maxRemarkLen+1)
	checkFields(t, normalizeAndValidate(c), "groups.0.remark")
	// 禁用群同样校验
	c = baseControl()
	c.Groups[0].Enabled = false
	c.Groups[0].Remark = "123"
	checkFields(t, normalizeAndValidate(c), "groups.0.remark")
	// 白名单上限与正数
	c = baseControl()
	c.Groups[0].Whitelist = make([]int64, maxWhitelistLen+1)
	checkFields(t, normalizeAndValidate(c), "groups.0.whitelist")
	c = baseControl()
	c.Groups[0].Whitelist = []int64{0}
	checkFields(t, normalizeAndValidate(c), "groups.0.whitelist.0")
}

func TestValidateRules(t *testing.T) {
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	ruleWith := func(typ string, params string, event string) rules.Rule {
		if event == "" {
			event = rules.EventMessage
		}
		return rules.Rule{
			ID: "t1", Name: "测试规则", Enabled: true, Event: event,
			When: []rules.Condition{{Type: typ, Params: raw(params)}},
			Then: []rules.Action{{Type: "mute", Params: raw(`{"minutes": 10}`)}},
			Break: true,
		}
	}
	// action 未知条件类型
	c := baseControl()
	c.Groups[0].Rules = []rules.Rule{ruleWith("unknown_type", `{}`, "")}
	checkFields(t, normalizeAndValidate(c), "groups.0.rules.0.when.0.type")
	// 正则无效
	c = baseControl()
	c.Groups[0].Rules = []rules.Rule{ruleWith("text_regex", `{"pattern": "["}`, "")}
	checkFields(t, normalizeAndValidate(c), "groups.0.rules.0.when.0.params")
	// pattern 为空
	c = baseControl()
	c.Groups[0].Rules = []rules.Rule{ruleWith("text_contains", `{"text": ""}`, "")}
	checkFields(t, normalizeAndValidate(c), "groups.0.rules.0.when.0.params")
	// mute 时长越界
	c = baseControl()
	c.Groups[0].Rules = []rules.Rule{{
		ID: "t1", Name: "x", Enabled: true, Event: rules.EventMessage,
		When: []rules.Condition{{Type: "text_contains", Params: raw(`{"text": "x"}`)}},
		Then: []rules.Action{{Type: "mute", Params: raw(`{"minutes": 999999}`)}},
		Break: true,
	}}
	checkFields(t, normalizeAndValidate(c), "groups.0.rules.0.then.0.params")
	// 条件与事件不匹配（刷屏条件用于入群事件）
	c = baseControl()
	c.Groups[0].Rules = []rules.Rule{ruleWith("flood", `{"window_sec": 10, "max_count": 8}`, rules.EventGroupIncrease)}
	checkFields(t, normalizeAndValidate(c), "groups.0.rules.0.when.0.type")
	// 规则名重复
	c = baseControl()
	c.Groups[0].Rules = []rules.Rule{ruleWith("text_contains", `{"text": "a"}`, ""), ruleWith("text_contains", `{"text": "b"}`, "")}
	checkFields(t, normalizeAndValidate(c), "groups.0.rules.1.name")
	// ID 非法
	c = baseControl()
	c.Groups[0].Rules = []rules.Rule{{
		ID: "bad id!", Name: "x", Enabled: true, Event: rules.EventMessage,
		Then: []rules.Action{{Type: "noop"}}, Break: true,
	}}
	checkFields(t, normalizeAndValidate(c), "groups.0.rules.0.id")
	// 合法规则通过（含正则）
	c = baseControl()
	c.Groups[0].Rules = []rules.Rule{{
		ID: "t1", Name: "合法", Enabled: true, Event: rules.EventMessage,
		When: []rules.Condition{{
			Type: "text_regex", Params: raw(`{"pattern": "^a.+b$"}`),
		}},
		Then: []rules.Action{{Type: "kick", Params: raw(`{"reason": "违规"}`)}},
		Break: true,
	}}
	if err := normalizeAndValidate(c); err != nil {
		t.Fatalf("合法规则不应失败: %v", err)
	}
	// 规则数上限
	c = baseControl()
	rs := make([]rules.Rule, rules.MaxRulesPerGroup+1)
	for i := range rs {
		rs[i] = rules.Rule{
			ID: "r" + strconv.Itoa(i), Name: "规则" + strconv.Itoa(i),
			Enabled: true, Event: rules.EventMessage,
			Then: []rules.Action{{Type: "noop"}}, Break: true,
		}
	}
	c.Groups[0].Rules = rs
	checkFields(t, normalizeAndValidate(c), "groups.0.rules")
}

func TestValidateControlChars(t *testing.T) {
	c := baseControl()
	c.Groups[0].Rules = []rules.Rule{{
		ID: "t1", Name: "x", Enabled: true, Event: rules.EventMessage,
		When: []rules.Condition{{
			Type: "text_contains", Params: json.RawMessage(`{"text": "欢迎\u0007"}`), // BEL 控制字符
		}},
		Then: []rules.Action{{Type: "noop"}}, Break: true,
	}}
	checkFields(t, normalizeAndValidate(c), "groups.0.rules.0.when.0.params")
	// 换行允许
	c = baseControl()
	c.Groups[0].Rules = []rules.Rule{{
		ID: "t1", Name: "x", Enabled: true, Event: rules.EventMessage,
		When: []rules.Condition{{
			Type: "text_contains", Params: json.RawMessage(`{"text": "第一行\n第二行"}`),
		}},
		Then: []rules.Action{{Type: "noop"}}, Break: true,
	}}
	if err := normalizeAndValidate(c); err != nil {
		t.Fatalf("多行文本不应失败: %v", err)
	}
	// 消息模板过长
	c = baseControl()
	long := strings.Repeat("长", maxTextLen+1)
	c.Groups[0].Rules = []rules.Rule{{
		ID: "t1", Name: "x", Enabled: true, Event: rules.EventMessage,
		Then: []rules.Action{{
			Type: "send_message", Params: json.RawMessage(`{"message": "` + long + `"}`),
		}},
		Break: true,
	}}
	checkFields(t, normalizeAndValidate(c), "groups.0.rules.0.then.0.params")
}
