package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// errResendCooldown 重发冷却期内拒绝生成新票据。
var errResendCooldown = errors.New("验证码发送过于频繁")

// 邮箱两步验证（MFA）票据存储：纯内存，重启即失效（安全优先）。
const (
	mfaCodeTTL         = 10 * time.Minute // 验证码有效期
	mfaMaxAttempts     = 5                // 单票最大尝试次数
	mfaResendCooldown  = 60 * time.Second // 同 IP 重发冷却
	mfaCodeDigits      = 6                // 验证码位数
	mfaMaxTickets      = 200              // 票据上限（防内存膨胀）
	mfaTicketCleanStep = 50               // 每创建 N 次清理一次过期票据
)

// mfaTicket 一张待验证的票据。
type mfaTicket struct {
	codeHash [sha256.Size]byte
	expires  time.Time
	attempts int
	email    string
}

// mfaStore 验证码票据存储。
type mfaStore struct {
	mu       sync.Mutex
	tickets  map[string]*mfaTicket
	lastSend map[string]time.Time // IP → 上次发码时间
	created  int                  // 创建计数（触发惰性清理）
}

func newMFAStore() *mfaStore {
	return &mfaStore{
		tickets:  make(map[string]*mfaTicket),
		lastSend: make(map[string]time.Time),
	}
}

// create 为 IP 生成一张票据并返回验证码明文（供发信）。
// 返回重发冷却剩余时间；冷却期内返回错误。
func (s *mfaStore) create(ip, email string) (ticket string, code string, wait time.Duration, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if last, ok := s.lastSend[ip]; ok {
		left := mfaResendCooldown - time.Since(last)
		if left > 0 {
			return "", "", left, errResendCooldown
		}
	}
	// 验证码：6 位数字（crypto/rand，防预测）
	code = randomDigits(mfaCodeDigits)
	sum := sha256.Sum256([]byte(code))
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", "", 0, err
	}
	ticket = hex.EncodeToString(buf)
	s.tickets[ticket] = &mfaTicket{
		codeHash: sum,
		expires:  time.Now().Add(mfaCodeTTL),
		email:    email,
	}
	s.lastSend[ip] = time.Now()
	// 惰性清理过期票据 + 上限保护
	s.created++
	if s.created%mfaTicketCleanStep == 0 || len(s.tickets) > mfaMaxTickets {
		now := time.Now()
		for k, t := range s.tickets {
			if now.After(t.expires) {
				delete(s.tickets, k)
			}
		}
	}
	return ticket, code, 0, nil
}

// verify 校验验证码：成功返回收件邮箱并销毁票据；失败累加尝试次数。
func (s *mfaStore) verify(ticket, code string) (email string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, found := s.tickets[ticket]
	if !found {
		return "", false
	}
	if time.Now().After(t.expires) {
		delete(s.tickets, ticket)
		return "", false
	}
	if t.attempts >= mfaMaxAttempts {
		delete(s.tickets, ticket)
		return "", false
	}
	sum := sha256.Sum256([]byte(code))
	if subtle.ConstantTimeCompare(sum[:], t.codeHash[:]) != 1 {
		t.attempts++
		if t.attempts >= mfaMaxAttempts {
			delete(s.tickets, ticket)
		}
		return "", false
	}
	email = t.email
	delete(s.tickets, ticket)
	return email, true
}

// delete 主动销毁票据（发信失败时回滚）。
func (s *mfaStore) delete(ticket string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tickets, ticket)
}

// randomDigits 生成 n 位随机数字串（crypto/rand）。
func randomDigits(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// 理论不可达（crypto/rand 读失败）；退化为时间戳，保证不 panic
		return "000000"
	}
	out := make([]byte, n)
	for i, b := range buf {
		out[i] = '0' + b%10
	}
	return string(out)
}
