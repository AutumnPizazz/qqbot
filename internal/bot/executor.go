package bot

import (
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"qqbot/internal/onebot"
)

// ---- rules.Executor 实现：规则引擎动作 → ActionService + 审计 ----
// 自动处罚与网页人工操作共用 ActionService，审计带规则名（note 由 rules 包拼装）。

// Mute 禁言并（可选）@ 通知。
func (b *Bot) Mute(groupID, targetID int64, minutes int, notifyText, note string) error {
	req := ActionRequest{
		ActorType: "automatic", Source: "automatic",
		GroupID: groupID, TargetID: targetID, DetailNote: note,
	}
	res, err := b.actions.Mute(req, minutes)
	if err != nil {
		slog.Warn("禁言失败（连接不可用）", "group", groupID, "user", targetID, "err", err)
		return err
	}
	if res.Result != "ok" {
		return errActionFailed(res)
	}
	if notifyText != "" {
		return b.client.SendGroupMsg(groupID, notifyText)
	}
	return nil
}

// Kick 移出并（可选）发通知。
func (b *Bot) Kick(groupID, targetID int64, notifyText, note string) error {
	req := ActionRequest{
		ActorType: "automatic", Source: "automatic",
		GroupID: groupID, TargetID: targetID, DetailNote: note,
	}
	res, err := b.actions.Kick(req, note)
	if err != nil {
		slog.Warn("移出失败（连接不可用）", "group", groupID, "user", targetID, "err", err)
		return err
	}
	if res.Result != "ok" {
		return errActionFailed(res)
	}
	if notifyText != "" {
		return b.client.SendGroupMsg(groupID, notifyText)
	}
	return nil
}

// Warn @ 目标发送警告文本并写审计。
func (b *Bot) Warn(groupID, targetID int64, text, note string) error {
	err := b.client.SendGroupMsgAt(groupID, targetID, text)
	b.recordAction(groupID, 0, targetID, "automatic", "警告", note, err)
	return err
}

// Recall 撤回指定消息。
func (b *Bot) Recall(messageID int32, note string) error {
	req := ActionRequest{
		ActorType: "automatic", Source: "automatic",
		TargetID: 0, DetailNote: note,
	}
	res, err := b.actions.Recall(req, messageID)
	if err != nil {
		return err
	}
	if res.Result != "ok" {
		return errActionFailed(res)
	}
	return nil
}

// Send 发送普通群消息（不审计）。
func (b *Bot) Send(groupID int64, text string) error {
	return b.client.SendGroupMsg(groupID, text)
}

// SendAt 发送 @ 目标的消息（不审计）。
func (b *Bot) SendAt(groupID, targetID int64, text string) error {
	return b.client.SendGroupMsgAt(groupID, targetID, text)
}

// ApproveJoin 同意加群申请 / 邀请。
func (b *Bot) ApproveJoin(groupID, userID int64, flag, subType string) error {
	err := b.client.SetGroupAddRequest(flag, subType, true, "")
	action := "同意入群申请"
	if subType == "invite" {
		action = "接受机器人入群邀请"
	}
	b.recordAction(groupID, 0, userID, "automatic", action, "", err)
	return err
}

// RejectJoin 拒绝加群申请 / 邀请。
func (b *Bot) RejectJoin(groupID, userID int64, flag, subType, reason string) error {
	err := b.client.SetGroupAddRequest(flag, subType, false, reason)
	action := "拒绝入群申请"
	if subType == "invite" {
		action = "拒绝机器人入群邀请"
	}
	detail := ""
	if reason != "" {
		detail = "原因：" + reason
	}
	b.recordAction(groupID, 0, userID, "automatic", action, detail, err)
	return err
}

// SendPrivate 私聊发送（不审计）。
func (b *Bot) SendPrivate(userID int64, text string) error {
	return b.client.SendPrivateMsg(userID, text)
}

// WholeBan 开启/关闭全员禁言（写审计）。
func (b *Bot) WholeBan(groupID int64, enable bool, note string) error {
	req := ActionRequest{
		ActorType: "automatic", Source: "automatic",
		GroupID: groupID, DetailNote: note,
	}
	res, err := b.actions.WholeBan(req, enable)
	if err != nil {
		slog.Warn("全员禁言失败（连接不可用）", "group", groupID, "enable", enable, "err", err)
		return err
	}
	if res.Result != "ok" {
		return errActionFailed(res)
	}
	return nil
}

// Card 设置/清空目标群名片（写审计）。
func (b *Bot) Card(groupID, targetID int64, card, note string) error {
	req := ActionRequest{
		ActorType: "automatic", Source: "automatic",
		GroupID: groupID, TargetID: targetID, DetailNote: note,
	}
	res, err := b.actions.Card(req, card)
	if err != nil {
		slog.Warn("设置群名片失败（连接不可用）", "group", groupID, "user", targetID, "err", err)
		return err
	}
	if res.Result != "ok" {
		return errActionFailed(res)
	}
	return nil
}

// ---- 消息类型提取（message_has_type 条件输入） ----

// cqTypeRe 从 CQ 码提取段类型，如 [CQ:image,file=...] → image。
var cqTypeRe = regexp.MustCompile(`\[CQ:([a-z]+)`)

// cqAtRe 从 CQ 码提取 @ 目标，如 [CQ:at,qq=123] / [CQ:at,qq=all]。
var cqAtRe = regexp.MustCompile(`\[CQ:at,qq=([0-9]+|all)\]`)

// extractMsgTypes 返回消息包含的 segment 类型集合（text/image/at/url/...）。
// 兼容两种消息格式：Message 数组（NapCat 标准）与 RawMessage 文本（CQ 码）。
func extractMsgTypes(m onebot.GroupMessage) []string {
	seen := map[string]bool{}
	if len(m.Message) > 0 {
		for _, seg := range m.Message {
			if seg.Type != "" {
				seen[seg.Type] = true
			}
		}
	} else if m.RawMessage != "" {
		matched := false
		for _, match := range cqTypeRe.FindAllStringSubmatch(m.RawMessage, -1) {
			seen[match[1]] = true
			matched = true
		}
		if !matched {
			seen["text"] = true // 无 CQ 码的纯文本
		}
	}
	// 文本中的链接归类为 url（segment 层没有 url 类型）
	lower := strings.ToLower(extractText(m))
	if strings.Contains(lower, "http://") || strings.Contains(lower, "https://") {
		seen["url"] = true
	}
	out := make([]string, 0, len(seen))
	for _, t := range []string{"text", "image", "at", "url", "face", "record", "video", "file"} {
		if seen[t] {
			out = append(out, t)
		}
	}
	for t := range seen {
		known := false
		for _, k := range out {
			if k == t {
				known = true
				break
			}
		}
		if !known {
			out = append(out, t)
		}
	}
	return out
}

// extractMsgCounts 返回消息各 segment 类型数量（image_count 条件用）。
func extractMsgCounts(m onebot.GroupMessage) map[string]int {
	counts := map[string]int{}
	if len(m.Message) > 0 {
		for _, seg := range m.Message {
			if seg.Type != "" {
				counts[seg.Type]++
			}
		}
	} else if m.RawMessage != "" {
		for _, match := range cqTypeRe.FindAllStringSubmatch(m.RawMessage, -1) {
			counts[match[1]]++
		}
	}
	return counts
}

// extractAtTargets 返回消息 @ 的 QQ 列表（at_count / mention_self 条件用）。
// @all 时返回空列表（无法枚举，交由 at_count=0 语义处理）。
func extractAtTargets(m onebot.GroupMessage) []int64 {
	var out []int64
	if len(m.Message) > 0 {
		for _, seg := range m.Message {
			if seg.Type != "at" {
				continue
			}
			switch qq := seg.Data["qq"].(type) {
			case string:
				if qq == "all" {
					return nil
				}
				if n, err := strconv.ParseInt(qq, 10, 64); err == nil && n > 0 {
					out = append(out, n)
				}
			case float64:
				if int64(qq) > 0 {
					out = append(out, int64(qq))
				}
			}
		}
		return out
	}
	for _, match := range cqAtRe.FindAllStringSubmatch(m.RawMessage, -1) {
		if match[1] == "all" {
			return nil
		}
		if n, err := strconv.ParseInt(match[1], 10, 64); err == nil && n > 0 {
			out = append(out, n)
		}
	}
	return out
}
