//go:build windows

package gitws

import (
	"os/exec"
	"time"
)

// contain bounds waiting on output pipes held by stray children. Killing a
// whole process tree on Windows needs a job object; until runners are
// deployed there, only the direct process is killed.
func contain(cmd *exec.Cmd) {
	cmd.WaitDelay = 5 * time.Second
}
