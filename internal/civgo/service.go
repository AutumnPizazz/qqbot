package civgo

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"qqbot/internal/onebot"
)

// Sender 消息发送抽象（onebot.Manager 天然实现；测试注入 fake）。
type Sender interface {
	SendGroupMsgAt(groupID, userID int64, text string) error
}

// Registerer 事件注册抽象（onebot.Manager 天然实现；测试注入 fake）。
type Registerer interface {
	On(eventType string, h onebot.Handler)
}

// Manager 聚合接口：civgo 模块对 OneBot 的全部依赖。
type Manager interface {
	Sender
	Registerer
	// SendGroupMsg 纯文本群消息（分条回复时除首条外避免重复 @）
	SendGroupMsg(groupID int64, text string) error
}

// 编译期断言：onebot.Manager 满足 civgo 依赖。
var _ Manager = (*onebot.Manager)(nil)

// Options Service 构造参数。
type Options struct {
	DataDir string
	Manager Manager
	// SendMail 邮件发送（main 注入 mailer.Sender 包装；nil = 未注入，用量预警不启用）。
	SendMail func(to, subject, body string) error
}

// ChatCompleter 对话接口（ChatClient 实现；测试注入 fake）。
type ChatCompleter interface {
	Complete(ctx context.Context, instructions string, input []InputItem, tools []Tool) (Completion, error)
}

// Service civgo 社区服务聚合：独立 message handler（与规则引擎并行）、
// 文档同步、文档地图（AI 自主检索）、AI 问答（function calling）、限流。
type Service struct {
	store   *Store
	index   *Indexer // 已废弃：向量索引（过渡期保留，cg0.1.7 移除）
	docmap  *DocmapStore
	agent   *Agent // AI 自主检索代理（工具循环）
	history *HistoryStore // 群问答历史（AI 决策召回）
	meter   *UsageMeter // token 用量计量与预警
	mgr     Manager
	rl      *rateLimiter
	sem     chan struct{} // AI 并发信号量
	dataDir string
	docsDir string // 文档目录（工具执行器用）
}

// New 创建 Service。配置缺失/无效、缺 git、缺 api_key → ErrNotConfigured
// （调用方静默跳过，不影响主流程）。嵌入自检失败不阻断：降级 keyword 检索。
func New(opts Options) (*Service, error) {
	store, err := NewStore(filepath.Join(opts.DataDir, "civgo", "civgo.json"))
	if err != nil {
		return nil, err
	}
	if _, err := exec.LookPath("git"); err != nil {
		slog.Warn("civgo 未找到 git 可执行文件，模块禁用")
		return nil, ErrNotConfigured
	}
	cfg := store.Get()
	if !cfg.Enabled {
		return nil, ErrNotConfigured
	}

	index := NewIndexer(NewEmbedClient(cfg.AI), filepath.Join(opts.DataDir, "civgo", "index.json"), cfg.Retrieval)
	index.Load()
	docmap := NewDocmapStore(filepath.Join(opts.DataDir, "civgo", "docmap.json"))
	docsDir := filepath.Join(opts.DataDir, "civgo", "repo", cfg.Repo.DocsPath)

	chat := NewChatClient(cfg.AI)
	// function calling 自检：网关明确不支持（4xx）→ 模块禁用并明确报告；
	// 网络抖动/5xx → 告警后继续启动（agent 循环中工具失败自然降级直答）。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	ok, cerr := chat.SelfCheckTools(ctx)
	cancel()
	if cerr != nil {
		if ok {
			slog.Warn("civgo function calling 自检暂不确定（继续启动）", "err", cerr)
		} else {
			return nil, fmt.Errorf("civgo 网关不支持 function calling，问答模块禁用（可更换支持 tools 的网关）: %w", cerr)
		}
	}

	history := NewHistoryStore(filepath.Join(opts.DataDir, "civgo", "history"),
		func() *Config { return store.Get() })
	te := NewToolExecutor(docmap, docsDir, func() *Config { return store.Get() })
	te.SetHistory(history)
	agent := NewAgent(chat, te, func() *Config { return store.Get() })
	meter := NewUsageMeter(func() *Config { return store.Get() }, opts.SendMail, "")
	return &Service{
		store:   store,
		index:   index,
		docmap:  docmap,
		agent:   agent,
		history: history,
		meter:   meter,
		mgr:     opts.Manager,
		rl:      newRateLimiter(),
		sem:     make(chan struct{}, cfg.RateLimit.MaxConcurrentAI),
		dataDir: opts.DataDir,
		docsDir: docsDir,
	}, nil
}

// Start 注册消息 handler 并启动同步/自检恢复 goroutine。
func (s *Service) Start(ctx context.Context) {
	syncer := NewSyncer(s.store, s.index, s.docmap, s.dataDir)
	go syncer.Run(ctx)
	s.mgr.On("message", s.OnMessage)
	slog.Info("civgo 社区服务已启动",
		"repo", s.store.Get().Repo.URL,
		"groups", s.store.Get().Groups)
}

// ---- 事件处理 ----

// atRe 匹配 CQ at 码。
var atRe = regexp.MustCompile(`\[CQ:at,qq=(\d+|all)\]`)

// cqCodeRe 匹配任意 CQ 码段（问题文本提取用）。
var cqCodeRe = regexp.MustCompile(`\[CQ:[^\]]*\]`)

// OnMessage 群消息入口：仅处理配置群中 @ 机器人的消息。
func (s *Service) OnMessage(raw json.RawMessage) error {
	var m onebot.GroupMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("解析消息事件失败: %w", err)
	}
	if m.MessageType != "group" || m.UserID == m.SelfID {
		return nil
	}
	cfg := s.store.Get()
	if !cfg.Enabled {
		return nil
	}
	if !containsGroup(cfg.Groups, m.GroupID) {
		return nil
	}

	// @ 检测：@机器人 或 @all
	mentioned := false
	for _, mm := range atRe.FindAllStringSubmatch(m.RawMessage, -1) {
		if mm[1] == "all" {
			mentioned = true
			break
		}
		if qq, err := strconv.ParseInt(mm[1], 10, 64); err == nil && qq == m.SelfID {
			mentioned = true
			break
		}
	}
	if !mentioned {
		return nil
	}

	// 问题文本：剔除 CQ 码 → 清理前导 @残留/标点
	q := cqCodeRe.ReplaceAllString(m.RawMessage, "")
	q = trimLeadingNonCJK(strings.TrimSpace(q))
	if runeLen(q) < 2 {
		return nil // 纯 @ 无正文，不回答
	}
	if runeLen(q) > DefaultMaxQuestionLen {
		q = truncateRunes(q, DefaultMaxQuestionLen)
	}

	// 限流（用户 + 群 双桶）
	if !s.rl.Allow(m.GroupID, m.UserID, cfg.RateLimit.PerUserMin, cfg.RateLimit.PerGroupMin) {
		s.reply(m, "⏳ 提问太频繁啦，稍等一会儿再试吧")
		return nil
	}

	// 并发信号量（非阻塞，满则友好拒绝）
	select {
	case s.sem <- struct{}{}:
	default:
		s.reply(m, "🤖 当前请求较多，请稍后再试")
		return nil
	}

	go func() {
		defer func() { <-s.sem }()
		s.handleQuestion(m, q)
	}()
	return nil
}

// handleQuestion agent 问答编排：AI 自主检索（工具循环）→ 回复 → 记录历史（goroutine 内执行）。
func (s *Service) handleQuestion(m onebot.GroupMessage, q string) {
	start := time.Now()
	ctx := context.Background()

	answer, usage, err := s.agent.Run(ctx, q, m.GroupID)
	if err != nil {
		slog.Warn("civgo AI 调用失败", "err", err, "ms", time.Since(start).Milliseconds())
		s.reply(m, "🤖 AI 服务暂时不可用（已记录），请稍后再试")
		return
	}
	// 问答完成后记录历史（AI 决策召回的数据源）；回答截断由 HistoryStore 负责
	s.history.Append(m.GroupID, m.UserID, q, answer, usage.InputTokens+usage.OutputTokens)
	// token 用量计量与预警（窗口超阈值触发邮件）
	s.meter.Add(usage.InputTokens+usage.OutputTokens, m.GroupID, m.UserID)
	s.sendAnswer(m, answer)
	slog.Info("civgo 问答完成", "group", m.GroupID, "user", m.UserID,
		"q", truncateRunes(q, 50),
		"in_tok", usage.InputTokens, "out_tok", usage.OutputTokens,
		"ms", time.Since(start).Milliseconds())
}

// sendAnswer 组装回复并分条发送（单条 ≤ DefaultMaxReplyLen，最多 5 条）。
// SendGroupMsgAt 内部自动加 [CQ:at,qq=<uid>] 前缀（仅首条 @，后续条纯文本）。
func (s *Service) sendAnswer(m onebot.GroupMessage, answer string) {
	parts := splitReply(answer, "")
	for i, p := range parts {
		var err error
		if i == 0 {
			err = s.mgr.SendGroupMsgAt(m.GroupID, m.UserID, p)
		} else {
			err = s.mgr.SendGroupMsg(m.GroupID, p)
		}
		if err != nil {
			slog.Warn("civgo 回复发送失败", "group", m.GroupID, "err", err)
		}
	}
}

// splitReply 把长回答切成 ≤ DefaultMaxReplyLen 的片段（优先按段落，最多 5 条）。
func splitReply(first, tail string) []string {
	maxLen := DefaultMaxReplyLen
	if runeLen(first)+runeLen(tail) <= maxLen {
		return []string{first}
	}
	var parts []string
	cur := first
	for runeLen(cur) > maxLen {
		r := []rune(cur)
		cut := -1
		for i := maxLen; i > maxLen/2; i-- {
			if r[i] == '\n' {
				cut = i
				break
			}
		}
		if cut < 0 {
			cut = maxLen
		}
		parts = append(parts, string(r[:cut]))
		cur = strings.TrimLeft(string(r[cut:]), "\n")
	}
	if cur != "" {
		parts = append(parts, cur)
	}
	if len(parts) > 5 {
		parts = parts[:5]
		parts[4] = truncateRunes(parts[4], maxLen-10) + "…（内容过长，其余已省略）"
	}
	return parts
}

// reply 快捷回复（不占限流额度）。SendGroupMsgAt 内部自动 @ 提问者。
func (s *Service) reply(m onebot.GroupMessage, text string) {
	if err := s.mgr.SendGroupMsgAt(m.GroupID, m.UserID, text); err != nil {
		slog.Warn("civgo 回复发送失败", "group", m.GroupID, "err", err)
	}
}

// ---- 限流（令牌桶） ----

type bucket struct {
	tokens float64
	last   time.Time
}

type rateLimiter struct {
	mu     sync.Mutex
	users  map[int64]*bucket
	groups map[int64]*bucket
	lastGC time.Time
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{
		users:  map[int64]*bucket{},
		groups: map[int64]*bucket{},
		lastGC: time.Now(),
	}
}

// Allow 用户 + 群双桶放行（每桶速率 = perMin/60 每秒，容量 = perMin）。
func (rl *rateLimiter) Allow(groupID, userID int64, perUserMin, perGroupMin int) bool {
	now := time.Now()
	rl.mu.Lock()
	defer rl.mu.Unlock()
	okUser := takeToken(rl.users, userID, perUserMin, now)
	okGroup := takeToken(rl.groups, groupID, perGroupMin, now)
	// 惰性清理 10 分钟无活动桶
	if now.Sub(rl.lastGC) > time.Minute {
		rl.lastGC = now
		prune(rl.users, now)
		prune(rl.groups, now)
	}
	return okUser && okGroup
}

func takeToken(m map[int64]*bucket, key int64, perMin int, now time.Time) bool {
	b := m[key]
	if b == nil {
		b = &bucket{tokens: float64(perMin), last: now}
		m[key] = b
	}
	rate := float64(perMin) / 60.0
	b.tokens = minFloat(float64(perMin), b.tokens+now.Sub(b.last).Seconds()*rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func prune(m map[int64]*bucket, now time.Time) {
	for k, b := range m {
		if now.Sub(b.last) > 10*time.Minute {
			delete(m, k)
		}
	}
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// ---- 工具 ----

func containsGroup(groups []int64, gid int64) bool {
	for _, g := range groups {
		if g == gid {
			return true
		}
	}
	return false
}
