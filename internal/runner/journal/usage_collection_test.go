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
