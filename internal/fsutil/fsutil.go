// Package fsutil 提供文件系统小工具（原子写盘）。
package fsutil

import (
	"os"
	"path/filepath"
)

// WriteFileAtomic 先写入同目录临时文件（指定权限），fsync 后原子 rename，
// 并同步目录项（Windows/部分文件系统不支持，忽略即可）。
// 避免进程中断留下半文件；覆盖目标前不破坏原文件。
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := file.Name()
	defer func() {
		_ = file.Close()
		_ = os.Remove(tmp)
	}()
	if err = file.Chmod(perm); err != nil {
		return err
	}
	if _, err = file.Write(data); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	fsyncDir(dir)
	return nil
}

// fsyncDir 同步目录项（rename 落盘）。Windows/部分文件系统不支持，忽略即可。
func fsyncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}
