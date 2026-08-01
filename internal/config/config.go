// Package config 负责加载并校验机器人 YAML 配置。
package config

import (
	"fmt"
	"os"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"

	"qqbot/internal/rules"
)

// Config 是根配置。
type Config struct {
	Bot         BotConfig         `yaml:"bot"`
	OneBot      OneBotConfig      `yaml:"onebot"`
	LoginNotify LoginNotifyConfig `yaml:"login_notify"` // 登录二维码邮件通知（可选）
	Email       EmailConfig       `yaml:"-"`           // 邮箱两步验证（control.json v2 生效配置）
	Groups      []GroupConfig     `yaml:"groups"`
}

// EmailConfig SMTP 发信配置（登录验证码；明文密码由 state 解密填充）。
type EmailConfig struct {
	Enabled      bool
	SMTPHost     string
	SMTPPort     int
	SMTPUser     string
	SMTPPassword string
	To           string
}

// BotConfig 机器人基础配置。
type BotConfig struct {
	Name    string         `yaml:"name"`     // 机器人显示名，用于欢迎语等
	Owner   int64          `yaml:"owner"`    // 超级管理员 QQ
	TimeLoc string         `yaml:"timezone"` // 展示用时区，如 Asia/Shanghai
	Loc     *time.Location `yaml:"-"`
}

// OneBotConfig 连接配置。
type OneBotConfig struct {
	WSURL        string `yaml:"ws_url"`         // 正向 WS 地址，如 ws://127.0.0.1:3001
	AccessToken  string `yaml:"access_token"`   // 与 NapCat 配置一致的 AccessToken
	APITimeoutMs int    `yaml:"api_timeout_ms"` // API 调用超时（毫秒），默认 5000
}

// LoginNotifyConfig 旧版登录通知配置（阶段 5 起邮件通知已停用）。
// 该类型只参与一次性迁移：仅读取 webui_url/webui_token 迁移到 NapCat 配置，
// SMTP/IMAP 等字段不再被生产代码使用（yaml.v3 解析时静默忽略）。
type LoginNotifyConfig struct {
	WebUIURL string `yaml:"webui_url"`
	// WebUI 密码/token：NapCat WebUI 面板登录凭据（docker logs napcat 可查）。
	WebUIToken string `yaml:"webui_token"`
}

// GroupConfig 单个群的管理配置。
// Keyword/Welcome/Flood/JoinRequest 仅作旧 config.yaml 迁移输入；
// Rules 为规则引擎生效配置（control.json v2 转换而来），生产逻辑只读 Rules。
type GroupConfig struct {
	GroupID   int64   `yaml:"group_id"`
	Enabled   bool    `yaml:"enabled"`
	Whitelist []int64 `yaml:"whitelist"` // 全局白名单（群内豁免用户）

	Rules []rules.Rule `yaml:"-"` // 规则引擎规则（v2 生效配置）

	Welcome     *WelcomeConfig     `yaml:"welcome"`
	Keyword     *KeywordConfig     `yaml:"keyword_filter"`
	Flood       *FloodConfig       `yaml:"flood"`
	JoinRequest *JoinRequestConfig `yaml:"join_request"`
}

// WelcomeConfig 新人欢迎。
type WelcomeConfig struct {
	Enabled bool   `yaml:"enabled"`
	Message string `yaml:"message"` // 支持占位符 {nickname} {group_id}
}

// KeywordConfig 关键词过滤。
type KeywordConfig struct {
	Enabled    bool          `yaml:"enabled"`
	WarnLimit  int           `yaml:"warn_limit"` // warn 累计多少次升级为禁言，0 表示不升级
	MuteLimit  int           `yaml:"mute_limit"` // mute 累计多少次升级为移出，0 表示不升级
	StrikeTTL  time.Duration `yaml:"-"`
	StrikeTTLH int           `yaml:"strike_ttl_hours"` // 违规计分有效期（小时），默认 24
	Rules      []KeywordRule `yaml:"rules"`
}

// KeywordRule 一条关键词规则。
type KeywordRule struct {
	Pattern     string         `yaml:"pattern"`      // 匹配文本；regex=true 时按正则解释
	Regex       bool           `yaml:"regex"`        // 是否按正则匹配
	Action      string         `yaml:"action"`       // warn / mute / kick
	MuteMinutes int            `yaml:"mute_minutes"` // action=mute 时禁言分钟数，默认 30
	re          *regexp.Regexp `yaml:"-"`
}

// FloodConfig 刷屏检测。
type FloodConfig struct {
	Enabled       bool `yaml:"enabled"`
	WindowSeconds int  `yaml:"window_seconds"` // 统计窗口，默认 10
	MaxMessages   int  `yaml:"max_messages"`   // 窗口内允许的最大消息数，默认 8
	MuteMinutes   int  `yaml:"mute_minutes"`   // 处罚禁言分钟数，默认 10
	KickOnRepeat  bool `yaml:"kick_on_repeat"` // 重复触发多次刷屏后移出
	KickThreshold int  `yaml:"kick_threshold"` // 触发移出的累计刷屏次数，默认 3
}

// JoinRequestConfig 加群请求自动处理。
type JoinRequestConfig struct {
	AutoApprove bool   `yaml:"auto_approve"`   // 自动同意加群申请（机器人需为群管理员）
	Keyword     string `yaml:"reject_keyword"` // 申请备注含此关键词时自动拒绝
	RejectText  string `yaml:"reject_reason"`  // 拒绝原因
}

// Load 读取并校验配置文件。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置失败: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate 校验配置合法性。
func (c *Config) Validate() error {
	if c.OneBot.WSURL == "" {
		return fmt.Errorf("onebot.ws_url 不能为空")
	}
	if c.Bot.Owner == 0 {
		return fmt.Errorf("bot.owner 未设置（超级管理员 QQ）")
	}
	if c.Bot.TimeLoc != "" {
		loc, err := time.LoadLocation(c.Bot.TimeLoc)
		if err != nil {
			return fmt.Errorf("bot.timezone 无效: %w", err)
		}
		c.Bot.Loc = loc
	} else {
		c.Bot.Loc = time.Local
	}

	seen := map[int64]bool{}
	for i := range c.Groups {
		g := &c.Groups[i]
		if g.GroupID == 0 {
			return fmt.Errorf("groups[%d].group_id 不能为 0", i)
		}
		if seen[g.GroupID] {
			return fmt.Errorf("groups[%d].group_id %d 重复", i, g.GroupID)
		}
		seen[g.GroupID] = true
		if !g.Enabled {
			continue
		}
		if g.Keyword != nil {
			if g.Keyword.StrikeTTLH <= 0 {
				g.Keyword.StrikeTTLH = 24
			}
			g.Keyword.StrikeTTL = time.Duration(g.Keyword.StrikeTTLH) * time.Hour
			for j := range g.Keyword.Rules {
				r := &g.Keyword.Rules[j]
				switch r.Action {
				case "warn", "mute", "kick":
				default:
					return fmt.Errorf("群 %d 关键词规则 %q action 必须是 warn/mute/kick", g.GroupID, r.Pattern)
				}
				if r.Regex {
					re, err := regexp.Compile(r.Pattern)
					if err != nil {
						return fmt.Errorf("群 %d 关键词规则 %q 正则无效: %w", g.GroupID, r.Pattern, err)
					}
					r.re = re
				}
			}
		}
		if g.Flood != nil {
			if g.Flood.WindowSeconds <= 0 {
				g.Flood.WindowSeconds = 10
			}
			if g.Flood.MaxMessages <= 0 {
				g.Flood.MaxMessages = 8
			}
			if g.Flood.MuteMinutes <= 0 {
				g.Flood.MuteMinutes = 10
			}
			if g.Flood.KickThreshold <= 0 {
				g.Flood.KickThreshold = 3
			}
		}
	}
	return nil
}

// Group 返回指定群的配置；未启用或不存在返回 nil。
func (c *Config) Group(groupID int64) *GroupConfig {
	for i := range c.Groups {
		g := &c.Groups[i]
		if g.GroupID == groupID && g.Enabled {
			return g
		}
	}
	return nil
}

// IsWhitelisted 判断用户是否在群白名单或超级管理员之列。
func (c *Config) IsWhitelisted(groupID, userID int64) bool {
	if userID == c.Bot.Owner {
		return true
	}
	if g := c.Group(groupID); g != nil {
		for _, id := range g.Whitelist {
			if id == userID {
				return true
			}
		}
	}
	return false
}

// Regexp 返回编译后的正则（仅 regex=true 的规则非 nil）。
func (r *KeywordRule) Regexp() *regexp.Regexp {
	return r.re
}
