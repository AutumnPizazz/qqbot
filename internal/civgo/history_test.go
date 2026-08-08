package civgo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testHistory 构造历史存储（临时目录持久化）。
func testHistory(t *testing.T, cfg *Config) (*HistoryStore, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "history")
	return NewHistoryStore(dir, func() *Config { return cfg }), dir
}

func TestHistoryAppendRecall(t *testing.T) {
	cfg := DefaultConfig()
	cfg.History.MaxEntriesPerGroup = 50
	h, _ := testHistory(t, cfg)
	h.Append(111, 1001, "弓手射程多少？", "弓手射程 2 格。", 100)
	h.Append(111, 1002, "骑士怎么招募？", "需要兵营。", 200)
	h.Append(222, 2001, "别的群的问题", "别的群回答", 50)

	// 按群隔离
	rec := h.Recall(111, "", 5)
	if len(rec) != 2 {
		t.Fatalf("群 111 应有 2 条，got %d", len(rec))
	}
	if len(h.Recall(222, "", 5)) != 1 {
		t.Fatal("群 222 应隔离")
	}
	// 时间倒序：最近（骑士）在前
	if !strings.Contains(rec[0].Question, "骑士") {
		t.Errorf("应时间倒序: %+v", rec)
	}
	// 关键词召回
	rec2 := h.Recall(111, "弓手", 5)
	if len(rec2) != 1 || !strings.Contains(rec2[0].Question, "弓手") {
		t.Errorf("关键词召回错误: %+v", rec2)
	}
	rec3 := h.Recall(111, "不存在词", 5)
	if len(rec3) != 0 {
		t.Errorf("无关关键词不应命中: %+v", rec3)
	}
	// 条数上限
	if len(h.Recall(111, "", 1)) != 1 {
		t.Error("maxItems 应生效")
	}
}

func TestHistoryRingEvict(t *testing.T) {
	cfg := DefaultConfig()
	cfg.History.MaxEntriesPerGroup = 3
	h, _ := testHistory(t, cfg)
	for i := 0; i < 5; i++ {
		h.Append(111, 1001, "问题"+string(rune('0'+i)), "回答", 1)
	}
	rec := h.Recall(111, "", 10)
	if len(rec) != 3 {
		t.Fatalf("环形应保留 3 条，got %d", len(rec))
	}
	if !strings.Contains(rec[0].Question, "问题4") {
		t.Errorf("应保留最新: %+v", rec)
	}
	if strings.Contains(rec[0].Question, "问题0") {
		t.Errorf("最旧应被淘汰: %+v", rec)
	}
}

func TestHistoryTruncate(t *testing.T) {
	h, _ := testHistory(t, DefaultConfig())
	long := strings.Repeat("长", 500)
	h.Append(111, 1001, long, long, 1)
	rec := h.Recall(111, "", 1)
	if len(rec) != 1 {
		t.Fatal("应有一条")
	}
	if runeLen(rec[0].Question) > historyQuestionMax+1 {
		t.Errorf("问题应截断到 %d: %d", historyQuestionMax, runeLen(rec[0].Question))
	}
	if runeLen(rec[0].Answer) > historyAnswerMax+1 {
		t.Errorf("回答应截断到 %d: %d", historyAnswerMax, runeLen(rec[0].Answer))
	}
}

func TestHistoryPersist(t *testing.T) {
	cfg := DefaultConfig()
	h, dir := testHistory(t, cfg)
	h.Append(111, 1001, "弓手射程？", "2 格", 10)
	h.Append(111, 1002, "骑士？", "近战", 20)

	// 新实例载入
	h2 := NewHistoryStore(dir, func() *Config { return cfg })
	rec := h2.Recall(111, "", 5)
	if len(rec) != 2 {
		t.Fatalf("持久化载入应 2 条，got %d", len(rec))
	}
	if !strings.Contains(rec[0].Question, "骑士") {
		t.Errorf("载入顺序应为时间倒序: %+v", rec)
	}
	if rec[0].Tokens != 20 {
		t.Errorf("Tokens 应持久化: %+v", rec[0])
	}
}

func TestHistoryPersistCorruptLine(t *testing.T) {
	cfg := DefaultConfig()
	h, dir := testHistory(t, cfg)
	h.Append(111, 1001, "正常问题", "正常回答", 1)
	// 写入损坏行
	f, err := os.OpenFile(filepath.Join(dir, "111.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("{corrupt json line}\n")
	f.Close()

	h2 := NewHistoryStore(dir, func() *Config { return cfg })
	rec := h2.Recall(111, "", 5)
	if len(rec) != 1 {
		t.Fatalf("损坏行应跳过，保留 1 条，got %d", len(rec))
	}
}

func TestFormatEntries(t *testing.T) {
	now := time.Now()
	entries := []HistoryEntry{
		{Time: now.Add(-3 * time.Minute), UserID: 1001, Question: "那个技能怎么解锁？", Answer: "40 级解锁天赋树。"},
		{Time: now.Add(-2 * time.Hour), UserID: 1002, Question: "旧问题", Answer: "旧回答"},
	}
	out := FormatEntries(entries)
	if !strings.Contains(out, "3 分钟前") || !strings.Contains(out, "用户1001") {
		t.Errorf("格式应含时间与提问者: %s", out)
	}
	if !strings.Contains(out, "解锁天赋树") {
		t.Errorf("应含回答: %s", out)
	}
	if FormatEntries(nil) != "" {
		t.Error("空列表应返回空串")
	}
	// 超长截断
	huge := make([]HistoryEntry, 0, 50)
	for i := 0; i < 50; i++ {
		huge = append(huge, HistoryEntry{Time: now, UserID: 1,
			Question: strings.Repeat("问", 100), Answer: strings.Repeat("答", 300)})
	}
	out2 := FormatEntries(huge)
	if runeLen(out2) > historyRecallChars+200 {
		t.Errorf("输出应受限: %d", runeLen(out2))
	}
}

// TestRecallHistoryTool 经工具执行器验证 recall_history 接入。
func TestRecallHistoryTool(t *testing.T) {
	cfg := DefaultConfig()
	h, _ := testHistory(t, cfg)
	h.Append(111, 1001, "弓手射程多少？", "弓手射程 2 格。", 10)

	te, _ := testToolEnv(t)
	te.SetHistory(h)
	out, err := te.Execute("recall_history", `{"query":"弓手"}`, newContextBudget(cfg.Agent), 111)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "弓手射程多少") {
		t.Errorf("召回输出错误: %s", out)
	}
	// 无历史群
	out2, _ := te.Execute("recall_history", `{}`, newContextBudget(cfg.Agent), 999)
	if !strings.Contains(out2, "没有相关") {
		t.Errorf("无历史应提示: %s", out2)
	}
	// 未绑定 history
	te2, _ := testToolEnv(t)
	_ = te2
	out3, _ := te2.Execute("recall_history", `{}`, newContextBudget(cfg.Agent), 111)
	if !strings.Contains(out3, "未启用") {
		t.Errorf("未绑定应提示未启用: %s", out3)
	}
}
