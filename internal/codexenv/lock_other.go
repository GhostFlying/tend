//go:build !linux

package codexenv

import "context"

func lock(context.Context, string) (func(), error) { return nil, ErrUnsupported }
