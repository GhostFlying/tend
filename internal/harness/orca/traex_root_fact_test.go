package orca

import (
	"context"
	"encoding/json"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"os"
	"testing"
	"time"
)

func TestTraexRootSupplementPublishesFactWithoutChangingLifecycle(t *testing.T) {
	for _, state := range []string{"completed", "cancelled", "failed"} {
		t.Run(state, func(t *testing.T) {
			f, w, a, body := traexEndingFixture(t)
			kind := "task_complete"
			end := map[string]any{"type": kind, "turn_id": "turn-original"}
			if state == "cancelled" {
				end["type"] = "turn_aborted"
			}
			if state == "failed" {
				end["error"] = map[string]any{"codex_error_info": "response_stream_disconnected"}
			}
			body += rootRecord(5000, "event_msg", end)
			if err := os.WriteFile(a.ProviderSession.TranscriptPath, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				got, done, err := w.step(t.Context())
				if err != nil || done || got.RootTurn == nil || got.RootTurn.State != "unknown" || got.RootTurn.Source != "native-api" || got.RootTurn.CompletedAt != 0 {
					t.Fatalf("fact entered lifecycle: done=%v err=%v root=%+v", done, err, got.RootTurn)
				}
				var restored workerState
				if err := json.Unmarshal([]byte(f.thread), &restored); err != nil {
					t.Fatal(err)
				}
				w.st = restored
			}
			count := 0
			for _, e := range w.pending {
				var event struct {
					Root *RootTurn `json:"root_turn"`
				}
				if json.Unmarshal(e.Body, &event) == nil && event.Root != nil {
					count++
					if event.Root.State != state || event.Root.Source != transcriptRootSource || event.Root.CompletedAt != 5000 {
						t.Fatalf("wrong reported fact: %+v", event.Root)
					}
				}
			}
			if count != 1 || w.st.RootObservationRevision != 1 {
				t.Fatalf("observation oscillated: events=%d revision=%d", count, w.st.RootObservationRevision)
			}
			if f.orca.count("terminal send", nil) != 0 || f.orca.count("agent launch", nil) != 0 || f.orca.count("terminal close", nil) != 0 {
				t.Fatal("fact authorized a native action")
			}
		})
	}
}

func TestTraexRootSupplementSavedFactDoesNotAuthorizeStart(t *testing.T) {
	for _, mode := range []string{"ordinary", "acceptance", "continuing"} {
		t.Run(mode, func(t *testing.T) {
			f, w, a, body := traexEndingFixture(t)
			body += rootEnd(5000, "turn-original")
			if err := os.WriteFile(a.ProviderSession.TranscriptPath, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if mode == "continuing" {
				w.st.Recovery = &recoveryState{Attempts: 1, Phase: recoveryContinuing, Code: "response_stream_disconnected", SessionID: w.st.SessionID, TurnID: "turn-original", Terminal: w.st.Terminal, Pane: w.st.Pane, TurnAt: 5000, OriginalPrompt: w.st.Said, LaunchID: w.st.RootLaunchID, RuntimeID: w.st.RootRuntimeID}
			}
			if err := w.recordObservedRoot(t.Context(), *a); err != nil {
				t.Fatal(err)
			}
			if w.st.RootObservationPhase != "known" {
				t.Fatal("confirmed fact was not journaled")
			}
			original := f.thread
			for range 2 {
				spec := w.spec
				spec.Thread = original
				if mode == "acceptance" {
					spec.Role = protocol.RoleAccept
				}
				ctx, cancel := context.WithCancelCause(t.Context())
				started, err := f.ws.Start(ctx, spec)
				if err != nil {
					cancel(harness.ErrHeld)
					t.Fatal(err)
				}
				drained := make(chan struct{})
				go func() {
					defer close(drained)
					for range started.Events() {
					}
				}()
				finished := make(chan error, 1)
				go func() { _, err := started.Wait(); finished <- err }()
				select {
				case err := <-finished:
					cancel(harness.ErrHeld)
					t.Fatalf("persisted fact completed the worker: %v", err)
				case <-time.After(20 * time.Millisecond):
				}
				cancel(harness.ErrHeld)
				select {
				case <-finished:
				case <-time.After(time.Second):
					t.Fatal("worker did not cancel")
				}
				<-drained
				original = f.thread
				var restored workerState
				if err := json.Unmarshal([]byte(original), &restored); err != nil {
					t.Fatal(err)
				}
				if restored.RootObservationRevision != 1 {
					t.Fatalf("fact revision changed across restart: %d", restored.RootObservationRevision)
				}
			}
			if f.orca.count("terminal send", nil) != 0 || f.orca.count("agent launch", nil) != 0 || f.orca.count("terminal close", nil) != 0 {
				t.Fatal("saved fact authorized native input")
			}
		})
	}
}

func TestTraexRootSupplementStrongNativeClosingKeepsOriginalUnknownClose(t *testing.T) {
	f, w, a, body := traexEndingFixture(t)
	a.MainAgent = &MainAgent{State: stateDone, Outcome: "failure", StateStartedAt: 5000}
	a.StateStartedAt = 5000
	body += rootRecord(5000, "event_msg", map[string]any{"type": "task_complete", "turn_id": "turn-original", "error": map[string]any{"codex_error_info": "other"}})
	if err := os.WriteFile(a.ProviderSession.TranscriptPath, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	w.st.Recovery = &recoveryState{Attempts: 1, Phase: recoveryClosing, Code: "invalid_encrypted_content", SessionID: w.st.SessionID, TurnID: "turn-original", Terminal: w.st.Terminal, Pane: w.st.Pane, TurnAt: 5000, OriginalPrompt: w.st.Said, LaunchID: w.st.RootLaunchID, RuntimeID: w.st.RootRuntimeID}
	if err := w.recordObservedRoot(t.Context(), *a); err != nil {
		t.Fatal(err)
	}
	spec := w.spec
	spec.Thread = f.thread
	// Weak native evidence remains insufficient despite the journaled fact.
	if started, err := f.ws.Start(t.Context(), spec); err == nil || started != nil {
		t.Fatal("weak native closing unexpectedly attached")
	}
	if f.orca.count("terminal close", nil) != 0 {
		t.Fatal("reported fact authorized a close")
	}
	// The existing strong native path may attempt the original authorized close.
	a.RootTurn.State, a.RootTurn.Operations, a.RootTurn.ErrorCode = "failed", "settled", "other"
	a.RootTurn.CompletedAt = 5000
	f.orca.failClose = true
	if started, err := f.ws.Start(t.Context(), spec); err == nil || started != nil {
		t.Fatal("unknown Close was not returned to caller")
	}
	time.Sleep(20 * time.Millisecond)
	if f.orca.count("terminal close", nil) != 1 || f.orca.count("terminal send", nil) != 0 || f.orca.count("agent launch", nil) != 0 {
		t.Fatal("unknown native close was automatically retried or changed strategy")
	}
}

func TestTraexRootSupplementReportConflictDoesNotTakeControl(t *testing.T) {
	_, w, a, body := traexEndingFixture(t)
	body += rootAppend(3000, map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "unconfirmed other input"}}}) + rootEnd(5000, "turn-original")
	if err := os.WriteFile(a.ProviderSession.TranscriptPath, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	got, done, err := w.step(t.Context())
	if err != nil || done || w.holding != "" || got.RootTurn.State != "unknown" || got.RootTurn.Source != "native-api" {
		t.Fatalf("transcript conflict entered control: done=%v err=%v held=%q root=%+v", done, err, w.holding, got.RootTurn)
	}
}

func TestTraexRootSupplementCancelledReportDoesNotChangeSavedFact(t *testing.T) {
	f, w, a, body := traexEndingFixture(t)
	body += rootEnd(5000, "turn-original")
	if err := os.WriteFile(a.ProviderSession.TranscriptPath, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := w.recordObservedRoot(t.Context(), *a); err != nil {
		t.Fatal(err)
	}
	saved := f.thread
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := w.recordObservedRoot(ctx, *a); err == nil {
		t.Fatal("cancelled reporting continued")
	}
	if f.thread != saved || w.st.RootObservationRevision != 1 {
		t.Fatal("cancelled observation rewrote the saved fact")
	}
}
