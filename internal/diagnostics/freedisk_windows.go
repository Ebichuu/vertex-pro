//go:build windows

package diagnostics

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

// FreeDiskBytes 返回 path 所在卷的可用字节数（Windows 实现）。
func FreeDiskBytes(path string) (uint64, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return 0, err
	}
	root := filepath.VolumeName(absolute) + `\`
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(windows.StringToUTF16Ptr(root), &free, &total, &totalFree); err != nil {
		return 0, err
	}
	return free, nil
}
