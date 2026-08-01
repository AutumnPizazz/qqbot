package rules

import (
	"encoding/json"
	"fmt"
)

// ---- v1 旧版配置的最小输入结构（由 state 层从 control.json v1 解码后传入） ----

// V1Welcome 旧版新人欢迎。
type V1Welcome struct {
	Enabled bool
	Message string
}

// V1KeywordRule 旧版一条关键词规则。
type V1KeywordRule struct {
	Pattern     string
	Regex       bool
	Action      string // warn / mute / kick
	MuteMinutes int
}

// V1Keyword 旧版关键词过滤。
type V1Keyword struct {
	Enabled    bool
	WarnLimit  int // warn 累计升级禁言阈值，0=不升级
	MuteLimit  int // mute 累计升级移出阈值，0=不升级
	StrikeTTLH int // 计分有效期（小时），默认 24
	Rules      []V1KeywordRule
}

// V1Flood 旧版刷屏检测。
type V1Flood struct {
	Enabled       bool
	WindowSeconds int
	MaxMessages   int
	MuteMinutes   int
	KickOnRepeat  bool
	KickThreshold int
}

// V1JoinRequest 旧版加群请求自动处理。
type V1JoinRequest struct {
	AutoApprove bool
	Keyword     string
	RejectText  string
}

// InitialRules 新群的默认规则（与迁移生成的基底一致，保证默认安全）：
// 管理员/白名单豁免 + 机器人入群邀请审批（仅超级管理员可同意）。
func InitialRules(seq *int) []Rule {
	var out []Rule
	nextID := func() string {
		*seq++
		return fmt.Sprintf("r%d", *seq)
	}
	out = append(out, Rule{
		ID: nextID(), Name: "默认豁免", Enabled: true,
		Event: EventMessage, When: []Condition{{Type: "is_exempt"}},
		Then: []Action{}, Break: true,
	})
	out = append(out, inviteRules(nextID)...)
	return out
}

// MigrateV1 把旧版群配置转换为等价规则列表（设计文档 §8 映射）。
// seq 为跨群规则 ID 生成器（调用方持有，保证全局唯一）。
func MigrateV1(w *V1Welcome, k *V1Keyword, f *V1Flood, j *V1JoinRequest, seq *int) []Rule {
	out := InitialRules(seq)
	nextID := func() string {
		*seq++
		return fmt.Sprintf("r%d", *seq)
	}

	// 2. 新人欢迎
	if w != nil && w.Enabled && w.Message != "" {
		out = append(out, Rule{
			ID: nextID(), Name: "新人欢迎", Enabled: true,
			Event: EventGroupIncrease, When: nil,
			Then: []Action{{
				Type:   "send_message",
				Params: mustJSON(SendMessageParams{Message: w.Message, At: true}),
			}},
			Break: true,
		})
	}

	// 3. 关键词过滤（每条 1~2 条规则；升级规则必须排在普通规则之前）
	if k != nil && k.Enabled && len(k.Rules) > 0 {
		ttl := k.StrikeTTLH
		if ttl <= 0 {
			ttl = 24
		}
		for _, kr := range k.Rules {
			if kr.Pattern == "" {
				continue
			}
			condType := "text_contains"
			if kr.Regex {
				condType = "text_regex"
			}
			match := Condition{Type: condType, Params: mustJSON(TextRegexParams{Pattern: kr.Pattern})}
			if condType == "text_contains" {
				match.Params = mustJSON(TextContainsParams{Text: kr.Pattern})
			}
			baseName := "关键词-" + truncateRunes(kr.Pattern, 16)

			// 升级规则（前置）：计数 ≥ 阈值 → 更重处罚 + 清零
			if kr.Action == "warn" && k.WarnLimit > 0 {
				out = append(out, Rule{
					ID: nextID(), Name: baseName + "升级禁言", Enabled: true,
					Event: EventMessage,
					When: []Condition{
						match,
						{Type: "strike_count", Params: mustJSON(StrikeCountParams{
							CounterID: "kw", MinCount: k.WarnLimit, WindowHours: ttl,
						})},
					},
					Then: []Action{
						{Type: "mute", Params: mustJSON(MuteParams{Minutes: 30})},
						{Type: "reset_counter", Params: mustJSON(CounterParams{CounterID: "kw"})},
					},
					Break: true,
				})
			}
			if kr.Action == "mute" && k.MuteLimit > 0 {
				out = append(out, Rule{
					ID: nextID(), Name: baseName + "升级移出", Enabled: true,
					Event: EventMessage,
					When: []Condition{
						match,
						{Type: "strike_count", Params: mustJSON(StrikeCountParams{
							CounterID: "kw", MinCount: k.MuteLimit, WindowHours: ttl,
						})},
					},
					Then: []Action{
						{Type: "kick", Params: mustJSON(KickParams{Reason: "多次违规发布内容"})},
						{Type: "reset_counter", Params: mustJSON(CounterParams{CounterID: "kw"})},
					},
					Break: true,
				})
			}

			// 普通规则：处罚 + 计数
			acts := []Action{}
			switch kr.Action {
			case "warn":
				acts = append(acts, Action{Type: "warn", Params: mustJSON(WarnParams{
					Reason: "群内禁止发布内容：" + kr.Pattern, CounterID: "kw",
				})})
			case "mute":
				minutes := kr.MuteMinutes
				if minutes <= 0 {
					minutes = 30
				}
				acts = append(acts, Action{Type: "mute", Params: mustJSON(MuteParams{
					Minutes: minutes, Reason: "发布违规内容：" + kr.Pattern,
				})})
			case "kick":
				acts = append(acts, Action{Type: "kick", Params: mustJSON(KickParams{
					Reason: "发布违规内容：" + kr.Pattern,
				})})
			}
			if kr.Action == "warn" || kr.Action == "mute" {
				acts = append(acts, Action{Type: "increment_counter", Params: mustJSON(CounterParams{
					CounterID: "kw", Step: 1, WindowHours: ttl,
				})})
			}
			out = append(out, Rule{
				ID: nextID(), Name: baseName, Enabled: true,
				Event: EventMessage, When: []Condition{match},
				Then: acts, Break: true,
			})
		}
	}

	// 4. 刷屏检测（升级规则前置）
	if f != nil && f.Enabled {
		floodCond := Condition{Type: "flood", Params: mustJSON(FloodParams{
			WindowSec: f.WindowSeconds, MaxCount: f.MaxMessages,
		})}
		if f.KickOnRepeat && f.KickThreshold > 0 {
			out = append(out, Rule{
				ID: nextID(), Name: "刷屏多次移出", Enabled: true,
				Event: EventMessage,
				When: []Condition{
					floodCond,
					{Type: "strike_count", Params: mustJSON(StrikeCountParams{
						CounterID: "flood", MinCount: f.KickThreshold, WindowHours: 24,
					})},
				},
				Then: []Action{
					{Type: "kick", Params: mustJSON(KickParams{Reason: "多次刷屏"})},
					{Type: "reset_counter", Params: mustJSON(CounterParams{CounterID: "flood"})},
				},
				Break: true,
			})
		}
		acts := []Action{
			{Type: "mute", Params: mustJSON(MuteParams{Minutes: f.MuteMinutes, Reason: "刷屏"})},
			{Type: "increment_counter", Params: mustJSON(CounterParams{
				CounterID: "flood", Step: 1, WindowHours: 24,
			})},
		}
		out = append(out, Rule{
			ID: nextID(), Name: "刷屏检测", Enabled: true,
			Event: EventMessage, When: []Condition{floodCond},
			Then: acts, Break: true,
		})
	}

	// 5. 入群审批（邀请部分已由 InitialRules 生成）
	if j != nil {
		if j.Keyword != "" {
			out = append(out, Rule{
				ID: nextID(), Name: "拒绝申请-" + truncateRunes(j.Keyword, 12), Enabled: true,
				Event: EventGroupRequest,
				When:  []Condition{{Type: "request_contains", Params: mustJSON(TextContainsParams{Text: j.Keyword})}},
				Then: []Action{{Type: "reject_join", Params: mustJSON(RejectJoinParams{
					Reason: j.RejectText,
				})}},
				Break: true,
			})
		}
		if j.AutoApprove {
			out = append(out, Rule{
				ID: nextID(), Name: "自动同意申请", Enabled: true,
				Event: EventGroupRequest,
				When:  []Condition{{Type: "subtype_is", Params: mustJSON(SubtypeParams{Subtype: "add"})}},
				Then:  []Action{{Type: "approve_join"}},
				Break: true,
			})
		}
	}

	return out
}

// inviteRules 机器人入群邀请审批规则（原固定逻辑）。
func inviteRules(nextID func() string) []Rule {
	return []Rule{
		{
			ID: nextID(), Name: "同意邀请", Enabled: true,
			Event: EventGroupRequest,
			When:  []Condition{{Type: "subtype_is", Params: mustJSON(SubtypeParams{Subtype: "invite"})}, {Type: "is_owner"}},
			Then:  []Action{{Type: "approve_join"}},
			Break: true,
		},
		{
			ID: nextID(), Name: "拒绝邀请", Enabled: true,
			Event: EventGroupRequest,
			When:  []Condition{{Type: "subtype_is", Params: mustJSON(SubtypeParams{Subtype: "invite"})}},
			Then:  []Action{{Type: "reject_join", Params: mustJSON(RejectJoinParams{Reason: "机器人不接受邀请"})}},
			Break: true,
		},
	}
}

// mustJSON 序列化参数（迁移输入均为内部构造，失败仅理论不可达）。
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic("rules: 参数序列化失败: " + err.Error())
	}
	return b
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
