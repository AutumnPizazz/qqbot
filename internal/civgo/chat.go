package civgo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// systemPrompt 固定系统提示（走 Responses API 的 instructions 字段，
// 稳定不变；检索到的文档上下文走 input 中的 system 消息）。
// 定位是「游戏顾问」而非「文档检索者」：回答自然流畅，不出现文件名/引用痕迹。
const systemPrompt = `你是 civgo 游戏社区的游戏顾问，像一位熟悉游戏的老玩家一样为群友解答问题。
回答规则：
1. 回答基于你掌握的游戏资料，自然流畅地组织信息；不要提及"文档""资料""文件"等字眼，不要出现任何引用标注或文件名。
2. 资料中没有的信息，直接说"目前还没有这方面的信息"或"这个还没确定"，不要编造。
3. 使用简体中文，简洁、直接，面向 QQ 群聊场景；控制在 500 字内。
4. 与游戏无关的问题（闲聊、编程、其他游戏等），礼貌说明你是 civgo 游戏顾问，只回答游戏内容相关的问题。
5. 你可以使用工具自主查阅游戏资料：先了解有哪些文档，再阅读与问题相关的部分，最后汇总作答。不要编造资料中没有的内容。`

// Usage 一次对话的 token 用量。
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// Tool function tool 定义（Responses API tools 数组元素；Parameters 为 JSON Schema 对象）。
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

// InputItem 对话 input 数组条目：
//   - 消息：{"role": "user"|"system", "content": "..."}
//   - 工具调用（回填历史）：{"type": "function_call", "call_id", "name", "arguments"}
//   - 工具结果：{"type": "function_call_output", "call_id", "output"}
type InputItem map[string]any

// ToolCall 响应中的一次工具调用。
type ToolCall struct {
	CallID    string
	Name      string
	Arguments string // 原始 JSON 字符串（可能含坏 JSON，执行器容错）
}

// Completion 一次对话完成结果：文本（若直接回答）+ 工具调用列表 + 用量。
// 一次响应可能同时含多个 function_call（并发执行后按序回填）或零个（直接回答）。
type Completion struct {
	Text  string
	Calls []ToolCall
	Usage Usage
}

// ChatClient Responses API 客户端（POST {base}/responses）。
// 注意：deepseek-v4-flash 在网关挂在 responses 路由，不是 chat/completions。
type ChatClient struct {
	baseURL   string // 含 /v1
	apiKey    string
	model     string
	maxTokens int
	http      *http.Client
}

// NewChatClient 创建对话客户端（不发起网络请求）。
func NewChatClient(cfg AIConfig) *ChatClient {
	return &ChatClient{
		baseURL:   strings.TrimSuffix(cfg.BaseURL, "/"),
		apiKey:    cfg.APIKey,
		model:     cfg.ChatModel,
		maxTokens: cfg.MaxOutputTokens,
		http:      &http.Client{Timeout: time.Duration(cfg.ChatTimeoutSec) * time.Second},
	}
}

// responseItem 响应 output 数组中的一个条目（判别联合，只解析需要的字段）。
type responseItem struct {
	Type      string `json:"type"`
	ID        string `json:"id"`        // 部分网关用 id 而非 call_id
	CallID    string `json:"call_id"`   // 标准 Responses API 字段
	Name      string `json:"name"`      // function_call 的工具名
	Arguments string `json:"arguments"` // function_call 的参数 JSON 字符串
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// responseBody Responses API 响应（部分字段）。
type responseBody struct {
	ID     string         `json:"id"`
	Output []responseItem `json:"output"`
	Usage  struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// Complete 调用 /v1/responses 完成一次对话（支持工具调用）。
//   - instructions：固定系统提示（角色/规则，见 systemPrompt）；
//   - input：对话条目（system 文档上下文、用户问题、历史工具调用与结果）；
//   - tools：工具定义列表（空 = 不带工具，纯对话）。
//
// 返回文本（若有）+ 工具调用列表 + 用量。输出为空且无工具调用时报错。
func (c *ChatClient) Complete(ctx context.Context, instructions string, input []InputItem, tools []Tool) (Completion, error) {
	payload := map[string]any{
		"model":             c.model,
		"instructions":      instructions,
		"input":             input,
		"max_output_tokens": c.maxTokens,
		"store":             false,
	}
	if len(tools) > 0 {
		toolObjs := make([]map[string]any, len(tools))
		for i, t := range tools {
			toolObjs[i] = map[string]any{
				"type":        "function",
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.Parameters,
			}
		}
		payload["tools"] = toolObjs
		payload["tool_choice"] = "auto"
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Completion{}, err
	}

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return Completion{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return Completion{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return Completion{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Completion{}, fmt.Errorf("responses HTTP %d: %s", resp.StatusCode, truncateStr(string(raw), 500))
	}
	var rb responseBody
	if err := json.Unmarshal(raw, &rb); err != nil {
		return Completion{}, fmt.Errorf("responses 响应解析失败: %w", err)
	}

	completion := Completion{Usage: Usage{InputTokens: rb.Usage.InputTokens, OutputTokens: rb.Usage.OutputTokens}}
	// 仅取 type=="message" 的条目中 type=="output_text" 的内容（跳过 reasoning 等）；
	// function_call 条目收集为工具调用（call_id 兼容 id 字段）。
	for _, item := range rb.Output {
		switch item.Type {
		case "message":
			for _, c := range item.Content {
				if c.Type == "output_text" && c.Text != "" {
					if completion.Text != "" {
						completion.Text += "\n"
					}
					completion.Text += c.Text
				}
			}
		case "function_call":
			callID := item.CallID
			if callID == "" {
				callID = item.ID
			}
			completion.Calls = append(completion.Calls, ToolCall{
				CallID:    callID,
				Name:      item.Name,
				Arguments: item.Arguments,
			})
		}
	}
	if completion.Text == "" && len(completion.Calls) == 0 {
		return Completion{}, fmt.Errorf("responses 输出为空（id=%s）", truncateStr(rb.ID, 24))
	}
	slog.Debug("对话完成", "model", c.model, "calls", len(completion.Calls),
		"in_tok", completion.Usage.InputTokens, "out_tok", completion.Usage.OutputTokens,
		"ms", time.Since(start).Milliseconds())
	return completion, nil
}

// SelfCheckTools 验证网关 function calling 支持：发一个带最小 tools 的请求。
// 返回 (支持, err)：
//   - (true, nil)：网关支持（200，无论返回 function_call 还是普通 message）；
//   - (false, err)：网关明确不支持（4xx，err 含响应摘要）；
//   - (true, err)：暂不确定（网络错误/5xx/超时），err 供告警。
func (c *ChatClient) SelfCheckTools(ctx context.Context) (bool, error) {
	payload := map[string]any{
		"model": c.model,
		"input": []InputItem{{"role": "user", "content": "ping"}},
		"tools": []map[string]any{{
			"type":        "function",
			"name":        "ping_tool",
			"description": "自检工具",
			"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
		}},
		"tool_choice":      "auto",
		"store":            false,
		"max_output_tokens": 16,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return true, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return true, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return true, fmt.Errorf("自检网络失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return true, err
	}
	if resp.StatusCode == http.StatusOK {
		return true, nil
	}
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return false, fmt.Errorf("网关不支持 function calling（HTTP %d: %s）", resp.StatusCode, truncateStr(string(raw), 500))
	}
	return true, fmt.Errorf("自检服务端异常（HTTP %d: %s）", resp.StatusCode, truncateStr(string(raw), 300))
}
