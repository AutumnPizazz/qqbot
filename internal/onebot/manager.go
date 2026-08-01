package onebot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Endpoint 一次 OneBot 连接的配置。
type Endpoint struct {
	URL         string
	AccessToken string
	Timeout     time.Duration
}

// Status 是连接状态机的对外状态。
type Status string

const (
	StatusDisconnected Status = "disconnected"
	StatusConnecting   Status = "connecting"
	StatusConnected    Status = "connected"
	StatusReconnecting Status = "reconnecting"
	StatusError        Status = "error"
)

// 错误语义（REST API 层映射）：
//   - ErrDisconnected   → 503 onebot_disconnected（断线期间动作不可用）
//   - ErrGenerationClosed → 503 onebot_reconfigured（动作落在被重配取消的旧 generation）
var (
	// ErrDisconnected 表示当前没有可用连接。
	ErrDisconnected = errors.New("onebot_disconnected")
	// ErrGenerationClosed 表示动作落在已被重配置取消的旧连接上。
	ErrGenerationClosed = errors.New("onebot_reconfigured")
)

// Manager 是 generation-aware 的 OneBot 连接管理器。
//
// 与直接持有单个 Client 不同，Manager 在每次重配置时递增 generation：
//   - 每个 generation 拥有独立 context 与连接对象；readLoop/pingLoop/连接绑定同一 generation。
//   - Reconfigure 先发布新 endpoint，再取消并关闭旧 generation；
//     旧 pending 请求立即返回 ErrGenerationClosed。
//   - 主循环等待旧 generation 完全退出后才连接最新 endpoint。
//   - 断线期间动作 API 返回 ErrDisconnected。
//
// 事件处理器注册在 Manager 上，每个新 generation 创建时自动绑定，
// 因此事件监听跨重配置保持不变。
type Manager struct {
	mu        sync.Mutex
	ep        Endpoint
	gen       int64 // 单调递增的 generation 号
	client    *Client
	genCancel context.CancelFunc // 当前 generation 的取消函数
	status    Status
	lastErr   error
	lastErrAt time.Time
	handlers  map[string][]Handler
	onChange  []func(Status)
	gs        *groupSender // 每群限速队列（跨 generation 存活）
}

// NewManager 创建连接管理器。
func NewManager(ep Endpoint) *Manager {
	if ep.Timeout <= 0 {
		ep.Timeout = 5 * time.Second
	}
	return &Manager{
		ep:       ep,
		status:   StatusDisconnected,
		handlers: map[string][]Handler{},
		gs:       newGroupSender(),
	}
}

// On 注册事件处理器，跨 generation 保持（等价于 Client.On）。
func (m *Manager) On(eventType string, h Handler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handlers[eventType] = append(m.handlers[eventType], h)
}

// SetStatusListener 注册状态变化回调（锁外异步调用）。
func (m *Manager) SetStatusListener(fn func(Status)) {
	m.mu.Lock()
	m.onChange = append(m.onChange, fn)
	m.mu.Unlock()
}

// Reconfigure 热更新连接配置：发布新 endpoint，取消并关闭旧 generation。
// 旧循环退出后主循环自动连接最新 endpoint。不阻塞调用方。
func (m *Manager) Reconfigure(ep Endpoint) {
	if ep.Timeout <= 0 {
		ep.Timeout = 5 * time.Second
	}
	m.mu.Lock()
	m.ep = ep
	m.gen++
	m.setStatusLocked(StatusDisconnected, nil)
	client := m.client
	cancel := m.genCancel
	notify := true
	m.mu.Unlock()
	if cancel != nil {
		cancel() // 取消旧 generation（readLoop/pingLoop/pending 全部终止）
	}
	if client != nil {
		client.Close() // 立即唤醒 readLoop，让旧循环尽快退出
	}
	if notify {
		m.notifyStatus(StatusDisconnected)
	}
	slog.Info("OneBot 配置已更新，等待旧连接退出后重连", "url", ep.URL)
}

// Endpoint 返回当前配置的 endpoint。
func (m *Manager) Endpoint() Endpoint {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ep
}

// Status 返回当前状态、最近错误与错误时间。
func (m *Manager) Status() (Status, error, time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status, m.lastErr, m.lastErrAt
}

func (m *Manager) setStatusLocked(s Status, err error) {
	m.status = s
	if err != nil {
		m.lastErr = err
		m.lastErrAt = time.Now()
	} else {
		m.lastErr = nil
		m.lastErrAt = time.Time{}
	}
}

func (m *Manager) notifyStatus(s Status) {
	m.mu.Lock()
	fns := append([]func(Status){}, m.onChange...)
	m.mu.Unlock()
	for _, fn := range fns {
		fn(s)
	}
}

// Run 启动连接主循环，阻塞直到 ctx 取消。
// 循环内：使用最新 endpoint 创建 generation → 运行 → 重配/断线后重建。
func (m *Manager) Run(ctx context.Context) error {
	for {
		ep, gen := m.currentGen()
		if err := m.runGeneration(ctx, ep, gen); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		m.mu.Lock()
		reconfigured := m.gen != gen
		m.mu.Unlock()
		if reconfigured {
			continue // 立即用新 endpoint 重连
		}
		// 断线退避后重连
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(5 * time.Second):
		}
	}
}

// currentGen 返回当前 endpoint 与 generation 号。
func (m *Manager) currentGen() (Endpoint, int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ep, m.gen
}

// runGeneration 运行一个 generation：创建 Client、绑定 handler、执行连接循环。
// 返回 nil 表示 generation 正常结束（ctx 取消或已被重配取代）。
func (m *Manager) runGeneration(ctx context.Context, ep Endpoint, gen int64) error {
	genCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	client := New(ep.URL, ep.AccessToken, ep.Timeout)
	client.SetOnConnected(func() {
		m.mu.Lock()
		if m.gen == gen {
			m.setStatusLocked(StatusConnected, nil)
		}
		m.mu.Unlock()
		m.notifyStatus(StatusConnected)
	})
	client.SetOnDisconnected(func() {
		m.mu.Lock()
		if m.gen == gen {
			m.setStatusLocked(StatusReconnecting, fmt.Errorf("连接断开"))
		}
		m.mu.Unlock()
		m.notifyStatus(StatusReconnecting)
	})

	m.mu.Lock()
	if m.gen != gen {
		// 创建期间已被重配：直接丢弃这个 generation
		m.mu.Unlock()
		cancel()
		return nil
	}
	m.client = client
	m.genCancel = cancel
	m.setStatusLocked(StatusConnecting, nil)
	m.mu.Unlock()
	m.notifyStatus(StatusConnecting)

	for _, evType := range []string{"message", "notice", "request", "meta_event"} {
		m.mu.Lock()
		hs := append([]Handler(nil), m.handlers[evType]...)
		m.mu.Unlock()
		for _, h := range hs {
			client.On(evType, h)
		}
	}

	if err := client.Run(genCtx); err != nil {
		return err
	}

	// generation 结束：根据结束原因更新状态
	m.mu.Lock()
	if m.gen != gen {
		// 已被重配取代，状态由 Reconfigure 维护
		m.client = nil
		m.genCancel = nil
		m.mu.Unlock()
		return nil
	}
	m.client = nil
	m.genCancel = nil
	replaced := genCtx.Err() != nil
	m.setStatusLocked(StatusReconnecting, fmt.Errorf("连接断开"))
	m.mu.Unlock()
	if !replaced {
		m.notifyStatus(StatusReconnecting)
	}
	return nil
}

// Call 执行动作；无连接或状态非 connected 返回 ErrDisconnected，
// 落在旧 generation 返回 ErrGenerationClosed。
func (m *Manager) Call(action string, params map[string]any) (json.RawMessage, error) {
	m.mu.Lock()
	c := m.client
	connected := m.status == StatusConnected
	m.mu.Unlock()
	if c == nil || !connected || !c.IsConnected() {
		return nil, ErrDisconnected
	}
	return c.Call(action, params)
}

// enqueueSend 走跨 generation 的每群限速队列；写帧时取当前活跃连接。
func (m *Manager) enqueueSend(key int64, msg map[string]any) error {
	return m.gs.enqueue(key, msg, func(data []byte) error {
		m.mu.Lock()
		c := m.client
		m.mu.Unlock()
		if c == nil || !c.IsConnected() {
			return ErrDisconnected
		}
		return c.sendRaw(data)
	})
}

// SendGroupMsg 向群发送文本消息（走每群限速队列）。
func (m *Manager) SendGroupMsg(groupID int64, text string) error {
	payload := map[string]any{"group_id": groupID, "message": text}
	return m.enqueueSend(groupID, map[string]any{"action": "send_group_msg", "params": payload})
}

// SendPrivateMsg 向用户发送私聊消息（走每用户限速队列）。
func (m *Manager) SendPrivateMsg(userID int64, text string) error {
	payload := map[string]any{"user_id": userID, "message": text}
	return m.enqueueSend(userID, map[string]any{"action": "send_private_msg", "params": payload})
}

// SendGroupMsgAt 在群内 @ 某人并附带文本。
func (m *Manager) SendGroupMsgAt(groupID, userID int64, text string) error {
	msg := fmt.Sprintf("[CQ:at,qq=%d] %s", userID, text)
	return m.SendGroupMsg(groupID, msg)
}

// SetGroupBan 禁言群成员，duration 单位秒；0 表示解除禁言。
func (m *Manager) SetGroupBan(groupID, userID int64, duration int64) error {
	_, err := m.Call("set_group_ban", map[string]any{
		"group_id": groupID, "user_id": userID, "duration": duration,
	})
	return err
}

// SetGroupKick 移出群成员。
func (m *Manager) SetGroupKick(groupID, userID int64, rejectAdd bool) error {
	_, err := m.Call("set_group_kick", map[string]any{
		"group_id": groupID, "user_id": userID, "reject_add_request": rejectAdd,
	})
	return err
}

// SetGroupWholeBan 开启或关闭全员禁言。
func (m *Manager) SetGroupWholeBan(groupID int64, enabled bool) error {
	_, err := m.Call("set_group_whole_ban", map[string]any{
		"group_id": groupID, "enable": enabled,
	})
	return err
}

// DeleteMsg 撤回指定消息。
func (m *Manager) DeleteMsg(messageID int32) error {
	_, err := m.Call("delete_msg", map[string]any{"message_id": messageID})
	return err
}

// SetGroupCard 设置群成员名片；card 为空时清空名片。
func (m *Manager) SetGroupCard(groupID, userID int64, card string) error {
	_, err := m.Call("set_group_card", map[string]any{
		"group_id": groupID, "user_id": userID, "card": card,
	})
	return err
}

// GetGroupMemberInfo 获取群成员信息（昵称等）。
func (m *Manager) GetGroupMemberInfo(groupID, userID int64) (Sender, error) {
	var s Sender
	data, err := m.Call("get_group_member_info", map[string]any{
		"group_id": groupID, "user_id": userID,
	})
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(data, &s)
	return s, err
}

// SetGroupAddRequest 处理加群请求。
func (m *Manager) SetGroupAddRequest(flag, subType string, approve bool, reason string) error {
	_, err := m.Call("set_group_add_request", map[string]any{
		"flag": flag, "sub_type": subType, "approve": approve, "reason": reason,
	})
	return err
}
