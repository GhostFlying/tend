//go:build integration

package traex_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/traex"
	"github.com/GhostFlying/tend/internal/mainthread"
)

// TestIsolatedContextUsageAndCompact uses an empty temporary conversation, never
// the hosted ledger or its native thread. It verifies acceptance AND completion.
func TestIsolatedContextUsageAndCompact(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	s, err := traex.Start(ctx, traex.Options{Dir: t.TempDir(), Version: "tend-integration"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	model := os.Getenv("TEND_TRAEX_MODEL")
	if model == "" {
		model = "GPT-5.6-Sol"
	}
	cfg := mainthread.ThreadConfig(t.TempDir(), model)
	cfg.DeveloperInstructions = "This is an isolated integration test. Do not call tools. Return the exact word ready."
	th, err := s.StartThread(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	native, ok := th.(harness.ContextManager)
	if !ok {
		t.Fatal("context manager missing")
	}
	if _, err = th.StartTurn(ctx, harness.TurnInput{Text: "Return ready, without tools."}); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case ev := <-th.Events():
			if ev.Tool != nil {
				t.Fatalf("unexpected native tool %s", ev.Tool.Name)
			}
			if ev.Kind == harness.EventTurnCompleted {
				if ev.TurnStatus != "completed" {
					t.Fatal(ev.TurnStatus)
				}
				goto completed
			}
		}
	}
completed:
	usage, err := native.ContextUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if usage.ActiveTokens == nil || usage.EffectiveWindow == nil {
		t.Fatalf("no coherent snapshot: %+v", usage)
	}
	t.Logf("native snapshot revision=%d active=%d window=%d measurement=%s", usage.Revision, *usage.ActiveTokens, *usage.EffectiveWindow, usage.Measurement)
	if err = native.Compact(ctx); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case <-ctx.Done():
			t.Fatal("compact completion not observed", ctx.Err())
		case ev := <-th.Events():
			if ev.Kind == harness.EventItemCompleted && ev.ItemType == harness.ItemContextCompaction {
				t.Log("native compact completion observed")
				return
			}
		}
	}
}
