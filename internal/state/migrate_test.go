package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qqbot/internal/rules"
)

// writeOldSetup 写入一套完整的旧版配置（config.yaml + runtime.json + aliases.json）。
func writeOldSetup(t *testing.T, dir string) string {
	t.Helper()
	yaml := `
bot:
  name: 群管小助手
  owner: 10001
  timezone: Asia/Shanghai
onebot:
  ws_url: ws://127.0.0.1:3001
  access_token: plain-onebot-token
  api_timeout_ms: 3000
login_notify:
  enabled: false
  webui_url: http://127.0.0.1:6099
  webui_token: plain-napcat-token
  smtp:
    host: smtp.163.com
    port: 465
  to: [admin@example.com]
groups:
  - group_id: 111111111
    enabled: true
    whitelist: [222222222]
    welcome:
      enabled: true
      message: "欢迎 {nickname} 加入群 {group_id}"
    keyword_filter:
      enabled: true
      warn_limit: 3
      rules:
        - pattern: 广告
          action: warn
        - pattern: 开挂
          regex: true
          action: mute
          mute_minutes: 60
    flood:
      enabled: true
      window_seconds: 20
      max_messages: 10
    join_request:
      auto_approve: false
      reject_keyword: 中介
      reject_reason: 不允许中介
  - group_id: 333333333
    enabled: false
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := `{
  "111111111": {
    "flood_enabled": false,
    "keyword_warn_limit": 5,
    "welcome_message": "运行时欢迎语"
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "runtime.json"), []byte(runtime), 0o600); err != nil {
		t.Fatal(err)
	}
	aliases := `{"111111111": "主群", "999999999": "幽灵群"}`
	if err := os.WriteFile(filepath.Join(dir, "aliases.json"), []byte(aliases), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// findRule 按名称查找规则。
func findRule(t *testing.T, rs []rules.Rule, name string) rules.Rule {
	t.Helper()
	for _, r := range rs {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("规则 %q 不存在（实际: %v）", name, ruleNames(rs))
	return rules.Rule{}
}

func ruleNames(rs []rules.Rule) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Name)
	}
	return out
}

func TestMigrateFull(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeOldSetup(t, dir)
	keys := NewTestMasterKey()

	c, err := Migrate(MigrateOptions{DataDir: dir, ConfigPath: cfgPath, Keys: keys})
	if err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if c.System.BotName != "群管小助手" || c.System.Owner != 10001 {
		t.Fatalf("系统配置错误: %+v", c.System)
	}
	if c.System.Timezone != "Asia/Shanghai" {
		t.Fatalf("时区未迁移: %q", c.System.Timezone)
	}
	if c.System.OneBot.WSURL != "ws://127.0.0.1:3001" || c.System.OneBot.APITimeoutMs != 3000 {
		t.Fatalf("OneBot 配置错误: %+v", c.System.OneBot)
	}
	// token 已加密且可解密
	token, err := keys.Decrypt(c.System.OneBot.AccessToken, fieldOneBotToken)
	if err != nil || token != "plain-onebot-token" {
		t.Fatalf("OneBot token 解密失败: %v", err)
	}
	if strings.Contains(c.System.OneBot.AccessToken.Encrypted, "plain") {
		t.Fatal("token 明文落盘")
	}
	nt, err := keys.Decrypt(c.System.NapCat.WebUIToken, fieldNapCatToken)
	if err != nil || nt != "plain-napcat-token" {
		t.Fatalf("NapCat token 解密失败: %v", err)
	}
	if c.System.NapCat.WebUIURL != "http://127.0.0.1:6099" {
		t.Fatalf("NapCat URL 错误: %q", c.System.NapCat.WebUIURL)
	}

	// 群 1：runtime 覆盖 + aliases 备注 + 规则迁移
	g := c.Groups[0]
	if g.GroupID != 111111111 || !g.Enabled {
		t.Fatalf("群 1 错误: %+v", g)
	}
	if g.Remark != "主群" {
		t.Fatalf("群备注未合并: %q", g.Remark)
	}
	if len(g.Whitelist) != 1 || g.Whitelist[0] != 222222222 {
		t.Fatalf("白名单错误: %+v", g.Whitelist)
	}
	// 迁移规则：默认豁免 + 欢迎 + 关键词升级 + 关键词warn + 关键词mute + 拒绝申请 + 同意邀请 + 拒绝邀请
	// （flood 被 runtime 覆盖为 disabled，不生成）
	names := ruleNames(g.Rules)
	if len(g.Rules) < 7 {
		t.Fatalf("规则数量不足: %v", names)
	}
	if names[0] != "默认豁免" {
		t.Fatalf("豁免规则必须置顶: %v", names)
	}
	welcome := findRule(t, g.Rules, "新人欢迎")
	if welcome.Event != rules.EventGroupIncrease || len(welcome.When) != 0 {
		t.Fatalf("欢迎规则错误: %+v", welcome)
	}
	up := findRule(t, g.Rules, "关键词-广告升级禁言")
	upAct, _ := rules.DecodeRuleParams(false, up.Then[0].Type, up.Then[0].Params)
	if upAct.(*rules.MuteParams).Minutes != 30 {
		t.Fatalf("升级禁言分钟错误: %+v", upAct)
	}
	kw := findRule(t, g.Rules, "关键词-广告")
	if len(kw.When) != 1 || kw.When[0].Type != "text_contains" {
		t.Fatalf("warn 规则条件错误: %+v", kw.When)
	}
	if len(kw.Then) != 2 || kw.Then[1].Type != "increment_counter" {
		t.Fatalf("warn 规则动作错误（应含计数）: %+v", kw.Then)
	}
	mute := findRule(t, g.Rules, "关键词-开挂")
	cond, _ := rules.DecodeRuleParams(true, mute.When[0].Type, mute.When[0].Params)
	if mute.When[0].Type != "text_regex" || cond.(*rules.TextRegexParams).Pattern != "开挂" {
		t.Fatalf("mute 正则规则错误: %+v", mute.When)
	}
	rej := findRule(t, g.Rules, "拒绝申请-中介")
	if rej.Event != rules.EventGroupRequest {
		t.Fatalf("拒绝申请规则错误: %+v", rej)
	}
	// 群 2：禁用群也保留（无规则）
	if len(c.Groups) != 2 || c.Groups[1].GroupID != 333333333 || c.Groups[1].Enabled {
		t.Fatalf("禁用群未迁移: %+v", c.Groups)
	}
	if len(c.Groups[1].Rules) != 0 {
		t.Fatalf("禁用群不应生成规则: %v", ruleNames(c.Groups[1].Rules))
	}

	// 迁移结果通过 ConfigService 提交后生效配置正常
	svc, err := Open(dir, keys)
	if err != nil {
		t.Fatal(err)
	}
	migrated := c
	if _, err := svc.Update("migration", 0, func(c *Control) error {
		*c = *migrated // 迁移候选直接作为首版提交
		return nil
	}, "迁移初始化"); err != nil {
		t.Fatalf("迁移提交失败: %v", err)
	}
	eff := svc.Effective()
	if eff.Bot.Name != "群管小助手" || eff.OneBot.AccessToken != "plain-onebot-token" {
		t.Fatalf("生效配置错误: %+v", eff)
	}
	if eff.Bot.Loc == nil {
		t.Fatal("时区未加载")
	}
	g1 := eff.Group(111111111)
	if g1 == nil || len(g1.Rules) != len(migrated.Groups[0].Rules) {
		t.Fatalf("生效群配置与迁移候选不一致")
	}
	if g1.Rules[0].Name != "默认豁免" {
		t.Fatal("生效配置规则未传递")
	}
}

func TestMigrateMissingConfig(t *testing.T) {
	dir := t.TempDir()
	if _, err := Migrate(MigrateOptions{DataDir: dir, ConfigPath: filepath.Join(dir, "nope.yaml"), Keys: NewTestMasterKey()}); err == nil {
		t.Fatal("缺失旧配置应报错")
	}
	if _, err := Migrate(MigrateOptions{DataDir: dir, ConfigPath: "", Keys: NewTestMasterKey()}); err == nil {
		t.Fatal("空路径应报错")
	}
}

func TestMigrateInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("::not yaml::"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(MigrateOptions{DataDir: dir, ConfigPath: path, Keys: NewTestMasterKey()}); err == nil {
		t.Fatal("无效 YAML 应报错，不得静默当空配置")
	}
}

func TestMigrateCorruptRuntimeFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	goodYAML := "bot:\n  owner: 10001\nonebot:\n  ws_url: ws://127.0.0.1:3001\n"
	if err := os.WriteFile(path, []byte(goodYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runtime.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(MigrateOptions{DataDir: dir, ConfigPath: path, Keys: NewTestMasterKey()}); err == nil {
		t.Fatal("损坏的 runtime.json 应中止迁移")
	}
}

func TestMigrateInvalidResultRejected(t *testing.T) {
	dir := t.TempDir()
	// 群号重复 + 非法正则
	badYAML := `
bot:
  owner: 10001
onebot:
  ws_url: ws://127.0.0.1:3001
groups:
  - group_id: 1
    enabled: true
    keyword:
      enabled: true
      rules:
        - pattern: "["
          regex: true
          action: warn
  - group_id: 1
    enabled: true
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(badYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Migrate(MigrateOptions{DataDir: dir, ConfigPath: path, Keys: NewTestMasterKey()})
	if err == nil {
		t.Fatal("非法迁移结果应被拒绝")
	}
}

func TestMigrateRequiresMasterKey(t *testing.T) {
	dir := t.TempDir()
	path := writeOldSetup(t, dir)
	if _, err := Migrate(MigrateOptions{DataDir: dir, ConfigPath: path}); err == nil {
		t.Fatal("无主密钥应报错")
	}
}

// v1 → v2 启动自动迁移：完整 v1 control.json 打开后转换为规则并写回。
func TestV1ControlAutoMigrate(t *testing.T) {
	dir := t.TempDir()
	keys := NewTestMasterKey()

	// 构造 v1 control.json（模拟旧版本写盘结果）
	v1 := &v1Control{
		SchemaVersion: 1,
		Revision:      7,
		System: SystemConfig{
			BotName: "机器人", Owner: 10001,
			OneBot: OneBotConfig{WSURL: "ws://127.0.0.1:3001", APITimeoutMs: 5000},
		},
		Groups: []v1GroupConfig{
			{
				GroupID: 111111111, Enabled: true, Remark: "主群",
				Whitelist: []int64{222222222},
				Welcome:   &WelcomeConfig{Enabled: true, Message: "欢迎 {nickname}"},
				Keyword: &KeywordConfig{
					Enabled: true, WarnLimit: 3, StrikeTTLH: 24,
					Rules: []KeywordRule{{Pattern: "广告", Action: "warn"}},
				},
				Flood: &FloodConfig{
					Enabled: true, WindowSeconds: 10, MaxMessages: 8,
					MuteMinutes: 10, KickOnRepeat: true, KickThreshold: 3,
				},
				JoinRequest: &JoinRequestConfig{
					AutoApprove: true, Keyword: "中介", RejectText: "不允许中介",
				},
			},
		},
		History: []v1History{{
			Revision: 6, Time: mustParseTime("2026-08-01T10:00:00Z"),
			Actor: "admin", Summary: "旧配置",
			Config: &v1Control{SchemaVersion: 1, Revision: 6,
				System: SystemConfig{
					BotName: "机器人", Owner: 10001,
					OneBot: OneBotConfig{WSURL: "ws://127.0.0.1:3001", APITimeoutMs: 5000},
				},
				Groups: []v1GroupConfig{{
					GroupID: 111111111, Enabled: true,
					Welcome: &WelcomeConfig{Enabled: true, Message: "欢迎 {nickname}"},
				}},
			},
		}},
	}
	data, err := jsonMarshalIndent(v1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "control.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	svc, err := Open(dir, keys)
	if err != nil {
		t.Fatalf("v1 自动迁移失败: %v", err)
	}
	cur := svc.Current()
	if cur.SchemaVersion != 2 {
		t.Fatalf("迁移后 schema 版本错误: %d", cur.SchemaVersion)
	}
	if cur.Revision != 7 {
		t.Fatalf("迁移不应改变 revision: %d", cur.Revision)
	}
	g := cur.Groups[0]
	names := ruleNames(g.Rules)
	// 豁免 + 欢迎 + 广告升级 + 广告warn + 刷屏升级 + 刷屏 + 自动同意 + 同意邀请 + 拒绝邀请
	if len(g.Rules) < 9 {
		t.Fatalf("规则数量不足: %v", names)
	}
	findRule(t, g.Rules, "默认豁免")
	findRule(t, g.Rules, "新人欢迎")
	findRule(t, g.Rules, "关键词-广告升级禁言")
	findRule(t, g.Rules, "刷屏多次移出")
	findRule(t, g.Rules, "刷屏检测")
	findRule(t, g.Rules, "自动同意申请")
	// 历史已转换
	if len(cur.History) != 1 || cur.History[0].Revision != 6 {
		t.Fatalf("历史转换失败: %+v", cur.History)
	}
	if cur.History[0].Config.SchemaVersion != 2 {
		t.Fatal("历史快照未转换到 v2")
	}
	// 磁盘已写回 v2
	raw, err := os.ReadFile(filepath.Join(dir, "control.json"))
	if err != nil {
		t.Fatal(err)
	}
	re, err := DecodeControl(raw)
	if err != nil || re.SchemaVersion != 2 {
		t.Fatalf("磁盘未写回 v2: %v", err)
	}
	// 再次打开不重复迁移（幂等）
	svc2, err := Open(dir, keys)
	if err != nil {
		t.Fatal(err)
	}
	if svc2.Current().Revision != 7 {
		t.Fatal("重复打开不应改变 revision")
	}
}

// v1 文件损坏时不得误迁移。
func TestV1ControlMigrateCorruptFails(t *testing.T) {
	dir := t.TempDir()
	keys := NewTestMasterKey()
	// schema_version=1 但结构损坏（缺 system.onebot.ws_url 且 schema 字段错误）
	bad := `{"schema_version": 1, "revision": 1, "system": {"bot_name": "x", "owner": 0}}`
	if err := os.WriteFile(filepath.Join(dir, "control.json"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, keys); err == nil {
		t.Fatal("损坏的 v1 文件应报错")
	}
}

func mustParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func jsonMarshalIndent(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	return b, err
}
