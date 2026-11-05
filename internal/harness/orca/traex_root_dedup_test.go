package orca

import (
	"encoding/json"
	"os"
	"testing"
)

func TestTraexRootTerminalFactAfterRunningHasDurableRevision(t *testing.T) {
	for _, state := range []string{"completed", "cancelled", "failed"} {
		t.Run(state, func(t *testing.T) {
			f, w, a, body := traexEndingFixture(t)
			running := *a
			root := *a.RootTurn
			root.State, root.Operations, root.ErrorCode = "running", "pending", ""
			running.RootTurn = &root
			if err := w.recordObservedRoot(t.Context(), running); err != nil {
				t.Fatal(err)
			}
			if w.st.RootObservationPhase != "known" || w.st.RootObservationRevision != 1 || len(w.pending) != 1 {
				t.Fatal("running observation missing", w.st.RootObservationRevision, len(w.pending))
			}
			firstID := w.pending[0].NativeID
			end := map[string]any{"type": "task_complete", "turn_id": "turn-original"}
			if state == "cancelled" {
				end["type"] = "turn_aborted"
			}
			if state == "failed" {
				end["error"] = map[string]any{"codex_error_info": "response_stream_disconnected"}
			}
			if err := os.WriteFile(a.ProviderSession.TranscriptPath, []byte(body+rootRecord(5000, "event_msg", end)), 0600); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				// Sampling time is not a new semantic fact.
				a.RootTurn.ObservedAt++
				if err := w.recordObservedRoot(t.Context(), *a); err != nil {
					t.Fatal(err)
				}
			}
			if w.st.RootObservationRevision != 2 || len(w.pending) != 2 {
				t.Fatalf("terminal fact deduplicated as running: revision=%d events=%d", w.st.RootObservationRevision, len(w.pending))
			}
			terminal := w.pending[1]
			var event struct {
				State string    `json:"root_state"`
				Root  *RootTurn `json:"root_turn"`
			}
			if err := json.Unmarshal(terminal.Body, &event); err != nil || event.Root == nil || event.State != state || event.Root.CompletedAt != 5000 || event.Root.Operations != "settled" || terminal.NativeID == firstID {
				t.Fatal("new exact terminal observation missing", event, terminal.NativeID, err)
			}
			var restored workerState
			if err := json.Unmarshal([]byte(f.thread), &restored); err != nil || restored.RootObservationRevision != 2 {
				t.Fatal("new observation revision was not durable", restored.RootObservationRevision, err)
			}
			w.st, w.observationEmitted, w.pending = restored, 0, nil
			if err := w.recordObservedRoot(t.Context(), *a); err != nil {
				t.Fatal(err)
			}
			if w.st.RootObservationRevision != 2 || len(w.pending) != 1 || w.pending[0].NativeID != terminal.NativeID {
				t.Fatal("restart changed the terminal fact identity", w.st.RootObservationRevision, w.pending)
			}
			if a.RootTurn.State != "unknown" || a.RootTurn.Source != "native-api" || a.RootTurn.CompletedAt != 0 {
				t.Fatal("terminal fact entered lifecycle Agent", a.RootTurn)
			}
			if f.orca.count("terminal send", nil) != 0 || f.orca.count("agent launch", nil) != 0 || f.orca.count("terminal close", nil) != 0 {
				t.Fatal("observation revision authorized a native action")
			}
		})
	}
}
