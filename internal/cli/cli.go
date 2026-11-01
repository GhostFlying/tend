// Package cli implements the owner command line. Business rules live on the server.
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"slices"

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

var usage = commandUsage(nil)

// Main runs the tend command with args, without the program name, and
// returns the exit code.
func Main(ctx context.Context, e *Env, args []string) int {
	// Interrupting a CLI command must also release a blocked stdin producer,
	// including JSON input collected before an HTTP request is constructed.
	if input, ok := e.Stdin.(io.Closer); ok {
		stop := context.AfterFunc(ctx, func() { _ = input.Close() })
		defer stop()
	}

	if len(args) == 0 {
		fmt.Fprint(e.Stderr, usage)
		return exitUsage
	}

	if args[0] == "help" {
		if len(args) == 1 {
			fmt.Fprint(e.Stdout, commandUsage(nil))
			return exitOK
		}
		return Main(ctx, e, append(append([]string(nil), args[1:]...), "--help"))
	}
	if args[len(args)-1] == "--help" || args[len(args)-1] == "-h" {
		prefix := args[:len(args)-1]

		if len(prefix) == 2 && prefix[0] == "issue" && slices.Contains([]string{"events", "schedules", "webhooks"}, prefix[1]) {
			noun := map[string]string{"events": "event", "schedules": "schedule", "webhooks": "webhook"}[prefix[1]]
			fmt.Fprintf(e.Stdout, "Alias: tend issue %s <issue_id> [flags]\n", prefix[1])
			return Main(ctx, e, []string{noun, "list", "--help"})
		}
		if len(prefix) == 0 || hasCommandChildren(prefix) {
			fmt.Fprint(e.Stdout, commandUsage(prefix))
			return exitOK
		}
		// A leaf command owns its FlagSet help. Route that output to stdout while
		// preserving the same parser and its complete flag definitions.
		helpEnv := *e
		helpEnv.Stderr = e.Stdout
		e = &helpEnv
	}
	if command, tail, ok := findCapabilityCommand(args); ok {
		return report(e, runCapabilityCommand(ctx, e, command, tail))
	}
	if len(args) >= 2 && args[0] == "request" && args[1] == "replay" {
		return report(e, runRequestReplay(ctx, e, args[2:]))
	}
	if len(args) >= 2 && args[0] == "repo" && args[1] == "local" {
		return report(e, runLocalRepository(ctx, e, args[2:]))
	}
	if len(args) >= 2 && args[0] == "agents" && args[1] == "local" {
		return report(e, runLocalAgentCommand(ctx, e, args[2:]))
	}
	if args[0] == "doctor" {
		return report(e, runDoctor(ctx, e, args[1:]))
	}
	if args[0] == "host" || args[0] == "queue" {
		return report(e, runHostQueue(ctx, e, args))
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "schedule", "event", "webhook", "observation", "request":
		return report(e, runTriggers(ctx, e, cmd, rest))
	case "message", "channel":
		return report(e, runMessaging(ctx, e, cmd, rest))
	case "login":
		return report(e, runLogin(ctx, e, rest))
	case "next-action", "planner":
		return report(e, runPlanningRecord(ctx, e, rest, false))
	case "attention":
		return report(e, runAttention(ctx, e, rest))
	case "issue":
		return report(e, runIssue(ctx, e, rest))
	case "agents":
		return report(e, runAgentCatalog(ctx, e, rest))
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
	case "run", "source":
		return report(e, runDirectConversation(ctx, e, cmd, rest))
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
	if err == nil || err == flag.ErrHelp {
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
			if err == flag.ErrHelp {
				return nil, err
			}
			return nil, usageError{err.Error()}
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}
