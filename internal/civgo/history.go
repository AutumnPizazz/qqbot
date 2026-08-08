package civgo

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// 历史条目截断上限。
const (
	historyQuestionMax = 100  // 问题截断字数
	historyAnswerMax   = 300  // 回答截断字数
	historyRecallChars = 2500 // 单次召回输出总字符上限
)

// HistoryEntry 一条群问答历史（AI 决策召回的最小单元）。
type HistoryEntry struct {
	Time     time.Time `json:"time"`
	UserID   int64     `json:"user_id"`
	Question string    `json:"question"` // 截断后
	Answer   string    `json:"answer"`   // 截断后
	Tokens   int       `json:"tokens"`   // 该次问答总消耗（in+out）
}

// HistoryStore 按群维护问答历史环形缓冲，可选持久化（jsonl）。
type HistoryStore struct {
	mu     sync.Mutex
	groups map[int64][]HistoryEntry
	dir    string         // 持久化目录（空 = 不持久化）
	cfg    func() *Config // 热重载快照
}

// NewHistoryStore 创建历史存储并载入既有持久化数据（损坏行跳过，不阻断）。
func NewHistoryStore(dir string, cfg func() *Config) *HistoryStore {
	s := &HistoryStore{groups: map[int64][]HistoryEntry{}, dir: dir, cfg: cfg}
	if dir != "" {
		s.loadAll()
	}
	return s
}

// Append 追加一条问答记录（截断 + 环形淘汰 + 可选持久化）。
func (s *HistoryStore) Append(groupID, userID int64, question, answer string, tokens int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	max := s.cfg().History.MaxEntriesPerGroup
	if max < 1 {
		max = 50
	}
	entry := HistoryEntry{
		Time:     time.Now(),
		UserID:   userID,
		Question: truncateRunes(strings.TrimSpace(question), historyQuestionMax),
		Answer:   truncateRunes(strings.TrimSpace(answer), historyAnswerMax),
		Tokens:   tokens,
	}
	list := s.groups[groupID]
	// 保证时间严格单调（同纳秒并发追加时排序稳定）
	if n := len(list); n > 0 && !entry.Time.After(list[n-1].Time) {
		entry.Time = list[n-1].Time.Add(time.Nanosecond)
	}
	list = append(list, entry)
	if len(list) > max {
		list = list[len(list)-max:]
	}
	s.groups[groupID] = list
	if s.dir != "" && s.cfg().History.Persist {
		s.appendPersist(groupID, entry)
	}
}

// Recall 按关键词召回本群历史（时间倒序，最多 maxItems 条）。
// query 空 = 最近优先；非空 = 命中任一分词 token 的条目（同 keyword 检索的分词语义）。
func (s *HistoryStore) Recall(groupID int64, query string, maxItems int) []HistoryEntry {
	s.mu.Lock()
	list := append([]HistoryEntry(nil), s.groups[groupID]...)
	s.mu.Unlock()
	if len(list) == 0 || maxItems < 1 {
		return nil
	}
	var toks []string
	if strings.TrimSpace(query) != "" {
		toks = tokenize(query)
		if len(toks) == 0 {
			return nil
		}
	}
	// 倒序（最近在前）
	sort.SliceStable(list, func(i, j int) bool { return list[i].Time.After(list[j].Time) })
	out := make([]HistoryEntry, 0, maxItems)
	for _, e := range list {
		if len(out) >= maxItems {
			break
		}
		if len(toks) > 0 {
			hay := strings.ToLower(e.Question + " " + e.Answer)
			hit := false
			for _, tok := range toks {
				if strings.Contains(hay, tok) {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

// FormatEntries 将召回条目格式化为给 AI 的文本（带时间/提问者，总字符受限）。
func FormatEntries(entries []HistoryEntry) string {
	if len(entries) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("【本群历史问答（时间倒序）】\n")
	for i, e := range entries {
		ago := time.Since(e.Time)
		when := "刚刚"
		switch {
		case ago < time.Minute:
			when = "刚刚"
		case ago < time.Hour:
			when = fmt.Sprintf("%d 分钟前", int(ago.Minutes()))
		default:
			when = e.Time.Format("01-02 15:04")
		}
		block := fmt.Sprintf("[%s 用户%d] 问：%s\n答：%s\n",
			when, e.UserID, e.Question, strings.ReplaceAll(e.Answer, "\n", " "))
		if sb.Len()+len(block) > historyRecallChars {
			if i > 0 {
				sb.WriteString("…（历史过多，仅显示最近部分）\n")
			}
			break
		}
		sb.WriteString(block)
		if i < len(entries)-1 {
			sb.WriteString("---\n")
		}
	}
	return sb.String()
}

// ---- 持久化（jsonl） ----

// historyPath 某群的历史文件路径。
func (s *HistoryStore) historyPath(groupID int64) string {
	return filepath.Join(s.dir, fmt.Sprintf("%d.jsonl", groupID))
}

// appendPersist 追加写一行 JSON（调用方已持锁）。
func (s *HistoryStore) appendPersist(groupID int64, e HistoryEntry) {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		slog.Warn("civgo 历史目录创建失败", "err", err)
		return
	}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	f, err := os.OpenFile(s.historyPath(groupID), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		slog.Warn("civgo 历史写入失败", "group", groupID, "err", err)
		return
	}
	defer f.Close()
	f.Write(append(data, '\n'))
}

// loadAll 载入全部历史文件（启动时调用；损坏行跳过）。
func (s *HistoryStore) loadAll() {
	files, err := filepath.Glob(filepath.Join(s.dir, "*.jsonl"))
	if err != nil {
		return
	}
	for _, path := range files {
		base := filepath.Base(path)
		groupID := int64(0)
		fmt.Sscanf(strings.TrimSuffix(base, ".jsonl"), "%d", &groupID)
		if groupID == 0 {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 256*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			var e HistoryEntry
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				continue // 损坏行跳过
			}
			s.groups[groupID] = append(s.groups[groupID], e)
		}
		f.Close()
	}
}

// Count 返回全部群的累计历史条数（管理后台状态展示）。
func (s *HistoryStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, list := range s.groups {
		n += len(list)
	}
	return n
}
