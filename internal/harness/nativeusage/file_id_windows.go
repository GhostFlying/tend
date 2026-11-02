//go:build windows

package nativeusage

import (
	"fmt"
	"os"
	"syscall"
)

func fileIdentity(file *os.File, _ os.FileInfo) string {
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(file.Fd()), &info); err != nil {
		return ""
	}
	return fmt.Sprintf("%d:%d:%d", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow)
}
