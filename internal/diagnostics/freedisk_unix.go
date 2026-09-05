//go:build !windows

package diagnostics

import (
	"path/filepath"
	"syscall"
)

// FreeDiskBytes 返回 path 所在文件系统的可用字节数（POSIX 实现）。
func FreeDiskBytes(path string) (uint64, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return 0, err
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(filepath.Dir(absolute), &stat); err != nil {
		return 0, err
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), nil
}
