//go:build !linux

package nativeusage

import "os"

// Do not reuse headers without a platform change-time proof.
func fileChange(info os.FileInfo) string { return "" }
