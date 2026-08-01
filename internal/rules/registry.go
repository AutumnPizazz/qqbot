package rules

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
)

// ConditionSpec 条件类型注册条目。
type ConditionSpec struct {
	Type   string
	Label  string
	Desc   string
	Events []string
	Zero   any // 参数零值（JSON schema 生成用）
	Parse  func(raw json.RawMessage) (any, error)
	Eval   func(e *Engine, ctx *EventContext, p any) (bool, error)
}

// ActionSpec 动作类型注册条目。
type ActionSpec struct {
	Type      string
	Label     string
	Desc      string
	Events    []string
	Zero      any
	Parse     func(raw json.RawMessage) (any, error)
	Run       func(e *Engine, ctx *EventContext, x *ruleExec, p any) error
	Dangerous bool
}

// ConditionSpecs / ActionSpecs 注册表（全部类型的唯一事实源：
// 校验解析、执行、rule-meta API、前端动态表单共用）。
var ConditionSpecs = map[string]*ConditionSpec{
	"always":            {Type: "always", Label: "恒真", Desc: "条件恒为真（空条件与此等价）", Events: AllEvents, Zero: nil, Parse: parseNoParams, Eval: evalAlways},
	"is_exempt":         {Type: "is_exempt", Label: "豁免身份", Desc: "群主/群管理/白名单/超级管理员", Events: []string{EventMessage}, Zero: nil, Parse: parseNoParams, Eval: evalIsExempt},
	"is_owner":          {Type: "is_owner", Label: "超级管理员", Desc: "用户是系统超级管理员", Events: AllEvents, Zero: nil, Parse: parseNoParams, Eval: evalIsOwner},
	"user_id":           {Type: "user_id", Label: "指定 QQ", Desc: "用户是指定 QQ", Events: AllEvents, Zero: &UserIDParams{}, Parse: parseUserID, Eval: evalUserID},
	"user_id_in":        {Type: "user_id_in", Label: "QQ 列表", Desc: "用户属于指定 QQ 列表", Events: AllEvents, Zero: &UserIDsParams{}, Parse: parseUserIDs, Eval: evalUserIDIn},
	"user_role":         {Type: "user_role", Label: "群角色", Desc: "群主/群管理/普通成员", Events: []string{EventMessage}, Zero: &UserRoleParams{}, Parse: parseUserRole, Eval: evalUserRole},
	"in_whitelist":      {Type: "in_whitelist", Label: "白名单内", Desc: "用户在群白名单", Events: []string{EventMessage}, Zero: nil, Parse: parseNoParams, Eval: evalInWhitelist},
	"text_contains":     {Type: "text_contains", Label: "文本包含", Desc: "消息文本包含关键词（忽略大小写）", Events: []string{EventMessage}, Zero: &TextContainsParams{}, Parse: parseTextContains, Eval: evalTextContains},
	"text_regex":        {Type: "text_regex", Label: "正则匹配", Desc: "消息文本匹配正则表达式", Events: []string{EventMessage}, Zero: &TextRegexParams{}, Parse: parseTextRegex, Eval: evalTextRegex},
	"text_repeat":       {Type: "text_repeat", Label: "重复消息", Desc: "同一用户窗口内发送相同文本 ≥N 次（含本条）", Events: []string{EventMessage}, Zero: &TextRepeatParams{}, Parse: parseTextRepeat, Eval: evalTextRepeat},
	"message_has_type":  {Type: "message_has_type", Label: "消息类型", Desc: "消息包含指定类型（图片/链接/@等）", Events: []string{EventMessage}, Zero: &MessageHasTypeParams{}, Parse: parseMessageHasType, Eval: evalMessageHasType},
	"flood":             {Type: "flood", Label: "刷屏", Desc: "窗口内发送消息 ≥N 条（含本条）", Events: []string{EventMessage}, Zero: &FloodParams{}, Parse: parseFlood, Eval: evalFlood},
	"strike_count":      {Type: "strike_count", Label: "累计计数", Desc: "计数器累计 ≥N 次（需配合计数动作）", Events: []string{EventMessage}, Zero: &StrikeCountParams{}, Parse: parseStrikeCount, Eval: evalStrikeCount},
	"time_between":      {Type: "time_between", Label: "时间段", Desc: "当前时间落在区间（支持跨午夜）", Events: []string{EventMessage}, Zero: &TimeBetweenParams{}, Parse: parseTimeBetween, Eval: evalTimeBetween},
	"request_contains":  {Type: "request_contains", Label: "备注包含", Desc: "加群申请备注包含关键词", Events: []string{EventGroupRequest}, Zero: &TextContainsParams{}, Parse: parseTextContains, Eval: evalRequestContains},
	"request_regex":     {Type: "request_regex", Label: "备注正则", Desc: "加群申请备注匹配正则", Events: []string{EventGroupRequest}, Zero: &TextRegexParams{}, Parse: parseTextRegex, Eval: evalRequestRegex},
	"subtype_is":        {Type: "subtype_is", Label: "申请类型", Desc: "加群申请 / 机器人被邀请", Events: []string{EventGroupRequest}, Zero: &SubtypeParams{}, Parse: parseSubtype, Eval: evalSubtypeIs},
	"request_comment_empty": {Type: "request_comment_empty", Label: "备注为空", Desc: "加群申请未填写备注（广告特征）", Events: []string{EventGroupRequest}, Zero: nil, Parse: parseNoParams, Eval: evalRequestCommentEmpty},
	"text_length":       {Type: "text_length", Label: "消息长度", Desc: "消息字符数在区间内（防长文本）", Events: []string{EventMessage}, Zero: &TextLengthParams{}, Parse: parseTextLength, Eval: evalTextLength},
	"url_count":         {Type: "url_count", Label: "链接数量", Desc: "消息含链接 ≥N 个（广告特征）", Events: []string{EventMessage}, Zero: &CountThresholdParams{}, Parse: parseCountThreshold, Eval: evalURLCount},
	"image_count":       {Type: "image_count", Label: "图片数量", Desc: "消息含图片 ≥N 张（图片刷屏）", Events: []string{EventMessage}, Zero: &CountThresholdParams{}, Parse: parseCountThreshold, Eval: evalImageCount},
	"at_count":          {Type: "at_count", Label: "@人数", Desc: "消息 @ 的人 ≥N 个（@轰炸）", Events: []string{EventMessage}, Zero: &CountThresholdParams{}, Parse: parseCountThreshold, Eval: evalAtCount},
	"mention_self":      {Type: "mention_self", Label: "@机器人", Desc: "消息 @ 了机器人", Events: []string{EventMessage}, Zero: nil, Parse: parseNoParams, Eval: evalMentionSelf},
	"weekday":           {Type: "weekday", Label: "星期几", Desc: "当前是选中的星期（周末严管等）", Events: []string{EventMessage}, Zero: &WeekdayParams{}, Parse: parseWeekday, Eval: evalWeekday},
	"probability":       {Type: "probability", Label: "随机概率", Desc: "按概率命中（如 30% 警告）", Events: []string{EventMessage}, Zero: &ProbabilityParams{}, Parse: parseProbability, Eval: evalProbability},
	"user_joined_within": {Type: "user_joined_within", Label: "入群 N 天内", Desc: "用户入群未满 N 天（新人保护期）", Events: []string{EventMessage}, Zero: &DaysParams{}, Parse: parseDays, Eval: evalUserJoinedWithin},
	"inviter_is":        {Type: "inviter_is", Label: "指定邀请人", Desc: "由指定 QQ 邀请入群", Events: []string{EventGroupIncrease}, Zero: &InviterParams{}, Parse: parseInviter, Eval: evalInviterIs},
}

var ActionSpecs = map[string]*ActionSpec{
	"send_message":      {Type: "send_message", Label: "发送消息", Desc: "发送自定义消息（支持模板变量）", Events: AllEvents, Zero: &SendMessageParams{}, Parse: parseSendMessage, Run: actSendMessage},
	"warn":              {Type: "warn", Label: "警告", Desc: "@ 目标并发出警告（写审计）", Events: []string{EventMessage}, Zero: &WarnParams{}, Parse: parseWarn, Run: actWarn},
	"mute":              {Type: "mute", Label: "禁言", Desc: "禁言目标 N 分钟（默认 @ 通知）", Events: []string{EventMessage}, Zero: &MuteParams{}, Parse: parseMute, Run: actMute},
	"kick":              {Type: "kick", Label: "移出群", Desc: "把目标移出群（默认发通知）", Events: []string{EventMessage}, Zero: &KickParams{}, Parse: parseKick, Run: actKick, Dangerous: true},
	"recall":            {Type: "recall", Label: "撤回消息", Desc: "撤回本条违规消息", Events: []string{EventMessage}, Zero: nil, Parse: parseNoParams, Run: actRecall, Dangerous: true},
	"increment_counter": {Type: "increment_counter", Label: "计数 +N", Desc: "累计计数加 N（升级处罚的计分动作）", Events: []string{EventMessage}, Zero: &CounterParams{}, Parse: parseCounterInc, Run: actIncrementCounter},
	"reset_counter":     {Type: "reset_counter", Label: "计数清零", Desc: "清零某计数器", Events: []string{EventMessage}, Zero: &CounterParams{}, Parse: parseCounterReset, Run: actResetCounter},
	"set_counter":       {Type: "set_counter", Label: "计数设值", Desc: "直接把计数器设为指定值", Events: []string{EventMessage}, Zero: &CounterParams{}, Parse: parseCounterSet, Run: actSetCounter},
	"approve_join":      {Type: "approve_join", Label: "同意申请", Desc: "同意加群申请 / 邀请", Events: []string{EventGroupRequest}, Zero: nil, Parse: parseNoParams, Run: actApproveJoin},
	"reject_join":       {Type: "reject_join", Label: "拒绝申请", Desc: "拒绝加群申请 / 邀请（可带原因）", Events: []string{EventGroupRequest}, Zero: &RejectJoinParams{}, Parse: parseRejectJoin, Run: actRejectJoin},
	"noop":              {Type: "noop", Label: "无操作", Desc: "什么都不做（占位）", Events: AllEvents, Zero: nil, Parse: parseNoParams, Run: actNoop},
	"send_private":      {Type: "send_private", Label: "私聊通知", Desc: "向目标发送私聊消息（违规提醒等）", Events: []string{EventMessage}, Zero: &SendPrivateParams{}, Parse: parseSendPrivate, Run: actSendPrivate},
	"whole_ban":         {Type: "whole_ban", Label: "全员禁言", Desc: "开启/解除全员禁言（宵禁自动化）", Events: []string{EventMessage}, Zero: &WholeBanParams{}, Parse: parseWholeBan, Run: actWholeBan, Dangerous: true},
	"card":              {Type: "card", Label: "设置名片", Desc: "设置目标群名片（可带模板变量；留空=清空）", Events: []string{EventMessage, EventGroupIncrease}, Zero: &CardParams{}, Parse: parseCard, Run: actCard},
	"decrement_counter": {Type: "decrement_counter", Label: "计数 -N", Desc: "累计计数减 N（撤销计分）", Events: []string{EventMessage}, Zero: &CounterParams{}, Parse: parseCounterDec, Run: actDecrementCounter},
}

// parseNoParams 无参数类型：忽略任何参数内容。
func parseNoParams(json.RawMessage) (any, error) { return nil, nil }

// 各类型解析（参数校验见 params.go 的分支实现，此处统一入口）。
func parseUserID(raw json.RawMessage) (any, error)          { return validateConditionParams("user_id", raw) }
func parseUserIDs(raw json.RawMessage) (any, error)         { return validateConditionParams("user_id_in", raw) }
func parseUserRole(raw json.RawMessage) (any, error)        { return validateConditionParams("user_role", raw) }
func parseTextContains(raw json.RawMessage) (any, error)    { return validateConditionParams("text_contains", raw) }
func parseTextRegex(raw json.RawMessage) (any, error) {
	p, err := validateConditionParams("text_regex", raw)
	if err != nil {
		return nil, err
	}
	if err := compileRegexParams(p.(*TextRegexParams)); err != nil {
		return nil, paramErrf("正则无效: %v", err)
	}
	return p, nil
}
func parseTextRepeat(raw json.RawMessage) (any, error)      { return validateConditionParams("text_repeat", raw) }
func parseMessageHasType(raw json.RawMessage) (any, error)  { return validateConditionParams("message_has_type", raw) }
func parseFlood(raw json.RawMessage) (any, error)           { return validateConditionParams("flood", raw) }
func parseStrikeCount(raw json.RawMessage) (any, error)     { return validateConditionParams("strike_count", raw) }
func parseTimeBetween(raw json.RawMessage) (any, error)     { return validateConditionParams("time_between", raw) }
func parseSubtype(raw json.RawMessage) (any, error)         { return validateConditionParams("subtype_is", raw) }
func parseTextLength(raw json.RawMessage) (any, error)      { return validateConditionParams("text_length", raw) }
func parseCountThreshold(raw json.RawMessage) (any, error)  { return validateConditionParams("url_count", raw) }
func parseWeekday(raw json.RawMessage) (any, error)         { return validateConditionParams("weekday", raw) }
func parseProbability(raw json.RawMessage) (any, error)     { return validateConditionParams("probability", raw) }
func parseDays(raw json.RawMessage) (any, error)            { return validateConditionParams("user_joined_within", raw) }
func parseInviter(raw json.RawMessage) (any, error)         { return validateConditionParams("inviter_is", raw) }
func parseSendMessage(raw json.RawMessage) (any, error)     { return validateActionParams("send_message", raw) }
func parseWarn(raw json.RawMessage) (any, error)            { return validateActionParams("warn", raw) }
func parseMute(raw json.RawMessage) (any, error)            { return validateActionParams("mute", raw) }
func parseKick(raw json.RawMessage) (any, error)            { return validateActionParams("kick", raw) }
func parseCounterInc(raw json.RawMessage) (any, error)  { return validateActionParams("increment_counter", raw) }
func parseCounterDec(raw json.RawMessage) (any, error)  { return validateActionParams("decrement_counter", raw) }
func parseCounterReset(raw json.RawMessage) (any, error) { return validateActionParams("reset_counter", raw) }
func parseCounterSet(raw json.RawMessage) (any, error)   { return validateActionParams("set_counter", raw) }
func parseRejectJoin(raw json.RawMessage) (any, error)      { return validateActionParams("reject_join", raw) }
func parseSendPrivate(raw json.RawMessage) (any, error)     { return validateActionParams("send_private", raw) }
func parseWholeBan(raw json.RawMessage) (any, error)        { return validateActionParams("whole_ban", raw) }
func parseCard(raw json.RawMessage) (any, error)            { return validateActionParams("card", raw) }

// ValidateRules 校验整组规则（state 层委托入口）。
// base 为字段路径前缀（如 groups.0.rules）；返回 nil 表示合法。
func ValidateRules(rules []Rule, base string) *RuleError {
	ve := &RuleError{}
	if len(rules) > MaxRulesPerGroup {
		ve.add(base, "规则数量超出上限（最多 100 条）")
	}
	seenID := map[string]int{}
	seenName := map[string]int{}
	for i := range rules {
		r := &rules[i]
		rbase := base + "." + strconv.Itoa(i)
		if r.ID == "" {
			ve.add(rbase+".id", "规则 ID 不能为空")
		} else if !reRuleID.MatchString(r.ID) {
			ve.add(rbase+".id", "规则 ID 只能包含字母/数字/_/-（1~32 字符）")
		} else if prev, dup := seenID[r.ID]; dup {
			ve.add(rbase+".id", "规则 ID 与 rules."+strconv.Itoa(prev)+" 重复")
		} else {
			seenID[r.ID] = i
		}
		if r.Name == "" {
			ve.add(rbase+".name", "规则名不能为空")
		} else if len([]rune(r.Name)) > MaxRuleNameLen {
			ve.add(rbase+".name", "规则名过长（最多 64 字符）")
		} else if prev, dup := seenName[r.Name]; dup {
			ve.add(rbase+".name", "规则名与 rules."+strconv.Itoa(prev)+" 重复")
		} else {
			seenName[r.Name] = i
		}
		validEvent := false
		for _, ev := range AllEvents {
			if ev == r.Event {
				validEvent = true
				break
			}
		}
		if !validEvent {
			ve.add(rbase+".event", "event 只能是 message/group_increase/group_request")
		}
		if r.WhenMode != "" && r.WhenMode != WhenModeAll && r.WhenMode != WhenModeAny {
			ve.add(rbase+".when_mode", "when_mode 只能是 all/any")
		}
		if len(r.When) > MaxConditionsPerRule {
			ve.add(rbase+".when", "条件数量超出上限（最多 8 个）")
		}
		if len(r.Then) > MaxActionsPerRule {
			ve.add(rbase+".then", "动作数量超出上限（最多 4 个）")
		}
		// 编译（参数解析 + 类型/事件匹配），错误直接写入 ve
		compileRule(r, rbase, ve)
	}
	if len(ve.Fields) == 0 {
		return nil
	}
	return ve
}

// compileRule 编译一条规则：解析全部条件/动作参数，检查事件匹配。
// 校验错误直接写入 ve（不返回值，避免与 ve 同源切片自增）。
func compileRule(r *Rule, base string, ve *RuleError) {
	for i := range r.When {
		c := &r.When[i]
		cbase := base + ".when." + strconv.Itoa(i)
		spec, ok := ConditionSpecs[c.Type]
		if !ok {
			ve.add(cbase+".type", "未知条件类型 "+strconv.Quote(c.Type))
			continue
		}
		if !eventAllowed(spec.Events, r.Event) {
			ve.add(cbase+".type", "条件「"+spec.Label+"」不适用于事件 "+r.Event)
			continue
		}
		if _, err := spec.Parse(c.Params); err != nil {
			ve.add(cbase+".params", err.Error())
		}
	}
	for i := range r.Then {
		a := &r.Then[i]
		abase := base + ".then." + strconv.Itoa(i)
		spec, ok := ActionSpecs[a.Type]
		if !ok {
			ve.add(abase+".type", "未知动作类型 "+strconv.Quote(a.Type))
			continue
		}
		if !eventAllowed(spec.Events, r.Event) {
			ve.add(abase+".type", "动作「"+spec.Label+"」不适用于事件 "+r.Event)
			continue
		}
		if _, err := spec.Parse(a.Params); err != nil {
			ve.add(abase+".params", err.Error())
		}
	}
}

func eventAllowed(allowed []string, ev string) bool {
	for _, a := range allowed {
		if a == ev {
			return true
		}
	}
	return false
}

// ---- JSON schema 生成（rule-meta API） ----

// typeMeta 注册表条目 → API 元数据。
type typeMeta struct {
	Type        string         `json:"type"`
	Label       string         `json:"label"`
	Desc        string         `json:"desc,omitempty"`
	Events      []string       `json:"events"`
	Dangerous   bool           `json:"dangerous,omitempty"`
	ParamsSchema map[string]any `json:"params_schema,omitempty"`
}

// schemaOf 基于参数零值 struct 的 tag 反射生成 JSON schema。
// 支持 tag：title / max / min / enum / item_enum / multiline / default。
func schemaOf(zero any) map[string]any {
	if zero == nil {
		return nil
	}
	t := reflect.TypeOf(zero)
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	props := map[string]any{}
	required := []string{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := strings.SplitN(f.Tag.Get("json"), ",", 2)[0]
		if name == "" || name == "-" {
			continue
		}
		ps := fieldSchema(f)
		if ps == nil {
			continue
		}
		props[name] = ps
		if strings.Contains(f.Tag.Get("required"), "true") {
			required = append(required, name)
		}
	}
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func fieldSchema(f reflect.StructField) map[string]any {
	s := map[string]any{}
	if title := f.Tag.Get("title"); title != "" {
		s["title"] = title
	} else {
		s["title"] = f.Name
	}
	if def := f.Tag.Get("default"); def != "" {
		s["default"] = def
	}
	if f.Tag.Get("multiline") == "true" {
		s["multiline"] = true
	}
	switch f.Type.Kind() {
	case reflect.String:
		s["type"] = "string"
		if max := f.Tag.Get("max"); max != "" {
			if n, err := strconv.Atoi(max); err == nil {
				s["maxLength"] = n
			}
		}
		if enum := f.Tag.Get("enum"); enum != "" {
			s["enum"] = splitEnum(enum)
		}
	case reflect.Int, reflect.Int64:
		s["type"] = "integer"
		if min := f.Tag.Get("min"); min != "" {
			if n, err := strconv.Atoi(min); err == nil {
				s["minimum"] = n
			}
		}
		if max := f.Tag.Get("max"); max != "" {
			if n, err := strconv.Atoi(max); err == nil {
				s["maximum"] = n
			}
		}
	case reflect.Bool:
		s["type"] = "boolean"
	case reflect.Slice:
		s["type"] = "array"
		items := map[string]any{"type": "string"}
		if f.Type.Elem().Kind() != reflect.String {
			items = map[string]any{"type": "integer"}
		}
		if enum := f.Tag.Get("item_enum"); enum != "" {
			items["enum"] = splitEnum(enum)
		}
		s["items"] = items
	default:
		return nil
	}
	return s
}

func splitEnum(s string) []any {
	parts := strings.Split(s, ",")
	out := make([]any, 0, len(parts))
	for _, p := range parts {
		out = append(out, p)
	}
	return out
}
