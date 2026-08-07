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
}

// 编译期断言：onebot.Manager 满足 civgo 依赖。
var _ Manager = (*onebot.Manager)(nil)

// Options Service 构造参数。
type Options struct {
	DataDir string
	Manager Manager
}

// ChatCompleter 对话接口（ChatClient 实现；测试注入 fake）。
type ChatCompleter interface {
	Complete(ctx context.Context, instructions, systemDoc, userText string) (string, Usage, error)
}

// Service civgo 社区服务聚合：独立 message handler（与规则引擎并行）、
// 文档同步、向量检索、AI 问答、限流。
type Service struct {
	store   *Store
	index   *Indexer
	chat    ChatCompleter
	embed   *EmbedClient
	mgr     Manager
	rl      *rateLimiter
	sem     chan struct{} // AI 并发信号量
	dataDir string
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

	embed := NewEmbedClient(cfg.AI)
	index := NewIndexer(embed, filepath.Join(opts.DataDir, "civgo", "index.json"), cfg.Retrieval)
	index.Load()

	// 嵌入端点自检：失败降级 keyword（不阻断启动）
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := embed.SelfCheck(ctx); err != nil {
		slog.Warn("civgo 嵌入自检失败，降级 keyword 检索", "err", err)
		index.SetMode("keyword")
	}
	cancel()

	return &Service{
		store:   store,
		index:   index,
		chat:    NewChatClient(cfg.AI),
		embed:   embed,
		mgr:     opts.Manager,
		rl:      newRateLimiter(),
		sem:     make(chan struct{}, cfg.RateLimit.MaxConcurrentAI),
		dataDir: opts.DataDir,
	}, nil
}

// Start 注册消息 handler 并启动同步/自检恢复 goroutine。
func (s *Service) Start(ctx context.Context) {
	syncer := NewSyncer(s.store, s.index, s.dataDir)
	go syncer.Run(ctx)
	go s.embedRecoverLoop(ctx)
	s.mgr.On("message", s.OnMessage)
	slog.Info("civgo 社区服务已启动",
		"repo", s.store.Get().Repo.URL,
		"mode", s.index.Mode(),
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

// handleQuestion 检索 → 拼上下文 → 调 AI → 回复（goroutine 内执行）。
func (s *Service) handleQuestion(m onebot.GroupMessage, q string) {
	start := time.Now()
	cfg := s.store.Get()
	ctx := context.Background()

	hits, err := s.index.Search(ctx, q)
	if err != nil {
		// 嵌入失败：尝试 keyword 兜底；仍失败则友好提示
		if s.index.Mode() == "vector" {
			slog.Warn("civgo 向量检索失败，尝试 keyword 兜底", "err", err)
			hits = s.index.KeywordSearch(q, cfg.Retrieval.TopK)
			if len(hits) > 0 {
				s.index.SetMode("keyword")
			}
		}
		if len(hits) == 0 {
			s.reply(m, "🤖 知识检索服务暂时不可用，请稍后再试")
			return
		}
	}
	if len(hits) == 0 {
		s.reply(m, "📚 知识库中暂未找到与「"+truncateRunes(q, 30)+"」相关的内容。换个说法试试？也可以等游戏文档更新后再问～")
		return
	}

	docContext := buildDocContext(hits, cfg.Retrieval.MaxContextChars)
	answer, usage, err := s.chat.Complete(ctx, systemPrompt, docContext, q)
	if err != nil {
		slog.Warn("civgo AI 调用失败", "err", err, "ms", time.Since(start).Milliseconds())
		s.reply(m, "🤖 AI 服务暂时不可用（已记录），请稍后再试")
		return
	}

	s.sendAnswer(m, answer, hits)
	slog.Info("civgo 问答完成", "group", m.GroupID, "user", m.UserID,
		"q", truncateRunes(q, 50), "hits", len(hits),
		"in_tok", usage.InputTokens, "out_tok", usage.OutputTokens,
		"ms", time.Since(start).Milliseconds())
}

// buildDocContext 把检索命中块拼成知识源文本（按分数降序，累计不超过上限）。
func buildDocContext(hits []Hit, maxChars int) string {
	var sb strings.Builder
	total := 0
	for _, h := range hits {
		head := ""
		if h.Chunk.Heading != "" {
			head = " §" + h.Chunk.Heading
		}
		block := fmt.Sprintf("【来源: %s%s】\n%s\n\n", h.Chunk.File, head, h.Chunk.Text)
		if total+runeLen(block) > maxChars {
			break
		}
		sb.WriteString(block)
		total += runeLen(block)
	}
	if sb.Len() == 0 && len(hits) > 0 {
		// 单块就超限：取第一块截断
		sb.WriteString(truncateRunes(hits[0].Chunk.Text, maxChars))
	}
	return sb.String()
}

// sendAnswer 组装回复并分条发送（单条 ≤ DefaultMaxReplyLen，最多 5 条）。
func (s *Service) sendAnswer(m onebot.GroupMessage, answer string, hits []Hit) {
	sources := make([]string, 0, 5)
	seen := map[string]bool{}
	for _, h := range hits {
		if len(sources) >= 5 {
			break
		}
		if !seen[h.Chunk.File] {
			seen[h.Chunk.File] = true
			sources = append(sources, h.Chunk.File)
		}
	}
	tail := ""
	if len(sources) > 0 {
		tail = "\n\n📄 " + strings.Join(sources, "、")
	}
	first := "[CQ:at,qq=" + strconv.FormatInt(m.UserID, 10) + "] " + answer
	parts := splitReply(first, tail)
	for i, p := range parts {
		if i < len(parts)-1 || tail == "" {
			if err := s.mgr.SendGroupMsgAt(m.GroupID, m.UserID, p); err != nil {
				slog.Warn("civgo 回复发送失败", "group", m.GroupID, "err", err)
			}
			continue
		}
		// 最后一条附来源尾注
		final := p + tail
		if runeLen(final) > DefaultMaxReplyLen {
			final = truncateRunes(p, DefaultMaxReplyLen-runeLen(tail)) + tail
		}
		if err := s.mgr.SendGroupMsgAt(m.GroupID, m.UserID, final); err != nil {
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

// reply 快捷回复（不占限流额度）。
func (s *Service) reply(m onebot.GroupMessage, text string) {
	if err := s.mgr.SendGroupMsgAt(m.GroupID, m.UserID, "[CQ:at,qq="+strconv.FormatInt(m.UserID, 10)+"] "+text); err != nil {
		slog.Warn("civgo 回复发送失败", "group", m.GroupID, "err", err)
	}
}

// embedRecoverLoop keyword 模式下周期性自检，成功回切 vector。
func (s *Service) embedRecoverLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.index.Mode() != "keyword" {
				continue
			}
			cfg := s.store.Get()
			if cfg.Retrieval.Mode != "vector" {
				continue // 用户配置就是 keyword，不折腾
			}
			cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := s.embed.SelfCheck(cctx)
			cancel()
			if err == nil {
				s.index.SetMode("vector")
				slog.Info("civgo 嵌入自检恢复，回切 vector 检索")
			}
		}
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
