package rules

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Executor 是动作执行接口，由 bot 包实现（内部走 ActionService + 审计）。
// note 为审计说明（rules 包已带规则名前缀）；notifyText 为群内通知文案（空=不通知）。
type Executor interface {
	// Mute 禁言 targetID minutes 分钟；notifyText 非空时 @ 目标发送通知。
	Mute(groupID, targetID int64, minutes int, notifyText, note string) error
	// Kick 移出群成员；notifyText 非空时发通知。
	Kick(groupID, targetID int64, notifyText, note string) error
	// Warn @ 目标发送警告文本并写审计。
	Warn(groupID, targetID int64, text, note string) error
	// Recall 撤回指定消息（message 事件）。
	Recall(messageID int32, note string) error
	// Send 发送普通群消息（不审计）。
	Send(groupID int64, text string) error
	// SendAt 发送 @ 目标的消息（不审计）。
	SendAt(groupID, targetID int64, text string) error
	// ApproveJoin 同意加群申请 / 邀请。
	ApproveJoin(groupID, userID int64, flag, subType string) error
	// RejectJoin 拒绝加群申请 / 邀请。
	RejectJoin(groupID, userID int64, flag, subType string, reason string) error
	// SendPrivate 私聊发送（违规通知等，不审计）。
	SendPrivate(userID int64, text string) error
	// WholeBan 开启或关闭全员禁言（写审计）。
	WholeBan(groupID int64, enable bool, note string) error
	// Card 设置或清空目标群名片（写审计）。
	Card(groupID, targetID int64, card, note string) error
}

// ---- 动作执行实现 ----

// ruleExec 一次规则命中的执行上下文。
type ruleExec struct {
	rule    *Rule
	matched string // 首个命中条件的匹配文本（{matched_text}）
}

// note 生成审计说明（规则名前缀）。
func (x *ruleExec) note(extra string) string {
	if extra == "" {
		return fmt.Sprintf("规则「%s」", x.rule.Name)
	}
	return fmt.Sprintf("规则「%s」：%s", x.rule.Name, extra)
}

// replaceVars 模板变量替换。
func replaceVars(s string, ctx *EventContext, x *ruleExec, count int) string {
	nick := ""
	if ctx.Nickname != nil {
		nick = ctx.Nickname()
	}
	if nick == "" {
		nick = strconv.FormatInt(ctx.UserID, 10)
	}
	return strings.NewReplacer(
		"{nickname}", nick,
		"{user_id}", strconv.FormatInt(ctx.UserID, 10),
		"{group_id}", strconv.FormatInt(ctx.GroupID, 10),
		"{bot_name}", ctx.BotName,
		"{matched_text}", x.matched,
		"{rule_name}", x.rule.Name,
		"{count}", strconv.Itoa(count),
	).Replace(s)
}

// muteNotifyText 禁言通知文案（与旧版行为等价）。
func muteNotifyText(ctx *EventContext, p *MuteParams) string {
	reason := p.Reason
	if reason == "" {
		reason = "违反群规"
	}
	return fmt.Sprintf("[CQ:at,qq=%d] 你因【%s】被禁言 %d 分钟", ctx.UserID, reason, p.Minutes)
}

// kickNotifyText 移出通知文案（与旧版行为等价）。
func kickNotifyText(ctx *EventContext, p *KickParams) string {
	reason := p.Reason
	if reason == "" {
		reason = "违反群规"
	}
	return fmt.Sprintf("已将 %d 移出本群：%s", ctx.UserID, reason)
}

func actSendMessage(e *Engine, ctx *EventContext, x *ruleExec, p any) error {
	pp := p.(*SendMessageParams)
	text := replaceVars(pp.Message, ctx, x, 0)
	if pp.At {
		return e.exec.SendAt(ctx.GroupID, ctx.UserID, text)
	}
	return e.exec.Send(ctx.GroupID, text)
}

func actWarn(e *Engine, ctx *EventContext, x *ruleExec, p any) error {
	pp := p.(*WarnParams)
	count := 0
	if pp.CounterID != "" {
		count = e.counters.Get(ctx.GroupID, pp.CounterID, ctx.UserID, 24*time.Hour)
	}
	text := replaceVars(pp.Reason, ctx, x, count)
	if !strings.HasPrefix(text, "警告") {
		text = "警告：" + text
	}
	return e.exec.Warn(ctx.GroupID, ctx.UserID, text, x.note(pp.Reason))
}

func actMute(e *Engine, ctx *EventContext, x *ruleExec, p any) error {
	pp := p.(*MuteParams)
	notify := ""
	if pp.Notice {
		notify = replaceVars(muteNotifyText(ctx, pp), ctx, x, 0)
	}
	return e.exec.Mute(ctx.GroupID, ctx.UserID, pp.Minutes, notify, x.note(pp.Reason))
}

func actKick(e *Engine, ctx *EventContext, x *ruleExec, p any) error {
	pp := p.(*KickParams)
	notify := ""
	if pp.Notice {
		notify = replaceVars(kickNotifyText(ctx, pp), ctx, x, 0)
	}
	return e.exec.Kick(ctx.GroupID, ctx.UserID, notify, x.note(pp.Reason))
}

func actRecall(e *Engine, ctx *EventContext, x *ruleExec, _ any) error {
	if ctx.MessageID <= 0 {
		return paramErrf("消息 ID 无效（仅消息事件可撤回）")
	}
	return e.exec.Recall(ctx.MessageID, x.note("撤回违规消息"))
}

func actIncrementCounter(e *Engine, ctx *EventContext, _ *ruleExec, p any) error {
	pp := p.(*CounterParams)
	window := time.Duration(pp.WindowHours) * time.Hour
	if pp.WindowHours <= 0 {
		window = 24 * time.Hour
	}
	step := pp.Step
	if step <= 0 {
		step = 1
	}
	e.counters.Increment(ctx.GroupID, pp.CounterID, ctx.UserID, step, window)
	return nil
}

func actResetCounter(e *Engine, ctx *EventContext, _ *ruleExec, p any) error {
	pp := p.(*CounterParams)
	e.counters.Reset(ctx.GroupID, pp.CounterID, ctx.UserID)
	return nil
}

func actSetCounter(e *Engine, ctx *EventContext, _ *ruleExec, p any) error {
	pp := p.(*CounterParams)
	e.counters.Set(ctx.GroupID, pp.CounterID, ctx.UserID, pp.Count)
	return nil
}

func actApproveJoin(e *Engine, ctx *EventContext, _ *ruleExec, _ any) error {
	if ctx.Flag == "" {
		return paramErrf("请求 flag 为空（仅加群申请事件可审批）")
	}
	return e.exec.ApproveJoin(ctx.GroupID, ctx.UserID, ctx.Flag, ctx.SubType)
}

func actRejectJoin(e *Engine, ctx *EventContext, _ *ruleExec, p any) error {
	if ctx.Flag == "" {
		return paramErrf("请求 flag 为空（仅加群申请事件可审批）")
	}
	pp := p.(*RejectJoinParams)
	return e.exec.RejectJoin(ctx.GroupID, ctx.UserID, ctx.Flag, ctx.SubType, pp.Reason)
}

func actNoop(_ *Engine, _ *EventContext, _ *ruleExec, _ any) error {
	return nil
}

func actSendPrivate(e *Engine, ctx *EventContext, x *ruleExec, p any) error {
	pp := p.(*SendPrivateParams)
	text := replaceVars(pp.Message, ctx, x, 0)
	return e.exec.SendPrivate(ctx.UserID, text)
}

func actWholeBan(e *Engine, ctx *EventContext, x *ruleExec, p any) error {
	pp := p.(*WholeBanParams)
	note := ""
	if pp.Enable {
		note = "开启全员禁言"
	} else {
		note = "解除全员禁言"
	}
	return e.exec.WholeBan(ctx.GroupID, pp.Enable, x.note(note))
}

func actCard(e *Engine, ctx *EventContext, x *ruleExec, p any) error {
	pp := p.(*CardParams)
	card := replaceVars(pp.Card, ctx, x, 0)
	return e.exec.Card(ctx.GroupID, ctx.UserID, card, x.note("设置群名片"))
}

func actDecrementCounter(e *Engine, ctx *EventContext, _ *ruleExec, p any) error {
	pp := p.(*CounterParams)
	step := pp.Step
	if step <= 0 {
		step = 1
	}
	e.counters.Decrement(ctx.GroupID, pp.CounterID, ctx.UserID, step)
	return nil
}
