//go:build !linux

package procx

import "os/exec"

// DieWithParent is unavailable here. Coordinator admission rejects platforms
// without the verified process and native tool boundary.
func DieWithParent(_ *exec.Cmd) {}
