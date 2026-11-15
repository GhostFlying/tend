//go:build !windows

package main

import (
	"os/exec"
	"syscall"

	"github.com/GhostFlying/tend/internal/procx"
)

// Let the coordinator report cancellation before final group cleanup. Its
// commands inherit this group, so none can detach from the owning check task.
func containTask(cmd *exec.Cmd) {
	procx.Contain(cmd)
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
}
