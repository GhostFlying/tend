//go:build !linux

package maintenance

import (
	"errors"
	"os"
)

func lockState(string) (*os.File, error) {
	return nil, errors.New("maintenance: service requires Linux")
}
