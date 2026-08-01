package state

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"qqbot/internal/rules"
)

// 校验常量。
const (
	maxRemarkLen    = 32  // 群备注长度上限
	maxTextLen      = 500 // 欢迎语等文本上限
	maxWhitelistLen = 100 // 每群白名单数量上限
	maxAPITimeoutMs = 60000
)

var (
	rePureDigits = regexp.MustCompile(`^[0-9]+$`)
)

// normalizeAndValidate 先填充默认值，再执行增强校验。
// 对启用和禁用群都执行结构校验（设计文档 7.1）。
func normalizeAndValidate(c *Control) error {
	normalizeControl(c)
	ve := validateControl(c)
	if ve == nil {
		return nil
	}
	return ve
}

// normalizeControl 填充默认值（v2：规则参数必填，仅 OneBot 超时保留默认）。
func normalizeControl(c *Control) {
	if c.System.OneBot.APITimeoutMs <= 0 {
		c.System.OneBot.APITimeoutMs = 5000
	}
}

// validateControl 校验整个规范化配置。
func validateControl(c *Control) *ValidationError {
	ve := &ValidationError{Message: "配置校验失败"}
	if c.System.Owner <= 0 {
		ve.add("system.owner", "超级管理员 QQ 必须为正数")
	}
	if c.System.BotName == "" {
		ve.add("system.bot_name", "机器人名称不能为空")
	}
	if len(c.System.BotName) > 64 {
		ve.add("system.bot_name", "机器人名称过长（最多 64 字符）")
	}
	validateOneBot(&c.System.OneBot, ve)
	validateNapCat(&c.System.NapCat, ve)
	validateEmail(&c.System.Email, ve)

	seen := map[int64]int{}
	seenRemark := map[string]int{}
	for i := range c.Groups {
		g := &c.Groups[i]
		base := "groups." + strconv.Itoa(i)
		if g.GroupID <= 0 {
			ve.add(base+".group_id", "群号必须为正数")
		} else if prev, dup := seen[g.GroupID]; dup {
			ve.add(base+".group_id", fmt.Sprintf("群号 %d 与 groups.%d 重复", g.GroupID, prev))
		} else {
			seen[g.GroupID] = i
		}
		if g.Remark != "" {
			if len([]rune(g.Remark)) > maxRemarkLen {
				ve.add(base+".remark", fmt.Sprintf("群备注过长（最多 %d 字符）", maxRemarkLen))
			} else if rePureDigits.MatchString(g.Remark) {
				ve.add(base+".remark", "群备注不能是纯数字（与群号混淆）")
			} else if prev, dup := seenRemark[g.Remark]; dup {
				ve.add(base+".remark", fmt.Sprintf("群备注 %q 与 groups.%d 重复", g.Remark, prev))
			} else {
				seenRemark[g.Remark] = i
			}
		}
		validateWhitelist(g, base, ve)
		if len(g.Rules) > 0 {
			if re := rules.ValidateRules(g.Rules, base+".rules"); re != nil {
				for _, f := range re.Fields {
					ve.add(f.Field, f.Message)
				}
			}
		}
	}
	if len(ve.Fields) == 0 {
		return nil
	}
	return ve
}

func validateOneBot(o *OneBotConfig, ve *ValidationError) {
	base := "system.onebot"
	if o.WSURL == "" {
		ve.add(base+".ws_url", "OneBot 地址不能为空")
	} else if u, err := url.Parse(o.WSURL); err != nil || (u.Scheme != "ws" && u.Scheme != "wss") {
		ve.add(base+".ws_url", "OneBot 地址必须是 ws:// 或 wss://")
	} else if u.Host == "" {
		ve.add(base+".ws_url", "OneBot 地址缺少主机")
	}
	if o.APITimeoutMs < 1 || o.APITimeoutMs > maxAPITimeoutMs {
		ve.add(base+".api_timeout_ms", fmt.Sprintf("API 超时范围必须为 1~%d 毫秒", maxAPITimeoutMs))
	}
}

func validateNapCat(n *NapCatConfig, ve *ValidationError) {
	base := "system.napcat"
	if n.WebUIURL == "" {
		return // NapCat 可选：未配置时仅影响二维码/登录状态能力
	}
	u, err := url.Parse(n.WebUIURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		ve.add(base+".webui_url", "NapCat 地址必须是 http:// 或 https://")
		return
	}
	if u.User != nil {
		ve.add(base+".webui_url", "NapCat 地址不允许包含用户名/密码")
	}
	if u.Host == "" {
		ve.add(base+".webui_url", "NapCat 地址缺少主机")
	}
}

// validateEmail 邮箱两步验证配置：开启时必须完整；未开启时仅结构校验。
func validateEmail(e *EmailConfig, ve *ValidationError) {
	base := "system.email"
	if !e.Enabled {
		return
	}
	if e.SMTPHost == "" {
		ve.add(base+".smtp_host", "SMTP 服务器不能为空")
	}
	if e.SMTPPort != 465 && e.SMTPPort != 587 && e.SMTPPort != 25 {
		ve.add(base+".smtp_port", "SMTP 端口只能是 465(SSL)/587(STARTTLS)/25")
	}
	if e.SMTPUser == "" {
		ve.add(base+".smtp_user", "SMTP 账号不能为空")
	}
	if e.SMTPPassword == nil {
		ve.add(base+".smtp_password", "SMTP 授权码不能为空")
	}
	if e.To == "" {
		ve.add(base+".to", "收件邮箱不能为空")
	}
}

func validateWhitelist(g *GroupConfig, base string, ve *ValidationError) {
	if len(g.Whitelist) > maxWhitelistLen {
		ve.add(base+".whitelist", fmt.Sprintf("白名单数量超出上限（最多 %d 个）", maxWhitelistLen))
	}
	seen := map[int64]bool{}
	for i, id := range g.Whitelist {
		if id <= 0 {
			ve.add(fmt.Sprintf("%s.whitelist.%d", base, i), "QQ 号必须为正数")
		} else if seen[id] {
			ve.add(fmt.Sprintf("%s.whitelist.%d", base, i), "白名单内 QQ 号重复")
		} else {
			seen[id] = true
		}
	}
}

// validateText 通用文本校验：长度与禁止控制字符（允许 \n 多行文本）。
func validateText(field, s string, maxLen int, name string, ve *ValidationError) {
	if len([]rune(s)) > maxLen {
		ve.add(field, fmt.Sprintf("%s过长（最多 %d 字符）", name, maxLen))
	}
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r' {
			ve.add(field, fmt.Sprintf("%s包含禁止的控制字符", name))
			return
		}
	}
}

// validateURLScheme 校验 URL scheme 属于白名单（供 CLI 等复用）。
func validateURLScheme(s string, allowed ...string) bool {
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	for _, a := range allowed {
		if strings.EqualFold(u.Scheme, a) {
			return true
		}
	}
	return false
}