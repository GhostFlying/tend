//go:build !windows

package runner

import (
	"os"
	"syscall"
)

func openEvidenceFile(path string) (*os.File, error) {
	// O_NONBLOCK prevents a path replaced by a FIFO after the preflight stat
	// from blocking the open. The caller verifies the actual opened type.
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
