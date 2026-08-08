package civgo

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// guardWindow guard 拒绝计数的滑动窗口时长。
const guardWindow = 10 * time.Minute

// guardAlertCooldown 越狱提醒邮件冷却（防止刷屏）。
const guardAlertCooldown = 30 * time.Minute

// jailbreakPatterns 越狱/指令注入类模式（任何语境命中即拒绝）。
// 均为高置信指令性表述，正常游戏提问几乎不会出现。
var jailbreakPatterns = []string{
	"忽略之前的", "忽略所有", "忽略系统", "无视之前的", "无视所有", "无视系统",
	"系统提示词", "系统指令", "system prompt", "systemprompt", "system_prompt",
	"越狱", "jailbreak", "解除限制", "不受限制", "无限制模式", "开发者模式",
	"dan模式", "上帝模式", "忘记你的规则", "忘记你的设定", "重新定义你的角色",
	"把上一轮", // 诱导复盘系统消息的常见开头
}

// harmfulCombos 明确有害主题组合（动词+对象/完整短语）。
// 选词刻意避开游戏文档常用词（战争/武器/攻击/建筑等），只保留现实危害高置信表述。
var harmfulCombos = []string{
	// 爆炸物/武器
	"制作炸弹", "制造炸弹", "自制炸弹", "炸弹配方", "炸弹制作", "制作炸药",
	"购买枪支", "买枪", "枪支制作", "自制枪支", "枪支改造",
	// 毒品
	"制毒", "毒品配方", "买毒品", "购买毒品", "贩毒", "毒品制作", "如何制毒",
	// 诈骗/犯罪
	"电信诈骗", "诈骗教程", "骗钱方法", "如何诈骗", "诈骗话术", "盗刷",
	// 暴力/杀人
	"怎么杀人", "如何杀人", "杀人方法", "杀人教程", "谋杀方法",
	// 自杀/自残（引导）
	"自杀方法", "怎么自杀", "如何自杀", "自残方法",
	// 网络攻击
	"入侵网站", "攻击网站", "盗号教程", "黑客教程", "制作木马", "木马教程", "木马程序", "写木马", "编写病毒", "制作病毒", "病毒程序",
	// 侵害未成年人
	"儿童色情",
}

// Guard 群聊安全防护：输入侧检测越狱/有害内容，命中即拒绝；
// 滑动窗口内拒绝次数超阈值 → 邮件提醒管理员（复用注入的 SMTP 通道）。
type Guard struct {
	cfg   func() *Config
	send  func(to, subject, body string) error // 可 nil（无邮件通道则不提醒）
	defTo string

	mu        sync.Mutex
	rejects   []time.Time // 窗口内拒绝时间戳
	lastAlert time.Time
}

// NewGuard 创建安全防护。send 为 nil 或 alert_email 为空时不提醒（仅拦截）。
func NewGuard(cfg func() *Config, send func(to, subject, body string) error, defTo string) *Guard {
	return &Guard{cfg: cfg, send: send, defTo: defTo}
}

// Check 检测提问文本，返回拒绝回复（空串 = 放行）。
// 小写化匹配（英文关键词大小写不敏感）。
func (g *Guard) Check(q string) string {
	cfg := g.cfg().Guard
	if !cfg.Enabled {
		return ""
	}
	lower := strings.ToLower(q)
	for _, p := range jailbreakPatterns {
		if strings.Contains(lower, p) {
			return g.reject(cfg, "jailbreak:"+p)
		}
	}
	for _, p := range harmfulCombos {
		if strings.Contains(lower, p) {
			return g.reject(cfg, "harmful:"+p)
		}
	}
	return ""
}

// reject 记录拒绝并返回拒绝话术；窗口内超阈值时异步提醒管理员。
func (g *Guard) reject(cfg GuardConfig, reason string) string {
	g.mu.Lock()
	now := time.Now()
	g.rejects = append(g.rejects, now)
	// 裁剪窗口外的记录（保留最近 2 倍窗口，惰性清理）
	cutoff := now.Add(-2 * guardWindow)
	kept := g.rejects[:0]
	for _, t := range g.rejects {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	g.rejects = kept
	// 窗口内计数
	n := 0
	winStart := now.Add(-guardWindow)
	for _, t := range g.rejects {
		if t.After(winStart) {
			n++
		}
	}
	needAlert := g.send != nil && cfg.AlertEmail != "" &&
		cfg.AlertThreshold > 0 && n >= cfg.AlertThreshold &&
		now.Sub(g.lastAlert) >= guardAlertCooldown
	if needAlert {
		g.lastAlert = now
	}
	g.mu.Unlock()

	slog.Warn("civgo 安全拦截", "reason", reason, "window_rejects", n)
	if needAlert {
		go g.alert(cfg.AlertEmail, n, reason)
	}
	return cfg.RejectReply
}

// alert 发送越狱提醒邮件（异步；失败仅日志）。
func (g *Guard) alert(to string, n int, reason string) {
	body := fmt.Sprintf(
		"【civgo 机器人安全提醒】\n\n近 10 分钟内已拦截 %d 次越狱/有害提问。\n最近命中模式：%s\n\n"+
			"请检查群内是否有用户尝试让机器人输出不当内容；必要时可在后台调整 guard 配置或群限流。", n, reason)
	if err := g.send(to, "【civgo】检测到越狱/有害提问", body); err != nil {
		slog.Warn("civgo 安全提醒邮件发送失败", "err", err)
		return
	}
	slog.Info("civgo 安全提醒邮件已发送", "to", to, "window_rejects", n)
}
