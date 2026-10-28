package gitws

import (
	"fmt"
	"os"
	"path/filepath"
)

// prepareScratch resolves aliases once, so creation and the worker brief refer
// to the same directory. It does not run a per-launch sandbox write probe.
func prepareScratch(path string) (string, error) {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return "", fmt.Errorf("gitws: prepare scratch: %w", err)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("gitws: absolute scratch: %w", err)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("gitws: resolve scratch: %w", err)
	}
	return path, nil
}
