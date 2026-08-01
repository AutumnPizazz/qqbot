// Package state 实现网页管理后台的唯一配置源 control.json：
// 规范化 DTO、AES-GCM 加密字段、事务化 ConfigService 与一次性数据迁移。
//
// 设计要点（见 docs/WEB_ADMIN_DESIGN.md）：
//   - control.json 是唯一配置写入源；config.yaml / runtime.json / aliases.json
//     只参与一次性迁移，不再被生产代码写入。
//   - 所有配置修改必须经过 ConfigService.Update：候选副本 → 校验 →
//     临时文件 + fsync + 原子 rename → 成功后切换生效快照。
//   - 敏感字段（OneBot AccessToken、NapCat WebUI token）AES-256-GCM 加密落盘，
//     GET API 只暴露 configured 布尔值。
package state

import (
	"encoding/json"
	"time"

	"qqbot/internal/rules"
)

// SchemaVersion 是 control.json 的 schema 版本。
// v1：welcome/keyword/flood/join_request 四模块；v2：统一规则引擎（rules）。
const SchemaVersion = 2

// maxHistory 是 control.json 内保留的完整配置快照数量。
const maxHistory = 20

// EncryptedValue 是敏感字段的加密落盘形式：base64(nonce || ciphertext)。
type EncryptedValue struct {
	Encrypted string `json:"encrypted"`
	KeyID     string `json:"key_id"`
}

// Configured 判断是否已配置（供 API 返回，不暴露密文）。
func (v *EncryptedValue) Configured() bool { return v != nil && v.Encrypted != "" }

// Control 是 control.json 的根结构。
type Control struct {
	SchemaVersion int            `json:"schema_version"`
	Revision      int64          `json:"revision"`
	UpdatedAt     time.Time      `json:"updated_at"`
	System        SystemConfig   `json:"system"`
	Groups        []GroupConfig  `json:"groups"`
	History       []HistoryEntry `json:"history,omitempty"`
}

// SystemConfig 系统级配置（对应旧 config.yaml 的 bot + onebot + NapCat 部分）。
type SystemConfig struct {
	BotName  string       `json:"bot_name"`
	Owner    int64        `json:"owner"` // 超级管理员 QQ
	Timezone string       `json:"timezone,omitempty"`
	OneBot   OneBotConfig `json:"onebot"`
	NapCat   NapCatConfig `json:"napcat"`
	Email    EmailConfig  `json:"email,omitempty"` // 邮箱两步验证
}

// EmailConfig SMTP 发信配置（登录邮箱验证码）。
type EmailConfig struct {
	Enabled      bool            `json:"enabled,omitempty"` // 两步验证开关（需 SMTP 配置完整才能开启）
	SMTPHost     string          `json:"smtp_host,omitempty"`
	SMTPPort     int             `json:"smtp_port,omitempty"` // 465=SSL / 587=STARTTLS
	SMTPUser     string          `json:"smtp_user,omitempty"`
	SMTPPassword *EncryptedValue `json:"smtp_password,omitempty"`
	To           string          `json:"to,omitempty"` // 收件邮箱（验证码发到这里）
}

// OneBotConfig 正向 WebSocket 连接配置。
type OneBotConfig struct {
	WSURL        string          `json:"ws_url"`
	APITimeoutMs int             `json:"api_timeout_ms,omitempty"` // 默认 5000
	AccessToken  *EncryptedValue `json:"access_token,omitempty"`
}

// NapCatConfig WebUI 登录配置（旧 login_notify.webui_* 迁移而来）。
type NapCatConfig struct {
	WebUIURL   string          `json:"webui_url,omitempty"`
	WebUIToken *EncryptedValue `json:"webui_token,omitempty"`
}

// GroupConfig 单个群的完整配置 DTO（含群备注 remark，随同一 revision 提交）。
// v2 起规则统一由 rules 表达（事件→条件→动作），旧四模块已迁移。
type GroupConfig struct {
	GroupID   int64       `json:"group_id"`
	Enabled   bool        `json:"enabled"`
	Remark    string      `json:"remark,omitempty"`
	Whitelist []int64     `json:"whitelist,omitempty"`
	Rules     []rules.Rule `json:"rules,omitempty"` // 按序匹配，break 控制是否继续
}

// HistoryEntry 一条配置历史：完整快照 + 元信息。
// 快照 Config.History 恒为空，避免递归膨胀；恢复时从快照构建新 Control。
type HistoryEntry struct {
	Revision int64     `json:"revision"`
	Time     time.Time `json:"time"`
	Actor    string    `json:"actor"`   // 第一版固定 admin
	Summary  string    `json:"summary"` // 变更字段摘要
	Hash     string    `json:"hash"`    // 配置摘要哈希（sha256 hex）
	Config   *Control  `json:"config"`  // 完整可恢复快照（不含 history）
}

// DecodeControl 解析 control.json 内容；禁止未知字段，避免新旧 schema 混写。
// schema 版本不匹配（含 v1 旧文件）返回 *SchemaError，由 Open 决定迁移或报错。
func DecodeControl(data []byte) (*Control, error) {
	// 先宽松读取 schema_version：v1 文件包含 v2 未知字段，
	// 必须优先识别版本再严格解码，否则旧文件无法触发自动迁移。
	var probe struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, err
	}
	if probe.SchemaVersion != SchemaVersion {
		return nil, &SchemaError{Version: probe.SchemaVersion}
	}
	dec := json.NewDecoder(bytesReader(data))
	dec.DisallowUnknownFields()
	var c Control
	if err := dec.Decode(&c); err != nil {
		return nil, err
	}
	return &c, nil
}

// EncodeControl 序列化 control.json 内容（2 空格缩进，便于人工巡检）。
func EncodeControl(c *Control) ([]byte, error) {
	return json.MarshalIndent(c, "", "  ")
}
