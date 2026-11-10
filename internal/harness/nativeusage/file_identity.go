package nativeusage

import "os"

// FileIdentity returns a platform file identity used to detect atomic native
// transcript replacement. It is local metadata and must not leave the runner.
func FileIdentity(file *os.File, info os.FileInfo) string { return fileIdentity(file, info) }
