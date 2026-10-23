//go:build linux

package updater

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func lockDir(path string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(path, "supervisor.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("updater: another supervisor holds this instance: %w", err)
	}
	return f, nil
}
func unlock(f *os.File) error {
	if f == nil {
		return nil
	}
	return f.Close()
}
func supported() error { return nil }
