//go:build !linux

package codexenv

import (
	"context"
	"os/exec"
)

func platformCheck(context.Context) error { return ErrUnsupported }
func platformCommand(context.Context, Profile, string, string, string, []string, string) (*exec.Cmd, error) {
	return nil, ErrUnsupported
}
