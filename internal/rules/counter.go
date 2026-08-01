package rules

import (
	"strconv"
	"sync"
	"time"
)

// counterStore 按 (群, counterID, 用户) 维度累计计数，带滑动时间窗口。
// 数据仅存内存（与旧 strikeStore 一致：重启清零），用于升级处罚等有状态条件。
type counterStore struct {
	mu   sync.Mutex
	data map[string]map[int64]*counter // key=group/counterID → user → counter
}

type counter struct {
	count   int
	updated time.Time
}

func newCounterStore() *counterStore {
	return &counterStore{data: make(map[string]map[int64]*counter)}
}

func counterKey(groupID int64, counterID string) string {
	return strconv.FormatInt(groupID, 10) + "/" + counterID
}

// Increment 计数 +step，窗口滑动语义：距上次更新超过 window 时重置为 step。
// 返回累计次数。
func (s *counterStore) Increment(groupID int64, counterID string, userID int64, step int, window time.Duration) int {
	if step <= 0 {
		step = 1
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	key := counterKey(groupID, counterID)
	byUser := s.data[key]
	if byUser == nil {
		byUser = make(map[int64]*counter)
		s.data[key] = byUser
	}
	c := byUser[userID]
	if c == nil || now.Sub(c.updated) > window {
		c = &counter{count: 0}
		byUser[userID] = c
	}
	c.count += step
	c.updated = now
	s.evictLocked(key, byUser)
	return c.count
}

// Get 返回当前计数（窗口内有效，超窗视为 0）。
func (s *counterStore) Get(groupID int64, counterID string, userID int64, window time.Duration) int {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.data[counterKey(groupID, counterID)][userID]; c != nil && now.Sub(c.updated) <= window {
		return c.count
	}
	return 0
}

// Set 直接设置计数。
func (s *counterStore) Set(groupID int64, counterID string, userID int64, count int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := counterKey(groupID, counterID)
	byUser := s.data[key]
	if byUser == nil {
		byUser = make(map[int64]*counter)
		s.data[key] = byUser
	}
	byUser[userID] = &counter{count: count, updated: time.Now()}
	s.evictLocked(key, byUser)
}

// Reset 清零某用户计数。
func (s *counterStore) Reset(groupID int64, counterID string, userID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.data[counterKey(groupID, counterID)]; m != nil {
		delete(m, userID)
	}
}

// Decrement 计数 -step（不低于 0）。
func (s *counterStore) Decrement(groupID int64, counterID string, userID int64, step int) {
	if step <= 0 {
		step = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.data[counterKey(groupID, counterID)][userID]; c != nil {
		c.count -= step
		if c.count < 0 {
			c.count = 0
		}
		c.updated = time.Now()
	}
}

// ResetAll 清空全部状态（配置热更新时调用，避免旧计数污染新规则）。
func (s *counterStore) ResetAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = make(map[string]map[int64]*counter)
}

// Snapshot 导出全部计数（网页排障面板）。返回 key=群/计数器 → 用户 → 次数。
func (s *counterStore) Snapshot() map[string]map[int64]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]map[int64]int, len(s.data))
	for key, byUser := range s.data {
		um := make(map[int64]int, len(byUser))
		for uid, c := range byUser {
			um[uid] = c.count
		}
		out[key] = um
	}
	return out
}

// evictLocked 单 counter 最多保留 maxCounterUsers 个用户（超限丢弃最旧）。
const maxCounterUsers = 200

func (s *counterStore) evictLocked(key string, byUser map[int64]*counter) {
	if len(byUser) <= maxCounterUsers {
		return
	}
	type kv struct {
		uid int64
		t   time.Time
	}
	all := make([]kv, 0, len(byUser))
	for uid, c := range byUser {
		all = append(all, kv{uid, c.updated})
	}
	// 逐个淘汰最旧（超出部分很小，线性复杂度足够）
	for i := 0; i < len(byUser)-maxCounterUsers; i++ {
		oldest := 0
		for j := 1; j < len(all); j++ {
			if all[j].t.Before(all[oldest].t) {
				oldest = j
			}
		}
		delete(byUser, all[oldest].uid)
		all = append(all[:oldest], all[oldest+1:]...)
	}
}
