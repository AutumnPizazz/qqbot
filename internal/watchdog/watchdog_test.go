package watchdog

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeMail struct {
	mu   sync.Mutex
	sent []string
}

func (f *fakeMail) send(to, subject, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, subject)
	return nil
}

// count 并发安全地返回已发送邮件数。
func (f *fakeMail) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// checkSeq 依次返回预设的 (在线, 错误) 结果。
func checkSeq(results ...bool) func(context.Context) (bool, error) {
	i := 0
	return func(context.Context) (bool, error) {
		if i >= len(results) {
			return results[len(results)-1], nil
		}
		r := results[i]
		i++
		return r, nil
	}
}

func TestMonitorDedupe(t *testing.T) {
	mail := &fakeMail{}
	m := newMonitor(testLogger())

	// 连续 3 次掉线：只发 1 封
	m.tick(context.Background(), "a@b.c", checkSeq(false), mail.send)
	m.tick(context.Background(), "a@b.c", checkSeq(false), mail.send)
	m.tick(context.Background(), "a@b.c", checkSeq(false), mail.send)
	if len(mail.sent) != 1 {
		t.Fatalf("连续掉线应只发 1 封，实际 %d 封", len(mail.sent))
	}
	if !strings.Contains(mail.sent[0], "掉线") {
		t.Fatalf("邮件主题异常: %s", mail.sent[0])
	}
}

func TestMonitorRecoverResets(t *testing.T) {
	mail := &fakeMail{}
	m := newMonitor(testLogger())

	m.tick(context.Background(), "a@b.c", checkSeq(false), mail.send) // 掉线 → 发 1 封
	m.tick(context.Background(), "a@b.c", checkSeq(true), mail.send)  // 恢复
	m.tick(context.Background(), "a@b.c", checkSeq(false), mail.send) // 再次掉线 → 再发 1 封
	if len(mail.sent) != 2 {
		t.Fatalf("恢复后再次掉线应再发 1 封，实际 %d 封", len(mail.sent))
	}
}

func TestMonitorOnlineSilent(t *testing.T) {
	mail := &fakeMail{}
	m := newMonitor(testLogger())
	m.tick(context.Background(), "a@b.c", checkSeq(true), mail.send)
	m.tick(context.Background(), "a@b.c", checkSeq(true), mail.send)
	if len(mail.sent) != 0 {
		t.Fatalf("在线状态不应发信，实际 %d 封", len(mail.sent))
	}
}

func TestMonitorCheckErrorCountsAsDown(t *testing.T) {
	mail := &fakeMail{}
	m := newMonitor(testLogger())
	// check 返回错误 → 视为掉线并提醒
	errCheck := func(context.Context) (bool, error) {
		return false, context.DeadlineExceeded
	}
	m.tick(context.Background(), "a@b.c", errCheck, mail.send)
	if len(mail.sent) != 1 {
		t.Fatalf("check 错误应视为掉线并发信，实际 %d 封", len(mail.sent))
	}
}

func TestWatchEmptyTo(t *testing.T) {
	// 空收件邮箱：不 panic，直接返回
	done := make(chan struct{})
	go func() {
		Watch(context.Background(), checkSeq(true), (&fakeMail{}).send, Options{To: ""})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("空收件邮箱时 Watch 应立即返回")
	}
}

// 宽限期内确认在线：不发提醒、不进入掉线状态，且提前结束宽限期。
func TestWaitGraceOnlineNoNotify(t *testing.T) {
	mail := &fakeMail{}
	m := newMonitor(testLogger())

	start := time.Now()
	waitGrace(context.Background(), testLogger(), m, "a@b.c",
		checkSeq(false, true), mail.send, 5*time.Second, 5*time.Millisecond)
	if len(mail.sent) != 0 {
		t.Fatalf("宽限期内确认在线不应发信，实际 %d 封", len(mail.sent))
	}
	if m.down {
		t.Fatal("宽限期内确认在线后不应处于掉线状态")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("确认在线应提前结束宽限期（不应等满宽限期）")
	}
}

// 宽限期耗尽仍未在线：视为真掉线，发 1 封提醒并进入掉线状态。
func TestWaitGraceTimeoutSendsOnce(t *testing.T) {
	mail := &fakeMail{}
	m := newMonitor(testLogger())

	waitGrace(context.Background(), testLogger(), m, "a@b.c",
		checkSeq(false), mail.send, 60*time.Millisecond, 5*time.Millisecond)
	if len(mail.sent) != 1 {
		t.Fatalf("宽限期超时未在线应发 1 封，实际 %d 封", len(mail.sent))
	}
	if !m.down || !m.notified {
		t.Fatalf("宽限期超时后应进入掉线已提醒状态: down=%v notified=%v", m.down, m.notified)
	}
}

// ctx 取消：宽限期立即退出，不发提醒。
func TestWaitGraceCancelNoNotify(t *testing.T) {
	mail := &fakeMail{}
	m := newMonitor(testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 模拟进程退出
	waitGrace(ctx, testLogger(), m, "a@b.c",
		checkSeq(false), mail.send, 5*time.Second, 5*time.Millisecond)
	if len(mail.sent) != 0 {
		t.Fatalf("ctx 取消不应发信，实际 %d 封", len(mail.sent))
	}
}

// 集成：进程重启后 NapCat 短暂未就绪（宽限期内不在线）→ 宽限期超时发 1 封；
// 恢复在线后再次掉线 → 再发 1 封（去重状态机与宽限期衔接正常）。
func TestWatchStartupGraceTimeoutThenRecover(t *testing.T) {
	mail := &fakeMail{}
	// 序列：宽限期内持续不在线 → 超时（发 1 封）→ 恢复 → 再次掉线（再发 1 封）
	seq := []bool{false, false, false, false, false, true, false, false}
	i := 0
	check := func(context.Context) (bool, error) {
		r := seq[i]
		if i < len(seq)-1 {
			i++
		}
		return r, nil
	}

	done := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		Watch(ctx, check, mail.send, Options{
			To:           "a@b.c",
			Interval:     30 * time.Millisecond,
			StartupGrace: 60 * time.Millisecond,
			Logger:       testLogger(),
		})
		close(done)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for mail.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if mail.count() != 2 {
		t.Fatalf("宽限期超时 1 封 + 恢复后再次掉线 1 封，应共 2 封，实际 %d 封", mail.count())
	}
	// 结束监控，等待 goroutine 退出
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Watch 未在 ctx 取消后退出")
	}
}
