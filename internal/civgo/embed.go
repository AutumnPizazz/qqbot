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

// embedBatchSize 单次嵌入请求的文本条数上限（网关常见限制，分批避免超限）。
const embedBatchSize = 32

// EmbedClient OpenAI 兼容 embeddings 客户端（POST {base}/embeddings）。
type EmbedClient struct {
	baseURL string // 含 /v1
	apiKey  string
	model   string
	http    *http.Client
	batch   int
}

// NewEmbedClient 创建嵌入客户端（不发起网络请求）。
func NewEmbedClient(cfg AIConfig) *EmbedClient {
	return &EmbedClient{
		baseURL: strings.TrimSuffix(cfg.BaseURL, "/"),
		apiKey:  cfg.APIKey,
		model:   cfg.EmbeddingModel,
		http:    &http.Client{Timeout: time.Duration(cfg.EmbedTimeoutSec) * time.Second},
		batch:   embedBatchSize,
	}
}

// EmbedTexts 批量嵌入文本，返回与输入一一对应的向量（按响应 index 归位）。
// 空输入返回空切片。任一文本嵌入失败整体失败（调用方决定重试/降级）。
func (c *EmbedClient) EmbedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	out := make([][]float32, len(texts))
	for start := 0; start < len(texts); start += c.batch {
		end := start + c.batch
		if end > len(texts) {
			end = len(texts)
		}
		batch := texts[start:end]
		vecs, err := c.embedOnce(ctx, batch)
		if err != nil {
			return nil, fmt.Errorf("嵌入第 %d~%d 条失败: %w", start, end-1, err)
		}
		copy(out[start:end], vecs)
	}
	return out, nil
}

// embedOnce 单次嵌入请求。
func (c *EmbedClient) embedOnce(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]any{
		"model": c.model,
		"input": texts,
	})
	if err != nil {
		return nil, err
	}
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embeddings HTTP %d: %s", resp.StatusCode, truncateStr(string(raw), 500))
	}
	var parsed struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("embeddings 响应解析失败: %w", err)
	}
	if len(parsed.Data) != len(texts) {
		return nil, fmt.Errorf("embeddings 返回条数不符: want %d got %d", len(texts), len(parsed.Data))
	}
	out := make([][]float32, len(texts))
	for _, d := range parsed.Data {
		if d.Index < 0 || d.Index >= len(out) {
			return nil, fmt.Errorf("embeddings 返回越界 index %d", d.Index)
		}
		vec := make([]float32, len(d.Embedding))
		for i, v := range d.Embedding {
			vec[i] = float32(v)
		}
		out[d.Index] = vec
	}
	slog.Debug("嵌入完成", "n", len(texts), "dim", len(out[0]), "ms", time.Since(start).Milliseconds())
	return out, nil
}

// SelfCheck 验证嵌入端点与模型可用（嵌入一条最小文本）。
// 失败返回带响应体摘要的错误，供上层决定降级。
func (c *EmbedClient) SelfCheck(ctx context.Context) error {
	vecs, err := c.EmbedTexts(ctx, []string{"ping"})
	if err != nil {
		return err
	}
	if len(vecs) != 1 || len(vecs[0]) == 0 {
		return fmt.Errorf("嵌入自检返回异常向量")
	}
	return nil
}

// truncateStr 按字节截断字符串用于日志/错误信息。
func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
