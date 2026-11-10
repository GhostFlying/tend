package journal

import (
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/harness/nativeusage"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestCollectionPathMoveKeepsSourceIDAndCursor(t *testing.T) {
	j := open(t, filepath.Join(t.TempDir(), "journal.db"))
	defer j.Close()
	s := UsageSource{ID: "original", RunID: "run", RegistrationQueued: true, Source: nativeusage.Source{Provider: "codex", ThreadID: "thread", SessionID: "session", Path: "old", WriterID: "writer", NoDispatchProven: true}, Cursors: map[string]nativeusage.Cursor{"old": {Offset: 123}}}
	if err := j.RegisterUsageSource(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	s.ID, s.Source.Path, s.Source.SessionID = "new-path-hash", "new", ""
	if err := j.RegisterUsageSource(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	all, err := j.UsageSources(t.Context())
	if err != nil || len(all) != 1 || all[0].ID != "original" || all[0].Collection.State != protocol.UsageDormant || all[0].Source.SessionID != "session" || all[0].Cursors["new"].Offset != 123 || all[0].Cursors["old"].Offset != 123 {
		t.Fatal(all, err)
	}
	id, err := j.UsageSourceID(t.Context(), "run", "codex", "thread")
	if err != nil || id != "original" {
		t.Fatal(id, err)
	}
	if err := j.ActivateUsageSource(t.Context(), id, "turn"); err != nil {
		t.Fatal(err)
	}
	all, _ = j.UsageSources(t.Context())
	if all[0].Collection.RequestID != "turn" || all[0].Collection.State != protocol.UsageActive {
		t.Fatal(all)
	}
	previous := all[0]
	if err := j.ActivateUsageSource(t.Context(), id, "turn"); err != nil {
		t.Fatal(err)
	}
	if err := j.QueueUsage(t.Context(), previous, nil); protocol.CodeOf(err) != protocol.CodeStaleRevision {
		t.Fatal("old turn collection survived a new dispatch", err)
	}
}

func TestCollectionControlSurvivesRestartWithoutRepeatingEpoch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	j := open(t, path)
	s := UsageSource{ID: "source", RunID: "run", RegistrationQueued: true, Source: nativeusage.Source{ThreadID: "thread", Provider: "codex"}}
	if err := j.RegisterUsageSource(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	all, _ := j.UsageSources(t.Context())
	c := all[0].Collection
	command := protocol.UsageCollectionCommand{RequestID: "original", SourceID: s.ID, RunID: s.RunID, Action: "retire", Reason: "unavailable", ExpectedEpoch: c.Epoch, ExpectedRevision: c.Revision}
	if err := j.ApplyUsageCollectionControl(t.Context(), command); err != nil {
		t.Fatal(err)
	}
	all, _ = j.UsageSources(t.Context())
	if all[0].Collection.State != protocol.UsageNeedsReconcile || all[0].WriterStopped {
		t.Fatal("retirement forged writer stop", all)
	}
	_ = j.Close()
	j = open(t, path)
	defer j.Close()
	if err := j.ApplyUsageCollectionControl(t.Context(), command); err != nil {
		t.Fatal(err)
	}
	after, _ := j.UsageSources(t.Context())
	if after[0].Collection != all[0].Collection {
		t.Fatal("control replay changed collection", after)
	}
	command.RequestID = "late"
	command.Action = "resume"
	command.Reason = ""
	if err := j.ApplyUsageCollectionControl(t.Context(), command); err != nil {
		t.Fatal(err)
	}
	var result string
	if err := j.db.QueryRowContext(t.Context(), `SELECT result FROM native_usage_collection_controls WHERE request_id='late'`).Scan(&result); err != nil || result != "stale" {
		t.Fatal(result, err)
	}
}

func TestCollectionResumeRescansPrefixAndPreservesPendingUpload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	j := open(t, path)
	s := UsageSource{ID: "source", RunID: "run", RegistrationQueued: true, Coverage: "caught_up", WriterStopped: true,
		Source:  nativeusage.Source{Provider: "codex", ThreadID: "thread", Path: "history"},
		Cursors: map[string]nativeusage.Cursor{"history": {Offset: 900, Prefix: "old"}}}
	if err := j.RegisterUsageSource(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	all, err := j.UsageSources(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	s = all[0]
	upload := protocol.Envelope{RequestID: "original-upload", Operation: protocol.OpRunnerUsageUpload, Target: protocol.Target{Type: "host", ID: "host"}, Payload: []byte(`{"run_id":"run"}`)}
	if err := j.PutRequest(t.Context(), Request{Envelope: upload}); err != nil {
		t.Fatal(err)
	}
	command := protocol.UsageCollectionCommand{RequestID: "rescan", Rescan: true, SourceID: s.ID, RunID: s.RunID, Action: "resume", ExpectedEpoch: s.Collection.Epoch, ExpectedRevision: s.Collection.Revision}
	if err := j.ApplyUsageCollectionControl(t.Context(), command); err != nil {
		t.Fatal(err)
	}
	all, _ = j.UsageSources(t.Context())
	if len(all[0].Cursors) != 0 || all[0].Coverage != "pending" || !all[0].WriterStopped || all[0].Collection.State != protocol.UsageSettling {
		t.Fatal(all)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j = open(t, path)
	defer j.Close()
	if err := j.ApplyUsageCollectionControl(t.Context(), command); err != nil {
		t.Fatal(err)
	}
	after, _ := j.UsageSources(t.Context())
	if after[0].Collection != all[0].Collection {
		t.Fatal("rescan replay changed its epoch", after)
	}
	pending, err := j.Requests(t.Context())
	if err != nil || len(pending) != 1 || pending[0].Envelope.RequestID != upload.RequestID || string(pending[0].Envelope.Payload) != string(upload.Payload) {
		t.Fatal("rescan replaced original upload", pending, err)
	}
}

func TestCollectionUnknownWriterRegistrationAfterConfirmedStop(t *testing.T) {
	for _, writer := range []string{"", "original-writer"} {
		t.Run(writer, func(t *testing.T) { testCollectionUnknownWriter(t, writer) })
	}
}

func testCollectionUnknownWriter(t *testing.T, writer string) {
	j := open(t, filepath.Join(t.TempDir(), "journal.db"))
	defer j.Close()
	s := UsageSource{ID: "source", RunID: "run", RegistrationQueued: true, Source: nativeusage.Source{Provider: "codex", ThreadID: "thread", Path: "history", WriterID: writer}}
	if err := j.RegisterUsageSource(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	if err := j.StopUsageSource(t.Context(), "run", "thread"); err != nil {
		t.Fatal(err)
	}
	old, _ := j.UsageSources(t.Context())
	s.Source.WriterID = ""
	if err := j.RegisterUsageSource(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	if err := j.ActivateUsageSource(t.Context(), "source", "next"); err != nil {
		t.Fatal(err)
	}
	all, _ := j.UsageSources(t.Context())
	if all[0].WriterStopped || all[0].Collection.WriterID != "" || all[0].Collection.StopProof != "" || all[0].Collection.Epoch <= old[0].Collection.Epoch {
		t.Fatal(all)
	}
	if err := j.QueueUsage(t.Context(), old[0], nil); protocol.CodeOf(err) != protocol.CodeStaleRevision {
		t.Fatal("old writer collection committed", err)
	}
}
