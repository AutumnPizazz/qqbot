package admin

import (
	"strings"
	"testing"
	"time"
)

func TestMFACreateAndVerify(t *testing.T) {
	s := newMFAStore()
	ticket, code, wait, err := s.create("1.2.3.4", "admin@example.com")
	if err != nil || wait != 0 {
		t.Fatalf("创建失败: %v wait=%v", err, wait)
	}
	if len(ticket) != 32 || len(code) != mfaCodeDigits {
		t.Fatalf("票据/验证码格式错误: %q %q", ticket, code)
	}
	// 正确验证码
	email, ok := s.verify(ticket, code)
	if !ok || email != "admin@example.com" {
		t.Fatalf("验证失败: %v %q", ok, email)
	}
	// 一次性：再次验证失败
	if _, ok := s.verify(ticket, code); ok {
		t.Fatal("票据应一次性")
	}
}

func TestMFAWrongCodeAndAttempts(t *testing.T) {
	s := newMFAStore()
	ticket, code, _, _ := s.create("1.2.3.4", "a@b.c")
	// 5 次错误尝试后票据销毁
	for i := 0; i < mfaMaxAttempts; i++ {
		if _, ok := s.verify(ticket, "000000"); ok {
			t.Fatal("错误验证码不应通过")
		}
	}
	// 票据已销毁，即使验证码正确也失败
	if _, ok := s.verify(ticket, code); ok {
		t.Fatal("达到尝试上限后票据应销毁")
	}
}

func TestMFAExpiry(t *testing.T) {
	s := newMFAStore()
	ticket, code, _, _ := s.create("1.2.3.4", "a@b.c")
	s.mu.Lock()
	s.tickets[ticket].expires = time.Now().Add(-time.Minute)
	s.mu.Unlock()
	if _, ok := s.verify(ticket, code); ok {
		t.Fatal("过期票据不应通过")
	}
}

func TestMFAResendCooldown(t *testing.T) {
	s := newMFAStore()
	if _, _, _, err := s.create("1.2.3.4", "a@b.c"); err != nil {
		t.Fatal(err)
	}
	_, _, wait, err := s.create("1.2.3.4", "a@b.c")
	if err == nil || wait <= 0 {
		t.Fatalf("冷却期内应拒绝: err=%v wait=%v", err, wait)
	}
	// 其他 IP 不受影响
	if _, _, _, err := s.create("5.6.7.8", "a@b.c"); err != nil {
		t.Fatalf("其他 IP 应可创建: %v", err)
	}
}

func TestMaskEmail(t *testing.T) {
	if got := maskEmail("admin@example.com"); got != "a***@example.com" {
		t.Fatalf("脱敏错误: %q", got)
	}
	if !strings.Contains(maskEmail("a@b.c"), "***") {
		t.Fatal("短邮箱也应脱敏")
	}
}
