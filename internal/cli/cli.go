// Package cli is the tend command (D46): the owner's commands, the MCP
// bridge and hook command for the agents the owner works in, and their
// setup. It only forwards: tool definitions, context and every rule live
// on the server, and the command keeps no copy of the ledger. It depends
// on protocol and the standard library only, never on the server's
// packages.
package cli

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

// AddonEnv turns the agent add-on off for workers (D45).
const AddonEnv = protocol.AddonEnv

// ConfigDirEnv overrides where the configuration and credentials live.
const ConfigDirEnv = "TEND_CONFIG_DIR"

// Env is everything a command touches outside itself; tests replace it.
type Env struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Getenv func(string) string
	// Home is the user's home directory and ConfigDir tend's own
	// configuration directory.
	Home      string
	ConfigDir string
	HTTP      *http.Client
	IDs       protocol.IDGen
	// Executable is the path of the running tend binary and LookPath finds
	// commands on PATH; setup uses them to name tend in agent
	// configuration.
	Executable string
	LookPath   func(string) (string, error)
	// Run runs an external command, for setup.
	Run func(ctx context.Context, name string, args ...string) error
	// Output runs an external command and returns its standard output,
	// and Getwd returns the working directory; the hook and the MCP bridge
	// use them to find the repository the agent works in. Either may be
	// nil.
	Output func(ctx context.Context, name string, args ...string) ([]byte, error)
	Getwd  func() (string, error)
}

// addonOff reports whether the add-on must do nothing: in a worker, told
// so by AddonEnv or by the marker in its worktree's git directory.
func (e *Env) addonOff(ctx context.Context) bool {
	return e.Getenv(AddonEnv) == "off" || e.inWorker(ctx)
}

// inWorker reports whether the working directory is a worktree a TUI
// worker runs in (D66).
func (e *Env) inWorker(ctx context.Context) bool {
	if e.Getwd == nil {
		return false
	}
	dir, err := e.Getwd()
	if err != nil {
		return false
	}
	for parent := dir; ; parent = filepath.Dir(parent) {
		for _, rel := range []string{".codex/config.toml", ".trae/traecli.toml"} {
			b, err := os.ReadFile(filepath.Join(parent, rel))
			if err == nil && bytes.HasPrefix(b, []byte("# Written by tend-runner for the run in this worktree; removed when the run ends.\n")) {
				return true
			}
		}
		if filepath.Dir(parent) == parent {
			break
		}
	}
	if e.Output == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := e.Output(ctx, "git", "-C", dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(strings.TrimSpace(string(out)), protocol.WorkerMarker))
	return err == nil
}

// Exit codes. The hook command always exits 0 (C5): any other code may
// block the user's input.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

const usage = `usage: tend <command> [arguments]

Owner commands:
  login --server <url>             get tokens for this machine
  status [--project <p>] [--json]  projects with their tasks and queue, open decisions and hosts
  task <task_id> [--json]          one task in full
  logs [-f] <run_id|task_id>       a run's logs, or all runs of a task; -f follows the last
  decide <decision_id> <option> [--note <text>]
  retry <task_id> --kind implement|accept
  rate <task_id> <p0|p1|p2|p3>     rate a task: urgent, high, normal or low
  pause|resume|cancel <task_id>
  conversation handoff [--prepare-only | --status] [--json]
  takeover <task_id>               work in the task's agent yourself; resume hands it back

Agent add-on:
  setup <claude|codex|traex> [--write]   show or write the agent's configuration
  mcp --agent <agent>              MCP server on stdio (started by the agent)
  hook <agent> <event>             hook command (started by the agent)
`

// Main runs the tend command with args, without the program name, and
// returns the exit code.
func Main(ctx context.Context, e *Env, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(e.Stderr, usage)
		return exitUsage
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "hook":
		return runHook(ctx, e, rest)
	case "mcp":
		return runMCP(ctx, e, rest)
	case "login":
		return report(e, runLogin(ctx, e, rest))
	case "setup":
		return report(e, runSetup(ctx, e, rest))
	case "conversation":
		return report(e, runConversation(ctx, e, rest))
	case "status", "task", "logs", "decide", "retry", "rate", "pause", "resume", "cancel", "takeover":
		return report(e, runOwner(ctx, e, cmd, rest))
	case "help", "-h", "--help":
		fmt.Fprint(e.Stdout, usage)
		return exitOK
	}
	fmt.Fprintf(e.Stderr, "tend: unknown command %q\n\n%s", cmd, usage)
	return exitUsage
}

// usageError is a mistake in the command line.
type usageError struct{ msg string }

func (u usageError) Error() string { return u.msg }

func usagef(format string, args ...any) error { return usageError{fmt.Sprintf(format, args...)} }

// report prints err and maps it to an exit code.
func report(e *Env, err error) int {
	if err == nil {
		return exitOK
	}
	fmt.Fprintf(e.Stderr, "tend: %v\n", err)
	if _, ok := err.(usageError); ok {
		return exitUsage
	}
	return exitError
}

// newFlags returns a flag set that reports to the command's stderr.
func newFlags(e *Env, name string) *flag.FlagSet {
	fs := flag.NewFlagSet("tend "+name, flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	return fs
}

// parse parses args, allowing flags after positional arguments, and
// returns the positional ones.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, usageError{err.Error()}
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}
