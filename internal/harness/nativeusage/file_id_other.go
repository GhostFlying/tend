//go:build !linux && !darwin && !windows

package nativeusage

import "os"

func fileIdentity(*os.File, os.FileInfo) string { return "" }
