package civgo

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testAdmin 构造 Admin（临时 dataDir，无 Service）。
func testAdmin(t *testing.T) (*Admin, string) {
	t.Helper()
	dataDir := t.TempDir()
	return NewAdmin(dataDir, nil), dataDir
}

// adminConfigRaw 便捷构造提交体。
func adminConfigRaw(cfg map[string]any) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"config": cfg})
	return b
}

func TestAdminGetUnconfigured(t *testing.T) {
	a, _ := testAdmin(t)
	dto, ok := a.Get()
	if ok {
		t.Fatal("未配置时应返回 false")
	}
	if dto["enabled"] != false {
		t.Errorf("默认 enabled 应为 false: %v", dto["enabled"])
	}
	// api_key 脱敏
	ai, _ := dto["ai"].(map[string]any)
	if _, has := ai["api_key"].(string); has {
		t.Error("api_key 不应明文返回")
	}
}

func TestAdminSaveAndGet(t *testing.T) {
	a, dataDir := testAdmin(t)
	cfg := map[string]any{
		"enabled": true,
		"repo": map[string]any{
			"url": "https://github.com/x/civgo.git", "branch": "stable",
			"docs_path": "docs/game_content", "sync_interval_sec": 300,
			"clone_shallow": true, "sparse_checkout": true,
		},
		"ai": map[string]any{
			"base_url": "https://ai.realseek.wiki/v1", "api_key": "sk-123",
			"chat_model": "deepseek-v4-flash", "chat_timeout_sec": 90,
			"max_output_tokens": 2048,
		},
		"groups": []any{111, 222},
	}
	eff, err := a.Save(adminConfigRaw(cfg))
	if err != nil {
		t.Fatalf("Save 失败: %v", err)
	}
	if eff {
		t.Error("未启动时应返回 effective=false（需重启）")
	}
	// 文件已写盘且可 Load
	loaded, err := Load(filepath.Join(dataDir, "civgo", "civgo.json"))
	if err != nil {
		t.Fatalf("写盘后 Load 失败: %v", err)
	}
	if loaded.AI.APIKey != "sk-123" || loaded.Repo.Branch != "stable" {
		t.Errorf("写盘内容错误: %+v", loaded)
	}
	// Get 返回脱敏
	dto, ok := a.Get()
	if !ok {
		t.Fatal("已配置应返回 true")
	}
	ai, _ := dto["ai"].(map[string]any)
	key, _ := ai["api_key"].(map[string]any)
	if key["configured"] != true {
		t.Errorf("api_key 应标记 configured: %v", ai["api_key"])
	}
	gs, _ := dto["groups"].([]int64)
	if len(gs) != 2 {
		t.Errorf("groups 应返回: %v", dto["groups"])
	}
}

func TestAdminSaveAPIKeySemantics(t *testing.T) {
	a, _ := testAdmin(t)
	base := map[string]any{
		"enabled": true,
		"repo": map[string]any{
			"url": "https://github.com/x/civgo.git", "branch": "",
			"docs_path": "docs/game_content", "sync_interval_sec": 300,
			"clone_shallow": true, "sparse_checkout": true,
		},
		"ai": map[string]any{
			"base_url": "https://ai.realseek.wiki/v1", "api_key": "sk-original",
			"chat_model": "deepseek-v4-flash", "chat_timeout_sec": 90,
			"max_output_tokens": 2048,
		},
	}
	if _, err := a.Save(adminConfigRaw(base)); err != nil {
		t.Fatal(err)
	}
	// 保持（不传 api_key）
	keep := map[string]any{
		"enabled": true,
		"repo": map[string]any{
			"url": "https://github.com/x/civgo.git", "branch": "",
			"docs_path": "docs/game_content", "sync_interval_sec": 300,
			"clone_shallow": true, "sparse_checkout": true,
		},
		"ai": map[string]any{
			"base_url": "https://ai.realseek.wiki/v1", "api_key": map[string]any{"configured": true},
			"chat_model": "deepseek-v4-flash", "chat_timeout_sec": 90,
			"max_output_tokens": 2048,
		},
	}
	if _, err := a.Save(adminConfigRaw(keep)); err != nil {
		t.Fatal(err)
	}
	cfg, _ := Load(a.path)
	if cfg.AI.APIKey != "sk-original" {
		t.Errorf("configured 语义应保持 key，got %q", cfg.AI.APIKey)
	}
	// 设置新 key
	update := map[string]any{
		"enabled": true,
		"repo": map[string]any{
			"url": "https://github.com/x/civgo.git", "branch": "",
			"docs_path": "docs/game_content", "sync_interval_sec": 300,
			"clone_shallow": true, "sparse_checkout": true,
		},
		"ai": map[string]any{
			"base_url": "https://ai.realseek.wiki/v1", "api_key": "sk-new",
			"chat_model": "deepseek-v4-flash", "chat_timeout_sec": 90,
			"max_output_tokens": 2048,
		},
	}
	if _, err := a.Save(adminConfigRaw(update)); err != nil {
		t.Fatal(err)
	}
	cfg, _ = Load(a.path)
	if cfg.AI.APIKey != "sk-new" {
		t.Errorf("字符串语义应设置 key，got %q", cfg.AI.APIKey)
	}
	// 清除
	clearCfg := map[string]any{
		"enabled": true,
		"repo": map[string]any{
			"url": "https://github.com/x/civgo.git", "branch": "",
			"docs_path": "docs/game_content", "sync_interval_sec": 300,
			"clone_shallow": true, "sparse_checkout": true,
		},
		"ai": map[string]any{
			"base_url": "https://ai.realseek.wiki/v1", "api_key": map[string]any{"clear": true},
			"chat_model": "deepseek-v4-flash", "chat_timeout_sec": 90,
			"max_output_tokens": 2048,
		},
	}
	_, err := a.Save(adminConfigRaw(clearCfg))
	if err == nil {
		t.Fatal("清除 api_key 后校验应失败（api_key 必填）")
	}
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("应返回 ErrNotConfigured，got %v", err)
	}
	// 校验失败时不应写盘（旧文件保留）
	cfg, _ = Load(a.path)
	if cfg.AI.APIKey != "sk-new" {
		t.Errorf("失败保存不应覆盖旧配置，got %q", cfg.AI.APIKey)
	}
}

func TestAdminSaveValidation(t *testing.T) {
	a, _ := testAdmin(t)
	// repo.url 为空 → 校验失败
	bad := map[string]any{
		"enabled": true,
		"repo": map[string]any{
			"url": "", "branch": "", "docs_path": "docs/game_content",
			"sync_interval_sec": 300, "clone_shallow": true, "sparse_checkout": true,
		},
		"ai": map[string]any{
			"base_url": "https://ai.realseek.wiki/v1", "api_key": "sk-x",
			"chat_model": "m", "chat_timeout_sec": 90, "max_output_tokens": 2048,
		},
	}
	if _, err := a.Save(adminConfigRaw(bad)); err == nil || !strings.Contains(err.Error(), "repo.url") {
		t.Errorf("应报 repo.url 校验错误: %v", err)
	}
	// 坏 JSON
	if _, err := a.Save(json.RawMessage(`{bad`)); err == nil {
		t.Error("坏 JSON 应报错")
	}
	// 缺 config 字段
	if _, err := a.Save(json.RawMessage(`{}`)); err == nil {
		t.Error("缺 config 应报错")
	}
}

func TestAdminSaveReloadImmediate(t *testing.T) {
	// 模拟模块已启动：Service + 写盘后立即生效
	cfg := DefaultConfig()
	cfg.AI.APIKey = "sk-test"
	store := &Store{}
	store.cfgPtr.Store(cfg)
	dataDir := t.TempDir()
	// 先写一份配置
	svc := &Service{store: store}
	a := NewAdmin(dataDir, svc)
	a.path = filepath.Join(dataDir, "civgo", "civgo.json")
	store.path = a.path
	first := map[string]any{
		"enabled": true,
		"repo": map[string]any{
			"url": "https://github.com/x/civgo.git", "branch": "",
			"docs_path": "docs/game_content", "sync_interval_sec": 300,
			"clone_shallow": true, "sparse_checkout": true,
		},
		"ai": map[string]any{
			"base_url": "https://ai.realseek.wiki/v1", "api_key": "sk-a",
			"chat_model": "deepseek-v4-flash", "chat_timeout_sec": 90,
			"max_output_tokens": 2048,
		},
	}
	if _, err := a.Save(adminConfigRaw(first)); err != nil {
		t.Fatal(err)
	}
	// 此时 svc.store 未载入（内存中是旧 cfg）；模拟 ForceReload 生效：
	if err := store.ForceReload(); err != nil {
		t.Fatalf("ForceReload 失败: %v", err)
	}
	if store.Get().AI.APIKey != "sk-a" {
		t.Errorf("ForceReload 后应读到新配置: %q", store.Get().AI.APIKey)
	}
	// 坏配置 ForceReload 保留旧值
	if err := os.WriteFile(a.path, []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.ForceReload(); err == nil {
		t.Error("坏配置 ForceReload 应报错")
	}
	if store.Get().AI.APIKey != "sk-a" {
		t.Error("失败重载应保留旧配置")
	}
}
