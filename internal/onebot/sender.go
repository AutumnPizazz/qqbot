package onebot

import (
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// groupSender 对每个群做发送限速：同一群的消息按 interval 间隔串行发出，
// 避免触发 QQ 风控（千人群场景下尤其重要）。单群积压超过 maxQueue 时丢弃最旧消息。
// 实际写帧通过注入的 send 函数完成，与连接 generation 解耦：
// Manager 重配置后新消息自动写入新连接。
type groupSender struct {
	mu       sync.Mutex
	queues   map[int64]*groupQueue
	interval time.Duration
	maxQueue int
}

type groupQueue struct {
	ch chan map[string]any
}

const (
	defaultSendInterval = 800 * time.Millisecond
	defaultMaxQueue     = 200
)

var errQueueFull = errors.New("发送队列已满")

func newGroupSender() *groupSender {
	return &groupSender{
		queues:   make(map[int64]*groupQueue),
		interval: defaultSendInterval,
		maxQueue: defaultMaxQueue,
	}
}

// enqueue 将一次发送请求入队。send 负责实际写帧（返回错误时记录日志丢弃）。
func (s *groupSender) enqueue(key int64, msg map[string]any, send func([]byte) error) error {
	s.mu.Lock()
	q := s.queues[key]
	if q == nil {
		q = &groupQueue{ch: make(chan map[string]any, s.maxQueue)}
		s.queues[key] = q
		go s.worker(key, q, send)
	}
	select {
	case q.ch <- msg:
		s.mu.Unlock()
		return nil
	default:
		// 队列满：先丢弃最旧一条，再入队新消息
		select {
		case <-q.ch:
		default:
		}
		select {
		case q.ch <- msg:
			s.mu.Unlock()
			return nil
		default:
			s.mu.Unlock()
			return errQueueFull
		}
	}
}

func (s *groupSender) worker(key int64, q *groupQueue, send func([]byte) error) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for msg := range q.ch {
		data, err := json.Marshal(msg)
		if err == nil {
			if err := send(data); err != nil {
				slog.Warn("发送失败（消息丢弃）", "key", key, "err", err)
			}
		}
		<-ticker.C
	}
}

// sendRaw 直接向连接写一条文本帧，不走 echo 等待（发送类动作不需要回执）。
func (c *Client) sendRaw(data []byte) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return errors.New("连接未建立")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	return conn.WriteMessage(websocket.TextMessage, data)
}
