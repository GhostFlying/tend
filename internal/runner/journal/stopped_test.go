package journal

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestStartingAnotherAttemptInvalidatesPreviousStopProof(t *testing.T) {
	ctx := context.Background()
	j, err := Open(ctx, filepath.Join(t.TempDir(), "journal.db"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if err := j.AcceptAssignment(ctx, protocol.Assignment{RunID: "run_1"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := j.ConfirmStopped(ctx, "run_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := j.NextAttempt(ctx, "run_1"); err != nil {
		t.Fatal(err)
	}
	run, err := j.HistoricalRun(ctx, "run_1")
	if err != nil {
		t.Fatal(err)
	}
	if run.StoppedConfirmed {
		t.Fatal("old attempt's stop proof survived a new possible launch")
	}
}
