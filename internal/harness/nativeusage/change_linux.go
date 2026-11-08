//go:build linux

package nativeusage

import (
	"fmt"
	"os"
	"syscall"
)

func fileChange(info os.FileInfo) string {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprint(st.Ctim)
	}
	return ""
}
