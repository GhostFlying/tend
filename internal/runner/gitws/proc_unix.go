//go:build !windows

package gitws

import (
	"os/exec"
	"syscall"
	"time"
)

// contain runs cmd in its own process group and, on cancellation, kills the
// whole group, so helpers and children a command spawned cannot outlive a
// fence or a timeout. Output pipes held by stray children are abandoned
// after a grace period instead of blocking forever.
func contain(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
}
