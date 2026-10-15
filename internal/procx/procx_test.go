//go:build !windows

package procx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestKillGroupReachesLeftoverChildren(t *testing.T) {
	late := filepath.Join(t.TempDir(), "late")
	cmd := exec.CommandContext(context.Background(), "sh", "-c", `(sleep 1; touch "$LATE") >/dev/null 2>&1 &`)
	cmd.Env = append(os.Environ(), "LATE="+late)
	Contain(cmd)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	// The shell exited; its background child is still in the group.
	KillGroup(cmd)
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(late); !errors.Is(err, os.ErrNotExist) {
		t.Error("a child left behind kept running after KillGroup")
	}
}
