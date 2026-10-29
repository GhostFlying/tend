package store

import (
	"database/sql"
	"encoding/json"
	"github.com/GhostFlying/tend/internal/testkit"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

// Canonical task creation is not enabled. These are explicit store-fact fixtures
// for the read/context consumers that must be ready before that future entry.
func primaryReadTaskFixture(t *testing.T, s *Store, task, primary string, contexts []protocol.TaskWorkContext) {
	t.Helper()
	insertTask(t, s, task)
	raw, err := json.Marshal(map[string]any{"shared_context": contexts})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Write(t.Context(), func(tx *Tx) error {
		if err := tx.InsertSpec(t.Context(), task, 1, string(raw), "", 1); err != nil {
			return err
		}
		return tx.InsertTaskIssueOwner(t.Context(), task, primary)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalPrimaryScopeHasNoStageAndSeparateReferences(t *testing.T) {
	s, _ := openTest(t)
	parent := canonicalIssueFixture("wi_00000000000000000000000001")
	saveCanonicalFixture(t, s, parent, 0)
	primary := canonicalIssueFixture("wi_00000000000000000000000002")
	primary.ParentIssueID = parent.IssueID
	saveCanonicalFixture(t, s, primary, 0)
	primaryReadTaskFixture(t, s, "task_primary", primary.IssueID, []protocol.TaskWorkContext{{WorkItemID: primary.IssueID, ContextDigest: "old"}, {WorkItemID: parent.IssueID, ContextDigest: "old"}})
	reference := canonicalIssueFixture("wi_00000000000000000000000003")
	reference.Plan.Stages = []protocol.IssueStage{{StageID: "result", Title: "Result", Status: "planned", TaskIDs: []string{"task_primary"}}}
	saveCanonicalFixture(t, s, reference, 0)
	legacy := protocol.WorkItem{WorkItemID: "wi_legacy", Revision: 1, Status: "open", IssuePlan: protocol.IssuePlan{Title: "Legacy", Project: "default", Stages: []protocol.IssueStage{{StageID: "result", Title: "Result", Status: "planned", TaskIDs: []string{"task_primary"}}}}}
	if err := s.Write(t.Context(), func(tx *Tx) error { return tx.SaveIssue(t.Context(), legacy, 0, "fixture") }); err != nil {
		t.Fatal(err)
	}
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		scope, err := ResolveTaskIssueScope(t.Context(), tx.Q(), "task_primary")
		if err != nil {
			return err
		}
		if !scope.Canonical || scope.PrimaryID != primary.IssueID || len(scope.Responsibilities) != 1 || scope.Responsibilities[0].WorkItemID != primary.IssueID || len(scope.References) != 2 {
			t.Fatalf("scope=%+v", scope)
		}
		old, err := ListTaskIssues(t.Context(), tx.Q(), "task_primary")
		if err != nil {
			return err
		}
		if len(old) != 2 {
			t.Fatalf("legacy links changed: %+v", old)
		}
		for _, id := range []string{primary.IssueID, reference.IssueID, legacy.WorkItemID} {
			ids, err := IssueTaskIDs(t.Context(), tx.Q(), id)
			if err != nil {
				return err
			}
			want := []string{}
			if id == primary.IssueID {
				want = []string{"task_primary"}
			}
			if !slices.Equal(ids, want) {
				t.Fatalf("members %s=%v, want%v", id, ids, want)
			}
			readable, err := TaskIssueEvidenceReadable(t.Context(), tx.Q(), "task_primary", id)
			if err != nil {
				return err
			}
			if !readable {
				t.Fatalf("explicit evidence lost for %s", id)
			}
		}
		readable, err := TaskIssueEvidenceReadable(t.Context(), tx.Q(), "task_primary", parent.IssueID)
		if err != nil {
			return err
		}
		if readable {
			t.Fatal("ancestry became artifact scope")
		}
		items, canonical, err := TaskIssueContextItems(t.Context(), tx.Q(), "task_primary")
		if err != nil {
			return err
		}
		if !canonical || len(items) != 2 || items[0].WorkItemID != primary.IssueID || items[1].WorkItemID != parent.IssueID {
			t.Fatalf("inherited shared context=%+v", items)
		}
		for _, id := range []string{primary.IssueID, parent.IssueID, reference.IssueID, legacy.WorkItemID} {
			blockers, err := TaskContextBlockers(t.Context(), tx.Q(), id)
			if err != nil {
				return err
			}
			want := 0
			if id == primary.IssueID || id == parent.IssueID {
				want = 1
			}
			if len(blockers) != want {
				t.Fatalf("blockers%s=%+v", id, blockers)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(t.Context(), func(tx *Tx) error {
		if _, err := tx.InsertMessage(t.Context(), MessageRow{MessageID: "msg_primary", Author: "owner", Body: "For this execution"}); err != nil {
			return err
		}
		return tx.BindMessage(t.Context(), "msg_primary", "task", "task_primary", "fixture")
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		ids, err := FollowupIDs(t.Context(), tx.Q(), `SELECT object_id FROM conversation_bindings WHERE message_id='msg_primary' AND object_type='work_item' ORDER BY object_id`)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(ids, []string{primary.IssueID}) {
			t.Fatalf("source escaped primary: %v", ids)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := contextRevisionFixture(t, s, primary.IssueID)
	refBefore := contextRevisionFixture(t, s, reference.IssueID)
	legacyBefore := contextRevisionFixture(t, s, legacy.WorkItemID)
	mustExec(t, s, `UPDATE tasks SET updated_at=updated_at+1 WHERE task_id='task_primary'`)
	if contextRevisionFixture(t, s, primary.IssueID) != before+1 || contextRevisionFixture(t, s, reference.IssueID) != refBefore || contextRevisionFixture(t, s, legacy.WorkItemID) != legacyBefore {
		t.Fatal("task update did not invalidate only the no-stage primary")
	}
	if readCanonicalFixture(t, s, primary.IssueID, 1).Revision != 1 {
		t.Fatal("task update rewrote immutable issue history")
	}
}

func contextRevisionFixture(t *testing.T, s *Store, id string) int64 {
	t.Helper()
	var rev int64
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT revision FROM work_item_contexts WHERE work_item_id=?`, id).Scan(&rev)
	}); err != nil {
		t.Fatal(err)
	}
	return rev
}

func TestCanonicalPrimaryScopePreservesLegacySharedResponsibilities(t *testing.T) {
	s, _ := openTest(t)
	for _, id := range []string{"wi_legacy_a", "wi_legacy_b"} {
		item := protocol.WorkItem{WorkItemID: id, Revision: 1, Status: "open", IssuePlan: protocol.IssuePlan{Title: "Legacy", Project: "default", Stages: []protocol.IssueStage{{StageID: "stage", Title: "Stage", Status: "planned"}}}}
		if err := s.Write(t.Context(), func(tx *Tx) error { return tx.SaveIssue(t.Context(), item, 0, "fixture") }); err != nil {
			t.Fatal(err)
		}
	}
	primaryReadTaskFixture(t, s, "task_legacy", "wi_legacy_a", nil)
	for _, id := range []string{"wi_legacy_a", "wi_legacy_b"} {
		mustExec(t, s, `INSERT INTO work_item_task_links(work_item_id,stage_id,task_id)VALUES(?,'stage','task_legacy')`, id)
	}
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		scope, err := ResolveTaskIssueScope(t.Context(), tx.Q(), "task_legacy")
		if err != nil {
			return err
		}
		if scope.Canonical || len(scope.Responsibilities) != 2 || len(scope.References) != 0 {
			t.Fatalf("legacy=%+v", scope)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	a, b := contextRevisionFixture(t, s, "wi_legacy_a"), contextRevisionFixture(t, s, "wi_legacy_b")
	mustExec(t, s, `UPDATE tasks SET updated_at=updated_at+1 WHERE task_id='task_legacy'`)
	if contextRevisionFixture(t, s, "wi_legacy_a") != a+1 || contextRevisionFixture(t, s, "wi_legacy_b") != b+1 {
		t.Fatal("legacy shared invalidation changed")
	}
}

func TestCanonicalPrimaryContextMigrationPreservesExistingProvenance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		t.Fatal(err)
	}
	clock := testkit.NewClock()
	old := &Store{write: db, read: db, now: clock.Now}
	if err = old.migrate(t.Context(), 54); err != nil {
		t.Fatal(err)
	}
	issue := canonicalIssueFixture("wi_00000000000000000000000004")
	saveCanonicalFixture(t, old, issue, 0)
	primaryReadTaskFixture(t, old, "task_before_migration", issue.IssueID, nil)
	before := contextRevisionFixture(t, old, issue.IssueID)
	mustExec(t, old, `UPDATE tasks SET updated_at=2 WHERE task_id='task_before_migration'`)
	if contextRevisionFixture(t, old, issue.IssueID) != before {
		t.Fatal("old trigger unexpectedly knew stage-free primary")
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	current, err := Open(t.Context(), path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	if !reflect.DeepEqual(readCanonicalFixture(t, current, issue.IssueID, 1), issue) {
		t.Fatal("migration rewrote issue history")
	}
	if err = current.Read(t.Context(), func(tx *ReadTx) error {
		primary, err := GetTaskIssueOwner(t.Context(), tx.Q(), "task_before_migration")
		if err != nil {
			return err
		}
		if primary != issue.IssueID {
			t.Fatal("migration changed primary")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if contextRevisionFixture(t, current, issue.IssueID) != before {
		t.Fatal("migration invented context review")
	}
	mustExec(t, current, `UPDATE tasks SET updated_at=3 WHERE task_id='task_before_migration'`)
	if contextRevisionFixture(t, current, issue.IssueID) != before+1 {
		t.Fatal("new trigger missed existing no-stage primary")
	}
}
