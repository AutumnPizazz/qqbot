package bot

import (
	"encoding/json"
	"testing"
	"time"

	"qqbot/internal/config"
	"qqbot/internal/onebot"
	"qqbot/internal/rules"
)

// newTestBot 创建测试用 Bot（临时目录存放运行时数据，连接未建立）。
func newTestBot(t *testing.T) *Bot {
	t.Helper()
	cfg := baseCfg()
	cfg.Bot.Owner = 10001
	return NewWithDir(cfg,
		onebot.NewManager(onebot.Endpoint{URL: "ws://127.0.0.1:3001", Timeout: time.Second}),
		t.TempDir())
}

// newTestBotWithRules 创建带指定群规则集的 Bot。
func newTestBotWithRules(t *testing.T, groups ...config.GroupConfig) *Bot {
	t.Helper()
	cfg := baseCfg()
	cfg.Bot.Owner = 10001
	cfg.Groups = groups
	b := NewWithDir(cfg,
		onebot.NewManager(onebot.Endpoint{URL: "ws://127.0.0.1:3001", Timeout: time.Second}),
		t.TempDir())
	return b
}

// baseCfg 最小合法生效配置。
func baseCfg() *config.Config {
	cfg, _ := config.Load("../../config.example.yaml")
	if cfg != nil {
		return cfg
	}
	return &config.Config{
		Bot:    config.BotConfig{Name: "机器人", Owner: 10001, Loc: time.Local},
		OneBot: config.OneBotConfig{WSURL: "ws://127.0.0.1:3001", APITimeoutMs: 5000},
	}
}

// rawJSON 快速构造消息事件 JSON。
func rawJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// warnRule 构造一条关键词警告规则。
func warnRule(id, name, keyword, reason string) rules.Rule {
	return rules.Rule{
		ID: id, Name: name, Enabled: true, Event: rules.EventMessage,
		When: []rules.Condition{{
			Type: "text_contains", Params: mustJSONParams(rules.TextContainsParams{Text: keyword}),
		}},
		Then: []rules.Action{{Type: "warn", Params: mustJSONParams(rules.WarnParams{Reason: reason})}},
		Break: true,
	}
}

// exemptRule 管理员/白名单豁免规则（新群默认置顶）。
func exemptRule(id string) rules.Rule {
	return rules.Rule{
		ID: id, Name: "默认豁免", Enabled: true, Event: rules.EventMessage,
		When: []rules.Condition{{Type: "is_exempt"}},
		Then: nil, Break: true,
	}
}

func mustJSONParams(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
