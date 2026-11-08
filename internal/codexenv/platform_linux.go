//go:build linux

package codexenv

import (
	"context"
	"os/exec"
)

func platformCheck(ctx context.Context) error {
	return ctx.Err()
}
func platformCommand(ctx context.Context, p Profile, shot, work, bin string, args []string, cwd string) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = cwd
	return cmd, nil
}
