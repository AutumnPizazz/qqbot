package bot

import (
	"log/slog"
	"strconv"
	"sync"
	"time"
)

// joinCache 缓存群成员的入群时间（新人保护期条件 user_joined_within 的输入）。
// TTL 过期后重新查询 get_group_member_info；查询失败返回 nil（条件不满足，保守处理）。
type joinCache struct {
	mu   sync.Mutex
	ttl  time.Duration
	data map[string]joinInfo
}

type joinInfo struct {
	joinTime time.Time
	fetched  time.Time
}

func newJoinCache(ttl time.Duration) *joinCache {
	return &joinCache{ttl: ttl, data: make(map[string]joinInfo)}
}

// joinTimeOf 返回 (群, 用户) 的入群时间；未知返回 nil。
func (b *Bot) joinTimeOf(groupID, userID int64) *time.Time {
	key := strconv.FormatInt(groupID, 10) + "/" + strconv.FormatInt(userID, 10)
	b.joins.mu.Lock()
	info, ok := b.joins.data[key]
	now := time.Now()
	if ok && now.Sub(info.fetched) < b.joins.ttl {
		b.joins.mu.Unlock()
		t := info.joinTime
		return &t
	}
	b.joins.mu.Unlock()

	// 缓存未命中：查询成员信息（只更新缓存，不阻塞事件处理太久）
	sender, err := b.client.GetGroupMemberInfo(groupID, userID)
	if err != nil || sender.JoinTime <= 0 {
		slog.Debug("获取成员入群时间失败", "group", groupID, "user", userID, "err", err)
		return nil
	}
	t := time.Unix(sender.JoinTime, 0)
	b.joins.mu.Lock()
	b.joins.data[key] = joinInfo{joinTime: t, fetched: now}
	b.joins.mu.Unlock()
	return &t
}
