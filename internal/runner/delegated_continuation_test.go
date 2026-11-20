package runner

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

func TestDelegatedEarlierTerminalCannotCompleteLaterTurn(t *testing.T) {
	r, as, workers, spaces, _ := implementationReconcileFixture(t)
	// Native turn one completed without submit_result. The delegated worker then
	// started turn two and submitted result.json, but crashed before its terminal.
	if err := r.cfg.Journal.AppendEvent(t.Context(), as.RunID, protocol.RunEvent{Kind: "native.turn_started", Body: []byte(`{"thread_id":"original-result-thread","turn_id":"later-turn"}`)}); err != nil {
		t.Fatal(err)
	}
	handled, err := r.reconcileImplementationResult(t.Context(), as)
	run, readErr := r.cfg.Journal.Run(t.Context(), as.RunID)
	observed, obsErr := readWorkerOutcome(spaces.prepared.StateDir, as.RunID, "original-result-thread", 1)
	if readErr != nil || obsErr != nil {
		t.Fatal(readErr, obsErr)
	}
	if len(workers.seen) != 0 {
		t.Fatal("a previous turn authorized closing the current native process")
	}
	if handled && err == nil && run.Phase == journal.PhaseWorkerDone {
		t.Fatalf("later turn was completed from earlier turn %q terminal: %+v", observed.TurnID, run)
	}
}
