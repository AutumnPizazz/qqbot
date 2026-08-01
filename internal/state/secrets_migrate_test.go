package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// buildV1Control 构造带 v1 AAD 密文的 v1 配置（模拟旧版本写盘结果）。
func buildV1Control(t *testing.T, keys *MasterKey, rev int64) *v1Control {
	t.Helper()
	// 用 v1 AAD 加密（旧版本行为）
	obToken, err := keys.encrypt("legacy-onebot-token", fieldOneBotToken, 1)
	if err != nil {
		t.Fatal(err)
	}
	ncToken, err := keys.encrypt("legacy-napcat-token", fieldNapCatToken, 1)
	if err != nil {
		t.Fatal(err)
	}
	return &v1Control{
		SchemaVersion: 1,
		Revision:      rev,
		System: SystemConfig{
			BotName: "旧机器人", Owner: 10001,
			OneBot: OneBotConfig{WSURL: "ws://127.0.0.1:3001", APITimeoutMs: 5000,
				AccessToken: obToken},
			NapCat: NapCatConfig{WebUIURL: "http://127.0.0.1:6099", WebUIToken: ncToken},
		},
		Groups: []v1GroupConfig{{
			GroupID: 123456789, Enabled: true,
			Keyword: &KeywordConfig{Enabled: true,
				Rules: []KeywordRule{{Pattern: "广告", Action: "warn"}}},
		}},
		History: []v1History{{
			Revision: rev - 1, Time: mustParseTime("2026-08-01T10:00:00Z"),
			Actor: "admin", Summary: "旧配置",
			Config: &v1Control{SchemaVersion: 1, Revision: rev - 1,
				System: SystemConfig{
					BotName: "旧机器人", Owner: 10001,
					OneBot: OneBotConfig{WSURL: "ws://127.0.0.1:3001", APITimeoutMs: 5000,
						AccessToken: obToken},
				},
				Groups: []v1GroupConfig{{GroupID: 123456789, Enabled: true}},
			},
		}},
	}
}

// 迁移后 v1 密文可解密（v1 AAD → 重加密为当前版本）。
func TestV1MigrateReencryptsTokens(t *testing.T) {
	dir := t.TempDir()
	keys := NewTestMasterKey()
	v1 := buildV1Control(t, keys, 9)
	data, err := json.MarshalIndent(v1, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "control.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	svc, err := Open(dir, keys)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	// 迁移后 token 可解密且值正确
	eff := svc.Effective()
	if eff.OneBot.AccessToken != "legacy-onebot-token" {
		t.Fatalf("OneBot token 解密错误: %q", eff.OneBot.AccessToken)
	}
	// 磁盘上的密文已是当前版本 AAD（用 v1 AAD 解不开）
	raw, _ := os.ReadFile(filepath.Join(dir, "control.json"))
	var cur Control
	if err := json.Unmarshal(raw, &cur); err != nil {
		t.Fatal(err)
	}
	plain, legacy, err := keys.DecryptCompat(cur.System.OneBot.AccessToken, fieldOneBotToken)
	if err != nil || plain != "legacy-onebot-token" {
		t.Fatalf("磁盘密文无法解密: %v %q", err, plain)
	}
	if legacy {
		t.Fatal("磁盘密文仍为 v1 AAD（应已重加密）")
	}
	// 历史快照也修复了
	if len(cur.History) != 1 || cur.History[0].Config == nil {
		t.Fatal("历史快照缺失")
	}
	p2, legacy2, err := keys.DecryptCompat(cur.History[0].Config.System.OneBot.AccessToken, fieldOneBotToken)
	if err != nil || p2 != "legacy-onebot-token" || legacy2 {
		t.Fatalf("历史快照密文未修复: %v %q legacy=%v", err, p2, legacy2)
	}
	// 再次打开（幂等，不重复迁移）
	svc2, err := Open(dir, keys)
	if err != nil {
		t.Fatal(err)
	}
	if svc2.Effective().OneBot.AccessToken != "legacy-onebot-token" {
		t.Fatal("重复打开后 token 不可读")
	}
}

// 已损坏的中间态：v2 文件 + v1 AAD 密文（迁移写盘成功但解密失败）→ 自动修复。
func TestV2WithLegacyCipherAutoRepair(t *testing.T) {
	dir := t.TempDir()
	keys := NewTestMasterKey()
	v1 := buildV1Control(t, keys, 5)
	// 直接构造 v2 结构（schema_version=2）但保留 v1 密文
	c := &Control{
		SchemaVersion: 2,
		Revision:      v1.Revision,
		UpdatedAt:     v1.UpdatedAt,
		System:        v1.System,
	}
	for _, g := range v1.Groups {
		c.Groups = append(c.Groups, GroupConfig{
			GroupID: g.GroupID, Enabled: g.Enabled,
			Whitelist: g.Whitelist,
		})
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "control.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	svc, err := Open(dir, keys)
	if err != nil {
		t.Fatalf("自动修复失败: %v", err)
	}
	eff := svc.Effective()
	if eff.OneBot.AccessToken != "legacy-onebot-token" {
		t.Fatalf("修复后 token 不可读: %q", eff.OneBot.AccessToken)
	}
	// 磁盘已重加密
	raw, _ := os.ReadFile(filepath.Join(dir, "control.json"))
	var cur Control
	if err := json.Unmarshal(raw, &cur); err != nil {
		t.Fatal(err)
	}
	if _, legacy, err := keys.DecryptCompat(cur.System.OneBot.AccessToken, fieldOneBotToken); err != nil || legacy {
		t.Fatalf("磁盘密文未重加密: legacy=%v err=%v", legacy, err)
	}
}

// 密钥确实不匹配时仍应报错（不得被误修复掩盖）。
func TestOpenWrongKeyStillFails(t *testing.T) {
	dir := t.TempDir()
	keys := NewTestMasterKey()
	v1 := buildV1Control(t, keys, 3)
	data, _ := json.MarshalIndent(v1, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "control.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	// 用另一个密钥打开：迁移后密文无法解密（key_id 不匹配），应报错而非静默修复
	other := NewTestMasterKey()
	if _, err := Open(dir, other); err == nil {
		t.Fatal("密钥不匹配应报错")
	}
}
