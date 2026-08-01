package rules

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ---- 参数范围常量 ----

const (
	MaxTextLen       = 500  // 文本类参数上限
	MaxPatternLen    = 200  // 正则/关键词长度上限
	MaxMuteMinutes   = 43200 // 30 天
	MaxWindowSec     = 3600
	MinWindowSec     = 3
	MaxCount         = 100
	MaxUserIDs       = 50
	MaxCounterIDLen  = 32
	MaxReasonLen     = 200
	MaxStrikeHours   = 720
	MaxMsgTypeSetLen = 10
)

var (
	reTimeHM  = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
	reRuleID  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
)

// ---- 条件参数 ----

// UserIDParams user_id：指定 QQ。
type UserIDParams struct {
	UserID int64 `json:"user_id" title:"QQ 号" min:"1" required:"true"`
}

// UserIDsParams user_id_in：多个 QQ。
type UserIDsParams struct {
	UserIDs []int64 `json:"user_ids" title:"QQ 号列表" required:"true"`
}

// UserRoleParams user_role：群角色。
type UserRoleParams struct {
	Roles []string `json:"roles" title:"角色" item_enum:"owner,admin,member" required:"true"`
}

// TextContainsParams text_contains / request_contains：子串（大小写不敏感）。
type TextContainsParams struct {
	Text string `json:"text" title:"关键词" max:"200" required:"true"`
}

// TextRegexParams text_regex / request_regex：正则。
type TextRegexParams struct {
	Pattern string `json:"pattern" title:"正则表达式" max:"200" required:"true"`
	re      *regexp.Regexp `json:"-"`
}

// TextRepeatParams text_repeat：窗口内相同文本出现次数。
type TextRepeatParams struct {
	WindowSec int `json:"window_sec" title:"窗口（秒）" min:"3" max:"3600" required:"true"`
	MinCount  int `json:"min_count" title:"出现次数" min:"2" max:"100" required:"true"`
}

// MessageHasTypeParams message_has_type：消息包含指定 segment 类型。
type MessageHasTypeParams struct {
	Types []string `json:"types" title:"消息类型" item_enum:"text,image,at,url,face,record,video,file" required:"true"`
}

// FloodParams flood：窗口内消息数（含本条）。
type FloodParams struct {
	WindowSec int `json:"window_sec" title:"窗口（秒）" min:"3" max:"3600" required:"true"`
	MaxCount  int `json:"max_count" title:"消息数阈值" min:"1" max:"100" required:"true"`
}

// StrikeCountParams strike_count：计数条件。
type StrikeCountParams struct {
	CounterID   string `json:"counter_id" title:"计数器 ID" max:"32" required:"true"`
	MinCount    int    `json:"min_count" title:"累计次数" min:"1" max:"100" required:"true"`
	WindowHours int    `json:"window_hours" title:"计分有效期（小时）" min:"1" max:"720" required:"true"`
}

// TimeBetweenParams time_between：时间段（跨午夜；start==end 视为全天）。
type TimeBetweenParams struct {
	Start string `json:"start" title:"开始（HH:MM）" max:"5" required:"true"`
	End   string `json:"end" title:"结束（HH:MM）" max:"5" required:"true"`
}

// SubtypeParams subtype_is：加群申请类型。
type SubtypeParams struct {
	Subtype string `json:"subtype" title:"申请类型" enum:"add,invite" required:"true"`
}

// TextLengthParams text_length：消息长度范围（按字符）。
type TextLengthParams struct {
	Min int `json:"min" title:"最少字符（0=不限）" min:"0" max:"5000"`
	Max int `json:"max" title:"最多字符（0=不限）" min:"0" max:"5000"`
}

// CountThresholdParams url_count / image_count / at_count：内容计数阈值。
type CountThresholdParams struct {
	MinCount int `json:"min_count" title:"数量阈值" min:"1" max:"50" required:"true"`
}

// WeekdayParams weekday：星期几。
type WeekdayParams struct {
	Days []int `json:"days" title:"星期" item_enum:"0,1,2,3,4,5,6" required:"true"` // 0=周日 ~ 6=周六
}

// ProbabilityParams probability：随机概率。
type ProbabilityParams struct {
	Percent int `json:"percent" title:"概率（%）" min:"1" max:"100" required:"true"`
}

// DaysParams user_joined_within：入群 N 天内。
type DaysParams struct {
	Days int `json:"days" title:"天数" min:"1" max:"3650" required:"true"`
}

// InviterParams inviter_is：邀请人 QQ。
type InviterParams struct {
	UserID int64 `json:"user_id" title:"邀请人 QQ" min:"1" required:"true"`
}

// ---- 动作参数 ----

// SendMessageParams send_message：发消息 / @目标（group_increase 时即欢迎语）。
type SendMessageParams struct {
	Message string `json:"message" title:"消息模板" max:"500" multiline:"true" required:"true"`
	At      bool   `json:"at" title:"@ 目标用户"`
}

// WarnParams warn：@ 警告。
type WarnParams struct {
	Reason    string `json:"reason" title:"警告原因" max:"200" required:"true"`
	CounterID string `json:"counter_id" title:"计数器 ID（{count} 变量来源，可选）" max:"32"`
}

// MuteParams mute：禁言。
type MuteParams struct {
	Minutes int    `json:"minutes" title:"禁言时长（分钟）" min:"1" max:"43200" required:"true"`
	Reason  string `json:"reason" title:"原因" max:"200"`
	Notice  bool   `json:"notice" title:"禁言后 @ 通知"` // 默认 true（旧版行为）
}

// KickParams kick：移出。
type KickParams struct {
	Reason string `json:"reason" title:"移出原因" max:"200"`
	Notice bool   `json:"notice" title:"移出后发通知"` // 默认 true（旧版行为）
}

// CounterParams increment/reset/set 计数动作共用。
type CounterParams struct {
	CounterID   string `json:"counter_id" title:"计数器 ID" max:"32" required:"true"`
	Step        int    `json:"step" title:"步长（默认 1）" min:"1" max:"100"`          // increment 用
	Count       int    `json:"count" title:"目标值" min:"0" max:"1000000"`           // set 用
	WindowHours int    `json:"window_hours" title:"计分有效期（小时）" min:"1" max:"720"` // increment 用，默认 24
}

// RejectJoinParams reject_join：拒绝加群。
type RejectJoinParams struct {
	Reason string `json:"reason" title:"拒绝原因" max:"200"`
}

// SendPrivateParams send_private：私聊发送。
type SendPrivateParams struct {
	Message string `json:"message" title:"消息模板" max:"500" multiline:"true" required:"true"`
}

// WholeBanParams whole_ban：全员禁言开关。
type WholeBanParams struct {
	Enable bool `json:"enable" title:"开启全员禁言" default:"true"`
}

// CardParams card：设置群名片。
type CardParams struct {
	Card string `json:"card" title:"群名片（留空=清空）" max:"64"`
}

// ---- 参数解析 ----

// parseParams 严格解码参数（禁止未知字段）并返回强类型。
func parseParams[T any](raw json.RawMessage) (*T, error) {
	p := new(T)
	if len(raw) == 0 {
		// 无参数类型允许空；有必填字段的类型由 Validate 兜底
		return p, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(p); err != nil {
		return nil, paramErrf("参数无效: %v", err)
	}
	return p, nil
}

// validateText 通用文本校验。
func validateText(v string, maxLen int, name string) error {
	if len([]rune(v)) > maxLen {
		return paramErrf("%s过长（最多 %d 字符）", name, maxLen)
	}
	for _, r := range v {
		if r < 0x20 && r != '\n' && r != '\t' && r != '\r' {
			return paramErrf("%s包含禁止的控制字符", name)
		}
	}
	return nil
}

func validateCounterID(id string) error {
	if id == "" {
		return paramErrf("计数器 ID 不能为空")
	}
	if len([]rune(id)) > MaxCounterIDLen {
		return paramErrf("计数器 ID 过长（最多 %d 字符）", MaxCounterIDLen)
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return paramErrf("计数器 ID 只能包含字母/数字/_/-")
		}
	}
	return nil
}

func validateMinutes(v int) error {
	if v < 1 || v > MaxMuteMinutes {
		return paramErrf("禁言时长范围必须为 1~%d 分钟", MaxMuteMinutes)
	}
	return nil
}

func validateWindowSec(v int) error {
	if v < MinWindowSec || v > MaxWindowSec {
		return paramErrf("窗口范围必须为 %d~%d 秒", MinWindowSec, MaxWindowSec)
	}
	return nil
}

func validateCountRange(v int) error {
	if v < 1 || v > MaxCount {
		return paramErrf("次数范围必须为 1~%d", MaxCount)
	}
	return nil
}

func validateEnum(v, allowed, name string) error {
	for _, a := range strings.Split(allowed, ",") {
		if v == a {
			return nil
		}
	}
	return paramErrf("%s只能是 %s", name, allowed)
}

// validateTimeHM 校验 HH:MM 格式。
func validateTimeHM(v, name string) error {
	if !reTimeHM.MatchString(v) {
		return paramErrf("%s必须是 HH:MM 格式（如 23:00）", name)
	}
	return nil
}

// parseTimeHM 解析 HH:MM 为当日分钟数。
func parseTimeHM(v string) (int, error) {
	h, m := 0, 0
	if _, err := fmt.Sscanf(v, "%d:%d", &h, &m); err != nil {
		return 0, err
	}
	return h*60 + m, nil
}

// inTimeRange 判断 now 是否落在 [start, end] 区间（支持跨午夜；start==end 全天）。
func inTimeRange(now time.Time, start, end string) (bool, error) {
	s, err := parseTimeHM(start)
	if err != nil {
		return false, paramErrf("开始时间无效: %s", start)
	}
	e, err := parseTimeHM(end)
	if err != nil {
		return false, paramErrf("结束时间无效: %s", end)
	}
	cur := now.Hour()*60 + now.Minute()
	if s == e {
		return true, nil // 全天
	}
	if s < e {
		return cur >= s && cur <= e, nil
	}
	return cur >= s || cur <= e, nil // 跨午夜
}

// ---- 条件参数校验（编译时调用） ----

func validateConditionParams(typ string, raw json.RawMessage) (any, error) {
	switch typ {
	case "always", "is_exempt", "is_owner", "in_whitelist":
		return nil, nil
	case "user_id":
		p, err := parseParams[UserIDParams](raw)
		if err != nil {
			return nil, err
		}
		if p.UserID <= 0 {
			return nil, paramErrf("QQ 号必须为正数")
		}
		return p, nil
	case "user_id_in":
		p, err := parseParams[UserIDsParams](raw)
		if err != nil {
			return nil, err
		}
		if len(p.UserIDs) == 0 {
			return nil, paramErrf("QQ 号列表不能为空")
		}
		if len(p.UserIDs) > MaxUserIDs {
			return nil, paramErrf("QQ 号列表超出上限（最多 %d 个）", MaxUserIDs)
		}
		seen := map[int64]bool{}
		for i, id := range p.UserIDs {
			if id <= 0 {
				return nil, paramErrf("QQ 号必须为正数（第 %d 项）", i+1)
			}
			if seen[id] {
				return nil, paramErrf("QQ 号重复：%d", id)
			}
			seen[id] = true
		}
		return p, nil
	case "user_role":
		p, err := parseParams[UserRoleParams](raw)
		if err != nil {
			return nil, err
		}
		if len(p.Roles) == 0 {
			return nil, paramErrf("角色列表不能为空")
		}
		for _, r := range p.Roles {
			if err := validateEnum(r, "owner,admin,member", "角色"); err != nil {
				return nil, err
			}
		}
		return p, nil
	case "text_contains", "request_contains":
		p, err := parseParams[TextContainsParams](raw)
		if err != nil {
			return nil, err
		}
		if p.Text == "" {
			return nil, paramErrf("关键词不能为空")
		}
		if err := validateText(p.Text, MaxPatternLen, "关键词"); err != nil {
			return nil, err
		}
		return p, nil
	case "text_regex", "request_regex":
		p, err := parseParams[TextRegexParams](raw)
		if err != nil {
			return nil, err
		}
		if p.Pattern == "" {
			return nil, paramErrf("正则不能为空")
		}
		if err := validateText(p.Pattern, MaxPatternLen, "正则"); err != nil {
			return nil, err
		}
		if _, err := regexp.Compile(p.Pattern); err != nil {
			return nil, paramErrf("正则无效: %v", err)
		}
		return p, nil
	case "text_repeat":
		p, err := parseParams[TextRepeatParams](raw)
		if err != nil {
			return nil, err
		}
		if err := validateWindowSec(p.WindowSec); err != nil {
			return nil, err
		}
		if err := validateCountRange(p.MinCount); err != nil {
			return nil, err
		}
		if p.MinCount < 2 {
			return nil, paramErrf("出现次数至少为 2")
		}
		return p, nil
	case "message_has_type":
		p, err := parseParams[MessageHasTypeParams](raw)
		if err != nil {
			return nil, err
		}
		if len(p.Types) == 0 {
			return nil, paramErrf("消息类型列表不能为空")
		}
		if len(p.Types) > MaxMsgTypeSetLen {
			return nil, paramErrf("消息类型列表超出上限（最多 %d 个）", MaxMsgTypeSetLen)
		}
		for _, t := range p.Types {
			if err := validateEnum(t, "text,image,at,url,face,record,video,file", "消息类型"); err != nil {
				return nil, err
			}
		}
		return p, nil
	case "flood":
		p, err := parseParams[FloodParams](raw)
		if err != nil {
			return nil, err
		}
		if err := validateWindowSec(p.WindowSec); err != nil {
			return nil, err
		}
		if err := validateCountRange(p.MaxCount); err != nil {
			return nil, err
		}
		return p, nil
	case "strike_count":
		p, err := parseParams[StrikeCountParams](raw)
		if err != nil {
			return nil, err
		}
		if err := validateCounterID(p.CounterID); err != nil {
			return nil, err
		}
		if err := validateCountRange(p.MinCount); err != nil {
			return nil, err
		}
		if p.WindowHours < 1 || p.WindowHours > MaxStrikeHours {
			return nil, paramErrf("计分有效期范围必须为 1~%d 小时", MaxStrikeHours)
		}
		return p, nil
	case "time_between":
		p, err := parseParams[TimeBetweenParams](raw)
		if err != nil {
			return nil, err
		}
		if err := validateTimeHM(p.Start, "开始时间"); err != nil {
			return nil, err
		}
		if err := validateTimeHM(p.End, "结束时间"); err != nil {
			return nil, err
		}
		return p, nil
	case "subtype_is":
		p, err := parseParams[SubtypeParams](raw)
		if err != nil {
			return nil, err
		}
		if err := validateEnum(p.Subtype, "add,invite", "申请类型"); err != nil {
			return nil, err
		}
		return p, nil
	case "text_length":
		p, err := parseParams[TextLengthParams](raw)
		if err != nil {
			return nil, err
		}
		if p.Min < 0 || p.Min > 5000 {
			return nil, paramErrf("最少字符范围必须为 0~5000")
		}
		if p.Max < 0 || p.Max > 5000 {
			return nil, paramErrf("最多字符范围必须为 0~5000")
		}
		if p.Max > 0 && p.Min > p.Max {
			return nil, paramErrf("最少字符不能大于最多字符")
		}
		if p.Min == 0 && p.Max == 0 {
			return nil, paramErrf("最少/最多字符至少填写一个")
		}
		return p, nil
	case "url_count", "image_count", "at_count":
		p, err := parseParams[CountThresholdParams](raw)
		if err != nil {
			return nil, err
		}
		if p.MinCount < 1 || p.MinCount > 50 {
			return nil, paramErrf("数量阈值范围必须为 1~50")
		}
		return p, nil
	case "weekday":
		p, err := parseParams[WeekdayParams](raw)
		if err != nil {
			return nil, err
		}
		if len(p.Days) == 0 {
			return nil, paramErrf("请至少选择一个星期")
		}
		seen := map[int]bool{}
		for _, d := range p.Days {
			if d < 0 || d > 6 {
				return nil, paramErrf("星期范围必须为 0~6（0=周日）")
			}
			if seen[d] {
				return nil, paramErrf("星期重复：%d", d)
			}
			seen[d] = true
		}
		return p, nil
	case "probability":
		p, err := parseParams[ProbabilityParams](raw)
		if err != nil {
			return nil, err
		}
		if p.Percent < 1 || p.Percent > 100 {
			return nil, paramErrf("概率范围必须为 1~100")
		}
		return p, nil
	case "user_joined_within":
		p, err := parseParams[DaysParams](raw)
		if err != nil {
			return nil, err
		}
		if p.Days < 1 || p.Days > 3650 {
			return nil, paramErrf("天数范围必须为 1~3650")
		}
		return p, nil
	case "request_comment_empty":
		return nil, nil
	case "inviter_is":
		p, err := parseParams[InviterParams](raw)
		if err != nil {
			return nil, err
		}
		if p.UserID <= 0 {
			return nil, paramErrf("QQ 号必须为正数")
		}
		return p, nil
	}
	return nil, paramErrf("未知条件类型 %q", typ)
}

// ---- 动作参数校验（编译时调用） ----

func validateActionParams(typ string, raw json.RawMessage) (any, error) {
	switch typ {
	case "recall", "approve_join", "noop":
		return nil, nil
	case "send_message":
		p, err := parseParams[SendMessageParams](raw)
		if err != nil {
			return nil, err
		}
		if p.Message == "" {
			return nil, paramErrf("消息内容不能为空")
		}
		if err := validateText(p.Message, MaxTextLen, "消息内容"); err != nil {
			return nil, err
		}
		return p, nil
	case "warn":
		p, err := parseParams[WarnParams](raw)
		if err != nil {
			return nil, err
		}
		if p.Reason == "" {
			return nil, paramErrf("警告原因不能为空")
		}
		if err := validateText(p.Reason, MaxReasonLen, "警告原因"); err != nil {
			return nil, err
		}
		if p.CounterID != "" {
			if err := validateCounterID(p.CounterID); err != nil {
				return nil, err
			}
		}
		return p, nil
	case "mute":
		p, err := parseParams[MuteParams](raw)
		if err != nil {
			return nil, err
		}
		if err := validateMinutes(p.Minutes); err != nil {
			return nil, err
		}
		if err := validateText(p.Reason, MaxReasonLen, "原因"); err != nil {
			return nil, err
		}
		return p, nil
	case "kick":
		p, err := parseParams[KickParams](raw)
		if err != nil {
			return nil, err
		}
		if err := validateText(p.Reason, MaxReasonLen, "移出原因"); err != nil {
			return nil, err
		}
		return p, nil
	case "increment_counter", "reset_counter", "set_counter", "decrement_counter":
		p, err := parseParams[CounterParams](raw)
		if err != nil {
			return nil, err
		}
		if err := validateCounterID(p.CounterID); err != nil {
			return nil, err
		}
		if (typ == "increment_counter" || typ == "decrement_counter") && (p.Step < 1 || p.Step > MaxCount) {
			return nil, paramErrf("步长范围必须为 1~%d", MaxCount)
		}
		if typ == "increment_counter" {
			if p.WindowHours < 1 || p.WindowHours > MaxStrikeHours {
				return nil, paramErrf("计分有效期范围必须为 1~%d 小时", MaxStrikeHours)
			}
		}
		if typ == "set_counter" && (p.Count < 0 || p.Count > 1000000) {
			return nil, paramErrf("目标值范围必须为 0~1000000")
		}
		return p, nil
	case "reject_join":
		p, err := parseParams[RejectJoinParams](raw)
		if err != nil {
			return nil, err
		}
		if err := validateText(p.Reason, MaxReasonLen, "拒绝原因"); err != nil {
			return nil, err
		}
		return p, nil
	case "send_private":
		p, err := parseParams[SendPrivateParams](raw)
		if err != nil {
			return nil, err
		}
		if p.Message == "" {
			return nil, paramErrf("消息内容不能为空")
		}
		if err := validateText(p.Message, MaxTextLen, "消息内容"); err != nil {
			return nil, err
		}
		return p, nil
	case "whole_ban":
		p, err := parseParams[WholeBanParams](raw)
		if err != nil {
			return nil, err
		}
		return p, nil
	case "card":
		p, err := parseParams[CardParams](raw)
		if err != nil {
			return nil, err
		}
		if err := validateText(p.Card, 64, "群名片"); err != nil {
			return nil, err
		}
		return p, nil
	}
	return nil, paramErrf("未知动作类型 %q", typ)
}
