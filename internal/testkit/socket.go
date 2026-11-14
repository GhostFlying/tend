package testkit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

// SocketDir creates a real, private, unique directory for local IPC fixtures.
// Data, reports and build artifacts should continue to use their own TempDir.
// Register listener/process cleanup after this call so it runs before removal.
func SocketDir(t testing.TB) string {
	t.Helper()
	dir, err := NewSocketDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("remove socket fixture directory: %v", err)
		}
	})
	return dir
}

// NewSocketDir is the same isolated allocation for the development sandbox,
// whose lifetime is owned by Run rather than a testing.T cleanup stack.
// The caller must close its listeners and then remove the directory.
func NewSocketDir() (string, error) {
	roots := []string{os.TempDir()}
	if runtime.GOOS != "windows" {
		roots = append(roots, "/tmp")
	}
	return shortSocketDir(roots)
}

func shortSocketDir(roots []string) (string, error) {
	var failures []error
	for _, root := range roots {
		absolute, err := filepath.Abs(root)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		real, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		dir, err := os.MkdirTemp(real, "ts-")
		if err != nil {
			failures = append(failures, err)
			continue
		}
		// Leave room for the existing run-scoped bridge names as well as
		// updater's default control.sock. MkdirTemp creates mode 0700.
		if err := protocol.ValidateUnixSocketPath("test socket", filepath.Join(dir, "tend-run_"+strings.Repeat("x", 26)+".sock")); err != nil {
			_ = os.RemoveAll(dir)
			failures = append(failures, err)
			continue
		}
		return dir, nil
	}
	return "", fmt.Errorf("create short real socket fixture directory: %w", errors.Join(failures...))
}
