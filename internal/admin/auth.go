package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

// 密码哈希参数（Argon2id，参数随哈希保存，可平滑升级）。
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // 64 MiB
	argonThreads = 4
	argonKeyLen  = 32
)

// PasswordHash 是可持久化的 Argon2id 哈希。
type PasswordHash struct {
	Algo    string `json:"algo"` // argon2id
	Time    uint32 `json:"time"`
	Memory  uint32 `json:"memory"`
	Threads uint8  `json:"threads"`
	Salt    []byte `json:"salt"`
	Hash    []byte `json:"hash"`
}

// hashPassword 计算密码哈希（随机盐）。
func hashPassword(pw string) (*PasswordHash, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	hash := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return &PasswordHash{
		Algo: "argon2id", Time: argonTime, Memory: argonMemory,
		Threads: argonThreads, Salt: salt, Hash: hash,
	}, nil
}

// verifyPassword 校验密码（常数时间比较）。
func verifyPassword(pw string, ph *PasswordHash) bool {
	if ph == nil || ph.Algo != "argon2id" {
		return false
	}
	hash := argon2.IDKey([]byte(pw), ph.Salt, ph.Time, ph.Memory, ph.Threads, uint32(len(ph.Hash)))
	return subtle.ConstantTimeCompare(hash, ph.Hash) == 1
}

// authFile 是 data/auth.json 的落盘结构（0600）。
// setup token 只保存哈希；密码只保存 Argon2id 哈希。
type authFile struct {
	SetupTokenHash string        `json:"setup_token_hash,omitempty"`
	SetupCreatedAt time.Time     `json:"setup_created_at,omitempty"`
	SetupConsumed  bool          `json:"setup_consumed,omitempty"`
	Password       *PasswordHash `json:"password_hash,omitempty"`
	PasswordSetAt  time.Time     `json:"password_set_at,omitempty"`
}

// authStore 管理管理员认证状态：setup token 与密码哈希。
type authStore struct {
	mu   sync.Mutex
	path string
	f    authFile
}

// openAuthStore 加载 auth.json；不存在时返回空 store。
func openAuthStore(dataDir string) (*authStore, error) {
	path := filepath.Join(dataDir, "auth.json")
	s := &authStore{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 auth.json 失败: %w", err)
	}
	if err := json.Unmarshal(data, &s.f); err != nil {
		return nil, fmt.Errorf("解析 auth.json 失败（可能已损坏）: %w", err)
	}
	return s, nil
}

func (s *authStore) saveLocked() error {
	data, err := json.MarshalIndent(&s.f, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".auth.json.tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = tmp.Close(); _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}

// SetupRequired 判断是否处于"待初始化"状态（未设置管理员密码）。
func (s *authStore) SetupRequired() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f.Password == nil
}

// EnsureSetupToken 生成一次性 setup token（仅当不存在且未设置密码时）。
// 明文只返回一次，由调用方输出到日志；磁盘只保存哈希。
func (s *authStore) EnsureSetupToken() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f.Password != nil {
		return "", errors.New("管理员密码已设置，setup token 不再可用")
	}
	if s.f.SetupTokenHash != "" && !s.f.SetupConsumed {
		return "", errors.New("setup token 已存在（见启动日志），重启不会生成新 token")
	}
	tok := randomToken(32)
	sum := sha256.Sum256([]byte(tok))
	s.f.SetupTokenHash = hex.EncodeToString(sum[:])
	s.f.SetupCreatedAt = time.Now()
	s.f.SetupConsumed = false
	if err := s.saveLocked(); err != nil {
		return "", err
	}
	return tok, nil
}

// VerifySetupToken 校验 setup token（未消费才有效）。
func (s *authStore) VerifySetupToken(tok string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f.SetupConsumed || s.f.SetupTokenHash == "" || s.f.Password != nil {
		return false
	}
	sum := sha256.Sum256([]byte(tok))
	return subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(s.f.SetupTokenHash)) == 1
}

// ConsumeSetupToken 原子标记 setup token 已消费（幂等）。
func (s *authStore) ConsumeSetupToken() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f.SetupConsumed {
		return nil
	}
	s.f.SetupConsumed = true
	return s.saveLocked()
}

// SetPassword 设置管理员密码（setup 时调用）。
// 密码仅要求非空（长度限制由部署方自行把握；登录限流已提供暴力破解防护）。
func (s *authStore) SetPassword(pw string) error {
	if pw == "" {
		return errors.New("密码不能为空")
	}
	ph, err := hashPassword(pw)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.f.Password = ph
	s.f.PasswordSetAt = time.Now()
	s.f.SetupConsumed = true // setup 完成即消费 token
	return s.saveLocked()
}

// VerifyPassword 校验管理员密码。
func (s *authStore) VerifyPassword(pw string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return verifyPassword(pw, s.f.Password)
}

// ChangePassword 修改密码：要求当前密码正确。
func (s *authStore) ChangePassword(current, next string) (bool, error) {
	if next == "" {
		return false, errors.New("新密码不能为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !verifyPassword(current, s.f.Password) {
		return false, nil
	}
	ph, err := hashPassword(next)
	if err != nil {
		return false, err
	}
	s.f.Password = ph
	s.f.PasswordSetAt = time.Now()
	return true, s.saveLocked()
}

// randomToken 生成 n 字节随机 token 的 hex 表示。
func randomToken(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(err) // 熵源不可用时不可继续
	}
	return hex.EncodeToString(buf)
}

// session 是内存登录会话。
type session struct {
	ID        string
	CSRF      string
	CreatedAt time.Time
	LastSeen  time.Time
	ExpiresAt time.Time
}

// sessionStore 是内存 session 存储（进程重启后全部退出登录）。
type sessionStore struct {
	mu      sync.Mutex
	byID    map[string]*session
	idleTTL time.Duration // 空闲过期
	absTTL  time.Duration // 绝对过期
}

// newSessionStore 创建 session 存储。
func newSessionStore() *sessionStore {
	return &sessionStore{
		byID:    map[string]*session{},
		idleTTL: 4 * time.Hour,
		absTTL:  7 * 24 * time.Hour,
	}
}

// create 创建新会话并返回。
func (s *sessionStore) create() *session {
	now := time.Now()
	sess := &session{
		ID:        randomToken(32), // 256 bit opaque
		CSRF:      randomToken(32),
		CreatedAt: now,
		LastSeen:  now,
		ExpiresAt: now.Add(s.absTTL),
	}
	s.mu.Lock()
	s.byID[sess.ID] = sess
	s.mu.Unlock()
	return sess
}

// get 返回有效会话并刷新空闲时间；过期/不存在返回 false。
func (s *sessionStore) get(id string) (*session, bool) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.byID[id]
	if !ok {
		return nil, false
	}
	if now.After(sess.ExpiresAt) || now.Sub(sess.LastSeen) > s.idleTTL {
		delete(s.byID, id)
		return nil, false
	}
	sess.LastSeen = now
	return sess, true
}

func (s *sessionStore) delete(id string) {
	s.mu.Lock()
	delete(s.byID, id)
	s.mu.Unlock()
}

// invalidateAll 使全部会话失效（改密/恢复后调用），返回失效数量。
func (s *sessionStore) invalidateAll() int {
	s.mu.Lock()
	n := len(s.byID)
	s.byID = map[string]*session{}
	s.mu.Unlock()
	return n
}

// rateLimiter 是固定窗口登录限流器（IP 维度 + 全局维度）。
type rateLimiter struct {
	mu          sync.Mutex
	window      time.Duration
	ipLimit     int
	globalLimit int
	ips         map[string]*rateWindow
	global      *rateWindow
}

type rateWindow struct {
	start time.Time
	count int
}

// newRateLimiter 创建限流器：window 内每个 IP 最多 ipLimit 次，全局最多 globalLimit 次。
func newRateLimiter(window time.Duration, ipLimit, globalLimit int) *rateLimiter {
	return &rateLimiter{
		window:      window,
		ipLimit:     ipLimit,
		globalLimit: globalLimit,
		ips:         map[string]*rateWindow{},
		global:      &rateWindow{start: time.Now()},
	}
}

// allow 判断是否放行（登录尝试；失败后由调用方调用 fail 计数）。
func (r *rateLimiter) allow(ip string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if !r.windowAllow(r.global, now, r.globalLimit) {
		return false
	}
	w := r.ips[ip]
	if w == nil {
		return true // 新 IP 首次尝试不受限
	}
	return r.windowAllow(w, now, r.ipLimit)
}

// fail 记录一次失败。
func (r *rateLimiter) fail(ip string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	w := r.ips[ip]
	if w == nil {
		w = &rateWindow{start: now}
		r.ips[ip] = w
	}
	r.roll(w, now)
	w.count++
	r.roll(r.global, now)
	r.global.count++
}

// roll 窗口过期则重置（调用方持有锁）。
func (r *rateLimiter) roll(w *rateWindow, now time.Time) {
	if now.Sub(w.start) >= r.window {
		w.start = now
		w.count = 0
	}
}

// windowAllow 检查计数是否未超限（调用方持有锁）。
func (r *rateLimiter) windowAllow(w *rateWindow, now time.Time, limit int) bool {
	r.roll(w, now)
	return w.count < limit
}

// ResetAdminPassword 重置管理员密码（CLI 停机恢复：qqbot admin reset-password）。
// 原子更新 Argon2id 哈希；进程重启后内存 session 自然失效。
func ResetAdminPassword(dataDir, newPassword string) error {
	if newPassword == "" {
		return errors.New("密码不能为空")
	}
	a, err := openAuthStore(dataDir)
	if err != nil {
		return err
	}
	return a.SetPassword(newPassword)
}
