//go:build !linux && !darwin

package gitws

import (
	"context"
	"errors"
)

const hostFileLocks = false

func lockHostResource(context.Context, string, string) (func(), error) {
	return nil, errors.New("shared runner workspace locks unsupported on this platform")
}
