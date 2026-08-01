// Package bot 实现机器人核心逻辑：规则引擎事件管线（消息/入群/加群申请）、
// 人工群管 ActionService 与审计。
//
// 阶段 5 起：QQ 私聊管理指令已停用（网页后台为唯一管理入口）；
// 阶段 6（规则引擎）起：所有自动行为（关键词/刷屏/欢迎/审批/豁免）由
// internal/rules 引擎统一处理，配置源为 control.json v2（rules 字段）。
package bot

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"time"

	"qqbot/internal/config"
	"qqbot/internal/onebot"
	"qqbot/internal/rules"
)

// Bot 聚合配置与客户端，注册事件处理器。
// 配置采用 atomic 快照：外部配置源（ConfigService）更新后经
// SetExternalConfigSource 原子替换，事件处理 goroutine 读取旧快照永远安全。
type Bot struct {
	cfgPtr  atomic.Pointer[config.Config] // 生效配置（来自 control.json）
	client  *onebot.Manager               // generation-aware 连接管理器
	actions *ActionService                // 人工群管统一入口（网页 API 与自动处罚共用）
	engine  *rules.Engine                 // 规则引擎（自动行为）
	audit   *auditStore
	ring    *msgRing
	joins   *joinCache                    // 成员入群时间缓存（新人保护期条件）
	started time.Time
}

// New 创建机器人实例（数据目录使用默认 data/）。
func New(cfg *config.Config, client *onebot.Manager) *Bot {
	return NewWithDir(cfg, client, "data")
}

// NewWithDir 创建机器人实例，持久化文件（audit.json）存放在 dataDir 下。
func NewWithDir(cfg *config.Config, client *onebot.Manager, dataDir string) *Bot {
	b := &Bot{
		client:  client,
		audit:   newAuditStore(filepath.Join(dataDir, "audit.json")),
		ring:    newMsgRing(500),
		started: time.Now(),
	}
	b.cfgPtr.Store(cfg)
	b.actions = NewActionService(client, func(e AuditEntry) error {
		if b.audit == nil {
			return nil
		}
		return b.audit.append(e)
	})
	b.joins = newJoinCache(10 * time.Minute)
	b.engine = rules.New(b)
	b.applyRules(cfg)
	return b
}

// cfg 返回当前生效配置。
func (b *Bot) cfg() *config.Config { return b.cfgPtr.Load() }

// SetExternalConfigSource 设置外部配置源：后续生效配置由 get 提供
// （ConfigService.Effective），原子替换生效快照并重建规则引擎。
func (b *Bot) SetExternalConfigSource(get func() *config.Config) {
	cfg := get()
	b.cfgPtr.Store(cfg)
	b.applyRules(cfg)
	slog.Info("机器人配置已更新（control.json 热生效）")
}

// applyRules 把生效配置按群编译进规则引擎；检测状态（计数/刷屏）随规则重建清零。
func (b *Bot) applyRules(cfg *config.Config) {
	byGroup := make(map[int64][]rules.Rule)
	for _, g := range cfg.Groups {
		if g.Enabled && len(g.Rules) > 0 {
			byGroup[g.GroupID] = g.Rules
		}
	}
	b.engine.ResetState()
	if err := b.engine.SetRulesByGroup(byGroup); err != nil {
		// 配置已通过 state 校验，理论不可达；保留旧规则并告警
		slog.Error("规则编译失败（保留旧规则）", "err", err)
		return
	}
	slog.Debug("规则引擎已加载", "groups", len(byGroup))
}

// Start 注册事件处理。
func (b *Bot) Start() {
	b.client.On("message", b.onMessage)
	b.client.On("notice", b.onNotice)
	b.client.On("request", b.onRequest)
	slog.Info("机器人已启动", "groups", len(b.cfg().Groups))
}

// onMessage 消息事件入口：私聊消息一律忽略（网页后台是唯一管理入口），
// 群聊消息进入规则引擎（豁免/关键词/刷屏等全部由规则表达）。
func (b *Bot) onMessage(raw json.RawMessage) error {
	var m onebot.GroupMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("解析消息事件失败: %w", err)
	}
	slog.Debug("收到消息", "type", m.MessageType, "user", m.UserID, "raw", truncateStr(m.RawMessage, 40))
	if m.MessageType == "private" {
		return nil
	}
	if m.MessageType != "group" || m.UserID == m.SelfID {
		return nil // 只处理群聊，忽略机器人自己的消息
	}
	if b.cfg().Group(m.GroupID) == nil {
		return nil // 未配置的群直接忽略
	}
	text := truncateStr(extractText(m), 200)
	// 记录最近消息（供网页撤回目标选择）
	b.ring.push(MessageRecord{
		MessageID: m.MessageID, GroupID: m.GroupID, UserID: m.UserID,
		Nickname: m.Sender.Nickname, Time: time.Unix(m.Time, 0), Text: text,
	})
	role := m.Sender.Role
	if role == "" {
		role = m.GroupSender.Role
	}
	cfg := b.cfg()
	ctx := &rules.EventContext{
		Event:     rules.EventMessage,
		GroupID:   m.GroupID,
		UserID:    m.UserID,
		SelfID:    m.SelfID,
		Role:      role,
		Text:      text,
		MsgTypes:  extractMsgTypes(m),
		MsgCounts: extractMsgCounts(m),
		AtTargets: extractAtTargets(m),
		MessageID: m.MessageID,
		Time:      time.Unix(m.Time, 0),
		IsWhitelisted: func() bool {
			return b.cfg().IsWhitelisted(m.GroupID, m.UserID)
		},
		IsOwner: cfg.Bot.Owner != 0 && m.UserID == cfg.Bot.Owner,
		Nickname: func() string {
			if info, err := b.client.GetGroupMemberInfo(m.GroupID, m.UserID); err == nil && info.Nickname != "" {
				return info.Nickname
		}
			return ""
		},
		JoinTime: func() *time.Time {
			return b.joinTimeOf(m.GroupID, m.UserID)
		},
		BotName: cfg.Bot.Name,
	}
	b.engine.Process(ctx)
	return nil
}

// onNotice 通知事件（新人入群等）→ 规则引擎。
func (b *Bot) onNotice(raw json.RawMessage) error {
	var n struct {
		NoticeType string `json:"notice_type"`
		GroupID    int64  `json:"group_id"`
		UserID     int64  `json:"user_id"`
		OperatorID int64  `json:"operator_id"` // 邀请人 QQ
		Time       int64  `json:"time"`
	}
	if err := json.Unmarshal(raw, &n); err != nil {
		return fmt.Errorf("解析通知事件失败: %w", err)
	}
	if n.NoticeType != "group_increase" {
		return nil
	}
	if b.cfg().Group(n.GroupID) == nil {
		return nil
	}
	ctx := &rules.EventContext{
		Event:      rules.EventGroupIncrease,
		GroupID:    n.GroupID,
		UserID:     n.UserID,
		OperatorID: n.OperatorID,
		Time:       time.Unix(n.Time, 0),
		Nickname: func() string {
			if info, err := b.client.GetGroupMemberInfo(n.GroupID, n.UserID); err == nil && info.Nickname != "" {
				return info.Nickname
			}
			return ""
		},
		BotName: b.cfg().Bot.Name,
	}
	b.engine.Process(ctx)
	return nil
}

// onRequest 请求事件（加群申请 / 邀请机器人入群）→ 规则引擎。
func (b *Bot) onRequest(raw json.RawMessage) error {
	var r onebot.GroupRequest
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("解析请求事件失败: %w", err)
	}
	if r.RequestType != "group" {
		return nil
	}
	if b.cfg().Group(r.GroupID) == nil {
		return nil
	}
	cfg := b.cfg()
	ctx := &rules.EventContext{
		Event:   rules.EventGroupRequest,
		GroupID: r.GroupID,
		UserID:  r.UserID,
		Text:    r.Comment,
		SubType: r.SubType, // add=加群申请 invite=机器人被邀请
		Flag:    r.Flag,
		Time:    time.Unix(r.Time, 0),
		IsOwner: cfg.Bot.Owner != 0 && r.UserID == cfg.Bot.Owner,
		BotName: cfg.Bot.Name,
	}
	b.engine.Process(ctx)
	return nil
}

// truncateStr 截断字符串用于日志。
func truncateStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// errActionFailed 动作结果非 ok 时的统一错误包装（Executor 用）。
func errActionFailed(res ActionResult) error {
	return errors.New(res.Detail)
}
