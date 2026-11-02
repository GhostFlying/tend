//go:build linux || darwin

package nativeusage

import (
	"fmt"
	"os"
	"syscall"
)

func fileIdentity(_ *os.File, info os.FileInfo) string {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("%d:%d", uint64(stat.Dev), uint64(stat.Ino))
	}
	return ""
}
