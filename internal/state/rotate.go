package state

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// RotateSecrets 用新主密钥重加密 control.json 中的全部敏感字段
// （OneBot AccessToken、NapCat WebUI token）。
//
// 约束（设计文档 19 节）：
//   - 默认要求服务停机（避免跨进程同时写文件）。
//   - 元数据（revision/updated_at/history）保持不变，只替换密文。
//   - 先解密全部 secret 成功后再重加密与原子替换；任一步失败不落盘。
//   - 已使用新 key 加密的字段（KeyID 匹配）跳过，操作幂等。
//   - 操作前自动备份原文件为 control.json.pre-rotate。
//
// 返回被旋转的字段路径列表。
func RotateSecrets(dataDir string, oldKey, newKey *MasterKey) ([]string, error) {
	path := filepath.Join(dataDir, "control.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 control.json 失败: %w", err)
	}
	c, err := DecodeControl(raw)
	if err != nil {
		return nil, fmt.Errorf("解析 control.json 失败: %w", err)
	}

	// 1. 收集待旋转字段（先全部解密，避免半途失败）
	type field struct {
		path string
		val  **EncryptedValue
	}
	var fields []field
	fields = append(fields, field{fieldOneBotToken, &c.System.OneBot.AccessToken})
	fields = append(fields, field{fieldNapCatToken, &c.System.NapCat.WebUIToken})

	type decrypted struct {
		path string
		val  *EncryptedValue
	}
	var done []decrypted
	for _, f := range fields {
		v := *f.val
		if v == nil || v.Encrypted == "" {
			continue // 未配置
		}
		if v.KeyID == newKey.KeyID() {
			continue // 已使用新 key（幂等）
		}
		plain, err := oldKey.Decrypt(v, f.path)
		if err != nil {
			return nil, fmt.Errorf("解密 %s 失败（old-key 与配置不匹配？）: %w", f.path, err)
		}
		enc, err := newKey.Encrypt(plain, f.path)
		if err != nil {
			return nil, fmt.Errorf("重加密 %s 失败: %w", f.path, err)
		}
		done = append(done, decrypted{f.path, enc})
	}
	if len(done) == 0 {
		return nil, nil // 没有需要旋转的字段
	}

	// 2. 应用新密文
	for _, d := range done {
		switch d.path {
		case fieldOneBotToken:
			c.System.OneBot.AccessToken = d.val
		case fieldNapCatToken:
			c.System.NapCat.WebUIToken = d.val
		}
	}

	// 3. 原子替换（先备份）
	data, err := EncodeControl(c)
	if err != nil {
		return nil, fmt.Errorf("序列化失败: %w", err)
	}
	if err := os.WriteFile(path+".pre-rotate", raw, 0o600); err != nil {
		return nil, fmt.Errorf("备份失败: %w", err)
	}
	if err := writeFileAtomic(path, data); err != nil {
		return nil, fmt.Errorf("写盘失败（control.json 未变更，请用 .pre-rotate 恢复）: %w", err)
	}
	rotated := make([]string, 0, len(done))
	for _, d := range done {
		rotated = append(rotated, d.path)
	}
	slog.Info("密钥轮换完成", "rotated", rotated, "backup", path+".pre-rotate", "at", time.Now())
	return rotated, nil
}
