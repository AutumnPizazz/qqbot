package bot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"qqbot/internal/onebot"
)

// actionTestServer 可编程 OneBot WS 服务端（回包策略可配置）。
type actionTestServer struct {
	upgrader websocket.Upgrader
	handler  func(c *websocket.Conn, data []byte)
	url      string
}

func newActionTestServer(t *testing.T, handler func(c *websocket.Conn, data []byte)) *actionTestServer {
	t.Helper()
	s := &actionTestServer{
		upgrader: websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
		handler:  handler,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := s.upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
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
	s.url = "ws://" + strings.TrimPrefix(srv.URL, "http://")
	t.Cleanup(srv.Close)
	return s
}

// okHandler 回 ok；recorded 记录收到的动作名。
func okHandler(recorded *[]string) func(*websocket.Conn, []byte) {
	return func(c *websocket.Conn, data []byte) {
		var req struct {
			Action string          `json:"action"`
			Echo   json.RawMessage `json:"echo"`
		}
		_ = json.Unmarshal(data, &req)
		*recorded = append(*recorded, req.Action)
		resp, _ := json.Marshal(map[string]any{
			"status": "ok", "retcode": 0, "data": map[string]any{}, "echo": req.Echo,
		})
		_ = c.WriteMessage(websocket.TextMessage, resp)
	}
}

// newActionSvc 创建已连接的 ActionService（返回 svc 与审计收集器）。
func newActionSvc(t *testing.T, handler func(*websocket.Conn, []byte)) (*ActionService, *[]AuditEntry) {
	t.Helper()
	srv := newActionTestServer(t, handler)
	m := onebot.NewManager(onebot.Endpoint{URL: srv.url, Timeout: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go m.Run(ctx)
	// 等待连接
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if st, _, _ := m.Status(); st == onebot.StatusConnected {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st, _, _ := m.Status(); st != onebot.StatusConnected {
		t.Fatalf("OneBot 未连接: %s", st)
	}
	var entries []AuditEntry
	svc := NewActionService(m, func(e AuditEntry) error {
		entries = append(entries, e)
		return nil
	})
	return svc, &entries
}

func TestActionMuteOK(t *testing.T) {
	var actions []string
	svc, entries := newActionSvc(t, okHandler(&actions))
	res, err := svc.Mute(ActionRequest{RequestID: "req-1", ActorID: 10001, GroupID: 111, TargetID: 222, SourceIP: "10.0.0.1"}, 30)
	if err != nil || res.Result != "ok" {
		t.Fatalf("Mute 应成功: res=%+v err=%v", res, err)
	}
	if len(actions) != 1 || actions[0] != "set_group_ban" {
		t.Fatalf("动作未按预期执行: %v", actions)
	}
	if len(*entries) != 1 {
		t.Fatal("应写入 1 条审计")
	}
	e := (*entries)[0]
	if e.RequestID != "req-1" || e.ActionStatus != "ok" || e.ActorType != "admin" ||
		e.SourceIP != "10.0.0.1" || e.TargetID != 222 || e.AuditStatus != "ok" {
		t.Fatalf("审计字段错误: %+v", e)
	}
}

func TestActionUnknownOnTimeout(t *testing.T) {
	// server 不回包 → Call 超时 → unknown
	svc, entries := newActionSvc(t, func(c *websocket.Conn, data []byte) {})
	res, err := svc.Mute(ActionRequest{RequestID: "req-2", GroupID: 111, TargetID: 222}, 30)
	if err != nil {
		t.Fatalf("超时应返回 unknown 结果而非错误: %v", err)
	}
	if res.Result != "unknown" {
		t.Fatalf("超时应为 unknown，实际 %s", res.Result)
	}
	if len(*entries) != 1 || (*entries)[0].ActionStatus != "unknown" {
		t.Fatalf("审计应为 unknown: %+v", *entries)
	}
}

func TestActionFailedOnRetcode(t *testing.T) {
	svc, entries := newActionSvc(t, func(c *websocket.Conn, data []byte) {
		var req struct {
			Echo json.RawMessage `json:"echo"`
		}
		_ = json.Unmarshal(data, &req)
		resp, _ := json.Marshal(map[string]any{
			"status": "failed", "retcode": 100, "data": map[string]any{},
			"echo": req.Echo, "message": "权限不足",
		})
		_ = c.WriteMessage(websocket.TextMessage, resp)
	})
	res, err := svc.Kick(ActionRequest{RequestID: "req-3", GroupID: 111, TargetID: 222}, "测试")
	if err != nil || res.Result != "failed" {
		t.Fatalf("远端失败应为 failed: res=%+v err=%v", res, err)
	}
	if len(*entries) != 1 || (*entries)[0].ActionStatus != "failed" {
		t.Fatalf("审计应为 failed: %+v", *entries)
	}
}

func TestActionDisconnected(t *testing.T) {
	// 未启动 Run 的 Manager：无连接
	m := onebot.NewManager(onebot.Endpoint{URL: "ws://127.0.0.1:1", Timeout: time.Second})
	svc := NewActionService(m, func(e AuditEntry) error { return nil })
	_, err := svc.Mute(ActionRequest{GroupID: 111, TargetID: 222}, 30)
	if !errors.Is(err, onebot.ErrDisconnected) {
		t.Fatalf("断线应返回 ErrDisconnected，实际 %v", err)
	}
}

func TestActionParamValidation(t *testing.T) {
	var actions []string
	svc, _ := newActionSvc(t, okHandler(&actions))
	if _, err := svc.Mute(ActionRequest{GroupID: 1, TargetID: 2}, 0); err == nil {
		t.Fatal("禁言 0 分钟应报错")
	}
	if _, err := svc.Mute(ActionRequest{GroupID: 1, TargetID: 2}, 999999); err == nil {
		t.Fatal("禁言超长应报错")
	}
	if _, err := svc.Recall(ActionRequest{GroupID: 1}, 0); err == nil {
		t.Fatal("消息 ID 无效应报错")
	}
	card := strings.Repeat("卡", 65)
	if _, err := svc.Card(ActionRequest{GroupID: 1, TargetID: 2}, card); err == nil {
		t.Fatal("名片过长应报错")
	}
	reason := strings.Repeat("因", 201)
	if _, err := svc.Kick(ActionRequest{GroupID: 1, TargetID: 2}, reason); err == nil {
		t.Fatal("移出原因过长应报错")
	}
}

func TestActionAuditFailureReported(t *testing.T) {
	var actions []string
	svc, _ := newActionSvc(t, okHandler(&actions))
	// 审计回调失败不影响动作结果
	svc.audit = func(e AuditEntry) error { return errors.New("磁盘满") }
	res, err := svc.Unmute(ActionRequest{RequestID: "req-x", GroupID: 111, TargetID: 222})
	if err != nil || res.Result != "ok" {
		t.Fatalf("审计失败不应影响动作结果: res=%+v err=%v", res, err)
	}
}
