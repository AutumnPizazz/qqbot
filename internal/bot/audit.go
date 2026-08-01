package bot

import (
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"time"

	"qqbot/internal/fsutil"
)

const maxAuditEntries = 10000

// AuditEntry 是一条可持久化的管理操作记录。
// 设计文档 16 节：request_id、actor_type、source_ip、config_revision、
// action_status、audit_status；QR 内容、session、CSRF、setup token 永不记录。
type AuditEntry struct {
	Time           time.Time `json:"time"`
	RequestID      string    `json:"request_id,omitempty"`
	GroupID        int64     `json:"group_id"`
	ActorType      string    `json:"actor_type,omitempty"` // admin / automatic / system
	ActorID        int64     `json:"actor_id,omitempty"`
	Source         string    `json:"source"` // config / manual / automatic / api
	SourceIP       string    `json:"source_ip,omitempty"`
	ConfigRevision int64     `json:"config_revision,omitempty"`
	Action         string    `json:"action"`
	TargetID       int64     `json:"target_id,omitempty"`
	ActionStatus   string    `json:"action_status"`          // ok / failed / unknown
	Result         string    `json:"result"`                 // 旧字段兼容：ok / failed（unknown 记 failed）
	AuditStatus    string    `json:"audit_status,omitempty"` // ok / failed（写盘结果）
	Detail         string    `json:"detail,omitempty"`
}

// AuditQuery 审计分页查询条件。
type AuditQuery struct {
	GroupID int64  // 0 = 全部群
	Source  string // "" = 全部来源
	Result  string // "" = 全部结果；支持 ok/failed/unknown
	Cursor  int64  // 上一页最后一条的 UnixMilli；0 = 首页
	Limit   int    // 默认 20，上限 100
}

// AuditPage 一页审计结果。
type AuditPage struct {
	Entries    []AuditEntry `json:"entries"`
	NextCursor int64        `json:"next_cursor,omitempty"` // 0 = 没有更多
}

type auditStore struct {
	mu      sync.Mutex
	path    string
	entries []AuditEntry
}

func newAuditStore(path string) *auditStore {
	s := &auditStore{path: path}
	data, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(data, &s.entries)
	}
	if len(s.entries) > maxAuditEntries {
		s.entries = append([]AuditEntry(nil), s.entries[len(s.entries)-maxAuditEntries:]...)
	}
	return s
}

// append 追加一条审计记录；返回写盘错误（调用方据此填 AuditStatus）。
func (s *auditStore) append(entry AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry.Time.IsZero() {
		entry.Time = time.Now()
	}
	if entry.ActionStatus == "" {
		entry.ActionStatus = map[bool]string{true: "ok", false: "failed"}[entry.Result == "ok"]
	}
	if entry.Result == "" {
		entry.Result = map[bool]string{true: "ok", false: "failed"}[entry.ActionStatus == "ok"]
	}
	if entry.ActorType == "" {
		entry.ActorType = "admin"
	}
	s.entries = append(s.entries, entry)
	if len(s.entries) > maxAuditEntries {
		s.entries = append([]AuditEntry(nil), s.entries[len(s.entries)-maxAuditEntries:]...)
	}
	data, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(s.path, data, 0o644)
}

// query 按条件倒序分页查询（游标 = 上一页最后一条时间戳）。
func (s *auditStore) query(q AuditQuery) AuditPage {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	page := AuditPage{}
	for i := len(s.entries) - 1; i >= 0 && len(page.Entries) < limit; i-- {
		e := s.entries[i]
		if q.GroupID != 0 && e.GroupID != q.GroupID {
			continue
		}
		if q.Source != "" && e.Source != q.Source {
			continue
		}
		if q.Result != "" && e.ActionStatus != q.Result {
			continue
		}
		ms := e.Time.UnixMilli()
		if q.Cursor != 0 && ms >= q.Cursor {
			continue
		}
		page.Entries = append(page.Entries, e)
	}
	if len(page.Entries) > 0 {
		last := page.Entries[len(page.Entries)-1]
		// 判断是否还有更多满足条件的记录
		hasMore := false
		for i := len(s.entries) - 1; i >= 0; i-- {
			e := s.entries[i]
			if q.GroupID != 0 && e.GroupID != q.GroupID {
				continue
			}
			if q.Source != "" && e.Source != q.Source {
				continue
			}
			if q.Result != "" && e.ActionStatus != q.Result {
				continue
			}
			if e.Time.UnixMilli() < last.Time.UnixMilli() {
				hasMore = true
				break
			}
		}
		if hasMore {
			page.NextCursor = last.Time.UnixMilli()
		}
	}
	return page
}

// recordAudit 写审计；失败记日志（配置成功与审计失败是两个独立结果）。
func (b *Bot) recordAudit(entry AuditEntry) {
	if b.audit == nil {
		return
	}
	if err := b.audit.append(entry); err != nil {
		slog.Error("保存审计记录失败", "err", err)
	}
}

// recordAction 兼容旧入口：自动处罚/旧指令的审计写入。
// ActionStatus 由 err 推导（ok/failed），actor 类型按 source 推导。
func (b *Bot) recordAction(groupID, actorID, targetID int64, source, action, detail string, err error) {
	status := "ok"
	if err != nil {
		status = "failed"
		if detail != "" {
			detail += "；"
		}
		detail += err.Error()
	}
	actorType := "admin"
	if source == "automatic" {
		actorType = "automatic"
	}
	entry := AuditEntry{
		GroupID: groupID, ActorID: actorID, TargetID: targetID,
		ActorType: actorType, Source: source, Action: action,
		ActionStatus: status, Result: status, Detail: detail,
	}
	if err := b.audit.append(entry); err != nil {
		slog.Error("保存审计记录失败", "err", err)
	}
}
