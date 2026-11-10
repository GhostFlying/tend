package journal

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestRolloutArchiveKeepsOriginalRequestAcrossRestartAndRejectsWrongReceipt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	j := open(t, path)
	next := RolloutArchiveCursor{SourceID: "original-file", Offset: 4, FileID: "file-identity", PrefixSize: 4, PrefixHash: "prefix", AnchorHash: "anchor"}
	chunk := protocol.NativeRolloutChunk{RunID: "original-run", SourceID: next.SourceID, ThreadID: "thread", Data: []byte("raw\n"), SHA256: "original-hash"}
	payload, _ := json.Marshal(chunk)
	env := protocol.Envelope{RequestID: "original-request", Operation: protocol.OpRunnerRolloutArchive, Target: protocol.Target{Type: "run", ID: chunk.RunID}, Payload: payload}
	if err := j.QueueRolloutArchive(t.Context(), "deployment", "thread", RolloutArchiveCursor{}, next, env); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j = open(t, path)
	defer j.Close()
	retained, err := j.RolloutArchive(t.Context(), "deployment", "thread")
	if err != nil || retained.Cursor.Offset != 0 || retained.Pending == nil || retained.Pending.RequestID != env.RequestID || string(retained.Pending.Payload) != string(payload) || retained.Next != next {
		t.Fatal(retained, err)
	}
	// A receipt belonging to a different chunk must not erase the original.
	receipt := protocol.NativeRolloutArchiveReceipt{SourceID: next.SourceID, Offset: 0, NextOffset: 4, SHA256: "wrong", IndexedOffset: 0}
	if err := j.AcknowledgeRolloutArchive(t.Context(), "deployment", "thread", env.RequestID, receipt); err == nil {
		t.Fatal("accepted foreign receipt")
	}
	if err := j.QueueRolloutArchive(t.Context(), "deployment", "thread", RolloutArchiveCursor{}, next, env); err == nil {
		t.Fatal("replaced pending original")
	}
	receipt.SHA256 = chunk.SHA256
	if err := j.AcknowledgeRolloutArchive(t.Context(), "deployment", "thread", env.RequestID, receipt); err != nil {
		t.Fatal(err)
	}
	retained, err = j.RolloutArchive(t.Context(), "deployment", "thread")
	if err != nil || retained.Cursor != next || retained.Pending != nil {
		t.Fatal(retained, err)
	}
	other, err := j.RolloutArchive(t.Context(), "other-deployment", "thread")
	if err != nil || other.Cursor.SourceID != "" {
		t.Fatal("archive cursor escaped deployment", other, err)
	}
	requests, err := j.Requests(t.Context())
	if err != nil || len(requests) != 0 {
		t.Fatal("host archive request became a run final request", requests, err)
	}
}
