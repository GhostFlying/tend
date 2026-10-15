//go:build windows

package procx

import (
	"os/exec"
	"time"
)

// Contain bounds waiting on output pipes held by stray children. Killing a
// whole process tree on Windows needs a job object; until runners are
// deployed there, only the direct process is killed.
func Contain(cmd *exec.Cmd) {
	cmd.WaitDelay = 5 * time.Second
}

// KillGroup kills the process itself; without a job object its children
// cannot be reached as a group.
func KillGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
