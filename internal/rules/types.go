// Package rules 实现网页可组合的管理规则引擎：
// 事件（消息/入群/加群申请）→ 条件（AND、可取反）→ 动作（顺序执行）→ continue/break。
//
// 设计见 docs/RULE_ENGINE_DESIGN.md。本包不依赖 state/bot（底层），
// 由 state（配置 DTO/校验）、bot（执行集成）、admin（rule-meta API）三方引用。
package rules

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// 事件类型常量（rule.event 字段）。
const (
	EventMessage       = "message"        // 群消息
	EventGroupIncrease = "group_increase" // 新人入群
	EventGroupRequest  = "group_request"  // 加群申请 / 机器人被邀请
)

// AllEvents 全部事件（rule-meta API 与校验用）。
var AllEvents = []string{EventMessage, EventGroupIncrease, EventGroupRequest}

// 上限常量（与 state 校验共用语义）。
const (
	MaxRulesPerGroup    = 100 // 每群规则数上限
	MaxConditionsPerRule = 8  // 每条规则条件数上限
	MaxActionsPerRule    = 4  // 每条规则动作数上限
	MaxRuleNameLen       = 64 // 规则名长度上限
	MaxRuleIDLen         = 32 // 规则 ID 长度上限
)

// 条件组合模式常量（rule.when_mode 字段）。
const (
	WhenModeAll = "all" // 全部条件满足（AND，默认）
	WhenModeAny = "any" // 任一条件满足（OR）
)

// Rule 一条规则：事件触发后，当条件（WhenMode 组合）满足时顺序执行动作。
type Rule struct {
	ID       string      `json:"id"`               // 群内唯一稳定标识
	Name     string      `json:"name"`             // 规则名（审计展示），群内唯一
	Enabled  bool        `json:"enabled"`
	Event    string      `json:"event"`            // EventMessage / EventGroupIncrease / EventGroupRequest
	WhenMode string      `json:"when_mode,omitempty"` // all=全部满足（AND，默认）/ any=任一满足（OR）
	When     []Condition `json:"when,omitempty"`   // 空 = 恒真
	Then     []Action    `json:"then"`             // 顺序执行；空 = 纯过滤器规则
	Break    bool        `json:"break"`            // true = 执行后停止匹配后续规则
}

// Condition 一个条件（判别联合：Type 决定 Params 解析器）。
type Condition struct {
	Type   string          `json:"type"`
	Negate bool            `json:"negate,omitempty"` // 取反
	Params json.RawMessage `json:"params,omitempty"`
}

// Action 一个动作（判别联合：Type 决定 Params 解析器）。
type Action struct {
	Type   string          `json:"type"`
	Params json.RawMessage `json:"params,omitempty"`
}

// EventContext 一次事件的完整上下文（由 bot 构造注入）。
type EventContext struct {
	Event     string    // EventMessage / EventGroupIncrease / EventGroupRequest
	GroupID   int64
	UserID    int64     // 消息发送者 / 入群者 / 申请人
	SelfID    int64
	Role      string    // owner / admin / member（message 事件）
	Text      string    // 消息纯文本（message）；申请备注（group_request）
	MsgTypes  []string  // 消息含有的 segment 类型（message 事件）
	MsgCounts map[string]int // 消息各 segment 类型数量（message 事件）
	AtTargets []int64   // 消息 @ 的 QQ 列表（message 事件；含 all 时为空列表）
	OperatorID int64    // group_increase：邀请人 QQ；其余为 0
	MessageID int32     // 撤回动作需要
	SubType   string    // group_request：add / invite
	Flag      string    // group_request：审批 flag
	Time      time.Time

	// 惰性派生（bot 注入，避免 rules 依赖配置细节）
	IsWhitelisted func() bool   // 群白名单判定
	IsOwner       bool          // userID == 系统 owner
	Nickname      func() string // 目标昵称（模板变量用；失败回退 QQ 号）
	JoinTime      func() *time.Time // 目标入群时间（user_joined_within 条件；获取失败返回 nil）
	BotName       string        // 机器人名（模板变量 {bot_name}）
}

// FieldError 一条字段级校验错误（state.ValidationError 同构，避免包依赖）。
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// RuleError 规则集校验错误集合。
type RuleError struct {
	Fields []FieldError
}

func (e *RuleError) Error() string {
	if e == nil || len(e.Fields) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, f := range e.Fields {
		if i > 0 {
			sb.WriteString("；")
		}
		sb.WriteString(f.Field)
		sb.WriteString(": ")
		sb.WriteString(f.Message)
	}
	return sb.String()
}

func (e *RuleError) add(field, msg string) {
	e.Fields = append(e.Fields, FieldError{Field: field, Message: msg})
}

// ValidationErrors 返回字段错误列表（nil 表示无错误）。
func (e *RuleError) ValidationErrors() []FieldError {
	if e == nil {
		return nil
	}
	return e.Fields
}

// 参数解析失败错误（编译/校验时兜底）。
type paramError struct{ msg string }

func (e *paramError) Error() string { return e.msg }

func paramErrf(format string, args ...any) error {
	return &paramError{msg: fmt.Sprintf(format, args...)}
}
