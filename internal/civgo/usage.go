package civgo

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

// usageTopN 预警邮件中 Top 消耗统计条数。
const usageTopN = 5

// UsageMeter token 用量滑动窗口计量 + 预警：窗口内消耗超阈值 → 邮件提醒
// （邮件发送由 main 注入的 mailer.Sender 承担，本模块零 SMTP 实现）。
type UsageMeter struct {
	mu       sync.Mutex
	buckets  map[int64]int64 // unix 分钟 → tokens（in+out 合计）
	groupUse map[int64]int64 // 窗口内按群累计
	userUse  map[int64]int64 // 窗口内按用户累计
	requests int             // 窗口内请求次数
	lastSend time.Time       // 上次预警时间（冷却）
	sending  bool            // 预警邮件发送中（防并发重复触发）
	cfg      func() *Config  // 热重载快照
	send     func(to, subject, body string) error // 注入的邮件发送（nil = 未注入）
	defTo    string          // 默认收件人（main 注入，来自系统邮箱配置）
}

// NewUsageMeter 创建计量器。send 为 nil 时不预警（仅计量）。
func NewUsageMeter(cfg func() *Config, send func(to, subject, body string) error, defTo string) *UsageMeter {
	return &UsageMeter{
		buckets:  map[int64]int64{},
		groupUse: map[int64]int64{},
		userUse:  map[int64]int64{},
		cfg:      cfg,
		send:     send,
		defTo:    defTo,
	}
}

// Add 上报一次问答用量并触发预警检查（问答 goroutine 内调用，不阻塞）。
func (m *UsageMeter) Add(tokens int, groupID, userID int64) {
	if tokens <= 0 {
		return
	}
	now := time.Now()
	m.mu.Lock()
	cfg := m.cfg().UsageAlert
	window := int64(cfg.WindowMinutes) * 60
	cur := now.Unix() / 60
	m.buckets[cur] += int64(tokens)
	m.groupUse[groupID] += int64(tokens)
	m.userUse[userID] += int64(tokens)
	m.requests++
	m.pruneLocked(now, window)
	// 预警判定：启用 + 邮件通道 + 冷却期外 + 窗口超阈值 + 无发送中的邮件
	total := m.windowTotalLocked(now, window)
	triggered := cfg.Enabled && m.send != nil && !m.sending &&
		now.Sub(m.lastSend) >= time.Duration(cfg.CooldownMinutes)*time.Minute &&
		total >= cfg.ThresholdTokens
	if triggered {
		m.sending = true
		to := cfg.EmailTo
		if to == "" {
			to = m.defTo
		}
		subject, body := m.buildAlert(cfg, total, now)
		// 清窗口（从当前分钟重新累计），避免连续触发
		m.buckets = map[int64]int64{cur: m.buckets[cur]}
		m.groupUse = map[int64]int64{}
		m.userUse = map[int64]int64{}
		m.requests = 0
		m.mu.Unlock()
		go m.deliver(to, subject, body)
		return
	}
	m.mu.Unlock()
}

// deliver 异步发送预警邮件；成功才记录冷却时间，失败不记冷却（下次满足即重发）。
func (m *UsageMeter) deliver(to, subject, body string) {
	if to == "" {
		slog.Warn("civgo token 用量预警无收件人（配置 usage_alert.email_to 或系统邮箱收件人），跳过发送")
		m.mu.Lock()
		m.sending = false
		m.mu.Unlock()
		return
	}
	if err := m.send(to, subject, body); err != nil {
		slog.Warn("civgo token 用量预警邮件发送失败（下次满足将重发）", "err", err)
		m.mu.Lock()
		m.sending = false
		m.mu.Unlock()
		return
	}
	slog.Info("civgo token 用量预警邮件已发送", "to", to)
	m.mu.Lock()
	m.lastSend = time.Now()
	m.sending = false
	m.mu.Unlock()
}

// pruneLocked 清理窗口外的过期分钟桶（调用方已持锁）。
func (m *UsageMeter) pruneLocked(now time.Time, windowSec int64) {
	oldest := now.Unix() - windowSec
	for k := range m.buckets {
		if k*60 < oldest {
			delete(m.buckets, k)
		}
	}
}

// windowTotalLocked 窗口内总用量（调用方已持锁）。
func (m *UsageMeter) windowTotalLocked(now time.Time, windowSec int64) int64 {
	var total int64
	oldest := now.Unix() - windowSec
	for k, v := range m.buckets {
		if k*60 >= oldest {
			total += v
		}
	}
	return total
}

// buildAlert 组装预警邮件（含窗口消耗、请求数、Top 群/用户）。
func (m *UsageMeter) buildAlert(cfg UsageAlertConfig, total int64, now time.Time) (string, string) {
	subject := fmt.Sprintf("【civgo】token 用量预警：近 %d 分钟消耗 %d", cfg.WindowMinutes, total)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("【civgo 机器人 token 用量预警】\n\n"))
	sb.WriteString(fmt.Sprintf("近 %d 分钟内消耗约 %d tokens（共 %d 次 AI 请求），已超过阈值 %d，请检查群内是否异常刷问。\n\n",
		cfg.WindowMinutes, total, m.requests, cfg.ThresholdTokens))
	sb.WriteString("消耗 Top 群：\n")
	for _, kv := range topK(m.groupUse, usageTopN) {
		sb.WriteString(fmt.Sprintf("  群 %d：%d tokens\n", kv.k, kv.v))
	}
	sb.WriteString("\n消耗 Top 用户：\n")
	for _, kv := range topK(m.userUse, usageTopN) {
		sb.WriteString(fmt.Sprintf("  QQ %d：%d tokens\n", kv.k, kv.v))
	}
	sb.WriteString("\n建议：可调低 rate_limit 限流或 agent.max_tool_calls；不需要时可在配置中关闭 usage_alert.enabled。\n")
	return subject, sb.String()
}

type kv struct {
	k int64
	v int64
}

// topK 取 map 中 value 最大的前 n 个（按 value 降序）。
func topK(m map[int64]int64, n int) []kv {
	out := make([]kv, 0, len(m))
	for k, v := range m {
		out = append(out, kv{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].v > out[j].v })
	if len(out) > n {
		out = out[:n]
	}
	return out
}
