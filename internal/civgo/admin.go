package civgo

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Admin civgo 配置管理适配（供 admin 后台注入；不依赖 admin 包，鸭子类型满足其接口）。
// 职责：脱敏读取、校验保存（api_key 保持/清除语义）、原子写盘、触发热重载、运行状态。
type Admin struct {
	path string // data/civgo/civgo.json
	mu   sync.Mutex
	svc  *Service // 可 nil（模块未启动；保存后需重启生效）
}

// NewAdmin 创建管理适配。svc 可先传 nil，模块启动后 SetService 注入。
func NewAdmin(dataDir string, svc *Service) *Admin {
	return &Admin{path: filepath.Join(dataDir, "civgo", "civgo.json"), svc: svc}
}

// SetService 注入 Service（模块启用后调用；幂等）。
func (a *Admin) SetService(svc *Service) {
	a.mu.Lock()
	a.svc = svc
	a.mu.Unlock()
}

// Get 返回脱敏配置 DTO（api_key 只暴露 configured）与是否已配置。
// 未配置（文件缺失/无效）时返回默认配置 DTO 与 false。
func (a *Admin) Get() (map[string]any, bool) {
	cfg, err := Load(a.path)
	if err != nil {
		dto := configDTO(DefaultConfig())
		dto["enabled"] = false // 未配置：默认模板但标记停用
		return dto, false
	}
	return configDTO(cfg), true
}

// Save 校验并保存配置，返回 (立即生效?, error)。
// raw 结构：{"config": { ... 与 Get 同构；api_key 支持：字符串=设置 / {"clear":true}=清除 /
// 其他（含缺失、{"configured":true}）=保持 ... }}
// 保存成功后：模块已启动 → 强制热重载立即生效；未启动 → 仅写盘，重启后生效。
func (a *Admin) Save(raw json.RawMessage) (bool, error) {
	var req struct {
		Config map[string]json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return false, fmt.Errorf("请求体无效: %w", err)
	}
	if req.Config == nil {
		return false, fmt.Errorf("缺少 config 字段")
	}
	// 基线：当前配置（缺失则默认值，便于首次配置）
	cur, err := Load(a.path)
	if err != nil {
		cur = DefaultConfig()
	}
	merged, err := mergeConfig(cur, req.Config)
	if err != nil {
		return false, err
	}
	if err := merged.Validate(); err != nil {
		return false, err
	}
	if err := writeConfigFile(a.path, merged); err != nil {
		return false, err
	}
	a.mu.Lock()
	svc := a.svc
	a.mu.Unlock()
	if svc == nil {
		return false, nil // 模块未启动：重启生效
	}
	if err := svc.Reload(); err != nil {
		return true, fmt.Errorf("配置已保存，但热重载失败（重启进程后生效）: %w", err)
	}
	return true, nil
}

// Status 返回运行状态（未启动时返回空 map）。
func (a *Admin) Status() map[string]any {
	a.mu.Lock()
	svc := a.svc
	a.mu.Unlock()
	if svc == nil {
		return map[string]any{}
	}
	return svc.Status()
}

// TestAI 测试 AI 网关（未启动时返回提示性错误）。
func (a *Admin) TestAI() error {
	a.mu.Lock()
	svc := a.svc
	a.mu.Unlock()
	if svc == nil {
		return fmt.Errorf("civgo 模块未启动（配置缺失或未生效），请先保存配置并重启进程")
	}
	return svc.TestAI()
}

// ---- DTO 与合并 ----

// configDTO 输出脱敏配置（api_key → {"configured": bool}）。
func configDTO(cfg *Config) map[string]any {
	return map[string]any{
		"enabled": cfg.Enabled,
		"repo": map[string]any{
			"url":               cfg.Repo.URL,
			"branch":            cfg.Repo.Branch,
			"docs_path":         cfg.Repo.DocsPath,
			"sync_interval_sec": cfg.Repo.SyncIntervalSec,
			"clone_shallow":     cfg.Repo.CloneShallow,
			"sparse_checkout":   cfg.Repo.SparseCheckout,
		},
		"ai": map[string]any{
			"base_url":          cfg.AI.BaseURL,
			"api_key":           map[string]any{"configured": cfg.AI.APIKey != ""},
			"chat_model":        cfg.AI.ChatModel,
			"chat_timeout_sec":  cfg.AI.ChatTimeoutSec,
			"max_output_tokens": cfg.AI.MaxOutputTokens,
		},
		"agent": map[string]any{
			"max_tool_calls":      cfg.Agent.MaxToolCalls,
			"max_context_chars":   cfg.Agent.MaxContextChars,
			"read_page_lines":     cfg.Agent.ReadPageLines,
			"read_page_max_chars": cfg.Agent.ReadPageMaxChars,
		},
		"history": map[string]any{
			"enabled":               cfg.History.Enabled,
			"max_entries_per_group": cfg.History.MaxEntriesPerGroup,
			"persist":               cfg.History.Persist,
			"max_recall_entries":    cfg.History.MaxRecallEntries,
		},
		"usage_alert": map[string]any{
			"enabled":          cfg.UsageAlert.Enabled,
			"window_minutes":   cfg.UsageAlert.WindowMinutes,
			"threshold_tokens": cfg.UsageAlert.ThresholdTokens,
			"cooldown_minutes": cfg.UsageAlert.CooldownMinutes,
			"email_to":         cfg.UsageAlert.EmailTo,
		},
		"groups": cfg.Groups,
		"rate_limit": map[string]any{
			"per_user_min":      cfg.RateLimit.PerUserMin,
			"per_group_min":     cfg.RateLimit.PerGroupMin,
			"max_concurrent_ai": cfg.RateLimit.MaxConcurrentAI,
		},
	}
}

// mergeConfig 把前端提交的 config 块合并到基线（api_key 特殊语义）。
// 缺失的键保持基线值；api_key 见 Save 注释。
func mergeConfig(cur *Config, raw map[string]json.RawMessage) (*Config, error) {
	merged := *cur
	decode := func(key string, dst any) error {
		v, ok := raw[key]
		if !ok {
			return nil
		}
		return json.Unmarshal(v, dst)
	}
	if err := decode("enabled", &merged.Enabled); err != nil {
		return nil, fmt.Errorf("config.enabled 无效: %w", err)
	}
	if err := decode("repo", &merged.Repo); err != nil {
		return nil, fmt.Errorf("config.repo 无效: %w", err)
	}
	if v, ok := raw["ai"]; ok {
		// 注意：外层字段名不能与内嵌 AIConfig.APIKey 同名（json 同名冲突会覆盖基线），用 KeyRaw。
		var ai struct {
			AIConfig
			KeyRaw json.RawMessage `json:"api_key"`
		}
		if err := json.Unmarshal(v, &ai); err != nil {
			return nil, fmt.Errorf("config.ai 无效: %w", err)
		}
		oldKey := cur.AI.APIKey
		merged.AI = ai.AIConfig // 内嵌解码后 APIKey 为空，按语义恢复/设置/清除
		switch {
		case ai.KeyRaw == nil:
			merged.AI.APIKey = oldKey // 未提交 api_key → 保持基线
		default:
			var s string
			if err := json.Unmarshal(ai.KeyRaw, &s); err == nil {
				merged.AI.APIKey = s // 字符串 → 设置
			} else {
				var tu struct {
					Clear bool `json:"clear"`
				}
				if err := json.Unmarshal(ai.KeyRaw, &tu); err == nil && tu.Clear {
					merged.AI.APIKey = "" // {"clear":true} → 清除
				} else {
					merged.AI.APIKey = oldKey // {"configured":...} → 保持基线
				}
			}
		}
	}
	if err := decode("agent", &merged.Agent); err != nil {
		return nil, fmt.Errorf("config.agent 无效: %w", err)
	}
	if err := decode("history", &merged.History); err != nil {
		return nil, fmt.Errorf("config.history 无效: %w", err)
	}
	if err := decode("usage_alert", &merged.UsageAlert); err != nil {
		return nil, fmt.Errorf("config.usage_alert 无效: %w", err)
	}
	if err := decode("groups", &merged.Groups); err != nil {
		return nil, fmt.Errorf("config.groups 无效: %w", err)
	}
	if err := decode("rate_limit", &merged.RateLimit); err != nil {
		return nil, fmt.Errorf("config.rate_limit 无效: %w", err)
	}
	return &merged, nil
}

// writeConfigFile 原子写 civgo.json（临时文件 + rename）。
func writeConfigFile(path string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".civgo-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
