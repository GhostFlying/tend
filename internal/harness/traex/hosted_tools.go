package traex

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"github.com/GhostFlying/tend/internal/harness"
)

// These native delegation functions bypass Tend's task and acceptance ledger.
// readonly/never and ThreadConfig's native command/research switches remain
// required. Owner-provided image presentation is intentionally not blocked.
const hostedNativeMatcher = "^(spawn_agent|send_input|resume_agent|wait_agent|close_agent)$"

// The fixed shell builtin has no external script, input interpolation, network,
// or business configuration dependency. The CLI's hook engine itself allows
// calls through on hook execution errors; this is not an OS isolation boundary.
const hostedNativeDeny = `printf '%s\n' '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"Delegate execution and investigation through Tend tasks."}}'`

type hostedToolBoundary struct{ Key, Hash, Dir string }
type hostedHookMetadata struct {
	Key     string `json:"key"`
	Hash    string `json:"currentHash"`
	Trust   string `json:"trustStatus"`
	Enabled bool   `json:"enabled"`
	Event   string `json:"eventName"`
	Handler string `json:"handlerType"`
	Command string `json:"command"`
	Matcher string `json:"matcher"`
	Source  string `json:"source"`
	Async   bool   `json:"async"`
}

// A preflight process discovers the CLI's exact source key/hash before any
// thread exists. Only the final hosted process receives its local trust state;
// owner configuration and worker processes are never rewritten.
func startHostedCoordinator(ctx context.Context, opts Options) (*Session, error) {
	// The installed Linux CLI has been verified to execute the fixed POSIX
	// builtin. Other hosts must verify their native hook shell before enabling
	// this role; a trusted metadata entry alone does not prove execution.
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("%w: hosted CLI role isolation is currently supported on Linux only", harness.ErrRejected)
	}
	hook := fmt.Sprintf(`hooks.PreToolUse=[{matcher=%q,hooks=[{type="command",command=%q}]}]`, hostedNativeMatcher, hostedNativeDeny)
	args := []string{"-c", hook}
	probe, err := startProcess(ctx, opts, args)
	if err != nil {
		return nil, err
	}
	metadata, readErr := probe.hostedHook(ctx, opts.Dir)
	closeErr := probe.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close hosted hook preflight: %w", closeErr)
	}
	trust := fmt.Sprintf(`hooks.state={%q={trusted_hash=%q}}`, metadata.Key, metadata.Hash)
	args = append(args, "-c", trust)
	session, err := startProcess(ctx, opts, args)
	if err != nil {
		return nil, err
	}
	session.hostedBoundary = &hostedToolBoundary{Key: metadata.Key, Hash: metadata.Hash, Dir: opts.Dir}
	if err := session.checkHostedBoundary(ctx); err != nil {
		_ = session.Close()
		return nil, err
	}
	return session, nil
}

func (s *Session) hostedHook(ctx context.Context, dir string) (hostedHookMetadata, error) {
	var result struct {
		Data []struct {
			Hooks    []hostedHookMetadata `json:"hooks"`
			Errors   []any                `json:"errors"`
			Warnings []string             `json:"warnings"`
		} `json:"data"`
	}
	if err := s.conn.call(ctx, "hooks/list", map[string]any{"cwds": []string{dir}}, &result); err != nil {
		return hostedHookMetadata{}, fmt.Errorf("hosted native hook inventory is unavailable: %w", err)
	}
	if len(result.Data) != 1 {
		return hostedHookMetadata{}, fmt.Errorf("%w: hosted hook inventory has no exact working directory", harness.ErrRejected)
	}
	entry := result.Data[0]
	if len(entry.Errors) > 0 || len(entry.Warnings) > 0 {
		return hostedHookMetadata{}, fmt.Errorf("%w: hosted hook inventory reports loading errors or warnings", harness.ErrRejected)
	}
	var selected hostedHookMetadata
	found := false
	for _, hook := range entry.Hooks {
		if hook.Command != hostedNativeDeny || hook.Matcher != hostedNativeMatcher || hook.Source != "sessionFlags" {
			continue
		}
		if found || !hook.Enabled || hook.Event != "preToolUse" || hook.Handler != "command" || hook.Async || hook.Key == "" || !strings.HasPrefix(hook.Hash, "sha256:") {
			return hostedHookMetadata{}, fmt.Errorf("%w: hosted native hook is not an enabled synchronous CLI hook", harness.ErrRejected)
		}
		selected, found = hook, true
	}
	if !found {
		return selected, fmt.Errorf("%w: hosted native hook was not registered by this process", harness.ErrRejected)
	}
	return selected, nil
}
func (s *Session) checkHostedBoundary(ctx context.Context) error {
	if s.hostedBoundary == nil {
		return nil
	}
	expected := s.hostedBoundary
	current, err := s.hostedHook(ctx, expected.Dir)
	if err != nil {
		return err
	}
	if current.Key != expected.Key || current.Hash != expected.Hash || current.Trust != "trusted" {
		return fmt.Errorf("%w: hosted native hook trust changed", harness.ErrRejected)
	}
	return nil
}
func (s *Session) checkHostedConfig(ctx context.Context, cfg harness.ThreadConfig) error {
	if s.hostedBoundary == nil {
		return nil
	}
	if cfg.Sandbox != "read-only" || cfg.ApprovalPolicy != "never" {
		return fmt.Errorf("%w: hosted coordination requires read-only permissions without approval escalation", harness.ErrRejected)
	}
	return s.checkHostedBoundary(ctx)
}
