package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

// defaultHookTimeout is how long the hook command waits for tend. The
// agents' own hook timeout, set by tend setup, is longer, so the command
// always gives up first and answers (C5).
const defaultHookTimeout = 3 * time.Second

// hookEvents maps the hook command's event names to tend's and to the
// agents' names in their output.
var hookEvents = map[string]struct {
	tend  protocol.HookEvent
	agent string
}{
	"session-start":      {protocol.HookSessionStart, "SessionStart"},
	"user-prompt-submit": {protocol.HookUserPromptSubmit, "UserPromptSubmit"},
}

// hookInput is what the agents pass a hook on stdin (C5).
type hookInput struct {
	SessionID string `json:"session_id"`
	Source    string `json:"source"`
	Prompt    string `json:"prompt"`
}

// unreachable is the context added when tend cannot answer, so the model
// knows its reminders are missing and writes may fail.
const unreachable = "[tend] tend is unreachable right now: there are no ledger reminders this time, and tend tools may fail."

// runHook answers an agent hook: `tend hook <agent> <event>`. It always
// exits 0, printing either nothing or one JSON object the three agents
// understand. Exit code 2 would make every agent block the user's input
// (C5).
func runHook(ctx context.Context, e *Env, args []string) int {
	if e.addonOff() {
		// A worker that inherited the owner's configuration.
		ctx, cancel := context.WithTimeout(ctx, defaultHookTimeout)
		defer cancel()
		_ = within(ctx, func() error {
			_, err := io.Copy(io.Discard, io.LimitReader(e.Stdin, 1<<20))
			return err
		})
		return exitOK
	}
	text, err := hook(ctx, e, args)
	if err != nil {
		fmt.Fprintf(e.Stderr, "tend hook: %v\n", err)
		var pe *protocol.Error
		switch {
		case isTransport(err) || errors.Is(err, context.DeadlineExceeded):
			text = unreachable
		case errors.As(err, &pe) && pe.Code == protocol.CodeUnauthenticated, errors.Is(err, errNotLoggedIn):
			text = "[tend] the tend hook is not logged in, so there are no ledger reminders; ask the owner to run `tend login`."
		default:
			text = "[tend] the tend hook failed (" + err.Error() + "), so there are no ledger reminders this time."
		}
	}
	if text == "" || len(args) != 2 {
		return exitOK
	}
	ev, ok := hookEvents[args[1]]
	if !ok {
		return exitOK
	}
	out := map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": ev.agent, "additionalContext": text}}
	_ = json.NewEncoder(e.Stdout).Encode(out)
	return exitOK
}

func hook(ctx context.Context, e *Env, args []string) (string, error) {
	if len(args) != 2 {
		return "", fmt.Errorf("usage: tend hook <agent> <session-start|user-prompt-submit>")
	}
	agent := protocol.AgentKind(args[0])
	ev, ok := hookEvents[args[1]]
	if !agent.Valid() || !ok {
		return "", fmt.Errorf("unknown agent %q or event %q", args[0], args[1])
	}
	timeout := defaultHookTimeout
	if v := e.Getenv("TEND_HOOK_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			timeout = d
		} else if s, err := strconv.Atoi(v); err == nil && s > 0 {
			timeout = time.Duration(s) * time.Second
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var in hookInput
	if err := within(ctx, func() error { return json.NewDecoder(io.LimitReader(e.Stdin, 4<<20)).Decode(&in) }); err != nil {
		return "", fmt.Errorf("read the hook input: %w", err)
	}
	if in.SessionID == "" {
		return "", errors.New("the hook input has no session_id")
	}
	c, err := e.client(protocol.ScopeAgent)
	if err != nil {
		return "", err
	}
	req := protocol.HookRequest{
		RequestID: e.IDs.New(protocol.PrefixRequest), Agent: agent, Event: ev.tend, SessionID: in.SessionID, Source: in.Source, Prompt: in.Prompt,
	}
	var res protocol.HookResponse
	// Resend under the same request ID while time is left: tend answers a
	// repeated request with its recorded answer.
	for {
		err = c.do(ctx, "POST", "/api/v1/agent/hook", req, &res)
		if !isTransport(err) || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(200 * time.Millisecond):
		}
	}
	if err != nil {
		return "", err
	}
	return res.Context, nil
}

// within runs read but gives up when ctx ends: the agent may stall while
// writing stdin, and the hook must still answer in time. A read left
// behind blocks only until the process exits, right after the answer.
func within(ctx context.Context, read func() error) error {
	done := make(chan error, 1)
	go func() { done <- read() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
