package rules

import (
	"strconv"
	"sync"
	"time"
)

// floodDetector 按（群, 用户）统计窗口内消息条数，用于刷屏条件。
// record 在每次消息事件登记（引擎 Process 入口），count 只读查询——
// 保证同一条消息被多条规则评估时计数不重复累加。
type floodDetector struct {
	mu      sync.Mutex
	entries map[string][]int64 // key=群/用户 → 消息时间戳（毫秒）
}

func newFloodDetector() *floodDetector {
	return &floodDetector{entries: make(map[string][]int64)}
}

// record 登记一条消息（追加当前时间戳）。
func (f *floodDetector) record(groupID, userID int64) {
	now := time.Now().UnixMilli()
	key := floodKey(groupID, userID)

	f.mu.Lock()
	defer f.mu.Unlock()
	list := f.entries[key]
	list = append(list, now)
	// 单用户最多保留 50 条，防止内存无限增长
	if len(list) > 50 {
		list = append([]int64(nil), list[len(list)-50:]...)
	}
	f.entries[key] = list
}

// count 返回窗口内消息数（含本条，只读）。
func (f *floodDetector) count(groupID, userID int64, window time.Duration) int {
	now := time.Now().UnixMilli()
	cutoff := now - window.Milliseconds()

	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, ts := range f.entries[floodKey(groupID, userID)] {
		if ts >= cutoff {
			n++
		}
	}
	return n
}

func (f *floodDetector) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = make(map[string][]int64)
}

func floodKey(groupID, userID int64) string {
	return strconv.FormatInt(groupID, 10) + "/" + strconv.FormatInt(userID, 10)
}

// recentText 一条最近消息的文本记录（重复文本检测用）。
type recentText struct {
	groupID int64
	userID  int64
	text    string
	time    time.Time
}

// recentRing 有界内存：保留最近 max 条群消息文本，供 text_repeat 条件查询。
type recentRing struct {
	mu   sync.Mutex
	max  int
	buf  []recentText
}

func newRecentRing(max int) *recentRing {
	if max <= 0 {
		max = 2000
	}
	return &recentRing{max: max}
}

// push 追加一条消息记录。
func (r *recentRing) push(rec recentText) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec.time.IsZero() {
		rec.time = time.Now()
	}
	r.buf = append(r.buf, rec)
	if len(r.buf) > r.max {
		r.buf = append([]recentText(nil), r.buf[len(r.buf)-r.max:]...)
	}
}

// countSame 返回指定群窗口内与 text 相同（且属于指定用户或任意用户）的消息条数（含本条）。
// sameUser=true 仅统计该用户自己的；false 统计群内所有人。
func (r *recentRing) countSame(groupID, userID int64, text string, window time.Duration, sameUser bool) int {
	if text == "" {
		return 0
	}
	cutoff := time.Now().Add(-window)
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for i := len(r.buf) - 1; i >= 0; i-- {
		rec := r.buf[i]
		if rec.groupID != groupID || rec.time.Before(cutoff) {
			continue
		}
		if sameUser && rec.userID != userID {
			continue
		}
		if rec.text == text {
			n++
		}
	}
	return n
}

func (r *recentRing) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = nil
}
