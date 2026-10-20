// Package cli is the tend command (D46): the owner's commands, the MCP
// bridge and hook command for the agents the owner works in, and their
// setup. It only forwards: tool definitions, context and every rule live
// on the server, and the command keeps no copy of the ledger. It depends
// on protocol and the standard library only, never on the server's
// packages.
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"

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
}

func (e *Env) addonOff() bool { return e.Getenv(AddonEnv) == "off" }

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
  status [--json]                  unfinished tasks, open decisions and hosts
  task <task_id> [--json]          one task in full
  logs [-f] <run_id|task_id>       a run's logs, or all runs of a task; -f follows the last
  decide <decision_id> <option> [--note <text>]
  retry <task_id> --kind implement|accept
  pause|resume|cancel <task_id>

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
	case "status", "task", "logs", "decide", "retry", "pause", "resume", "cancel":
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
