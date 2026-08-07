package civgo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func testAIConfig() AIConfig {
	return AIConfig{
		BaseURL:         "https://ai.realseek.wiki/v1",
		APIKey:          "sk-test",
		ChatModel:       "deepseek-v4-flash",
		EmbeddingModel:  "bge-m3",
		ChatTimeoutSec:  30,
		EmbedTimeoutSec: 10,
		MaxOutputTokens: 2048,
	}
}

// fakeEmbeds 构造一个返回固定向量的嵌入服务器，并记录收到的请求。
func fakeEmbeds(t *testing.T, dim int) (*httptest.Server, *[]map[string]any, *atomic.Int64) {
	t.Helper()
	var reqs []map[string]any
	var count atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("解码请求失败: %v", err)
			w.WriteHeader(400)
			return
		}
		reqs = append(reqs, body)
		if r.URL.Path != "/embeddings" {
			t.Errorf("路径应为 /embeddings，got %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		if body["model"] != "bge-m3" {
			t.Errorf("model 应为 bge-m3，got %v", body["model"])
		}
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("Authorization 头错误: %s", r.Header.Get("Authorization"))
		}
		input, _ := body["input"].([]any)
		data := make([]map[string]any, len(input))
		batchNo := count.Load() // 当前批次号（区分批内相同 index）
		for i := range input {
			vec := make([]float64, dim)
			for j := range vec {
				vec[j] = float64(batchNo*1000 + int64(i) + 1)
			}
			data[i] = map[string]any{"index": i, "embedding": vec}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": data, "model": "bge-m3"})
	}))
	return srv, &reqs, &count
}

func TestEmbedTextsBatch(t *testing.T) {
	srv, reqs, count := fakeEmbeds(t, 4)
	defer srv.Close()
	cfg := testAIConfig()
	cfg.BaseURL = srv.URL
	client := NewEmbedClient(cfg)

	texts := make([]string, 70) // 70 条 → 3 批（32/32/6）
	for i := range texts {
		texts[i] = fmt.Sprintf("文本%d", i)
	}
	vecs, err := client.EmbedTexts(context.Background(), texts)
	if err != nil {
		t.Fatalf("EmbedTexts 失败: %v", err)
	}
	if count.Load() != 3 {
		t.Errorf("应发 3 批请求，got %d", count.Load())
	}
	if len(vecs) != 70 {
		t.Fatalf("向量条数错误: %d", len(vecs))
	}
	// 归位正确性：第 i 条向量应编码 (批次+1)*1000 + 批内序号 + 1（批号从 1 起）
	for i, v := range vecs {
		batch, idx := i/32, i%32
		if len(v) != 4 || v[0] != float32((batch+1)*1000+idx+1) {
			t.Fatalf("第 %d 条向量错误: %v", i, v)
		}
	}
	// 首批输入应恰好 32 条
	if len((*reqs)[0]["input"].([]any)) != 32 {
		t.Errorf("首批应 32 条，got %d", len((*reqs)[0]["input"].([]any)))
	}
}

func TestEmbedTextsEmpty(t *testing.T) {
	srv, _, _ := fakeEmbeds(t, 4)
	defer srv.Close()
	cfg := testAIConfig()
	cfg.BaseURL = srv.URL
	client := NewEmbedClient(cfg)
	vecs, err := client.EmbedTexts(context.Background(), nil)
	if err != nil || vecs != nil {
		t.Fatalf("空输入应返回 nil,nil，got %v %v", vecs, err)
	}
}

func TestEmbedError4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":{"message":"invalid api key"}}`)
	}))
	defer srv.Close()
	cfg := testAIConfig()
	cfg.BaseURL = srv.URL
	client := NewEmbedClient(cfg)
	_, err := client.EmbedTexts(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("4xx 应报错")
	}
	if len(err.Error()) < 10 {
		t.Errorf("错误应含响应体信息: %v", err)
	}
}

func TestEmbedMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data": [`)
	}))
	defer srv.Close()
	cfg := testAIConfig()
	cfg.BaseURL = srv.URL
	client := NewEmbedClient(cfg)
	if _, err := client.EmbedTexts(context.Background(), []string{"x"}); err == nil {
		t.Fatal("畸形 JSON 应报错")
	}
}

func TestEmbedWrongCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{}}) // 空
	}))
	defer srv.Close()
	cfg := testAIConfig()
	cfg.BaseURL = srv.URL
	client := NewEmbedClient(cfg)
	if _, err := client.EmbedTexts(context.Background(), []string{"x", "y"}); err == nil {
		t.Fatal("条数不符应报错")
	}
}

func TestEmbedTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer srv.Close()
	cfg := testAIConfig()
	cfg.BaseURL = srv.URL
	cfg.EmbedTimeoutSec = 1
	client := NewEmbedClient(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := client.EmbedTexts(ctx, []string{"x"}); err == nil {
		t.Fatal("超时应报错")
	}
}

func TestSelfCheck(t *testing.T) {
	srv, _, _ := fakeEmbeds(t, 8)
	defer srv.Close()
	cfg := testAIConfig()
	cfg.BaseURL = srv.URL
	client := NewEmbedClient(cfg)
	if err := client.SelfCheck(context.Background()); err != nil {
		t.Fatalf("自检应通过: %v", err)
	}
}

func TestSelfCheckFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	cfg := testAIConfig()
	cfg.BaseURL = srv.URL
	client := NewEmbedClient(cfg)
	if err := client.SelfCheck(context.Background()); err == nil {
		t.Fatal("自检应失败")
	}
}
