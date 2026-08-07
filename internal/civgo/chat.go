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
const systemPrompt = `你是 civgo 游戏社区的游戏问答助手。你的知识来源是社区提供的游戏文档。
回答规则：
1. 只依据提供给你的文档片段回答问题；文档中没有的信息，明确说"文档中未找到"，不要编造。
2. 引用文档时在句末标注来源文件名（如 (archer.md)）。
3. 回答使用简体中文，简洁、直接、面向 QQ 群聊场景；控制在 500 字内。
4. 与游戏无关的问题（闲聊、编程、其他游戏等），礼貌说明"我是 civgo 游戏助手，只回答游戏内容相关的问题"。`

// Usage 一次对话的 token 用量。
type Usage struct {
	InputTokens  int
	OutputTokens int
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
	Type    string `json:"type"`
	Content []struct {
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

// Complete 调用 /v1/responses 完成一次对话。
//   - instructions：固定系统提示（角色/规则，见 systemPrompt）；
//   - systemDoc：本次的检索文档上下文（input 中的 system 消息）；
//   - userText：群友问题。
//
// 返回助手最终文本（拼接全部 output_text）与用量。
func (c *ChatClient) Complete(ctx context.Context, instructions, systemDoc, userText string) (string, Usage, error) {
	input := []map[string]string{}
	if systemDoc != "" {
		input = append(input, map[string]string{"role": "system", "content": systemDoc})
	}
	input = append(input, map[string]string{"role": "user", "content": userText})

	payload := map[string]any{
		"model":             c.model,
		"instructions":      instructions,
		"input":             input,
		"max_output_tokens": c.maxTokens,
		"store":             false,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", Usage{}, err
	}

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return "", Usage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", Usage{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", Usage{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return "", Usage{}, fmt.Errorf("responses HTTP %d: %s", resp.StatusCode, truncateStr(string(raw), 500))
	}
	var rb responseBody
	if err := json.Unmarshal(raw, &rb); err != nil {
		return "", Usage{}, fmt.Errorf("responses 响应解析失败: %w", err)
	}

	// 仅取 type=="message" 的条目中 type=="output_text" 的内容（跳过 reasoning 等）
	var sb strings.Builder
	for _, item := range rb.Output {
		if item.Type != "message" {
			continue
		}
		for _, c := range item.Content {
			if c.Type == "output_text" && c.Text != "" {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(c.Text)
			}
		}
	}
	if sb.Len() == 0 {
		return "", Usage{}, fmt.Errorf("responses 输出为空（id=%s）", truncateStr(rb.ID, 24))
	}
	usage := Usage{InputTokens: rb.Usage.InputTokens, OutputTokens: rb.Usage.OutputTokens}
	slog.Debug("对话完成", "model", c.model, "in_tok", usage.InputTokens,
		"out_tok", usage.OutputTokens, "ms", time.Since(start).Milliseconds())
	return sb.String(), usage, nil
}
