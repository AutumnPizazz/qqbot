package state

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// MasterKey 管理独立主密钥文件（只读，不写入 control.json）。
// 主密钥丢失后无法恢复加密凭据——必须与配置备份分开保存。
type MasterKey struct {
	key   []byte
	keyID string
}

// KeyID 返回主密钥标识（当前为 key 的 sha256 前 8 字节 hex）。
func (k *MasterKey) KeyID() string { return k.keyID }

// LoadMasterKey 从文件加载 32 字节主密钥。
// 文件内容支持两种编码：
//   - 64 位 hex（128 字符）→ hex 解码
//   - base64（44 字符）→ base64 解码
//
// 文件权限建议 0600；非本用户可读时仅告警（Windows 无 POSIX 权限语义）。
func LoadMasterKey(path string) (*MasterKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取主密钥文件失败: %w", err)
	}
	key, err := decodeKeyBytes(data)
	if err != nil {
		return nil, fmt.Errorf("主密钥文件 %s 格式无效: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		// 非致命：只读文件系统或 Windows 上可能失败
		_ = err
	}
	sum := sha256.Sum256(key)
	return &MasterKey{key: key, keyID: hex.EncodeToString(sum[:4])}, nil
}

func decodeKeyBytes(data []byte) ([]byte, error) {
	s := strings.TrimSpace(string(data))
	if s == "" {
		return nil, errors.New("文件为空")
	}
	if len(s) == 64 {
		key, err := hex.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("hex 解码失败: %w", err)
		}
		return key, nil
	}
	key, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(key) != 32 {
		return nil, errors.New("需要 32 字节密钥（64 位 hex 或 base64）")
	}
	return key, nil
}

// aad 构造 AEAD 附加认证数据：包含 schema 版本、key ID 与字段路径。
// 防止密文被移动到其他字段/版本复用。
func (k *MasterKey) aad(fieldPath string) []byte {
	return k.aadFor(SchemaVersion, fieldPath)
}

// aadFor 构造指定 schema 版本的 AEAD 附加认证数据（v1→v2 迁移兼容：
// 旧密文按 v1 版本解密后重加密）。
func (k *MasterKey) aadFor(version int, fieldPath string) []byte {
	return []byte(fmt.Sprintf("qqbot-control\x00v%d\x00%s\x00%s",
		version, k.keyID, fieldPath))
}

// Encrypt 加密明文并返回 EncryptedValue（每次生成随机 nonce）。
// fieldPath 是字段路径，如 "system.onebot.access_token"。
func (k *MasterKey) Encrypt(plain, fieldPath string) (*EncryptedValue, error) {
	return k.encrypt(plain, fieldPath, SchemaVersion)
}

// encrypt 按指定 schema 版本构造 AAD 加密（v1 分支供迁移兼容）。
func (k *MasterKey) encrypt(plain, fieldPath string, version int) (*EncryptedValue, error) {
	block, err := aes.NewCipher(k.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("生成 nonce 失败: %w", err)
	}
	ct := gcm.Seal(nil, nonce, []byte(plain), k.aadFor(version, fieldPath))
	payload := append(nonce, ct...)
	return &EncryptedValue{
		Encrypted: base64.RawStdEncoding.EncodeToString(payload),
		KeyID:     k.keyID,
	}, nil
}

// Decrypt 解密 EncryptedValue（当前 schema 版本 AAD，含 v1 回退兼容）。
func (k *MasterKey) Decrypt(v *EncryptedValue, fieldPath string) (string, error) {
	plain, _, err := k.DecryptCompat(v, fieldPath)
	return plain, err
}

// DecryptCompat 解密；优先当前 schema 版本 AAD，失败时回退 v1 AAD
// （v1→v2 自动迁移后旧密文仍可读）。
// 返回 (明文, 是否使用了旧版本 AAD)；keyID 不匹配直接报错。
func (k *MasterKey) DecryptCompat(v *EncryptedValue, fieldPath string) (string, bool, error) {
	if v == nil || v.Encrypted == "" {
		return "", false, nil
	}
	if v.KeyID != k.keyID {
		return "", false, fmt.Errorf("密钥不匹配（密文 key_id=%s，当前 %s）", v.KeyID, k.keyID)
	}
	plain, err := k.open(v, fieldPath, SchemaVersion)
	if err == nil {
		return plain, false, nil
	}
	if SchemaVersion != 1 {
		if legacy, err2 := k.open(v, fieldPath, 1); err2 == nil {
			return legacy, true, nil
		}
	}
	return "", false, err
}

// open 按指定 schema 版本 AAD 解密。
func (k *MasterKey) open(v *EncryptedValue, fieldPath string, version int) (string, error) {
	payload, err := base64.RawStdEncoding.DecodeString(v.Encrypted)
	if err != nil {
		return "", fmt.Errorf("密文解码失败: %w", err)
	}
	block, err := aes.NewCipher(k.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(payload) < gcm.NonceSize() {
		return "", errors.New("密文长度无效")
	}
	nonce, ct := payload[:gcm.NonceSize()], payload[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ct, k.aadFor(version, fieldPath))
	if err != nil {
		return "", fmt.Errorf("解密失败（主密钥与配置不匹配？）: %w", err)
	}
	return string(plain), nil
}

// NewTestMasterKey 生成随机主密钥（仅测试用）。
func NewTestMasterKey() *MasterKey {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	sum := sha256.Sum256(key)
	return &MasterKey{key: key, keyID: hex.EncodeToString(sum[:4])}
}
