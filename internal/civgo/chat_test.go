package civgo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeResponses 构造 Responses API mock 服务器，返回固定回答；记录请求体供断言。
func fakeResponses(t *testing.T, reply string) (*httptest.Server, *map[string]any) {
	t.Helper()
	var last map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("路径应为 /responses，got %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("Authorization 头错误: %s", r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("解码请求失败: %v", err)
			w.WriteHeader(400)
			return
		}
		last = body
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": "resp_test_001",
			"output": []map[string]any{
				{"type": "reasoning", "summary": []map[string]any{}}, // 应被跳过
				{"type": "message", "role": "assistant", "content": []map[string]any{
					{"type": "output_text", "text": reply},
				}},
			},
			"usage": map[string]any{"input_tokens": 100, "output_tokens": 42},
		})
	}))
	return srv, &last
}

func TestCompletePathAndBody(t *testing.T) {
	srv, last := fakeResponses(t, "弓手射程 2 格。")
	defer srv.Close()
	cfg := testAIConfig()
	cfg.BaseURL = srv.URL
	client := NewChatClient(cfg)

	input := []InputItem{
		{"role": "system", "content": "【来源: units/archer.md】\n弓手是远程单位。"},
		{"role": "user", "content": "弓手射程多少？"},
	}
	comp, err := client.Complete(context.Background(), systemPrompt, input, nil)
	if err != nil {
		t.Fatalf("Complete 失败: %v", err)
	}
	if comp.Text != "弓手射程 2 格。" {
		t.Errorf("回答提取错误: %q", comp.Text)
	}
	if len(comp.Calls) != 0 {
		t.Errorf("不应有工具调用: %+v", comp.Calls)
	}
	if comp.Usage.InputTokens != 100 || comp.Usage.OutputTokens != 42 {
		t.Errorf("usage 解析错误: %+v", comp.Usage)
	}
	b := *last
	if b["model"] != "deepseek-v4-flash" {
		t.Errorf("model 错误: %v", b["model"])
	}
	if b["max_output_tokens"] != float64(2048) {
		t.Errorf("max_output_tokens 错误: %v", b["max_output_tokens"])
	}
	if b["store"] != false {
		t.Errorf("store 应为 false: %v", b["store"])
	}
	if _, has := b["tools"]; has {
		t.Error("未传工具时不应带 tools 字段")
	}
	inst, _ := b["instructions"].(string)
	if !strings.Contains(inst, "civgo 游戏社区") {
		t.Errorf("instructions 应为系统提示: %v", inst)
	}
	in, _ := b["input"].([]any)
	if len(in) != 2 {
		t.Fatalf("input 应为 2 条消息，got %d", len(in))
	}
	sysMsg, _ := in[0].(map[string]any)
	userMsg, _ := in[1].(map[string]any)
	if sysMsg["role"] != "system" || !strings.Contains(sysMsg["content"].(string), "archer.md") {
		t.Errorf("input[0] 应为带文档的 system 消息: %v", sysMsg)
	}
	if userMsg["role"] != "user" || userMsg["content"] != "弓手射程多少？" {
		t.Errorf("input[1] 应为 user 问题: %v", userMsg)
	}
}

func TestCompleteNoSystemDoc(t *testing.T) {
	srv, last := fakeResponses(t, "你好")
	defer srv.Close()
	cfg := testAIConfig()
	cfg.BaseURL = srv.URL
	client := NewChatClient(cfg)
	comp, err := client.Complete(context.Background(), systemPrompt,
		[]InputItem{{"role": "user", "content": "在吗"}}, nil)
	if err != nil {
		t.Fatalf("Complete 失败: %v", err)
	}
	_ = comp
	in, _ := (*last)["input"].([]any)
	if len(in) != 1 {
		t.Fatalf("无文档时 input 应只有 user 消息，got %d", len(in))
	}
}

// TestCompleteWithTools 带工具请求：tools/tool_choice 字段、工具调用解析（call_id 兼容）。
func TestCompleteWithTools(t *testing.T) {
	var last map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&last)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"output": []map[string]any{
				{"type": "function_call", "call_id": "fc_abc", "name": "read_doc",
					"arguments": `{"path":"archer.md","start_line":1}`},
				{"type": "function_call", "id": "fc_def", "name": "list_docs", "arguments": "{}"},
			},
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 20},
		})
	}))
	defer srv.Close()
	cfg := testAIConfig()
	cfg.BaseURL = srv.URL
	client := NewChatClient(cfg)

	tools := []Tool{{Name: "list_docs", Description: "列出文档", Parameters: map[string]any{"type": "object"}}}
	comp, err := client.Complete(context.Background(), systemPrompt,
		[]InputItem{{"role": "user", "content": "弓手"}}, tools)
	if err != nil {
		t.Fatalf("Complete 失败: %v", err)
	}
	if comp.Text != "" {
		t.Errorf("工具响应不应有文本: %q", comp.Text)
	}
	if len(comp.Calls) != 2 {
		t.Fatalf("应有 2 个工具调用，got %+v", comp.Calls)
	}
	if comp.Calls[0].CallID != "fc_abc" || comp.Calls[0].Name != "read_doc" ||
		!strings.Contains(comp.Calls[0].Arguments, "archer.md") {
		t.Errorf("第 1 个调用解析错误: %+v", comp.Calls[0])
	}
	if comp.Calls[1].CallID != "fc_def" {
		t.Errorf("id 字段应兼容为 call_id: %+v", comp.Calls[1])
	}
	if comp.Usage.InputTokens != 10 || comp.Usage.OutputTokens != 20 {
		t.Errorf("usage 错误: %+v", comp.Usage)
	}
	// 请求体：tools 与 tool_choice
	ts, _ := last["tools"].([]any)
	if len(ts) != 1 {
		t.Fatalf("tools 应为 1 个，got %d", len(ts))
	}
	tool0, _ := ts[0].(map[string]any)
	if tool0["name"] != "list_docs" || tool0["type"] != "function" {
		t.Errorf("tool 定义错误: %v", tool0)
	}
	if last["tool_choice"] != "auto" {
		t.Errorf("tool_choice 应为 auto: %v", last["tool_choice"])
	}
}

// TestCompleteMixedTextAndCalls 同一响应含文本与工具调用（少见但按协议兼容）。
func TestCompleteMixedTextAndCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"output": []map[string]any{
				{"type": "message", "content": []map[string]any{
					{"type": "output_text", "text": "先查一下"},
				}},
				{"type": "function_call", "call_id": "fc_1", "name": "list_docs", "arguments": "{}"},
			},
		})
	}))
	defer srv.Close()
	cfg := testAIConfig()
	cfg.BaseURL = srv.URL
	client := NewChatClient(cfg)
	comp, err := client.Complete(context.Background(), systemPrompt,
		[]InputItem{{"role": "user", "content": "q"}}, []Tool{{Name: "list_docs"}})
	if err != nil {
		t.Fatalf("Complete 失败: %v", err)
	}
	if comp.Text != "先查一下" || len(comp.Calls) != 1 {
		t.Errorf("文本与调用应同时保留: %q %+v", comp.Text, comp.Calls)
	}
}

func TestCompleteMultiOutputText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"output": []map[string]any{
				{"type": "message", "content": []map[string]any{
					{"type": "output_text", "text": "第一段"},
					{"type": "output_text", "text": "第二段"},
				}},
			},
		})
	}))
	defer srv.Close()
	cfg := testAIConfig()
	cfg.BaseURL = srv.URL
	client := NewChatClient(cfg)
	comp, err := client.Complete(context.Background(), systemPrompt,
		[]InputItem{{"role": "user", "content": "q"}}, nil)
	if err != nil {
		t.Fatalf("Complete 失败: %v", err)
	}
	if comp.Text != "第一段\n第二段" {
		t.Errorf("多段应拼接: %q", comp.Text)
	}
}

func TestCompleteEmptyOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"output": []map[string]any{}})
	}))
	defer srv.Close()
	cfg := testAIConfig()
	cfg.BaseURL = srv.URL
	client := NewChatClient(cfg)
	if _, err := client.Complete(context.Background(), systemPrompt,
		[]InputItem{{"role": "user", "content": "q"}}, nil); err == nil {
		t.Fatal("空输出应报错")
	}
}

func TestCompleteError4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		fmt.Fprint(w, `{"error":{"message":"model not found"}}`)
	}))
	defer srv.Close()
	cfg := testAIConfig()
	cfg.BaseURL = srv.URL
	client := NewChatClient(cfg)
	_, err := client.Complete(context.Background(), systemPrompt,
		[]InputItem{{"role": "user", "content": "q"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("4xx 应报错且含状态码: %v", err)
	}
}

func TestCompleteTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer srv.Close()
	cfg := testAIConfig()
	cfg.BaseURL = srv.URL
	cfg.ChatTimeoutSec = 1
	client := NewChatClient(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := client.Complete(ctx, systemPrompt,
		[]InputItem{{"role": "user", "content": "q"}}, nil); err == nil {
		t.Fatal("超时应报错")
	}
}

func TestCompleteMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"output":`)
	}))
	defer srv.Close()
	cfg := testAIConfig()
	cfg.BaseURL = srv.URL
	client := NewChatClient(cfg)
	if _, err := client.Complete(context.Background(), systemPrompt,
		[]InputItem{{"role": "user", "content": "q"}}, nil); err == nil {
		t.Fatal("畸形 JSON 应报错")
	}
}

// ---- SelfCheckTools（网关 function calling 能力验证） ----

func TestSelfCheckToolsOK(t *testing.T) {
	var last map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&last)
		w.Header().Set("Content-Type", "application/json")
		// 支持 tools 的网关可能直接返回 message 或 function_call，均视为支持
		json.NewEncoder(w).Encode(map[string]any{
			"output": []map[string]any{{"type": "message", "content": []map[string]any{
				{"type": "output_text", "text": "pong"},
			}}},
		})
	}))
	defer srv.Close()
	cfg := testAIConfig()
	cfg.BaseURL = srv.URL
	client := NewChatClient(cfg)
	ok, err := client.SelfCheckTools(context.Background())
	if err != nil || !ok {
		t.Fatalf("应判定支持: ok=%v err=%v", ok, err)
	}
	// 自检请求应带 ping_tool
	ts, _ := last["tools"].([]any)
	if len(ts) != 1 {
		t.Fatalf("自检应带 1 个工具，got %d", len(ts))
	}
	tool0, _ := ts[0].(map[string]any)
	if tool0["name"] != "ping_tool" {
		t.Errorf("自检工具名错误: %v", tool0["name"])
	}
}

func TestSelfCheckToolsUnsupported(t *testing.T) {
	// 网关明确拒绝 tools 参数（常见于不兼容网关的 400）
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprint(w, `{"error":{"message":"invalid parameter: tools"}}`)
	}))
	defer srv.Close()
	cfg := testAIConfig()
	cfg.BaseURL = srv.URL
	client := NewChatClient(cfg)
	ok, err := client.SelfCheckTools(context.Background())
	if ok || err == nil {
		t.Fatalf("4xx 应判定不支持: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("错误应含状态码: %v", err)
	}
}

func TestSelfCheckToolsUncertain(t *testing.T) {
	// 5xx：暂不确定（ok=true + err）
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
		fmt.Fprint(w, `bad gateway`)
	}))
	defer srv.Close()
	cfg := testAIConfig()
	cfg.BaseURL = srv.URL
	client := NewChatClient(cfg)
	ok, err := client.SelfCheckTools(context.Background())
	if !ok || err == nil {
		t.Fatalf("5xx 应判定不确定（ok=true）: ok=%v err=%v", ok, err)
	}
}
