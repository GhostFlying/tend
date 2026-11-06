//go:build !linux && !darwin && !windows

package journal

import (
	"errors"
	"os"
)

func lockWriter(*os.File) error {
	return errors.New("exclusive journal ownership is unsupported on this platform")
}

func kernelFileIdentity(*os.File) (uint64, uint64, error) {
	return 0, 0, errors.New("journal kernel identity is unsupported on this platform")
}
