package traex

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
)

func TestHostedBoundaryRejectsChangedNativeTrustBeforeTurnAndResume(t *testing.T) {
	for _, change := range []string{"missing", "disabled", "hash_changed", "untrusted", "load_error"} {
		t.Run(change, func(t *testing.T) {
			f, s := newFake(t)
			s.hostedBoundary = &hostedToolBoundary{Key: "session-hook", Hash: "sha256:fixture", Dir: "workspace"}
			metadata := hostedHookMetadata{Key: "session-hook", Hash: "sha256:fixture", Trust: "trusted", Enabled: true, Event: "preToolUse", Handler: "command", Command: hostedNativeDeny, Matcher: hostedNativeMatcher, Source: "sessionFlags"}
			hooks := []hostedHookMetadata{metadata}
			var loadErrors []string
			f.on("hooks/list", func(json.RawMessage) any {
				return map[string]any{"data": []any{map[string]any{"hooks": hooks, "errors": loadErrors}}}
			})
			f.on(methodThreadStart, func(json.RawMessage) any {
				return map[string]any{"thread": map[string]any{"id": "hosted"}, "sandbox": map[string]any{"type": "readOnly"}, "approvalPolicy": "never"}
			})
			cfg := harness.ThreadConfig{Cwd: "workspace", Sandbox: "read-only", ApprovalPolicy: "never"}
			thread, err := s.StartThread(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "missing":
				hooks = nil
			case "disabled":
				hooks[0].Enabled = false
			case "hash_changed":
				hooks[0].Hash = "sha256:changed"
			case "untrusted":
				hooks[0].Trust = "untrusted"
			case "load_error":
				loadErrors = []string{"hook configuration could not be loaded"}
			}
			if _, err := thread.StartTurn(t.Context(), harness.TurnInput{Text: "Must not start"}); !errors.Is(err, harness.ErrRejected) {
				t.Fatalf("changed native boundary admitted turn: %v", err)
			}
			if f.params(methodTurnStart) != nil {
				t.Fatal("turn was sent before trust validation")
			}
			if _, err := s.ResumeThread(t.Context(), "hosted", cfg); !errors.Is(err, harness.ErrRejected) {
				t.Fatalf("changed native boundary admitted resume: %v", err)
			}
			if f.params(methodThreadResume) != nil {
				t.Fatal("resume was sent before trust validation")
			}
		})
	}
}

func TestHostedPreflightTransportFailureIsKnownUnsent(t *testing.T) {
	for _, phase := range []string{"preflight-timeout", "preflight-disconnect", "start-timeout"} {
		t.Run(phase, func(t *testing.T) {
			f, s := newFake(t)
			s.hostedBoundary = &hostedToolBoundary{Key: "session-hook", Hash: "sha256:fixture", Dir: "workspace"}
			metadata := hostedHookMetadata{Key: "session-hook", Hash: "sha256:fixture", Trust: "trusted", Enabled: true, Event: "preToolUse", Handler: "command", Command: hostedNativeDeny, Matcher: hostedNativeMatcher, Source: "sessionFlags"}
			f.on("hooks/list", func(json.RawMessage) any {
				return map[string]any{"data": []any{map[string]any{"hooks": []hostedHookMetadata{metadata}, "errors": []string{}}}}
			})
			f.on(methodThreadStart, func(json.RawMessage) any {
				return map[string]any{"thread": map[string]any{"id": "hosted"}, "sandbox": map[string]any{"type": "readOnly"}, "approvalPolicy": "never"}
			})
			th, err := s.StartThread(t.Context(), harness.ThreadConfig{Cwd: "workspace", Sandbox: "read-only", ApprovalPolicy: "never"})
			if err != nil {
				t.Fatal(err)
			}
			release := make(chan struct{})
			defer close(release)
			block := func(json.RawMessage) any { <-release; return map[string]any{} }
			cause := error(context.DeadlineExceeded)
			switch phase {
			case "preflight-timeout":
				f.on("hooks/list", block)
			case "preflight-disconnect":
				cause = ErrClosed
				f.on("hooks/list", func(json.RawMessage) any { _ = f.out.Close(); return nil })
			case "start-timeout":
				f.on(methodTurnStart, block)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
			defer cancel()
			_, err = th.StartTurn(ctx, harness.TurnInput{Text: "real dispatch boundary"})
			if !errors.Is(err, cause) {
				t.Fatalf("lost transport cause: %v", err)
			}
			sent := f.params(methodTurnStart) != nil
			if phase == "start-timeout" {
				if errors.Is(err, harness.ErrRejected) || !sent {
					t.Fatalf("actual sent start was made retryable: %v sent=%v", err, sent)
				}
			} else if !errors.Is(err, harness.ErrRejected) || sent {
				t.Fatalf("unsent preflight made uncertain: %v sent=%v", err, sent)
			}
		})
	}
}
