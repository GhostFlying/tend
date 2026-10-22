package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

// configHeader starts every agent configuration file tend writes; a file
// without it is the repository's or the owner's and is never touched.
const configHeader = "# Written by tend-runner for the run in this worktree; removed when the run ends.\n"

// configPaths are where each agent reads project-level configuration,
// relative to the worktree. traex's TUI is traecli and reads its own file.
var configPaths = map[protocol.AgentKind]string{
	protocol.AgentCodex: ".codex/config.toml",
	protocol.AgentTraex: ".trae/traecli.toml",
}

// Install gives the agent that will run in the worktree at dir an MCP
// server for its run's tools: command with args, which must end up
// running ServeMCP with the state file and run it is given. The state goes into the
// worktree's own git directory, so it is never part of the work, and the
// configuration file is added to the repository's exclude file, which
// linked worktrees share. Install may run again for the same worktree.
func Install(ctx context.Context, dir string, agent protocol.AgentKind, command string, args []string, st State) error {
	return InstallPrivate(ctx, dir, agent, command, args, st, "")
}

// InstallPrivate keeps credentials outside the workspace when privateDir is supplied.
func InstallPrivate(ctx context.Context, dir string, agent protocol.AgentKind, command string, args []string, st State, privateDir string) error {
	if st.Run == "" || st.Socket == "" || st.Token == "" {
		return errors.New("bridge: the state needs a run, a socket and a token")
	}
	rel, ok := configPaths[agent]
	if !ok {
		return fmt.Errorf("bridge: no MCP configuration is known for agent %q", agent)
	}
	cfg := filepath.Join(dir, rel)
	if err := checkOwn(cfg); err != nil {
		return err
	}
	var state string
	var err error
	if privateDir == "" {
		state, err = StatePath(ctx, dir)
	}
	if privateDir != "" {
		err = os.MkdirAll(privateDir, 0o700)
		state = filepath.Join(privateDir, protocol.WorkerMarker)
	}
	if err != nil {
		return err
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := os.WriteFile(state, b, 0o600); err != nil {
		return fmt.Errorf("bridge: write the run's state: %w", err)
	}
	// Rewriting keeps the mode of an existing file.
	if err := os.Chmod(state, 0o600); err != nil {
		return fmt.Errorf("bridge: restrict the run's state: %w", err)
	}
	if privateDir == "" {
		if err := exclude(ctx, dir, "/"+rel); err != nil {
			return err
		}
	} else if _, err := git(ctx, dir, "rev-parse", "--git-dir"); err == nil {
		if err := exclude(ctx, dir, "/"+rel); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		return fmt.Errorf("bridge: %w", err)
	}
	args = append(slices.Clone(args), "--state", state, "--run", st.Run)
	if err := os.WriteFile(cfg, []byte(config(command, args)), 0o644); err != nil {
		return fmt.Errorf("bridge: write %s: %w", rel, err)
	}
	return nil
}

// Uninstall removes what Install wrote for agent in the worktree at dir,
// except the exclude line. It may run when Install never did, or did
// only in part.
func Uninstall(ctx context.Context, dir string, agent protocol.AgentKind) error {
	return UninstallPrivate(ctx, dir, agent, "")
}

// UninstallPrivate removes only the configuration and private credentials it owns.
func UninstallPrivate(ctx context.Context, dir string, agent protocol.AgentKind, privateDir string) error {
	var errs []error
	if rel, ok := configPaths[agent]; ok {
		cfg := filepath.Join(dir, rel)
		if checkOwn(cfg) == nil {
			if err := os.Remove(cfg); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
			_ = os.Remove(filepath.Dir(cfg)) // only when it is empty
		}
	}
	var state string
	var err error
	if privateDir == "" {
		state, err = StatePath(ctx, dir)
	}
	if privateDir != "" {
		state, err = filepath.Join(privateDir, protocol.WorkerMarker), nil
	}
	if err == nil {
		err = os.Remove(state)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// StatePath is where the run's State lives for the worktree at dir: the
// worktree's own git directory.
func StatePath(ctx context.Context, dir string) (string, error) {
	out, err := git(ctx, dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	return filepath.Join(out, protocol.WorkerMarker), nil
}

// checkOwn fails when path exists and tend did not write it.
func checkOwn(path string) error {
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("bridge: %w", err)
	case !bytes.HasPrefix(b, []byte(configHeader)):
		return fmt.Errorf("bridge: %s already exists and is not tend's; the worker cannot be given its tools there", path)
	}
	return nil
}

// config is the agent's TOML configuration. A JSON string is a valid TOML
// basic string. The worker runs unattended (D38, D39): the agent asks
// nobody before running a command or calling the run's tools.
func config(command string, args []string) string {
	q := func(s string) string {
		b, _ := json.Marshal(s)
		return string(b)
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = q(a)
	}
	// An agent waiting at its update prompt would never start the turn.
	return configHeader + "\napproval_policy = \"never\"\nsandbox_mode = \"danger-full-access\"\ncheck_for_update_on_startup = false\n" +
		"\n[mcp_servers.tend_worker]\ncommand = " + q(command) + "\nargs = [" + strings.Join(quoted, ", ") + "]\n" +
		"default_tools_approval_mode = \"approve\"\n"
}

// exclude adds line to the exclude file of the repository at dir unless
// it is there.
func exclude(ctx context.Context, dir, line string) error {
	path, err := git(ctx, dir, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return err
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("bridge: %w", err)
	}
	if slices.Contains(strings.Split(string(b), "\n"), line) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("bridge: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("bridge: %w", err)
	}
	if len(b) > 0 && !bytes.HasSuffix(b, []byte("\n")) {
		line = "\n" + line
	}
	_, err = f.WriteString(line + "\n")
	return errors.Join(err, f.Close())
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	c := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		return "", fmt.Errorf("bridge: git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}
