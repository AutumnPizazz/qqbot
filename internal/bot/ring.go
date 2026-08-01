package bot

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

// MessageRecord 是最近收到的群消息记录（网页撤回目标选择，有界内存）。
type MessageRecord struct {
	MessageID int32     `json:"message_id"`
	GroupID   int64     `json:"group_id"`
	UserID    int64     `json:"user_id"`
	Nickname  string    `json:"nickname,omitempty"`
	Time      time.Time `json:"time"`
	Text      string    `json:"text"`
}

// msgRing 是有界内存环形缓冲：保留最近 max 条群消息。
type msgRing struct {
	mu  sync.Mutex
	max int
	buf []MessageRecord
}

func newMsgRing(max int) *msgRing {
	if max <= 0 {
		max = 500
	}
	return &msgRing{max: max}
}

// push 追加一条消息；超出上限时丢弃最旧。
func (r *msgRing) push(m MessageRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m.Time.IsZero() {
		m.Time = time.Now()
	}
	r.buf = append(r.buf, m)
	if len(r.buf) > r.max {
		r.buf = append([]MessageRecord(nil), r.buf[len(r.buf)-r.max:]...)
	}
}

// recent 返回指定群最近 limit 条消息（时间倒序）。
func (r *msgRing) recent(groupID int64, limit int) []MessageRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	out := make([]MessageRecord, 0, limit)
	for i := len(r.buf) - 1; i >= 0 && len(out) < limit; i-- {
		if groupID != 0 && r.buf[i].GroupID != groupID {
			continue
		}
		out = append(out, r.buf[i])
	}
	return out
}

// QueryAudit 导出审计分页查询（供网页 API 使用）。
func (b *Bot) QueryAudit(q AuditQuery) AuditPage {
	if b.audit == nil {
		return AuditPage{}
	}
	return b.audit.query(q)
}

// CountersByGroup 返回指定群的计数器状态（网页排障面板）。
// 返回 counterID → 用户 → 次数；无状态时为空 map。
func (b *Bot) CountersByGroup(groupID int64) map[string]map[int64]int {
	prefix := strconv.FormatInt(groupID, 10) + "/"
	out := map[string]map[int64]int{}
	for key, users := range b.engine.CounterSnapshot() {
		if strings.HasPrefix(key, prefix) {
			out[strings.TrimPrefix(key, prefix)] = users
		}
	}
	return out
}

// RecentMessages 导出最近群消息（供网页撤回目标选择）。
func (b *Bot) RecentMessages(groupID int64, limit int) []MessageRecord {
	return b.ring.recent(groupID, limit)
}

// Actions 返回人工群管服务。
func (b *Bot) Actions() *ActionService { return b.actions }
