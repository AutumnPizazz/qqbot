package state

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"qqbot/internal/rules"
)

// ---- v1 遗留结构（仅用于 control.json schema v1 → v2 自动迁移） ----
// v2 起生产代码不再写入这些字段。

// v1Control 是 schema v1 的根结构（结构与旧 control.go 完全一致）。
type v1Control struct {
	SchemaVersion int             `json:"schema_version"`
	Revision      int64           `json:"revision"`
	UpdatedAt     time.Time       `json:"updated_at"`
	System        SystemConfig    `json:"system"`
	Groups        []v1GroupConfig `json:"groups"`
	History       []v1History     `json:"history,omitempty"`
}

type v1GroupConfig struct {
	GroupID     int64               `json:"group_id"`
	Enabled     bool                `json:"enabled"`
	Remark      string              `json:"remark,omitempty"`
	Whitelist   []int64             `json:"whitelist,omitempty"`
	Welcome     *WelcomeConfig      `json:"welcome,omitempty"`
	Keyword     *KeywordConfig      `json:"keyword_filter,omitempty"`
	Flood       *FloodConfig        `json:"flood,omitempty"`
	JoinRequest *JoinRequestConfig  `json:"join_request,omitempty"`
}

type v1History struct {
	Revision int64      `json:"revision"`
	Time     time.Time  `json:"time"`
	Actor    string     `json:"actor"`
	Summary  string     `json:"summary"`
	Hash     string     `json:"hash"`
	Config   *v1Control `json:"config"`
}

// v1 旧模块类型保留（仅迁移解码用）。
type WelcomeConfig struct {
	Enabled bool   `json:"enabled"`
	Message string `json:"message,omitempty"`
}

type KeywordConfig struct {
	Enabled    bool          `json:"enabled"`
	WarnLimit  int           `json:"warn_limit,omitempty"`
	MuteLimit  int           `json:"mute_limit,omitempty"`
	StrikeTTLH int           `json:"strike_ttl_hours,omitempty"`
	Rules      []KeywordRule `json:"rules,omitempty"`
}

type KeywordRule struct {
	Pattern     string `json:"pattern"`
	Regex       bool   `json:"regex,omitempty"`
	Action      string `json:"action"`
	MuteMinutes int    `json:"mute_minutes,omitempty"`
}

type FloodConfig struct {
	Enabled       bool `json:"enabled"`
	WindowSeconds int  `json:"window_seconds,omitempty"`
	MaxMessages   int  `json:"max_messages,omitempty"`
	MuteMinutes   int  `json:"mute_minutes,omitempty"`
	KickOnRepeat  bool `json:"kick_on_repeat,omitempty"`
	KickThreshold int  `json:"kick_threshold,omitempty"`
}

type JoinRequestConfig struct {
	AutoApprove bool   `json:"auto_approve,omitempty"`
	Keyword     string `json:"reject_keyword,omitempty"`
	RejectText  string `json:"reject_reason,omitempty"`
}

// decodeV1Control 严格解码 schema v1 的 control.json 内容。
func decodeV1Control(data []byte) (*v1Control, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var c v1Control
	if err := dec.Decode(&c); err != nil {
		return nil, err
	}
	if c.SchemaVersion != 1 {
		return nil, fmt.Errorf("schema_version 不是 1（实际 %d）", c.SchemaVersion)
	}
	return &c, nil
}

// migrateV1ToV2 把 v1 配置转换为 v2 规则（设计文档 §8 映射）。
// 返回的 Control 已通过 normalizeAndValidate 校验；不写盘（调用方负责原子写回）。
func migrateV1ToV2(v1 *v1Control) (*Control, error) {
	c := &Control{
		SchemaVersion: SchemaVersion,
		Revision:      v1.Revision,
		UpdatedAt:     v1.UpdatedAt,
		System:        v1.System,
	}
	seq := 0
	for i := range v1.Groups {
		g := &v1.Groups[i]
		ng := GroupConfig{
			GroupID:   g.GroupID,
			Enabled:   g.Enabled,
			Remark:    g.Remark,
			Whitelist: append([]int64(nil), g.Whitelist...),
		}
		if g.Enabled {
			ng.Rules = rules.MigrateV1(
				toV1Welcome(g.Welcome), toV1Keyword(g.Keyword),
				toV1Flood(g.Flood), toV1Join(g.JoinRequest), &seq)
		}
		c.Groups = append(c.Groups, ng)
	}
	// 历史快照逐条转换（转换失败丢弃该条并记日志）
	for _, h := range v1.History {
		if h.Config == nil {
			continue
		}
		hc, err := migrateV1ToV2(h.Config)
		if err != nil {
			slog.Warn("v1 历史快照转换失败，已丢弃该条", "revision", h.Revision, "err", err)
			continue
		}
		hc.Revision = h.Revision
		hc.UpdatedAt = h.Time
		hc.History = nil
		c.History = append(c.History, HistoryEntry{
			Revision: h.Revision,
			Time:     h.Time,
			Actor:    h.Actor,
			Summary:  h.Summary,
			Hash:     controlHash(hc), // 内容已变，重新计算摘要
			Config:   hc,
		})
	}
	if err := normalizeAndValidate(c); err != nil {
		return nil, fmt.Errorf("v1 迁移后配置校验失败: %w", err)
	}
	return c, nil
}

func toV1Welcome(w *WelcomeConfig) *rules.V1Welcome {
	if w == nil {
		return nil
	}
	return &rules.V1Welcome{Enabled: w.Enabled, Message: w.Message}
}

func toV1Keyword(k *KeywordConfig) *rules.V1Keyword {
	if k == nil {
		return nil
	}
	out := &rules.V1Keyword{
		Enabled: k.Enabled, WarnLimit: k.WarnLimit, MuteLimit: k.MuteLimit,
		StrikeTTLH: k.StrikeTTLH,
	}
	for _, r := range k.Rules {
		out.Rules = append(out.Rules, rules.V1KeywordRule{
			Pattern: r.Pattern, Regex: r.Regex,
			Action: r.Action, MuteMinutes: r.MuteMinutes,
		})
	}
	return out
}

func toV1Flood(f *FloodConfig) *rules.V1Flood {
	if f == nil {
		return nil
	}
	return &rules.V1Flood{
		Enabled: f.Enabled, WindowSeconds: f.WindowSeconds,
		MaxMessages: f.MaxMessages, MuteMinutes: f.MuteMinutes,
		KickOnRepeat: f.KickOnRepeat, KickThreshold: f.KickThreshold,
	}
}

func toV1Join(j *JoinRequestConfig) *rules.V1JoinRequest {
	if j == nil {
		return nil
	}
	return &rules.V1JoinRequest{
		AutoApprove: j.AutoApprove, Keyword: j.Keyword, RejectText: j.RejectText,
	}
}
