package orca

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

func TestTraexRootTerminalFactUpgradesLegacyObservationState(t *testing.T) {
	f, w, a, body := traexEndingFixture(t)
	running := *a
	root := *a.RootTurn
	root.State, root.Operations, root.ErrorCode = "running", "pending", ""
	running.RootTurn = &root
	// An older worker persisted the canonical observation without a fact hash.
	if err := w.recordRootObservation(t.Context(), running); err != nil {
		t.Fatal(err)
	}
	var restored workerState
	if err := json.Unmarshal([]byte(f.thread), &restored); err != nil || restored.RootObservationRevision != 1 || restored.RootObservationFactHash != "" {
		t.Fatal("invalid historical observation fixture", restored, err)
	}
	w.st = restored
	if err := os.WriteFile(a.ProviderSession.TranscriptPath, []byte(body+rootEnd(5000, "turn-original")), 0600); err != nil {
		t.Fatal(err)
	}
	if err := w.recordObservedRoot(t.Context(), *a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(f.thread), &restored); err != nil || restored.RootObservationRevision != 2 || restored.RootObservationFactHash == "" || len(w.pending) != 2 {
		t.Fatal("historical state lost the new terminal fact", restored, len(w.pending), err)
	}
}

func TestTraexRootNativeSamplingTimeKeepsObservationRevision(t *testing.T) {
	_, w, a, _ := traexEndingFixture(t)
	a.RootTurn.State, a.RootTurn.Operations, a.RootTurn.ErrorCode = "running", "pending", ""
	for range 3 {
		a.RootTurn.ObservedAt++
		if err := w.recordObservedRoot(t.Context(), *a); err != nil {
			t.Fatal(err)
		}
	}
	if w.st.RootObservationRevision != 1 || len(w.pending) != 1 {
		t.Fatal("sampling clock created a new semantic observation", w.st.RootObservationRevision, len(w.pending))
	}
}

func TestTraexRootTerminalFactRetriesFailedPersistence(t *testing.T) {
	f, w, a, body := traexEndingFixture(t)
	running := *a
	root := *a.RootTurn
	root.State, root.Operations, root.ErrorCode = "running", "pending", ""
	running.RootTurn = &root
	if err := w.recordObservedRoot(t.Context(), running); err != nil {
		t.Fatal(err)
	}
	before := f.thread
	if err := os.WriteFile(a.ProviderSession.TranscriptPath, []byte(body+rootEnd(5000, "turn-original")), 0600); err != nil {
		t.Fatal(err)
	}
	save := w.spec.OnThread
	failure := errors.New("observation journal unavailable")
	w.spec.OnThread = func(string) error { return failure }
	if err := w.recordObservedRoot(t.Context(), *a); !errors.Is(err, failure) {
		t.Fatal("save failure was hidden", err)
	}
	if f.thread != before || len(w.pending) != 1 {
		t.Fatal("failed save published a terminal fact")
	}
	w.spec.OnThread = save
	if err := w.recordObservedRoot(t.Context(), *a); err != nil {
		t.Fatal(err)
	}
	var restored workerState
	if err := json.Unmarshal([]byte(f.thread), &restored); err != nil || restored.RootObservationRevision != 2 || len(w.pending) != 2 {
		t.Fatal("retry did not durably record terminal fact", restored.RootObservationRevision, len(w.pending), err)
	}
}
