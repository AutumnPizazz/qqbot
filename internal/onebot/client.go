package onebot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// writeTimeout 是单次写帧超时。
	writeTimeout = 10 * time.Second
	// pongWait 用于探测对端存活。
	pongWait = 60 * time.Second
	// pingPeriod 客户端主动心跳间隔。
	pingPeriod = 30 * time.Second
)

// Handler 处理一类事件（按 post_type 分发）。
type Handler func(raw json.RawMessage) error

// Client 是 OneBot v11 正向 WebSocket 客户端。
// 连接由后台 goroutine 维护，断线自动重连；API 调用带超时等待 echo 回包。
// Client 只服务于单个连接 generation：由 Manager 在重配置时销毁旧实例并创建新实例。
type Client struct {
	url     string
	token   string
	timeout time.Duration

	mu       sync.Mutex // 保护 conn / seq / pending / handlers / runCtx
	writeMu  sync.Mutex // 串行化 WS 写帧（gorilla 只允许单写者）
	conn     *websocket.Conn
	seq      int64
	pending  map[int64]chan ActionResponse
	handlers map[string][]Handler
	runCtx   context.Context // Run 传入的 ctx；取消后 pending 调用立即失败
	onConn   func()          // 连接建立回调（Manager 用于状态机）
	onDisc   func()          // 连接断开回调（Manager 用于状态机）

	// groupSender 是每群限速发送器，见 newGroupSender。
	gs *groupSender
}

// New 创建客户端。timeout 为 API 调用等待回包的超时时间。
func New(url, token string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Client{
		url:      url,
		token:    token,
		timeout:  timeout,
		pending:  make(map[int64]chan ActionResponse),
		handlers: make(map[string][]Handler),
		gs:       newGroupSender(),
	}
}

// On 注册事件处理器，eventType 为 post_type 值（message/notice/request/meta_event）。
func (c *Client) On(eventType string, h Handler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handlers[eventType] = append(c.handlers[eventType], h)
}

// Run 启动连接循环，阻塞直到 ctx 取消。断线后按固定间隔自动重连。
func (c *Client) Run(ctx context.Context) error {
	c.mu.Lock()
	c.runCtx = ctx
	c.mu.Unlock()
	for {
		if err := c.connect(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			slog.Error("连接 OneBot 失败", "url", c.url, "err", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(5 * time.Second):
			}
			continue
		}
		if err := c.readLoop(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("连接断开，准备重连", "err", err)
			c.mu.Lock()
			fn := c.onDisc
			c.mu.Unlock()
			if fn != nil {
				go fn() // 立即通知状态机（不等重连退避）
			}
		}
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(5 * time.Second):
		}
	}
}

// Close 立即关闭当前连接（唤醒 readLoop，使其返回错误）。
// 用于 Manager 重配置时终止旧 generation。
func (c *Client) Close() {
	c.mu.Lock()
	conn := c.conn
	c.conn = nil
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

// SetOnConnected 注册连接建立回调（异步调用，锁外）。
func (c *Client) SetOnConnected(fn func()) {
	c.mu.Lock()
	c.onConn = fn
	c.mu.Unlock()
}

// SetOnDisconnected 注册连接断开回调（readLoop 退出时触发，锁外异步调用）。
func (c *Client) SetOnDisconnected(fn func()) {
	c.mu.Lock()
	c.onDisc = fn
	c.mu.Unlock()
}

// IsConnected 判断当前是否有活跃连接。
func (c *Client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil
}

// connect 建立一次 WS 连接，并启动心跳协程。
func (c *Client) connect(ctx context.Context) error {
	header := http.Header{}
	if c.token != "" {
		header.Set("Authorization", "Bearer "+c.token)
	}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.url, header)
	if err != nil {
		return err
	}
	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	go c.pingLoop(ctx, conn)
	if fn := c.onConn; fn != nil {
		go fn()
	}
	slog.Info("已连接 OneBot", "url", c.url)
	return nil
}

// pingLoop 定期发送 ping 保持连接。
func (c *Client) pingLoop(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			conn.Close()
			return
		case <-ticker.C:
			conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				conn.Close()
				return
			}
		}
	}
}

// readLoop 读取消息并分发事件 / 匹配 API 回包。
func (c *Client) readLoop(ctx context.Context) error {
	for {
		_, data, err := c.connRead(ctx)
		if err != nil {
			return err
		}
		var env Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			slog.Warn("解析事件失败", "err", err)
			continue
		}
		if env.PostType == "" {
			// 可能是 API 回包（带 echo 字段）
			c.dispatchResponse(data)
			continue
		}
		c.dispatchEvent(env.PostType, data)
	}
}

// connRead 读取一条消息，注意读锁串行化。
func (c *Client) connRead(ctx context.Context) (int, []byte, error) {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return 0, nil, errors.New("连接未建立")
	}
	select {
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	default:
	}
	return conn.ReadMessage()
}

// dispatchResponse 将 echo 匹配到等待中的 API 调用。
func (c *Client) dispatchResponse(data []byte) {
	var resp ActionResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return
	}
	var echoStr string
	if err := json.Unmarshal(resp.Echo, &echoStr); err != nil {
		return
	}
	var seq int64
	if _, err := fmt.Sscanf(echoStr, "seq-%d", &seq); err != nil {
		return
	}
	c.mu.Lock()
	ch := c.pending[seq]
	delete(c.pending, seq)
	c.mu.Unlock()
	if ch != nil {
		ch <- resp
	}
}

// dispatchEvent 按 post_type 调用注册的处理器。
// 每个处理器在独立 goroutine 中运行：处理器内部会调用 Call 等待 echo 回包，
// 而回包由本循环读取，若同步调用会互相阻塞（死锁）。
func (c *Client) dispatchEvent(postType string, data []byte) {
	c.mu.Lock()
	hs := append([]Handler(nil), c.handlers[postType]...)
	c.mu.Unlock()
	for _, h := range hs {
		h := h
		go func() {
			if err := h(data); err != nil {
				slog.Error("事件处理失败", "post_type", postType, "err", err)
			}
		}()
	}
}

// Call 执行一次 OneBot 动作并等待回包。
func (c *Client) Call(action string, params map[string]any) (json.RawMessage, error) {
	c.mu.Lock()
	conn := c.conn
	if conn == nil {
		c.mu.Unlock()
		return nil, errors.New("连接未建立")
	}
	c.seq++
	seq := c.seq
	ch := make(chan ActionResponse, 1)
	c.pending[seq] = ch
	echo := fmt.Sprintf("seq-%d", seq)
	payload := map[string]any{
		"action": action,
		"params": params,
		"echo":   echo,
	}
	c.mu.Unlock()

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	c.writeMu.Lock()
	conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	werr := conn.WriteMessage(websocket.TextMessage, data)
	c.writeMu.Unlock()
	if werr != nil {
		c.dropPending(seq)
		return nil, werr
	}

	select {
	case resp := <-ch:
		if resp.Status != "ok" || resp.RetCode != 0 {
			return resp.Data, fmt.Errorf("动作 %s 失败: status=%s retcode=%d", action, resp.Status, resp.RetCode)
		}
		return resp.Data, nil
	case <-time.After(c.timeout):
		c.dropPending(seq)
		return nil, fmt.Errorf("动作 %s 超时", action)
	case <-c.runCtxDone():
		c.dropPending(seq)
		return nil, ErrGenerationClosed
	}
}

// runCtxDone 返回当前 generation ctx 的 Done（Run 未启动时返回永不关闭的 channel）。
func (c *Client) runCtxDone() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.runCtx == nil {
		return nil
	}
	return c.runCtx.Done()
}

func (c *Client) dropPending(seq int64) {
	c.mu.Lock()
	delete(c.pending, seq)
	c.mu.Unlock()
}

// --- 常用动作封装 ---

// SendGroupMsg 向群发送文本消息（走每群限速队列）。
func (c *Client) SendGroupMsg(groupID int64, text string) error {
	payload := map[string]any{"group_id": groupID, "message": text}
	return c.gs.enqueue(groupID, map[string]any{
		"action": "send_group_msg", "params": payload,
	}, c.sendRaw)
}

// SendPrivateMsg 向用户发送私聊消息（走每用户限速队列）。
func (c *Client) SendPrivateMsg(userID int64, text string) error {
	payload := map[string]any{"user_id": userID, "message": text}
	return c.gs.enqueue(userID, map[string]any{
		"action": "send_private_msg", "params": payload,
	}, c.sendRaw)
}

// SendGroupMsgAt 在群内 @ 某人并附带文本。
func (c *Client) SendGroupMsgAt(groupID, userID int64, text string) error {
	msg := fmt.Sprintf("[CQ:at,qq=%d] %s", userID, text)
	return c.SendGroupMsg(groupID, msg)
}

// SetGroupBan 禁言群成员，duration 单位秒；0 表示解除禁言。
func (c *Client) SetGroupBan(groupID, userID int64, duration int64) error {
	_, err := c.Call("set_group_ban", map[string]any{
		"group_id": groupID, "user_id": userID, "duration": duration,
	})
	return err
}

// SetGroupKick 移出群成员。
func (c *Client) SetGroupKick(groupID, userID int64, rejectAdd bool) error {
	_, err := c.Call("set_group_kick", map[string]any{
		"group_id": groupID, "user_id": userID, "reject_add_request": rejectAdd,
	})
	return err
}

// SetGroupWholeBan 开启或关闭全员禁言。
func (c *Client) SetGroupWholeBan(groupID int64, enabled bool) error {
	_, err := c.Call("set_group_whole_ban", map[string]any{
		"group_id": groupID, "enable": enabled,
	})
	return err
}

// DeleteMsg 撤回指定消息。
func (c *Client) DeleteMsg(messageID int32) error {
	_, err := c.Call("delete_msg", map[string]any{"message_id": messageID})
	return err
}

// SetGroupCard 设置群成员名片；card 为空时清空名片。
func (c *Client) SetGroupCard(groupID, userID int64, card string) error {
	_, err := c.Call("set_group_card", map[string]any{
		"group_id": groupID, "user_id": userID, "card": card,
	})
	return err
}

// GetGroupMemberInfo 获取群成员信息（昵称等）。
func (c *Client) GetGroupMemberInfo(groupID, userID int64) (Sender, error) {
	var s Sender
	data, err := c.Call("get_group_member_info", map[string]any{
		"group_id": groupID, "user_id": userID,
	})
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(data, &s)
	return s, err
}

// SetGroupAddRequest 处理加群请求。
func (c *Client) SetGroupAddRequest(flag, subType string, approve bool, reason string) error {
	_, err := c.Call("set_group_add_request", map[string]any{
		"flag": flag, "sub_type": subType, "approve": approve, "reason": reason,
	})
	return err
}
