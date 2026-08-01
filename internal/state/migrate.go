package state

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"qqbot/internal/config"
)

// MigrateOptions 一次性迁移的输入。
type MigrateOptions struct {
	DataDir    string // 目标数据目录（写入 control.json 的目录）
	ConfigPath string // 旧 config.yaml 路径（QQBOT_IMPORT_CONFIG）
	Keys       *MasterKey
}

// Migrate 读取旧 config.yaml，应用 runtime.json 群级覆盖、合并 aliases.json，
// 加密敏感 token，构造并完整校验候选 Control（不写盘——写盘必须经过
// ConfigService.Update 的统一事务）。
//
// 迁移规则（设计文档 17 节）：
//   - 旧 YAML/JSON 解析失败不得静默当成空配置，直接返回错误。
//   - 忽略 SMTP/IMAP 完整性要求；SMTP/IMAP 字段不进入新配置模型。
//   - 原文件保留（不再写入），调用方负责提示运维移除明文 secret。
func Migrate(opts MigrateOptions) (*Control, error) {
	dataDir := opts.DataDir
	if dataDir == "" {
		dataDir = "data"
	}
	if opts.Keys == nil {
		return nil, fmt.Errorf("迁移需要主密钥（QQBOT_MASTER_KEY_FILE）")
	}
	if opts.ConfigPath == "" {
		return nil, fmt.Errorf("迁移需要旧 config.yaml 路径（QQBOT_IMPORT_CONFIG）")
	}

	// 1. 解析旧 config.yaml（不调用 config.Load：跳过 login_notify 完整性校验）
	raw, err := os.ReadFile(opts.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("读取旧配置 %s 失败: %w", opts.ConfigPath, err)
	}
	var old config.Config
	if err := yaml.Unmarshal(raw, &old); err != nil {
		return nil, fmt.Errorf("解析旧配置 %s 失败: %w", opts.ConfigPath, err)
	}

	// 2. 加载 runtime.json（存在则必须可解析）
	overlays, err := loadRuntimeJSON(filepath.Join(dataDir, "runtime.json"))
	if err != nil {
		return nil, err
	}
	config.ApplyRuntimeOverlay(&old, overlays)

	// 3. 加载 aliases.json 并映射为群备注
	aliases, err := loadAliasesJSON(filepath.Join(dataDir, "aliases.json"))
	if err != nil {
		return nil, err
	}

	// 4. 构造规范化配置（token 加密）
	c := &Control{SchemaVersion: SchemaVersion}
	c.System = SystemConfig{
		BotName:  old.Bot.Name,
		Owner:    old.Bot.Owner,
		Timezone: old.Bot.TimeLoc,
		OneBot: OneBotConfig{
			WSURL:        old.OneBot.WSURL,
			APITimeoutMs: old.OneBot.APITimeoutMs,
		},
	}
	if old.OneBot.AccessToken != "" {
		enc, err := opts.Keys.Encrypt(old.OneBot.AccessToken, fieldOneBotToken)
		if err != nil {
			return nil, fmt.Errorf("加密 OneBot token 失败: %w", err)
		}
		c.System.OneBot.AccessToken = enc
	}
	if old.LoginNotify.WebUIURL != "" || old.LoginNotify.WebUIToken != "" {
		c.System.NapCat = NapCatConfig{WebUIURL: old.LoginNotify.WebUIURL}
		if old.LoginNotify.WebUIToken != "" {
			enc, err := opts.Keys.Encrypt(old.LoginNotify.WebUIToken, fieldNapCatToken)
			if err != nil {
				return nil, fmt.Errorf("加密 NapCat token 失败: %w", err)
			}
			c.System.NapCat.WebUIToken = enc
		}
	}

	// 4. 群转换：构造 v1 中间结构 → 复用 migrateV1ToV2 统一迁移路径
	// （旧模块 → 规则引擎，设计文档 RULE_ENGINE_DESIGN §8；禁用群不生成规则）
	v1 := &v1Control{SchemaVersion: 1, System: c.System}
	for i := range old.Groups {
		g := &old.Groups[i]
		ng := v1GroupConfig{
			GroupID: g.GroupID,
			Enabled: g.Enabled,
		}
		// runtime.json 中的覆盖已应用；补 aliases 备注
		ng.Remark = aliases[g.GroupID]
		if g.Whitelist != nil {
			ng.Whitelist = append([]int64(nil), g.Whitelist...)
		}
		if g.Welcome != nil {
			ng.Welcome = &WelcomeConfig{Enabled: g.Welcome.Enabled, Message: g.Welcome.Message}
		}
		if g.Keyword != nil {
			k := &KeywordConfig{
				Enabled: g.Keyword.Enabled, WarnLimit: g.Keyword.WarnLimit,
				MuteLimit: g.Keyword.MuteLimit, StrikeTTLH: g.Keyword.StrikeTTLH,
			}
			for _, r := range g.Keyword.Rules {
				k.Rules = append(k.Rules, KeywordRule{
					Pattern: r.Pattern, Regex: r.Regex,
					Action: r.Action, MuteMinutes: r.MuteMinutes,
				})
			}
			ng.Keyword = k
		}
		if g.Flood != nil {
			ng.Flood = &FloodConfig{
				Enabled: g.Flood.Enabled, WindowSeconds: g.Flood.WindowSeconds,
				MaxMessages: g.Flood.MaxMessages, MuteMinutes: g.Flood.MuteMinutes,
				KickOnRepeat: g.Flood.KickOnRepeat, KickThreshold: g.Flood.KickThreshold,
			}
		}
		if g.JoinRequest != nil {
			ng.JoinRequest = &JoinRequestConfig{
				AutoApprove: g.JoinRequest.AutoApprove,
				Keyword:     g.JoinRequest.Keyword, RejectText: g.JoinRequest.RejectText,
			}
		}
		v1.Groups = append(v1.Groups, ng)
	}
	c, err = migrateV1ToV2(v1)
	if err != nil {
		return nil, err
	}
	slog.Info("迁移候选配置构造完成",
		"groups", len(c.Groups), "owner", c.System.Owner,
		"onebot_ws", c.System.OneBot.WSURL,
		"napcat_url", c.System.NapCat.WebUIURL)
	return c, nil
}

// loadRuntimeJSON 严格读取 runtime.json（解析失败返回错误，不静默当空配置）。
func loadRuntimeJSON(path string) (map[int64]*config.RuntimeGroup, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 runtime.json 失败: %w", err)
	}
	var groups map[int64]*config.RuntimeGroup
	if err := json.Unmarshal(data, &groups); err != nil {
		return nil, fmt.Errorf("解析 runtime.json 失败（迁移中止，需人工检查该文件）: %w", err)
	}
	return groups, nil
}

// loadAliasesJSON 严格读取 aliases.json（{"群号": "备注"}）。
func loadAliasesJSON(path string) (map[int64]string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 aliases.json 失败: %w", err)
	}
	var raw map[string]string
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("解析 aliases.json 失败（迁移中止，需人工检查该文件）: %w", err)
	}
	out := map[int64]string{}
	for idStr, alias := range raw {
		var id int64
		if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil || id == 0 {
			continue
		}
		out[id] = alias
	}
	return out, nil
}
