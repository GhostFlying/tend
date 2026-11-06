//go:build linux || darwin

package journal

import (
	"os"
	"syscall"
)

func lockWriter(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func kernelFileIdentity(file *os.File) (uint64, uint64, error) {
	info, err := file.Stat()
	if err != nil {
		return 0, 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, syscall.EINVAL
	}
	return uint64(stat.Dev), uint64(stat.Ino), nil
}
