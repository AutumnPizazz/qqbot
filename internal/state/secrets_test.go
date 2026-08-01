package state

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMasterKeyRoundTrip(t *testing.T) {
	key := NewTestMasterKey()
	v, err := key.Encrypt("secret-token", fieldOneBotToken)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	if v.KeyID != key.KeyID() {
		t.Fatalf("key_id 不匹配: %s != %s", v.KeyID, key.KeyID())
	}
	if v.Encrypted == "secret-token" || strings.Contains(v.Encrypted, "secret-token") {
		t.Fatal("密文泄露明文")
	}
	plain, err := key.Decrypt(v, fieldOneBotToken)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if plain != "secret-token" {
		t.Fatalf("往返结果不一致: %q", plain)
	}
}

func TestMasterKeyRandomNonce(t *testing.T) {
	key := NewTestMasterKey()
	v1, _ := key.Encrypt("same", fieldOneBotToken)
	v2, _ := key.Encrypt("same", fieldOneBotToken)
	if v1.Encrypted == v2.Encrypted {
		t.Fatal("两次加密同一明文产生了相同密文（nonce 未随机化）")
	}
}

func TestMasterKeyAADPreventsCrossField(t *testing.T) {
	key := NewTestMasterKey()
	v, _ := key.Encrypt("token-a", fieldOneBotToken)
	// 用错误的字段路径解密必须失败
	if _, err := key.Decrypt(v, fieldNapCatToken); err == nil {
		t.Fatal("跨字段解密未被 AAD 拦截")
	}
	// 篡改密文必须失败
	bad := *v
	bad.Encrypted = "AAAA" + v.Encrypted[4:]
	if _, err := key.Decrypt(&bad, fieldOneBotToken); err == nil {
		t.Fatal("篡改密文未被检测")
	}
}

func TestMasterKeyKeyIDMismatch(t *testing.T) {
	key1 := NewTestMasterKey()
	key2 := NewTestMasterKey()
	v, _ := key1.Encrypt("secret", fieldOneBotToken)
	if _, err := key2.Decrypt(v, fieldOneBotToken); err == nil {
		t.Fatal("不同主密钥解密未报错")
	}
}

func TestMasterKeyFileLoad(t *testing.T) {
	dir := t.TempDir()
	// hex 编码
	key := NewTestMasterKey()
	hexPath := filepath.Join(dir, "key.hex")
	if err := os.WriteFile(hexPath, []byte(hex.EncodeToString(key.key)), 0o600); err != nil {
		t.Fatal(err)
	}
	k1, err := LoadMasterKey(hexPath)
	if err != nil {
		t.Fatalf("hex 加载失败: %v", err)
	}
	if k1.KeyID() != key.KeyID() {
		t.Fatal("hex 加载的密钥不匹配")
	}

	// 无效内容
	badPath := filepath.Join(dir, "key.bad")
	if err := os.WriteFile(badPath, []byte("not-a-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMasterKey(badPath); err == nil {
		t.Fatal("无效密钥未报错")
	}
	// 不存在
	if _, err := LoadMasterKey(filepath.Join(dir, "nope")); err == nil {
		t.Fatal("缺失密钥文件未报错")
	}
}

func TestEncryptedValueConfigured(t *testing.T) {
	var v *EncryptedValue
	if v.Configured() {
		t.Fatal("nil 应视为未配置")
	}
	v = &EncryptedValue{}
	if v.Configured() {
		t.Fatal("空值应视为未配置")
	}
	v = &EncryptedValue{Encrypted: "abc", KeyID: "k1"}
	if !v.Configured() {
		t.Fatal("非空应视为已配置")
	}
}
