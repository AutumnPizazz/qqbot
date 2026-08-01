package bot

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"qqbot/internal/onebot"
)

// ActionService 是人工群管的统一入口：网页 API 与自动处罚都通过它调用
// OneBot 并写审计（设计文档 12 节）。网页 handler 不得直接调用 onebot.Manager。
//
// 结果语义：
//   - Result=ok     远端动作确认成功。
//   - Result=failed 远端明确失败（OneBot retcode != 0）。
//   - Result=unknown OneBot 超时：QQ 端可能已执行成功，不能提示安全重试。
//
// 连接不可用（onebot.ErrDisconnected / ErrGenerationClosed）作为 error 返回，
// 由 API 层映射 503；动作本身不写审计（没有执行）。
type ActionService struct {
	mgr   *onebot.Manager
	audit func(AuditEntry) error // 审计写入回调（返回写盘错误）
}

// NewActionService 创建动作服务。
func NewActionService(mgr *onebot.Manager, audit func(AuditEntry) error) *ActionService {
	return &ActionService{mgr: mgr, audit: audit}
}

// ActionRequest 一次动作请求的公共上下文。
type ActionRequest struct {
	RequestID      string // 幂等 key（调用方生成，写入审计）
	ActorID        int64  // 执行者 QQ（网页固定为管理员）
	ActorType      string // admin / automatic / system
	Source         string // api / manual / automatic
	SourceIP       string // 网页来源 IP（可选）
	GroupID        int64
	TargetID       int64  // 目标 QQ；全员禁言/撤回为 0
	ConfigRevision int64  // 提交时的配置版本（可选）
	DetailNote     string // 附加审计说明（如自动处罚原因）
}

// ActionResult 一次动作的结果。
type ActionResult struct {
	Action    string `json:"action"`
	Result    string `json:"result"` // ok / failed / unknown
	Detail    string `json:"detail,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// record 构造审计记录并写入；远端动作成功但审计失败时以独立结果上报（不可回滚）。
func (s *ActionService) record(req ActionRequest, action, detail, actionStatus string) {
	if req.DetailNote != "" {
		if detail != "" {
			detail += "；"
		}
		detail += req.DetailNote
	}
	auditStatus := "ok"
	if s.audit != nil {
		if err := s.audit(AuditEntry{
			RequestID:      req.RequestID,
			GroupID:        req.GroupID,
			ActorType:      orDefault(req.ActorType, "admin"),
			ActorID:        req.ActorID,
			Source:         orDefault(req.Source, "api"),
			SourceIP:       req.SourceIP,
			ConfigRevision: req.ConfigRevision,
			Action:         action,
			TargetID:       req.TargetID,
			ActionStatus:   actionStatus,
			Result:         map[bool]string{true: "ok", false: "failed"}[actionStatus == "ok"],
			AuditStatus:    auditStatus,
			Detail:         detail,
		}); err != nil {
			auditStatus = "failed"
			slog.Error("动作审计写入失败", "action", action, "err", err)
		}
	}
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// execute 统一执行动作：调 OneBot → 结果分类 → 审计。
// 返回 ActionResult 与错误（仅连接类错误作为 error，映射 503）。
func (s *ActionService) execute(req ActionRequest, action, detail string, fn func() error) (ActionResult, error) {
	err := fn()
	if err == nil {
		s.record(req, action, detail, "ok")
		return ActionResult{Action: action, Result: "ok", RequestID: req.RequestID}, nil
	}
	if errors.Is(err, onebot.ErrDisconnected) || errors.Is(err, onebot.ErrGenerationClosed) {
		return ActionResult{}, err // 503：连接不可用，未执行，不写审计
	}
	if strings.Contains(err.Error(), "超时") {
		// OneBot 超时：远端可能已执行成功，结果未知，不能安全重试
		s.record(req, action, detail+"；"+err.Error(), "unknown")
		return ActionResult{Action: action, Result: "unknown", Detail: err.Error(), RequestID: req.RequestID}, nil
	}
	s.record(req, action, detail, "failed")
	return ActionResult{Action: action, Result: "failed", Detail: err.Error(), RequestID: req.RequestID}, nil
}

// Mute 禁言群成员。
func (s *ActionService) Mute(req ActionRequest, minutes int) (ActionResult, error) {
	if minutes <= 0 || minutes > 43200 {
		return ActionResult{}, fmt.Errorf("禁言时长范围必须为 1~43200 分钟")
	}
	detail := fmt.Sprintf("%d 分钟", minutes)
	return s.execute(req, "禁言", detail, func() error {
		return s.mgr.SetGroupBan(req.GroupID, req.TargetID, int64(minutes)*60)
	})
}

// Unmute 解除禁言。
func (s *ActionService) Unmute(req ActionRequest) (ActionResult, error) {
	return s.execute(req, "解除禁言", "", func() error {
		return s.mgr.SetGroupBan(req.GroupID, req.TargetID, 0)
	})
}

// Kick 移出群成员。
func (s *ActionService) Kick(req ActionRequest, reason string) (ActionResult, error) {
	if len([]rune(reason)) > 200 {
		return ActionResult{}, fmt.Errorf("移出原因过长（最多 200 字符）")
	}
	return s.execute(req, "移出群", reason, func() error {
		return s.mgr.SetGroupKick(req.GroupID, req.TargetID, false)
	})
}

// WholeBan 开启或关闭全员禁言。
func (s *ActionService) WholeBan(req ActionRequest, enabled bool) (ActionResult, error) {
	detail := map[bool]string{true: "开启", false: "关闭"}[enabled]
	return s.execute(req, "全员禁言", detail, func() error {
		return s.mgr.SetGroupWholeBan(req.GroupID, enabled)
	})
}

// Recall 撤回指定消息。
// messageID <= 0 直接返回参数错误；消息与群的关联校验由调用方提供
// （无法校验时必须向 UI/审计明确说明，见设计文档 12 节）。
func (s *ActionService) Recall(req ActionRequest, messageID int32) (ActionResult, error) {
	if messageID <= 0 {
		return ActionResult{}, fmt.Errorf("消息 ID 无效")
	}
	detail := fmt.Sprintf("消息 ID %d", messageID)
	return s.execute(req, "撤回消息", detail, func() error {
		return s.mgr.DeleteMsg(messageID)
	})
}

// Card 设置或清空群成员名片。
func (s *ActionService) Card(req ActionRequest, card string) (ActionResult, error) {
	if len([]rune(card)) > 64 {
		return ActionResult{}, fmt.Errorf("群名片过长（最多 64 字符）")
	}
	detail := card
	if card == "" {
		detail = "清空名片"
	}
	return s.execute(req, "设置群名片", detail, func() error {
		return s.mgr.SetGroupCard(req.GroupID, req.TargetID, card)
	})
}
