package journal

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestArchiveWatermarkPreservesConcurrentAppendAndCompletedRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	j := open(t, path)
	as := protocol.Assignment{RunID: "original-run", Protocol: 4}
	if err := j.AcceptAssignment(t.Context(), as, ""); err != nil {
		t.Fatal(err)
	}
	if err := j.RegisterRollout(t.Context(), "deployment", as.RunID, protocol.RolloutPreviewSource{SessionID: "original-thread"}); err != nil {
		t.Fatal(err)
	}
	work, err := j.PendingArchives(t.Context(), "", 10)
	if err != nil || len(work) != 1 {
		t.Fatal(work, err)
	}
	// An append during network IO must remain dirty after the captured prefix ACK.
	if err := j.AppendEvent(t.Context(), as.RunID, protocol.RunEvent{Kind: "native.turn_completed", Body: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := j.CompleteArchive(t.Context(), work[0]); err != nil {
		t.Fatal(err)
	}
	if err := j.Done(t.Context(), as.RunID); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j = open(t, path)
	defer j.Close()
	newWork, err := j.PendingArchives(t.Context(), "", 10)
	if err != nil || len(newWork) != 1 || newWork[0].Generation <= work[0].Generation {
		t.Fatal("completed Run or concurrent tail lost", newWork, err)
	}
	if err := j.CompleteArchive(t.Context(), newWork[0]); err != nil {
		t.Fatal(err)
	}
	if err := j.CompleteArchive(t.Context(), work[0]); err != nil {
		t.Fatal(err)
	}
	if pending, err := j.PendingArchives(t.Context(), "", 10); err != nil || len(pending) != 0 {
		t.Fatal("old ACK regressed the watermark", pending, err)
	}
}

func TestManagerEventAndArchiveGenerationCommitTogether(t *testing.T) {
	j := open(t, filepath.Join(t.TempDir(), "journal.db"))
	defer j.Close()
	as := protocol.Assignment{RunID: "original-run", Protocol: 4}
	if err := j.AcceptAssignment(t.Context(), as, ""); err != nil {
		t.Fatal(err)
	}
	if err := j.RegisterRollout(t.Context(), "deployment", as.RunID, protocol.RolloutPreviewSource{SessionID: "thread"}); err != nil {
		t.Fatal(err)
	}
	before, _ := j.PendingArchives(t.Context(), "", 10)
	if _, err := j.db.ExecContext(t.Context(), `CREATE TRIGGER fail_event BEFORE INSERT ON requests BEGIN SELECT RAISE(ABORT,'injected persistence failure'); END`); err != nil {
		t.Fatal(err)
	}
	env := &protocol.Envelope{RequestID: "original-event", Operation: protocol.OpManagerNativeEvent}
	if err := j.SaveManagerEvent(t.Context(), as.RunID, env, json.RawMessage(`{}`)); err == nil {
		t.Fatal("event persistence fault was ignored")
	}
	after, err := j.PendingArchives(t.Context(), "", 10)
	last, lastErr := j.LastManagerEvent(t.Context(), as.RunID)
	if err != nil || lastErr != nil || last != 0 || len(after) != 1 || after[0] != before[0] {
		t.Fatal("failed event changed sequence or dirty watermark", before, after, last, err, lastErr)
	}
	if _, err := j.db.ExecContext(t.Context(), `DROP TRIGGER fail_event`); err != nil {
		t.Fatal(err)
	}
	if err := j.SaveManagerEvent(t.Context(), as.RunID, env, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	after, err = j.PendingArchives(t.Context(), "", 10)
	if err != nil || len(after) != 1 || after[0].Generation != before[0].Generation+1 {
		t.Fatal(after, err)
	}
}
