//go:build linux

package updater

import (
	"errors"
	"os"
	"syscall"
)

// BoundedDataFileIdentity returns the actual regular file's kernel identity.
func BoundedDataFileIdentity(path string) (uint64, uint64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, 0, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || !ownedFile(info) {
		return 0, 0, errors.New("updater: bounded data must be an owned regular file")
	}
	return uint64(st.Dev), st.Ino, nil
}
