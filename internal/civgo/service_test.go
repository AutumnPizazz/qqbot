package civgo

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"qqbot/internal/onebot"
	"sync"
	"testing"
	"time"
)

// ---- fake Manager ----

// fakeManager 模拟 onebot.Manager：SendGroupMsgAt 内部自动加 [CQ:at,qq=xxx] 前缀
// （与真实实现一致，用于验证 civgo 不重复拼接 @）。
type fakeManager struct {
	mu      sync.Mutex
	sent    []string // 发送的文本（按序，含自动 @ 前缀）
	handler onebot.Handler
}

func (f *fakeManager) SendGroupMsgAt(groupID, userID int64, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, fmt.Sprintf("[CQ:at,qq=%d] %s", userID, text))
	return nil
}

func (f *fakeManager) SendGroupMsg(groupID int64, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, text)
	return nil
}

func (f *fakeManager) On(eventType string, h onebot.Handler) {
	if eventType == "message" {
		f.handler = h
	}
}

func (f *fakeManager) lastSent() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return ""
	}
	return f.sent[len(f.sent)-1]
}

func (f *fakeManager) allSent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

func (f *fakeManager) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

// msg 构造一条群消息 JSON（@机器人）。
func msg(groupID, userID, selfID int64, text string) json.RawMessage {
	if text != "" && !strings.Contains(text, "[CQ:") {
		text = "[CQ:at,qq=" + fmt.Sprint(selfID) + "] " + text
	}
	b, _ := json.Marshal(map[string]any{
		"time":         time.Now().Unix(),
		"self_id":      selfID,
		"message_id":   1,
		"message_type": "group",
		"group_id":     groupID,
		"user_id":      userID,
		"raw_message":  text,
		"sender":       map[string]any{"user_id": userID, "nickname": "测试"},
	})
	return b
}

// testService 构造 Service（fake chat + docmap + 有内容的文档目录）。
func testService(t *testing.T) (*Service, *fakeManager, *Store) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.AI.APIKey = "sk-test"
	cfg.Groups = []int64{111}
	store := &Store{}
	store.cfgPtr.Store(cfg)

	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, "docs")
	writeDoc(t, dir, "units/archer.md", "# 弓手\n弓手是远程单位，射程 2 格，攻击力 5。\n")
	writeDoc(t, dir, "units/knight.md", "# 骑士\n骑士是近战单位，移动力高。\n")

	dm := NewDocmapStore(filepath.Join(dataDir, "civgo", "docmap.json"))
	if _, err := BuildDocmap(dir, filepath.Join(dataDir, "civgo", "docmap.json"), nil); err != nil {
		t.Fatalf("建 docmap 失败: %v", err)
	}

	mgr := &fakeManager{}
	agent := NewAgent(&fixedChat{answer: "默认回答"},
		NewToolExecutor(dm, dir, func() *Config { return store.Get() }),
		func() *Config { return store.Get() })
	s := &Service{
		store:   store,
		docmap:  dm,
		agent:   agent,
		mgr:     mgr,
		rl:      newRateLimiter(),
		sem:     make(chan struct{}, cfg.RateLimit.MaxConcurrentAI),
		dataDir: dataDir,
		docsDir: dir,
	}
	return s, mgr, store
}

// fixedChat 固定回答的 chat 替身（实现 ChatCompleter 接口）。
type fixedChat struct {
	answer string
	err    error
}

func (f *fixedChat) Complete(ctx context.Context, instructions string, input []InputItem, tools []Tool) (Completion, error) {
	if f.err != nil {
		return Completion{}, f.err
	}
	return Completion{Text: f.answer, Usage: Usage{InputTokens: 1, OutputTokens: 1}}, nil
}

// 等待异步问答完成（OnMessage 起 goroutine）。
func waitReply(t *testing.T, mgr *fakeManager, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if mgr.count() >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待回复超时（已有 %d 条）", mgr.count())
}

func TestOnMessageBasicFlow(t *testing.T) {
	s, mgr, _ := testService(t)
	// 覆盖 chat：直接返回固定回答
	s.agent.chat = &fixedChat{answer: "弓手射程 2 格"}

	raw := msg(111, 1001, 999, "弓手射程多少？")
	if err := s.OnMessage(raw); err != nil {
		t.Fatalf("OnMessage 失败: %v", err)
	}
	waitReply(t, mgr, 1)
	reply := mgr.lastSent()
	if !strings.Contains(reply, "弓手射程 2 格") {
		t.Errorf("回答缺失: %s", reply)
	}
	// @ 前缀由 SendGroupMsgAt 内部生成且只出现一次（civgo 不重复拼接）
	if n := strings.Count(reply, "[CQ:at,qq=1001]"); n != 1 {
		t.Errorf("@ 应恰好 1 次（SendGroupMsgAt 自动生成），got %d: %s", n, reply)
	}
}

func TestOnMessageIgnoreCases(t *testing.T) {
	s, mgr, store := testService(t)
	// 未配置群
	if err := s.OnMessage(msg(222, 1001, 999, "弓手？")); err != nil {
		t.Fatal(err)
	}
	// 未 @ 机器人（含图片 CQ 码但不含 at）
	if err := s.OnMessage(msg(111, 1001, 999, "[CQ:image,file=x] 弓手射程多少？")); err != nil {
		t.Fatal(err)
	}
	// 私聊
	if err := s.OnMessage(json.RawMessage(`{"message_type":"private","user_id":1001,"self_id":999}`)); err != nil {
		t.Fatal(err)
	}
	// 机器人自己的消息
	if err := s.OnMessage(msg(111, 999, 999, "弓手？")); err != nil {
		t.Fatal(err)
	}
	// 纯 @ 无正文
	if err := s.OnMessage(msg(111, 1001, 999, "[CQ:at,qq=999]")); err != nil {
		t.Fatal(err)
	}
	// 模块禁用
	cfg := store.Get()
	cfg.Enabled = false
	if err := s.OnMessage(msg(111, 1001, 999, "弓手？")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if mgr.count() != 0 {
		t.Errorf("以上场景都不应回复，got %d 条", mgr.count())
	}
}

func TestOnMessageRateLimit(t *testing.T) {
	s, mgr, store := testService(t)
	cfg := store.Get()
	cfg.RateLimit.PerUserMin = 2
	s.sem = make(chan struct{}, 5)
	s.agent.chat = &fixedChat{answer: "回答"}

	// 前 2 次放行，第 3 次限流提示
	for i := 0; i < 2; i++ {
		if err := s.OnMessage(msg(111, 1001, 999, "弓手？")); err != nil {
			t.Fatal(err)
		}
	}
	waitReply(t, mgr, 2)
	if err := s.OnMessage(msg(111, 1001, 999, "弓手？")); err != nil {
		t.Fatal(err)
	}
	waitReply(t, mgr, 3)
	if !strings.Contains(mgr.lastSent(), "太频繁") {
		t.Errorf("第 3 次应提示限流: %s", mgr.lastSent())
	}
}

func TestOnMessageConcurrencyLimit(t *testing.T) {
	s, mgr, store := testService(t)
	cfg := store.Get()
	cfg.RateLimit.MaxConcurrentAI = 1
	s.sem = make(chan struct{}, 1)
	s.sem <- struct{}{} // 模拟并发已满
	if err := s.OnMessage(msg(111, 1001, 999, "弓手？")); err != nil {
		t.Fatal(err)
	}
	waitReply(t, mgr, 1)
	if !strings.Contains(mgr.lastSent(), "请求较多") {
		t.Errorf("并发满应提示: %s", mgr.lastSent())
	}
}

// TestAICallFail 问答阶段 AI 失败 → 友好提示（agent 层无工具结果时直接报错）。
func TestAICallFail(t *testing.T) {
	s, mgr, _ := testService(t)
	s.agent.chat = &fixedChat{err: fmt.Errorf("mock 失败")}
	if err := s.OnMessage(msg(111, 1001, 999, "弓手射程多少？")); err != nil {
		t.Fatal(err)
	}
	waitReply(t, mgr, 1)
	if !strings.Contains(mgr.lastSent(), "不可用") {
		t.Errorf("AI 失败应提示不可用，got: %s", mgr.lastSent())
	}
}

func TestSplitReply(t *testing.T) {
	// 短回答不切
	parts := splitReply("短回答", "📄 a.md")
	if len(parts) != 1 {
		t.Fatalf("短回答应一条，got %d", len(parts))
	}
	// 超长按段落切
	long := strings.Repeat("段落甲甲甲甲甲甲甲甲。\n", 300)
	parts = splitReply(long, "📄 a.md")
	if len(parts) < 2 {
		t.Fatalf("长回答应分条，got %d", len(parts))
	}
	for _, p := range parts {
		if runeLen(p) > DefaultMaxReplyLen {
			t.Errorf("分条超长: %d", runeLen(p))
		}
	}
	// 最多 5 条
	huge := strings.Repeat("超长内容", 5000)
	parts = splitReply(huge, "")
	if len(parts) > 5 {
		t.Errorf("最多 5 条，got %d", len(parts))
	}
	if !strings.Contains(parts[len(parts)-1], "省略") {
		t.Errorf("超限应提示省略: %s", parts[len(parts)-1][:20])
	}
}

func TestRateLimiter(t *testing.T) {
	rl := newRateLimiter()
	// 容量 2：前两次放行，第三次拒绝
	if !rl.Allow(1, 10, 2, 10) || !rl.Allow(1, 10, 2, 10) {
		t.Fatal("前两次应放行")
	}
	if rl.Allow(1, 10, 2, 10) {
		t.Fatal("第三次应拒绝")
	}
	// 不同用户（群有额度）放行
	if !rl.Allow(1, 11, 2, 10) {
		t.Fatal("其他用户应放行")
	}
	// 不同群 + 新用户放行
	if !rl.Allow(2, 12, 2, 10) {
		t.Fatal("其他群应放行")
	}
	// 群额度独立耗尽：perGroup=1 的新群，第一次放行后第二次即使新用户也拒绝
	if !rl.Allow(3, 13, 5, 1) {
		t.Fatal("群 3 首次应放行")
	}
	if rl.Allow(3, 14, 5, 1) {
		t.Fatal("群 3 额度耗尽应拒绝（即使新用户）")
	}
	// 时间恢复：把用户 15 / 群 4 的桶时间拨回 30 秒前 → 应恢复 tokens
	rl.mu.Lock()
	rl.users[15] = &bucket{tokens: 0, last: time.Now().Add(-30 * time.Second)}
	rl.groups[4] = &bucket{tokens: 0, last: time.Now().Add(-30 * time.Second)}
	rl.mu.Unlock()
	if !rl.Allow(4, 15, 2, 2) {
		t.Fatal("令牌应随时间恢复（30s × 2/60 = 1 token）")
	}
}

func TestSendAnswerSplits(t *testing.T) {
	s, mgr, _ := testService(t)
	m := onebotMsg(111, 1001)
	// 长回答分条发送
	long := strings.Repeat("很长很长的回答内容，", 400) // ~4000+ 字符
	s.sendAnswer(m, long)
	if mgr.count() < 2 {
		t.Fatalf("长回答应分多条发送，got %d", mgr.count())
	}
	all := mgr.allSent()
	// 首条 @ 提问者（SendGroupMsgAt 自动生成）
	if !strings.Contains(all[0], "[CQ:at,qq=1001]") {
		t.Errorf("首条应带 @: %s", all[0][:30])
	}
	// 后续条不再重复 @（SendGroupMsg 纯文本）
	for i := 1; i < len(all); i++ {
		if strings.Contains(all[i], "[CQ:at,qq=1001]") {
			t.Errorf("第 %d 条不应重复 @: %s", i+1, all[i][:30])
		}
	}
}

// ---- 小工具 ----

func onebotMsg(groupID, userID int64) onebot.GroupMessage {
	return onebot.GroupMessage{GroupID: groupID, UserID: userID, SelfID: 999, MessageType: "group"}
}

// TestHandleQuestionWithFixedChat 直接测 handleQuestion（注入固定 chat）。
func TestHandleQuestionWithFixedChat(t *testing.T) {
	s, mgr, _ := testService(t)
	s.agent.chat = &fixedChat{answer: "固定回答"}
	m := onebotMsg(111, 1001)
	s.handleQuestion(m, "弓手")
	waitReply(t, mgr, 1)
	if !strings.Contains(mgr.lastSent(), "固定回答") {
		t.Errorf("应回复固定回答: %s", mgr.lastSent())
	}
}

func TestHandleQuestionAIError(t *testing.T) {
	s, mgr, _ := testService(t)
	s.agent.chat = &fixedChat{err: fmt.Errorf("mock 失败")}
	m := onebotMsg(111, 1001)
	s.handleQuestion(m, "弓手")
	waitReply(t, mgr, 1)
	if !strings.Contains(mgr.lastSent(), "不可用") {
		t.Errorf("AI 失败应提示不可用: %s", mgr.lastSent())
	}
}
