// Package cli implements the owner command line. Business rules live on the server.
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"

	"github.com/GhostFlying/tend/internal/protocol"
)

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
}

// Command exit codes.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

const usage = `usage: tend <command> [arguments]

Owner commands:
  login --server <url>             get an owner token for this machine
  status [--project <p>] [--json]  projects with their tasks and queue, open decisions and hosts
  attention list|show|create|respond|confirm  original questions and human actions
  next-action list|put                    personal action / Issue planning order
  issue deadline list|show|put|cancel     sourced time and bounded reminders
  issue list|show|create|update|<control>  canonical issues; JSON input/output
  issue create-task|update-task          owner task contracts, independent workers
  issue request|replay                   inspect/replay an original request
  task <task_id> [--json]          one task in full
  logs [-f] <run_id|task_id>       a run's logs, or all runs of a task; -f follows the last
  decide <decision_id> <option> [--note <text>]
  retry <task_id> --kind implement|accept
  rate <task_id> <p0|p1|p2|p3>     rate a task: urgent, high, normal or low
  pause|resume|cancel <task_id>
  category put|show <id>           manage global agent classifications
  repo register|show <name>       manage repositories online
  archive prepare|status|cancel|host [--socket <path>] [--json]
  upgrade [start|status|retry|cancel] [--release <directory>] [--json]
  conversation handoff [--prepare-only | --status] [--json]
  attachment download <file_id> --out <path>  save an original attachment
  takeover <task_id>               work in the task's agent yourself; resume hands it back

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
	case "login":
		return report(e, runLogin(ctx, e, rest))
	case "next-action", "planner":
		return report(e, runPlanningRecord(ctx, e, rest, false))
	case "attention":
		return report(e, runAttention(ctx, e, rest))
	case "issue":
		return report(e, runIssue(ctx, e, rest))
	case "category":
		return report(e, runAgentCategory(ctx, e, rest))
	case "repo":
		return report(e, runRepository(ctx, e, rest))
	case "archive":
		return report(e, runArchive(ctx, e, rest))
	case "upgrade":
		return report(e, runUpgrade(ctx, e, rest))
	case "attachment":
		return report(e, runAttachment(ctx, e, rest))
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
