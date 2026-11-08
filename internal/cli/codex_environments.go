package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runCodexEnvironment(ctx context.Context, e *Env, args []string) error {
	if len(args) == 0 {
		return usagef("codex-env requires a management command")
	}
	action := args[0]
	tail := args[1:]
	if slices.Contains([]string{"skill", "plugin", "watch"}, action) {
		if len(tail) == 0 {
			return usagef("%s requires a subcommand", action)
		}
		action += " " + tail[0]
		tail = tail[1:]
	}
	if !slices.Contains(protocol.CodexEnvironmentActions(), action) {
		return usagef("unknown codex-env action")
	}
	fs := newFlags(e, "codex-env "+action)
	root := fs.String("root", filepath.Join(e.ConfigDir, "codex-env"), "absolute local environment store")
	id := fs.String("id", "", "environment ID")
	file := fs.String("file", "", "complete HuJSON environment description; - reads stdin")
	data := fs.String("data", "", "complete environment description")
	request := fs.String("request-id", "", "original management request ID for identical retry")
	revision := fs.Uint64("revision", 0, "expected local control revision")
	item := fs.String("item", "", "skill identity or plugin@marketplace identity")
	source := fs.String("source", "", "absolute complete local package source")
	generation := fs.String("generation", "", "retained snapshot ID for rollback")
	dry := fs.Bool("dry-run", false, "print the exact management request without changing state")
	asJSON := fs.Bool("json", true, "print complete local management JSON")
	after := fs.String("after", "", "execution cursor from the preceding inventory page")
	limit := fs.Int("limit", 100, "execution page size, 1..100")
	pos, err := parse(fs, tail)
	if err != nil {
		return err
	}
	if len(pos) > 1 || len(pos) == 1 && *id != "" {
		return usagef("use one environment ID")
	}
	if len(pos) == 1 {
		*id = pos[0]
	}
	if !filepath.IsAbs(*root) {
		return usagef("--root must be absolute")
	}
	if *limit < 1 || *limit > 100 {
		return usagef("--limit must be 1..100")
	}
	if action != "list" && !strings.HasPrefix(action, "watch ") && *id == "" && *file == "" && *data == "" {
		return usagef("environment ID or description required")
	}
	raw, err := issueInput(e, *data, *file)
	if err != nil {
		return err
	}
	mutation := !slices.Contains([]string{"list", "show", "inventory", "doctor", "plan", "watch status", "watch run"}, action)
	if action == "reconcile" && *request == "" {
		return usagef("reconcile requires the original --request-id")
	}
	if mutation && *request == "" {
		if e.IDs == nil {
			return fmt.Errorf("request ID generator unavailable")
		}
		*request = e.IDs.New(protocol.PrefixRequest)
	}
	q := protocol.LocalCodexEnvironmentRequest{Root: *root, Action: action, ID: *id, RequestID: *request, ExpectedRevision: *revision, Data: string(raw), ItemID: *item, Source: *source, Generation: *generation, DryRun: *dry}
	q.After, q.Limit = *after, *limit
	if *dry {
		return (owner{e: e, json: true}).printJSON(q)
	}
	if mutation {
		fmt.Fprintf(e.Stderr, "Codex environment request %s; revision %d; retain these for reconciliation\n", q.RequestID, q.ExpectedRevision)
	}
	if e.CodexEnvironment == nil {
		return fmt.Errorf("local Codex environment manager unavailable")
	}
	value, err := e.CodexEnvironment(ctx, q)
	if len(value) > 0 {
		if printErr := printCapabilityValue(e, value, *asJSON); printErr != nil {
			return printErr
		}
	}
	return err
}
