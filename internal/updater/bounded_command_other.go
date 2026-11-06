//go:build !linux

package updater

import "context"

func boundedCommand(context.Context, Config, string, string, string, string, uint64, string) (any, error) {
	return nil, ErrUnsupported
}
