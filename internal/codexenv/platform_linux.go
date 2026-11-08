//go:build linux

package codexenv

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
)

func platformCheck(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		return fmt.Errorf("%w: bubblewrap unavailable", ErrUnsupported)
	}
	return nil
}
func platformCommand(ctx context.Context, p Profile, shot, work, bin string, args []string, cwd string) (*exec.Cmd, error) {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, err
	}
	bin, err = exec.LookPath(bin)
	if err != nil {
		return nil, err
	}
	user, err := filepath.EvalSymlinks(p.UserHome)
	if err != nil {
		return nil, err
	}
	home, err := filepath.EvalSymlinks(p.Home)
	if err != nil {
		return nil, err
	}
	argv := []string{"--die-with-parent", "--unshare-user", "--bind", "/", "/"}
	for _, pair := range [][2]string{
		{filepath.Join(shot, "skills"), filepath.Join(user, ".agents", "skills")},
		{filepath.Join(shot, "empty"), filepath.Join(user, ".agents", "plugins")},
		{filepath.Join(work, "legacy-skills"), filepath.Join(home, "skills")},
		{shot, filepath.Join(home, ".tend-capabilities")},
		{filepath.Join(work, "plugins"), filepath.Join(home, "plugins")},
		{filepath.Join(shot, "config.toml"), filepath.Join(home, "config.toml")},
		{filepath.Join(shot, "AGENTS.md"), filepath.Join(home, "AGENTS.md")},
		{filepath.Join(shot, "AGENTS.override.md"), filepath.Join(home, "AGENTS.override.md")},
	} {
		flag := "--ro-bind"
		if pair[1] == filepath.Join(home, "plugins") || pair[1] == filepath.Join(home, "skills") {
			flag = "--bind"
		}
		argv = append(argv, flag, pair[0], pair[1])
	}
	if cwd != "" {
		argv = append(argv, "--chdir", cwd)
	}
	argv = append(argv, "--", bin)
	argv = append(argv, args...)
	return exec.CommandContext(ctx, bwrap, argv...), nil
}
