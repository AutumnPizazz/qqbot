package state

import (
	"qqbot/internal/config"
	"time"
)

// 敏感字段路径（AAD 的一部分，跨版本稳定）。
const (
	fieldOneBotToken = "system.onebot.access_token"
	fieldNapCatToken = "system.napcat.webui_token"
	fieldEmailPass   = "system.email.smtp_password"
)

// EffectiveConfig 把规范化配置转换为 Bot 使用的生效配置：
// 解密 token → 填充 config.Config → NormalizeAndValidate（默认值 + 正则编译）。
// 校验失败返回 *ValidationError。
func (s *ConfigService) buildEffective(c *Control) (*config.Config, error) {
	return buildEffectiveConfig(c, s.keys)
}

// buildEffectiveConfig 无状态版本（迁移时复用）。
func buildEffectiveConfig(c *Control, keys *MasterKey) (*config.Config, error) {
	if err := normalizeAndValidate(c); err != nil {
		return nil, err
	}
	oneBotToken, err := keys.Decrypt(c.System.OneBot.AccessToken, fieldOneBotToken)
	if err != nil {
		return nil, err
	}
	napCatToken, err := keys.Decrypt(c.System.NapCat.WebUIToken, fieldNapCatToken)
	if err != nil {
		return nil, err
	}
	_ = napCatToken // 阶段 1 不进入生产运行路径（NapCat Service 在阶段 2 使用）

	cfg := &config.Config{
		Bot: config.BotConfig{
			Name:    c.System.BotName,
			Owner:   c.System.Owner,
			TimeLoc: c.System.Timezone,
		},
		OneBot: config.OneBotConfig{
			WSURL:        c.System.OneBot.WSURL,
			AccessToken:  oneBotToken,
			APITimeoutMs: c.System.OneBot.APITimeoutMs,
		},
		Email: config.EmailConfig{
			Enabled:  c.System.Email.Enabled,
			SMTPHost: c.System.Email.SMTPHost,
			SMTPPort: c.System.Email.SMTPPort,
			SMTPUser: c.System.Email.SMTPUser,
			To:       c.System.Email.To,
		},
		Watchdog: config.WatchdogConfig{
			Enabled:  c.System.Watchdog.Enabled,
			Interval: watchdogInterval(c.System.Watchdog.IntervalMinutes),
			EmailTo:  watchdogEmailTo(c.System.Watchdog.EmailTo, c.System.Email.To),
		},
		Groups: make([]config.GroupConfig, 0, len(c.Groups)),
	}
	if c.System.Email.SMTPPassword != nil {
		emailPass, err := keys.Decrypt(c.System.Email.SMTPPassword, fieldEmailPass)
		if err != nil {
			return nil, err
		}
		cfg.Email.SMTPPassword = emailPass
	}
	for i := range c.Groups {
		g := &c.Groups[i]
		ng := config.GroupConfig{
			GroupID:   g.GroupID,
			Enabled:   g.Enabled,
			Whitelist: append([]int64(nil), g.Whitelist...),
			Rules:     g.Rules,
		}
		cfg.Groups = append(cfg.Groups, ng)
	}
	// 第二道校验：现有 Validate 负责默认值（StrikeTTL 派生字段）与正则编译。
	if err := cfg.Validate(); err != nil {
		return nil, newValidationError("config", err.Error())
	}
	return cfg, nil
}

// watchdogInterval 归一化检测间隔：<=0 用默认 10 分钟。
func watchdogInterval(minutes int) time.Duration {
	if minutes <= 0 {
		return 10 * time.Minute
	}
	return time.Duration(minutes) * time.Minute
}

// watchdogEmailTo 归一化提醒收件人：优先 watchdog.email_to，回退系统邮箱收件人。
func watchdogEmailTo(watchTo, emailTo string) string {
	if watchTo != "" {
		return watchTo
	}
	return emailTo
}
