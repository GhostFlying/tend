//go:build linux

package procx

import (
	"os/exec"
	"syscall"
)

// DieWithParent prevents a coordinator process from continuing after a runner
// crash. Call after Contain and before Start. Native coordinator work cannot
// detach itself or spawn execution workers outside the task ledger.
func DieWithParent(cmd *exec.Cmd) {
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
}
