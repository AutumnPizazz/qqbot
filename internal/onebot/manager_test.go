package onebot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// mockOneBotServer 是一个可编程的 OneBot WS 服务端。
type mockOneBotServer struct {
	t       *testing.T
	srv     *httptest.Server
	conns   chan *websocket.Conn
	handler func(c *websocket.Conn, data []byte)
	url     string

	mu     sync.Mutex
	nConns int
}

func newMockOneBotServer(t *testing.T, handler func(c *websocket.Conn, data []byte)) *mockOneBotServer {
	t.Helper()
	s := &mockOneBotServer{
		t:       t,
		conns:   make(chan *websocket.Conn, 16),
		handler: handler,
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		s.mu.Lock()
		s.nConns++
		s.mu.Unlock()
		s.conns <- ws
		ws.SetReadDeadline(time.Now().Add(120 * time.Second))
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				_ = ws.Close()
				return
			}
			if s.handler != nil {
				s.handler(ws, data)
			}
		}
	}))
	s.url = "ws://" + strings.TrimPrefix(s.srv.URL, "http://")
	t.Cleanup(s.srv.Close)
	return s
}

// nextConn 等待下一次连接建立（2 秒超时）。
func (s *mockOneBotServer) nextConn(t *testing.T) *websocket.Conn {
	t.Helper()
	select {
	case c := <-s.conns:
		return c
	case <-time.After(2 * time.Second):
		t.Fatal("等待客户端连接超时")
		return nil
	}
}

func (s *mockOneBotServer) connCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nConns
}

// echoHandler 是默认回包：解析 action/echo 并回 ok。
func echoHandler(c *websocket.Conn, data []byte) {
	var req struct {
		Action string          `json:"action"`
		Echo   json.RawMessage `json:"echo"`
	}
	_ = json.Unmarshal(data, &req)
	resp, _ := json.Marshal(map[string]any{
		"status": "ok", "retcode": 0, "data": map[string]any{"action": req.Action}, "echo": req.Echo,
	})
	_ = c.WriteMessage(websocket.TextMessage, resp)
}

// waitStatus 轮询等待 Manager 达到期望状态。
func waitStatus(t *testing.T, m *Manager, want Status, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s, _, _ := m.Status()
		if s == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	s, err, at := m.Status()
	t.Fatalf("状态未达到 %s（当前 %s, err=%v, at=%v）", want, s, err, at)
}

func TestManagerConnectAndAction(t *testing.T) {
	srv := newMockOneBotServer(t, echoHandler)
	m := NewManager(Endpoint{URL: srv.url, Timeout: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	waitStatus(t, m, StatusConnected, 3*time.Second)
	if _, err := m.Call("get_friend_list", nil); err != nil {
		t.Fatalf("连接后 Call 失败: %v", err)
	}
}

func TestManagerCallDisconnected(t *testing.T) {
	m := NewManager(Endpoint{URL: "ws://127.0.0.1:1", Timeout: time.Second})
	// 未启动 Run：无连接
	if _, err := m.Call("get_friend_list", nil); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("应返回 ErrDisconnected，实际 %v", err)
	}
}

func TestManagerReconfigureSwitchesGeneration(t *testing.T) {
	srvA := newMockOneBotServer(t, echoHandler)
	srvB := newMockOneBotServer(t, echoHandler)
	m := NewManager(Endpoint{URL: srvA.url, Timeout: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	waitStatus(t, m, StatusConnected, 3*time.Second)
	if srvA.connCount() != 1 {
		t.Fatalf("server A 应有 1 个连接，实际 %d", srvA.connCount())
	}

	// 重配到 server B
	m.Reconfigure(Endpoint{URL: srvB.url, Timeout: time.Second})
	waitStatus(t, m, StatusConnected, 5*time.Second)
	if srvB.connCount() != 1 {
		t.Fatalf("server B 应有 1 个连接，实际 %d", srvB.connCount())
	}
	// 旧连接应已关闭：server A 不应有新连接
	if srvA.connCount() != 1 {
		t.Fatalf("server A 不应出现新连接，实际 %d", srvA.connCount())
	}
	// 新 generation 上动作可用
	if _, err := m.Call("get_group_list", nil); err != nil {
		t.Fatalf("重配后 Call 失败: %v", err)
	}
}

func TestManagerPendingDroppedOnReconfigure(t *testing.T) {
	// server A 不回包（挂起 pending 请求）
	srvA := newMockOneBotServer(t, func(c *websocket.Conn, data []byte) {
		// 故意不回复
	})
	srvB := newMockOneBotServer(t, echoHandler)
	m := NewManager(Endpoint{URL: srvA.url, Timeout: 30 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	waitStatus(t, m, StatusConnected, 3*time.Second)

	errCh := make(chan error, 1)
	go func() {
		_, err := m.Call("get_friend_list", nil)
		errCh <- err
	}()
	time.Sleep(100 * time.Millisecond) // 确保 pending 已注册

	m.Reconfigure(Endpoint{URL: srvB.url, Timeout: time.Second})
	select {
	case err := <-errCh:
		if !errors.Is(err, ErrGenerationClosed) {
			t.Fatalf("pending 应返回 ErrGenerationClosed，实际 %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pending 请求未被取消")
	}
	waitStatus(t, m, StatusConnected, 5*time.Second)
}

func TestManagerEventHandlersSurviveReconfigure(t *testing.T) {
	srvA := newMockOneBotServer(t, echoHandler)
	srvB := newMockOneBotServer(t, echoHandler)
	m := NewManager(Endpoint{URL: srvA.url, Timeout: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	got := make(chan string, 4)
	m.On("message", func(raw json.RawMessage) error {
		var e struct {
			MessageType string `json:"message_type"`
		}
		_ = json.Unmarshal(raw, &e)
		got <- e.MessageType
		return nil
	})
	go m.Run(ctx)
	waitStatus(t, m, StatusConnected, 3*time.Second)

	send := func(c *websocket.Conn) {
		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"post_type":"message","message_type":"group"}`))
	}
	connA := srvA.nextConn(t)
	send(connA)
	select {
	case mt := <-got:
		if mt != "group" {
			t.Fatalf("事件类型错误: %s", mt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("重配前事件未到达")
	}

	m.Reconfigure(Endpoint{URL: srvB.url, Timeout: time.Second})
	waitStatus(t, m, StatusConnected, 5*time.Second)
	connB := srvB.nextConn(t)
	send(connB)
	select {
	case mt := <-got:
		if mt != "group" {
			t.Fatalf("重配后事件类型错误: %s", mt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("重配后事件未到达（handler 未跨 generation 保持）")
	}
}

func TestManagerSendGroupMsgQueued(t *testing.T) {
	got := make(chan string, 4)
	srv := newMockOneBotServer(t, func(c *websocket.Conn, data []byte) {
		var req struct {
			Action string `json:"action"`
		}
		_ = json.Unmarshal(data, &req)
		got <- req.Action
	})
	m := NewManager(Endpoint{URL: srv.url, Timeout: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	waitStatus(t, m, StatusConnected, 3*time.Second)
	if err := m.SendGroupMsg(123, "hello"); err != nil {
		t.Fatalf("SendGroupMsg 失败: %v", err)
	}
	select {
	case action := <-got:
		if action != "send_group_msg" {
			t.Fatalf("意外动作: %s", action)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("限速队列未发送消息")
	}
}

func TestManagerDetectsDisconnectImmediately(t *testing.T) {
	srv := newMockOneBotServer(t, echoHandler)
	m := NewManager(Endpoint{URL: srv.url, Timeout: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	waitStatus(t, m, StatusConnected, 3*time.Second)

	// 服务端断开连接 → 应立即进入 reconnecting（不等重连退避）
	conn := srv.nextConn(t)
	_ = conn.Close()
	waitStatus(t, m, StatusReconnecting, 2*time.Second)
	// 断线期间动作 503
	if _, err := m.Call("get_friend_list", nil); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("断线期间应 ErrDisconnected，实际 %v", err)
	}
}
