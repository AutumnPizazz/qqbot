package watchdog

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

type fakeMail struct {
	sent []string
}

func (f *fakeMail) send(to, subject, body string) error {
	f.sent = append(f.sent, subject)
	return nil
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
