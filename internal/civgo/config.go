// Package civgo 实现 civgo 社区服务：docs/game_content 文档自动同步 +
// 群聊 AI 问答（@机器人提问 → 分块向量检索 → 第三方 AI 回答）。
//
// 设计见 docs/CIVGO_DESIGN.md。本包不依赖 state/bot/admin（独立配置
// data/civgo/civgo.json），通过 onebot.Manager 的独立 message handler
// 接入，对既有模块零侵入。
package civgo

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// ErrNotConfigured 表示 civgo 未配置（配置缺失/缺少 api_key/缺少 git 等），
// 模块应整体禁用（不注册 handler、不同步），不影响主流程。
var ErrNotConfigured = errors.New("civgo 未配置（缺失配置/缺少 api_key/缺少 git），模块禁用")

// 校验边界常量（与 Validate 共用语义）。
const (
	minSyncIntervalSec = 30
	maxSyncIntervalSec = 3600
	minTopK            = 1
	maxTopK            = 20
	minChunkSize       = 200
	maxChunkSize       = 2000
	minChatTimeoutSec  = 10
	maxChatTimeoutSec  = 600
	minEmbedTimeoutSec = 5
	maxEmbedTimeoutSec = 120
	minMaxOutputTokens = 100
	maxMaxOutputTokens = 8192
	minContextChars    = 1000
	maxContextChars    = 100000
	// DefaultMaxReplyLen 单条群消息安全长度上限（QQ 群消息 + CQ at 码余量）。
	DefaultMaxReplyLen = 3500
	// DefaultMaxQuestionLen 单次提问最大字符数（超出截断）。
	DefaultMaxQuestionLen = 300
)

// Config civgo 服务配置（data/civgo/civgo.json）。
type Config struct {
	Enabled   bool            `json:"enabled"`
	Repo      RepoConfig      `json:"repo"`
	AI        AIConfig        `json:"ai"`
	Retrieval RetrievalConfig `json:"retrieval"`
	Groups    []int64         `json:"groups"`
	RateLimit RateLimitConfig `json:"rate_limit"`
}

// RepoConfig 文档仓库配置。
type RepoConfig struct {
	URL             string `json:"url"`
	Branch          string `json:"branch"` // 空 = 探测 origin/HEAD
	DocsPath        string `json:"docs_path"`
	SyncIntervalSec int    `json:"sync_interval_sec"`
	CloneShallow    bool   `json:"clone_shallow"`
	SparseCheckout  bool   `json:"sparse_checkout"`
}

// AIConfig 第三方 AI 网关配置（OpenAI 兼容）。
type AIConfig struct {
	BaseURL         string `json:"base_url"` // 含 /v1，请求拼 /responses、/embeddings
	APIKey          string `json:"api_key"`
	ChatModel       string `json:"chat_model"`
	EmbeddingModel  string `json:"embedding_model"`
	ChatTimeoutSec  int    `json:"chat_timeout_sec"`
	EmbedTimeoutSec int    `json:"embed_timeout_sec"`
	MaxOutputTokens int    `json:"max_output_tokens"`
}

// RetrievalConfig 检索配置。
type RetrievalConfig struct {
	Mode            string  `json:"mode"` // vector（默认）| keyword
	TopK            int     `json:"top_k"`
	ChunkSize       int     `json:"chunk_size"`
	ChunkOverlap    int     `json:"chunk_overlap"` // 预留（首版按行整行切块）
	MinScore        float64 `json:"min_score"`
	MaxContextChars int     `json:"max_context_chars"`
}

// RateLimitConfig 问答限流配置。
type RateLimitConfig struct {
	PerUserMin      int `json:"per_user_min"`
	PerGroupMin     int `json:"per_group_min"`
	MaxConcurrentAI int `json:"max_concurrent_ai"`
}

// DefaultConfig 返回默认配置（模板与兜底值）。
func DefaultConfig() *Config {
	return &Config{
		Enabled: true,
		Repo: RepoConfig{
			URL:             "https://github.com/AutumnPizazz/civgo.git",
			Branch:          "",
			DocsPath:        "docs/game_content",
			SyncIntervalSec: 300,
			CloneShallow:    true,
			SparseCheckout:  true,
		},
		AI: AIConfig{
			BaseURL:         "https://ai.realseek.wiki/v1",
			APIKey:          "",
			ChatModel:       "deepseek-v4-flash",
			EmbeddingModel:  "bge-m3",
			ChatTimeoutSec:  90,
			EmbedTimeoutSec: 30,
			MaxOutputTokens: 2048,
		},
		Retrieval: RetrievalConfig{
			Mode:            "vector",
			TopK:            6,
			ChunkSize:       800,
			ChunkOverlap:    100,
			MinScore:        0.25,
			MaxContextChars: 12000,
		},
		Groups: []int64{},
		RateLimit: RateLimitConfig{
			PerUserMin:      3,
			PerGroupMin:     10,
			MaxConcurrentAI: 2,
		},
	}
}

// Load 读取配置。文件不存在时生成默认模板（含说明字段）并返回 ErrNotConfigured；
// 解析或校验失败返回错误；api_key 为空返回 ErrNotConfigured。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		cfg := DefaultConfig()
		writeTemplate(path, cfg)
		slog.Warn("civgo 配置文件不存在，已生成默认模板，请填写 ai.api_key 后重启",
			"path", path)
		return nil, ErrNotConfigured
	}
	if err != nil {
		return nil, fmt.Errorf("读取 civgo 配置失败: %w", err)
	}
	cfg := DefaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析 civgo 配置失败: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// writeTemplate 写出默认模板（含 _template_note 说明字段，忽略解析）。
func writeTemplate(path string, cfg *Config) {
	tpl := struct {
		Config
		TemplateNote string `json:"_template_note"`
	}{Config: *cfg, TemplateNote: "civgo 社区服务配置。必填 ai.api_key（在 groups 中填入要启用的群号）；填好后重启生效。"}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		slog.Error("创建 civgo 配置目录失败", "err", err)
		return
	}
	data, err := json.MarshalIndent(tpl, "", "  ")
	if err != nil {
		slog.Error("序列化 civgo 配置模板失败", "err", err)
		return
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		slog.Error("写入 civgo 配置模板失败", "err", err)
	}
}

// Validate 校验配置。结构性错误返回普通 error；api_key 缺失返回 ErrNotConfigured
// （模块禁用，与缺失配置同语义）。
func (c *Config) Validate() error {
	if c.AI.APIKey == "" {
		return ErrNotConfigured
	}
	if c.Repo.URL == "" {
		return errors.New("repo.url 不能为空")
	}
	if strings.HasPrefix(c.Repo.DocsPath, "/") {
		return errors.New("repo.docs_path 必须是相对路径（不能以 / 开头）")
	}
	if c.Repo.DocsPath == "" {
		return errors.New("repo.docs_path 不能为空")
	}
	if c.Repo.SyncIntervalSec < minSyncIntervalSec || c.Repo.SyncIntervalSec > maxSyncIntervalSec {
		return fmt.Errorf("repo.sync_interval_sec 必须在 %d~%d 之间", minSyncIntervalSec, maxSyncIntervalSec)
	}
	if !strings.HasPrefix(c.AI.BaseURL, "http://") && !strings.HasPrefix(c.AI.BaseURL, "https://") {
		return errors.New("ai.base_url 必须以 http:// 或 https:// 开头")
	}
	if c.AI.ChatModel == "" {
		return errors.New("ai.chat_model 不能为空")
	}
	if c.AI.EmbeddingModel == "" {
		return errors.New("ai.embedding_model 不能为空")
	}
	if c.AI.ChatTimeoutSec < minChatTimeoutSec || c.AI.ChatTimeoutSec > maxChatTimeoutSec {
		return fmt.Errorf("ai.chat_timeout_sec 必须在 %d~%d 之间", minChatTimeoutSec, maxChatTimeoutSec)
	}
	if c.AI.EmbedTimeoutSec < minEmbedTimeoutSec || c.AI.EmbedTimeoutSec > maxEmbedTimeoutSec {
		return fmt.Errorf("ai.embed_timeout_sec 必须在 %d~%d 之间", minEmbedTimeoutSec, maxEmbedTimeoutSec)
	}
	if c.AI.MaxOutputTokens < minMaxOutputTokens || c.AI.MaxOutputTokens > maxMaxOutputTokens {
		return fmt.Errorf("ai.max_output_tokens 必须在 %d~%d 之间", minMaxOutputTokens, maxMaxOutputTokens)
	}
	if c.Retrieval.Mode != "vector" && c.Retrieval.Mode != "keyword" {
		return errors.New("retrieval.mode 只能是 vector 或 keyword")
	}
	if c.Retrieval.TopK < minTopK || c.Retrieval.TopK > maxTopK {
		return fmt.Errorf("retrieval.top_k 必须在 %d~%d 之间", minTopK, maxTopK)
	}
	if c.Retrieval.ChunkSize < minChunkSize || c.Retrieval.ChunkSize > maxChunkSize {
		return fmt.Errorf("retrieval.chunk_size 必须在 %d~%d 之间", minChunkSize, maxChunkSize)
	}
	if c.Retrieval.ChunkOverlap >= c.Retrieval.ChunkSize {
		return errors.New("retrieval.chunk_overlap 必须小于 chunk_size")
	}
	if c.Retrieval.MinScore < 0 || c.Retrieval.MinScore > 1 {
		return errors.New("retrieval.min_score 必须在 0~1 之间")
	}
	if c.Retrieval.MaxContextChars < minContextChars || c.Retrieval.MaxContextChars > maxContextChars {
		return fmt.Errorf("retrieval.max_context_chars 必须在 %d~%d 之间", minContextChars, maxContextChars)
	}
	if c.RateLimit.PerUserMin < 1 || c.RateLimit.PerGroupMin < 1 || c.RateLimit.MaxConcurrentAI < 1 {
		return errors.New("rate_limit 各项必须 ≥ 1")
	}
	if c.RateLimit.PerUserMin > c.RateLimit.PerGroupMin {
		return errors.New("rate_limit.per_user_min 不能大于 per_group_min")
	}
	return nil
}

// Store 持有配置的原子快照，支持按 mtime 热重载（失败保留旧配置）。
type Store struct {
	cfgPtr atomic.Pointer[Config]
	path   string
	mtime  time.Time
}

// NewStore 首次加载配置并记录文件 mtime。
func NewStore(path string) (*Store, error) {
	cfg, err := Load(path)
	if err != nil {
		return nil, err
	}
	s := &Store{path: path}
	s.cfgPtr.Store(cfg)
	if st, err := os.Stat(path); err == nil {
		s.mtime = st.ModTime()
	}
	return s, nil
}

// Get 返回当前生效配置（原子读，调用方只读勿改）。
func (s *Store) Get() *Config { return s.cfgPtr.Load() }

// ReloadIfChanged 检查配置文件 mtime，变化则重新加载并原子替换。
// 加载/校验失败保留旧配置并告警（fail-safe）。
func (s *Store) ReloadIfChanged() {
	st, err := os.Stat(s.path)
	if err != nil {
		return // 文件被临时移走：保留旧配置
	}
	if !st.ModTime().After(s.mtime) {
		return
	}
	cfg, err := Load(s.path)
	if err != nil {
		slog.Warn("civgo 配置重载失败（保留旧配置）", "err", err)
		return
	}
	s.cfgPtr.Store(cfg)
	s.mtime = st.ModTime()
	slog.Info("civgo 配置已热重载")
}
