package civgo

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	c := DefaultConfig()
	if !c.Enabled {
		t.Error("enabled 默认应为 true")
	}
	if c.Repo.URL != "https://github.com/AutumnPizazz/civgo.git" {
		t.Errorf("repo.url 默认值错误: %s", c.Repo.URL)
	}
	if c.Repo.DocsPath != "docs/game_content" {
		t.Errorf("docs_path 默认值错误: %s", c.Repo.DocsPath)
	}
	if c.AI.BaseURL != "https://ai.realseek.wiki/v1" {
		t.Errorf("base_url 默认值错误: %s", c.AI.BaseURL)
	}
	if c.AI.ChatModel != "deepseek-v4-flash" {
		t.Errorf("chat_model 默认值错误: %s", c.AI.ChatModel)
	}
	if c.Agent.MaxToolCalls != 8 || c.Agent.MaxContextChars != 12000 {
		t.Errorf("agent 默认值错误: %+v", c.Agent)
	}
	if !c.History.Enabled || c.History.MaxEntriesPerGroup != 50 {
		t.Errorf("history 默认值错误: %+v", c.History)
	}
	if !c.UsageAlert.Enabled || c.UsageAlert.WindowMinutes != 5 || c.UsageAlert.ThresholdTokens != 500000 {
		t.Errorf("usage_alert 默认值错误: %+v", c.UsageAlert)
	}
}

func TestLoadMissingCreatesTemplate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "civgo.json")
	cfg, err := Load(path)
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("缺失配置应返回 ErrNotConfigured，got %v", err)
	}
	if cfg != nil {
		t.Fatal("缺失配置时不应返回配置")
	}
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatalf("应生成模板文件: %v", rerr)
	}
	if len(data) == 0 {
		t.Fatal("模板文件不应为空")
	}
	// 模板应可被解析（含 _template_note 忽略字段）
	parsed, err := Load(path)
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("模板（api_key 空）应返回 ErrNotConfigured，got %v", err)
	}
	_ = parsed
}

func TestLoadValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "civgo.json")
	cfg := DefaultConfig()
	cfg.AI.APIKey = "sk-test"
	writeTestConfig(t, path, cfg)
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if got.AI.APIKey != "sk-test" || got.Repo.DocsPath != "docs/game_content" {
		t.Errorf("Load 解析错误: %+v", got)
	}
}

func TestLoadMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "civgo.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("坏 JSON 应报错")
	}
}

func TestValidateErrors(t *testing.T) {
	base := func() *Config {
		c := DefaultConfig()
		c.AI.APIKey = "sk-test"
		return c
	}
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string // 错误信息子串
	}{
		{"api_key 空", func(c *Config) { c.AI.APIKey = "" }, "未配置"},
		{"repo.url 空", func(c *Config) { c.Repo.URL = "" }, "repo.url"},
		{"docs_path 绝对路径", func(c *Config) { c.Repo.DocsPath = "/etc" }, "相对路径"},
		{"sync 间隔太小", func(c *Config) { c.Repo.SyncIntervalSec = 5 }, "sync_interval_sec"},
		{"sync 间隔太大", func(c *Config) { c.Repo.SyncIntervalSec = 7200 }, "sync_interval_sec"},
		{"base_url 无协议", func(c *Config) { c.AI.BaseURL = "ai.realseek.wiki/v1" }, "http"},
		{"chat_model 空", func(c *Config) { c.AI.ChatModel = "" }, "chat_model"},
		{"per_user > per_group", func(c *Config) { c.RateLimit.PerUserMin = 20 }, "per_user_min"},
		{"并发为 0", func(c *Config) { c.RateLimit.MaxConcurrentAI = 0 }, "rate_limit"},
		{"max_tool_calls 越界", func(c *Config) { c.Agent.MaxToolCalls = 0 }, "max_tool_calls"},
		{"max_context_chars 越界", func(c *Config) { c.Agent.MaxContextChars = 100 }, "max_context_chars"},
		{"read_page_lines 越界", func(c *Config) { c.Agent.ReadPageLines = 5 }, "read_page_lines"},
		{"read_page_max_chars 越界", func(c *Config) { c.Agent.ReadPageMaxChars = 100 }, "read_page_max_chars"},
		{"history 容量越界", func(c *Config) { c.History.MaxEntriesPerGroup = 500 }, "max_entries_per_group"},
		{"recall 条数越界", func(c *Config) { c.History.MaxRecallEntries = 0 }, "max_recall_entries"},
		{"窗口分钟越界", func(c *Config) { c.UsageAlert.WindowMinutes = 0 }, "window_minutes"},
		{"阈值过小", func(c *Config) { c.UsageAlert.ThresholdTokens = 10 }, "threshold_tokens"},
		{"冷却越界", func(c *Config) { c.UsageAlert.CooldownMinutes = 0 }, "cooldown_minutes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base()
			tc.mutate(c)
			err := c.Validate()
			if err == nil {
				t.Fatalf("应报错: %s", tc.name)
			}
			if !errors.Is(err, ErrNotConfigured) && tc.want == "未配置" {
				t.Fatalf("应为 ErrNotConfigured，got %v", err)
			}
			if !contains(err.Error(), tc.want) {
				t.Errorf("错误信息应包含 %q，got %v", tc.want, err)
			}
		})
	}
}

func TestValidateOK(t *testing.T) {
	c := DefaultConfig()
	c.AI.APIKey = "sk-test"
	if err := c.Validate(); err != nil {
		t.Fatalf("合法配置不应报错: %v", err)
	}
}

func TestStoreReloadOnChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "civgo.json")
	cfg := DefaultConfig()
	cfg.AI.APIKey = "sk-test"
	writeTestConfig(t, path, cfg)
	st, _ := os.Stat(path)
	mtime := st.ModTime()

	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore 失败: %v", err)
	}
	if store.Get().AI.APIKey != "sk-test" {
		t.Fatal("初始配置加载错误")
	}

	// 未变化：不重载
	store.ReloadIfChanged()
	if store.Get().AI.ChatModel != "deepseek-v4-flash" {
		t.Fatal("未变化不应重载")
	}

	// 变化：新值生效
	cfg2 := DefaultConfig()
	cfg2.AI.APIKey = "sk-test"
	cfg2.AI.ChatModel = "deepseek-v4-max"
	writeTestConfig(t, path, cfg2)
	future := mtime.Add(2 * time.Second)
	_ = os.Chtimes(path, future, future)
	store.ReloadIfChanged()
	if store.Get().AI.ChatModel != "deepseek-v4-max" {
		t.Fatal("mtime 变化后应重载")
	}

	// 坏 JSON：保留旧配置
	if err := os.WriteFile(path, []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	future2 := future.Add(2 * time.Second)
	_ = os.Chtimes(path, future2, future2)
	store.ReloadIfChanged()
	if store.Get().AI.ChatModel != "deepseek-v4-max" {
		t.Fatal("坏配置应保留旧值")
	}
}

func writeTestConfig(t *testing.T, path string, cfg *Config) {
	t.Helper()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
