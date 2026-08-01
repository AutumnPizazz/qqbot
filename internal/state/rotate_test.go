package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRotateSecrets(t *testing.T) {
	dir := t.TempDir()
	oldKey := NewTestMasterKey()
	newKey := NewTestMasterKey()

	svc, err := Open(dir, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update("admin", 0, func(c *Control) error {
		c.System.BotName = "机器人"
		c.System.Owner = 10001
		c.System.OneBot.WSURL = "ws://127.0.0.1:3001"
		tok, err := oldKey.Encrypt("token-a", fieldOneBotToken)
		if err != nil {
			return err
		}
		c.System.OneBot.AccessToken = tok
		nt, err := oldKey.Encrypt("token-n", fieldNapCatToken)
		if err != nil {
			return err
		}
		c.System.NapCat.WebUIURL = "http://127.0.0.1:6099"
		c.System.NapCat.WebUIToken = nt
		return nil
	}, "init"); err != nil {
		t.Fatal(err)
	}
	revBefore := svc.Revision()

	// 轮换
	rotated, err := RotateSecrets(dir, oldKey, newKey)
	if err != nil {
		t.Fatalf("轮换失败: %v", err)
	}
	if len(rotated) != 2 {
		t.Fatalf("应旋转 2 个字段，实际 %v", rotated)
	}
	// 备份存在
	if _, err := os.Stat(filepath.Join(dir, "control.json.pre-rotate")); err != nil {
		t.Fatal("缺少 pre-rotate 备份")
	}

	// 新 key 可解密，旧 key 不可
	svc2, err := Open(dir, newKey)
	if err != nil {
		t.Fatalf("新 key 打开失败: %v", err)
	}
	if svc2.Revision() != revBefore {
		t.Fatal("轮换不应改变 revision")
	}
	cur := svc2.Current()
	if plain, err := newKey.Decrypt(cur.System.OneBot.AccessToken, fieldOneBotToken); err != nil || plain != "token-a" {
		t.Fatalf("新 key 解密失败: %v", err)
	}
	if _, err := oldKey.Decrypt(cur.System.OneBot.AccessToken, fieldOneBotToken); err == nil {
		t.Fatal("旧 key 不应再能解密")
	}
	if plain, err := newKey.Decrypt(cur.System.NapCat.WebUIToken, fieldNapCatToken); err != nil || plain != "token-n" {
		t.Fatalf("NapCat token 解密失败: %v", err)
	}
	// 生效配置正确
	if eff := svc2.Effective(); eff.OneBot.AccessToken != "token-a" {
		t.Fatalf("生效配置 token 错误: %q", eff.OneBot.AccessToken)
	}

	// 幂等：重复轮换（新 key 作为 old）无变化
	rotated2, err := RotateSecrets(dir, newKey, newKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(rotated2) != 0 {
		t.Fatalf("重复轮换应为空: %v", rotated2)
	}
}

func TestRotateSecretsWrongOldKey(t *testing.T) {
	dir := t.TempDir()
	key1 := NewTestMasterKey()
	key2 := NewTestMasterKey()
	key3 := NewTestMasterKey()

	svc, _ := Open(dir, key1)
	if _, err := svc.Update("admin", 0, func(c *Control) error {
		c.System.BotName = "x"
		c.System.Owner = 10001
		c.System.OneBot.WSURL = "ws://127.0.0.1:3001"
		tok, _ := key1.Encrypt("t", fieldOneBotToken)
		c.System.OneBot.AccessToken = tok
		return nil
	}, "init"); err != nil {
		t.Fatal(err)
	}
	// 错误的 old key → 失败且不落盘
	before, _ := os.ReadFile(filepath.Join(dir, "control.json"))
	if _, err := RotateSecrets(dir, key2, key3); err == nil {
		t.Fatal("错误 old key 应失败")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "control.json"))
	if string(before) != string(after) {
		t.Fatal("失败轮换不得修改 control.json")
	}
	// 无敏感字段时返回空
	svc2, _ := Open(t.TempDir(), key1)
	_ = svc2
	empty := t.TempDir()
	svcE, _ := Open(empty, key1)
	if _, err := svcE.Update("admin", 0, func(c *Control) error {
		c.System.BotName = "x"
		c.System.Owner = 10001
		c.System.OneBot.WSURL = "ws://127.0.0.1:3001"
		return nil
	}, "init"); err != nil {
		t.Fatal(err)
	}
	rotated, err := RotateSecrets(empty, key1, key2)
	if err != nil || len(rotated) != 0 {
		t.Fatalf("无敏感字段应返回空: %v %v", rotated, err)
	}
}
