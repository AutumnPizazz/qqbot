package config

// RuntimeGroup 单个群的运行时覆盖字段。nil/空 = 使用文件配置的值。
// JSON 格式与旧 data/runtime.json 兼容（一次性迁移时读取）。
type RuntimeGroup struct {
	WelcomeEnabled     *bool          `json:"welcome_enabled,omitempty"`
	WelcomeMessage     *string        `json:"welcome_message,omitempty"`
	KeywordEnabled     *bool          `json:"keyword_enabled,omitempty"`
	KeywordStrikeTTLH  *int           `json:"keyword_strike_ttl_hours,omitempty"`
	FloodEnabled       *bool          `json:"flood_enabled,omitempty"`
	FloodWindowSeconds *int           `json:"flood_window_seconds,omitempty"`
	FloodMaxMessages   *int           `json:"flood_max_messages,omitempty"`
	FloodMuteMinutes   *int           `json:"flood_mute_minutes,omitempty"`
	FloodKickOnRepeat  *bool          `json:"flood_kick_on_repeat,omitempty"`
	FloodKickThreshold *int           `json:"flood_kick_threshold,omitempty"`
	KeywordWarnLimit   *int           `json:"keyword_warn_limit,omitempty"`
	KeywordMuteLimit   *int           `json:"keyword_mute_limit,omitempty"`
	JoinAutoApprove    *bool          `json:"join_auto_approve,omitempty"`
	JoinRejectKeyword  *string        `json:"join_reject_keyword,omitempty"`
	JoinRejectReason   *string        `json:"join_reject_reason,omitempty"`
	Whitelist          *[]int64       `json:"whitelist,omitempty"`
	Rules              *[]KeywordRule `json:"rules,omitempty"` // 非空时完全替代文件规则
}

// ApplyRuntimeOverlay 把运行时覆盖合并进配置（在 copy-on-write 的副本上调用，无锁）。
func ApplyRuntimeOverlay(cfg *Config, overlays map[int64]*RuntimeGroup) {
	for gid, rg := range overlays {
		g := cfg.Group(gid)
		if g == nil {
			continue
		}
		if g.Welcome == nil && (rg.WelcomeEnabled != nil || rg.WelcomeMessage != nil) {
			g.Welcome = &WelcomeConfig{}
		}
		if g.Keyword == nil && (rg.KeywordEnabled != nil || rg.KeywordStrikeTTLH != nil ||
			rg.KeywordWarnLimit != nil || rg.KeywordMuteLimit != nil || rg.Rules != nil) {
			g.Keyword = &KeywordConfig{}
		}
		if g.Flood == nil && (rg.FloodEnabled != nil || rg.FloodWindowSeconds != nil ||
			rg.FloodMaxMessages != nil || rg.FloodMuteMinutes != nil ||
			rg.FloodKickOnRepeat != nil || rg.FloodKickThreshold != nil) {
			g.Flood = &FloodConfig{}
		}
		if g.JoinRequest == nil && (rg.JoinAutoApprove != nil || rg.JoinRejectKeyword != nil || rg.JoinRejectReason != nil) {
			g.JoinRequest = &JoinRequestConfig{}
		}
		if rg.WelcomeEnabled != nil && g.Welcome != nil {
			g.Welcome.Enabled = *rg.WelcomeEnabled
		}
		if rg.WelcomeMessage != nil && g.Welcome != nil {
			g.Welcome.Message = *rg.WelcomeMessage
		}
		if rg.KeywordEnabled != nil && g.Keyword != nil {
			g.Keyword.Enabled = *rg.KeywordEnabled
		}
		if rg.KeywordStrikeTTLH != nil && g.Keyword != nil {
			g.Keyword.StrikeTTLH = *rg.KeywordStrikeTTLH
		}
		if rg.FloodEnabled != nil && g.Flood != nil {
			g.Flood.Enabled = *rg.FloodEnabled
		}
		if rg.FloodWindowSeconds != nil && g.Flood != nil {
			g.Flood.WindowSeconds = *rg.FloodWindowSeconds
		}
		if rg.FloodMaxMessages != nil && g.Flood != nil {
			g.Flood.MaxMessages = *rg.FloodMaxMessages
		}
		if rg.FloodMuteMinutes != nil && g.Flood != nil {
			g.Flood.MuteMinutes = *rg.FloodMuteMinutes
		}
		if rg.FloodKickOnRepeat != nil && g.Flood != nil {
			g.Flood.KickOnRepeat = *rg.FloodKickOnRepeat
		}
		if rg.FloodKickThreshold != nil && g.Flood != nil {
			g.Flood.KickThreshold = *rg.FloodKickThreshold
		}
		if rg.KeywordWarnLimit != nil && g.Keyword != nil {
			g.Keyword.WarnLimit = *rg.KeywordWarnLimit
		}
		if rg.KeywordMuteLimit != nil && g.Keyword != nil {
			g.Keyword.MuteLimit = *rg.KeywordMuteLimit
		}
		if rg.JoinAutoApprove != nil && g.JoinRequest != nil {
			g.JoinRequest.AutoApprove = *rg.JoinAutoApprove
		}
		if rg.JoinRejectKeyword != nil && g.JoinRequest != nil {
			g.JoinRequest.Keyword = *rg.JoinRejectKeyword
		}
		if rg.JoinRejectReason != nil && g.JoinRequest != nil {
			g.JoinRequest.RejectText = *rg.JoinRejectReason
		}
		if rg.Whitelist != nil {
			g.Whitelist = append([]int64(nil), (*rg.Whitelist)...)
		}
		if rg.Rules != nil && g.Keyword != nil {
			g.Keyword.Rules = append([]KeywordRule(nil), (*rg.Rules)...)
		}
	}
}
