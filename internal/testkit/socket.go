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

// SocketPath validates the complete leaf, independently of Go's build and
// testing temporary roots. Callers own listener shutdown before test cleanup.
func SocketPath(t testing.TB, leaf string) string {
	t.Helper()
	dir, err := newSocketDir(leaf)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("remove socket fixture directory: %v", err)
		}
	})
	return filepath.Join(dir, leaf)
}

// NewSocketDir is the same isolated allocation for the development sandbox,
// whose lifetime is owned by Run rather than a testing.T cleanup stack.
// The caller must close its listeners and then remove the directory.
func NewSocketDir() (string, error) {
	return newSocketDir("tend-run_" + strings.Repeat("x", 26) + ".sock")
}

func newSocketDir(leaf string) (string, error) {
	roots := []string{os.TempDir()}
	if runtime.GOOS != "windows" {
		roots = append(roots, "/tmp")
	}
	return shortSocketDir(roots, leaf)
}

func shortSocketDir(roots []string, leaf string) (string, error) {
	if leaf == "" || leaf == "." || leaf == ".." || filepath.Base(leaf) != leaf || strings.ContainsAny(leaf, "/\\") {
		return "", fmt.Errorf("test socket requires a single filename")
	}
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
		// Validate the actual filename. MkdirTemp creates mode 0700.
		if err := protocol.ValidateUnixSocketPath("test socket", filepath.Join(dir, leaf)); err != nil {
			_ = os.RemoveAll(dir)
			failures = append(failures, err)
			continue
		}
		return dir, nil
	}
	return "", fmt.Errorf("create short real socket fixture directory: %w", errors.Join(failures...))
}
