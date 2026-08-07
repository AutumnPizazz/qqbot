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

	doc := "【来源: units/archer.md】\n弓手是远程单位。"
	answer, usage, err := client.Complete(context.Background(), systemPrompt, doc, "弓手射程多少？")
	if err != nil {
		t.Fatalf("Complete 失败: %v", err)
	}
	if answer != "弓手射程 2 格。" {
		t.Errorf("回答提取错误: %q", answer)
	}
	if usage.InputTokens != 100 || usage.OutputTokens != 42 {
		t.Errorf("usage 解析错误: %+v", usage)
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
	inst, _ := b["instructions"].(string)
	if !strings.Contains(inst, "civgo 游戏社区") {
		t.Errorf("instructions 应为系统提示: %v", inst)
	}
	input, _ := b["input"].([]any)
	if len(input) != 2 {
		t.Fatalf("input 应为 2 条消息，got %d", len(input))
	}
	sysMsg, _ := input[0].(map[string]any)
	userMsg, _ := input[1].(map[string]any)
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
	if _, _, err := client.Complete(context.Background(), systemPrompt, "", "在吗"); err != nil {
		t.Fatalf("Complete 失败: %v", err)
	}
	input, _ := (*last)["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("无文档时 input 应只有 user 消息，got %d", len(input))
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
	answer, _, err := client.Complete(context.Background(), systemPrompt, "", "q")
	if err != nil {
		t.Fatalf("Complete 失败: %v", err)
	}
	if answer != "第一段\n第二段" {
		t.Errorf("多段应拼接: %q", answer)
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
	if _, _, err := client.Complete(context.Background(), systemPrompt, "", "q"); err == nil {
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
	_, _, err := client.Complete(context.Background(), systemPrompt, "", "q")
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
	if _, _, err := client.Complete(ctx, systemPrompt, "", "q"); err == nil {
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
	if _, _, err := client.Complete(context.Background(), systemPrompt, "", "q"); err == nil {
		t.Fatal("畸形 JSON 应报错")
	}
}
