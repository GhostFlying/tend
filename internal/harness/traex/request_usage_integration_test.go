//go:build integration

package traex

import (
	"context"
	"github.com/GhostFlying/tend/internal/harness"
	"testing"
	"time"
)

func TestIntegration_RequestUsage(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	s, err := Start(ctx, Options{Version: "integration"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	th, err := s.StartThread(ctx, harness.ThreadConfig{Cwd: t.TempDir(), Model: model(), Sandbox: "read-only", ApprovalPolicy: "never", DeveloperInstructions: "This is an isolated protocol test. Reply with exactly READY. Do not use tools."})
	if err != nil {
		t.Fatal(err)
	}
	id, err := th.StartTurn(ctx, harness.TurnInput{Text: "Reply READY."})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case ev := <-th.Events():
			if ev.RequestUsage != nil {
				u := ev.RequestUsage
				if u.ThreadID != th.ID() || u.TurnID != id || u.RequestID == "" || u.Total == nil {
					t.Fatalf("incomplete native request usage: %+v", u)
				}
				found = true
				t.Logf("observed native response: total=%d input=%v output=%v", *u.Total, u.Input != nil, u.Output != nil)
			}
			if ev.TurnID == id && ev.Kind == harness.EventTurnCompleted {
				if !found {
					t.Fatal("native executor did not expose request usage")
				}
				return
			}
		}
	}
}
