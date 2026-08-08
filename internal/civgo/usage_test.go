package civgo

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// sentBox 带锁的发送记录（race 安全）。
type sentBox struct {
	mu   sync.Mutex
	msgs []string
}

func (b *sentBox) add(s string) {
	b.mu.Lock()
	b.msgs = append(b.msgs, s)
	b.mu.Unlock()
}

func (b *sentBox) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.msgs)
}

func (b *sentBox) get(i int) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if i < 0 || i >= len(b.msgs) {
		return ""
	}
	return b.msgs[i]
}

// testMeter 构造计量器；send 记录发送内容。
func testMeter(t *testing.T, send func(to, subject, body string) error) (*UsageMeter, *sentBox) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.UsageAlert.WindowMinutes = 5
	cfg.UsageAlert.ThresholdTokens = 1000
	cfg.UsageAlert.CooldownMinutes = 30
	cfg.UsageAlert.EmailTo = "alert@x.com"
	box := &sentBox{}
	if send == nil {
		send = func(to, subject, body string) error {
			box.add(subject)
			return nil
		}
	}
	m := NewUsageMeter(func() *Config { return cfg }, send, "def@x.com")
	return m, box
}

// waitSend 等待发送计数达到 n。
func waitSend(t *testing.T, box *sentBox, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if box.count() >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待发送超时（已有 %d 条）", box.count())
}

func TestUsageMeterTriggerAndCooldown(t *testing.T) {
	m, box := testMeter(t, nil)
	m.Add(600, 111, 1001)
	if box.count() != 0 {
		t.Fatal("未达阈值不应触发")
	}
	m.Add(500, 111, 1001) // 合计 1100 ≥ 1000 → 触发
	waitSend(t, box, 1)
	if !strings.Contains(box.get(0), "token 用量预警") {
		t.Errorf("邮件主题错误: %s", box.get(0))
	}
	// 冷却期内：即使再超阈值也不触发
	m.Add(3000, 111, 1002)
	time.Sleep(50 * time.Millisecond)
	if box.count() != 1 {
		t.Errorf("冷却期内不应重复触发，got %d", box.count())
	}
	// 冷却期过后（拨回 lastSend）再触发
	m.mu.Lock()
	m.lastSend = time.Now().Add(-31 * time.Minute)
	m.mu.Unlock()
	m.Add(2000, 111, 1002)
	waitSend(t, box, 2)
}

func TestUsageMeterWindowReset(t *testing.T) {
	m, box := testMeter(t, nil)
	m.Add(1000, 111, 1001) // 触发
	waitSend(t, box, 1)
	// 触发后窗口已重置：再次 Add 600 不触发（不足 1000）
	m.Add(600, 111, 1001)
	time.Sleep(50 * time.Millisecond)
	if box.count() != 1 {
		t.Errorf("触发后窗口应重置，got %d", box.count())
	}
}

func TestUsageMeterEmailContent(t *testing.T) {
	var bodyMu sync.Mutex
	var gotBody string
	send := func(to, subject, body string) error {
		bodyMu.Lock()
		gotBody = body
		bodyMu.Unlock()
		return nil
	}
	m, _ := testMeter(t, send)
	m.Add(600, 111, 1001)
	m.Add(500, 222, 2002)
	m.Add(100, 111, 1001) // 合计 1200 ≥ 1000 → 触发
	deadline := time.Now().Add(2 * time.Second)
	for {
		bodyMu.Lock()
		b := gotBody
		bodyMu.Unlock()
		if b != "" || !time.Now().Before(deadline) {
			gotBody = b
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if gotBody == "" {
		t.Fatal("未收到邮件")
	}
	// 触发发生在第二次 Add 后（600+500=1100 ≥ 1000），第三次 Add(100) 在重置后的窗口内不触发
	for _, want := range []string{"近 5 分钟", "1100", "2 次", "Top 群", "群 111", "Top 用户", "QQ 1001"} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("邮件应含 %q:\n%s", want, gotBody)
		}
	}
	// 按量降序：群 111（700）在群 222（500）前
	i111 := strings.Index(gotBody, "群 111")
	i222 := strings.Index(gotBody, "群 222")
	if i111 < 0 || i222 < 0 || i111 > i222 {
		t.Errorf("Top 群应按消耗降序:\n%s", gotBody)
	}
}

func TestUsageMeterSendFailRetry(t *testing.T) {
	// 第一次发送失败 → 不记冷却 → 下次满足阈值重发
	var fail atomic.Bool
	fail.Store(true)
	var sent atomic.Int64
	send := func(to, subject, body string) error {
		if fail.Load() {
			return errTestSMTP
		}
		sent.Add(1)
		return nil
	}
	m, _ := testMeter(t, send)
	m.Add(1000, 111, 1001)
	time.Sleep(50 * time.Millisecond)
	if sent.Load() != 0 {
		t.Fatal("失败时不应记发送")
	}
	// 失败后 sending 复位，再次满足阈值 → 重发
	fail.Store(false)
	m.Add(1000, 111, 1001)
	deadline := time.Now().Add(2 * time.Second)
	for sent.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if sent.Load() != 1 {
		t.Errorf("发送失败后应重试，got %d", sent.Load())
	}
}

func TestUsageMeterNoSendInjected(t *testing.T) {
	// 未注入 send：仅计量不预警（不 panic）
	m, _ := testMeter(t, nil)
	m.send = nil
	m.Add(5000, 111, 1001)
	time.Sleep(30 * time.Millisecond)
	// 无异常即可
}

func TestUsageMeterWindowSliding(t *testing.T) {
	cfg := DefaultConfig()
	cfg.UsageAlert.ThresholdTokens = 100
	cfg.UsageAlert.WindowMinutes = 5
	send := func(to, subject, body string) error { return nil }
	m := NewUsageMeter(func() *Config { return cfg }, send, "")
	// 写入 5 分钟前的过期桶
	old := time.Now().Add(-10*time.Minute).Unix() / 60
	m.mu.Lock()
	m.buckets[old] = 9999
	m.mu.Unlock()
	// 当前窗口合计仍为 0（旧桶被 prune/排除）
	total := m.windowTotalLocked(time.Now(), 5*60)
	if total != 0 {
		t.Errorf("过期桶不应计入窗口: %d", total)
	}
}

var errTestSMTP = &testSMTPError{}

type testSMTPError struct{}

func (e *testSMTPError) Error() string { return "smtp 测试失败" }
