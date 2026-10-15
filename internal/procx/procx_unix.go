//go:build !windows

package procx

import (
	"os/exec"
	"syscall"
	"time"
)

// Contain runs cmd in its own process group and, on cancellation, kills the
// whole group, so helpers and children a command spawned cannot outlive a
// fence or a timeout. Output pipes held by stray children are abandoned
// after a grace period instead of blocking forever.
func Contain(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
}

// KillGroup kills whatever is left of cmd's process group. A command that
// exited normally can leave background children behind; exec only kills
// the group while it is still waiting, so callers that need nothing to
// survive must call this after Wait too.
func KillGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
