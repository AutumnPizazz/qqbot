package state

import (
	"qqbot/internal/fsutil"
)

// writeFileAtomic 原子写盘（0600）：先写临时文件，fsync 后 rename，失败不影响原文件。
// 统一实现见 internal/fsutil（state 与 bot 共用）。
func writeFileAtomic(path string, data []byte) error {
	return fsutil.WriteFileAtomic(path, data, 0o600)
}
