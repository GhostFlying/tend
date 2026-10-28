package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func canonicalIssueFixture(id string) protocol.Issue {
	return protocol.Issue{
		IssueControl: protocol.IssueControl{IssueID: id, State: protocol.IssueActive, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: "fixture-owner"}, AssignmentEpoch: 1, AssignmentState: "active"},
		Revision:     1, Plan: protocol.WorkItemPlan{Title: "Investigate a goal", Project: "default", Intent: "Produce verified results", Stages: []protocol.WorkItemStage{}}, DependsOn: []string{}, CreatedAt: 1, UpdatedAt: 1,
	}
}

func issueProvenance() IssueRevisionProvenance {
	return IssueRevisionProvenance{Actor: protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "fixture-owner", Client: "fixture-client"}, RequestID: "req_fixture", Reason: "owner request"}
}

func saveCanonicalFixture(t *testing.T, s *Store, issue protocol.Issue, expected int64) {
	t.Helper()
	if err := s.Write(t.Context(), func(tx *Tx) error { return tx.SaveCanonicalIssue(t.Context(), issue, expected, issueProvenance()) }); err != nil {
		t.Fatal(err)
	}
}

func readCanonicalFixture(t *testing.T, s *Store, id string, revision int64) protocol.Issue {
	t.Helper()
	var got protocol.Issue
	if err := s.Read(t.Context(), func(tx *ReadTx) (err error) {
		if revision == 0 {
			got, err = GetCanonicalIssue(t.Context(), tx.Q(), id)
		} else {
			got, err = GetCanonicalIssueRevision(t.Context(), tx.Q(), id, revision)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestCanonicalIssueCurrentAndHistoryShareOneRevision(t *testing.T) {
	s, path := openTest(t)
	root := canonicalIssueFixture("wi_00000000000000000000000001")
	saveCanonicalFixture(t, s, root, 0)
	child := canonicalIssueFixture("wi_00000000000000000000000002")
	child.ParentIssueID = root.IssueID
	saveCanonicalFixture(t, s, child, 0)
	root.Revision, root.UpdatedAt = 2, 2
	root.State = protocol.IssuePaused
	root.Plan.Title = "Updated goal"
	root.DependsOn = []string{child.IssueID}
	saveCanonicalFixture(t, s, root, 1)
	if got := readCanonicalFixture(t, s, root.IssueID, 0); !reflect.DeepEqual(got, root) {
		t.Fatalf("current=%+v, want %+v", got, root)
	}
	if got := readCanonicalFixture(t, s, root.IssueID, 2); !reflect.DeepEqual(got, root) {
		t.Fatalf("history=%+v, want %+v", got, root)
	}
	original := canonicalIssueFixture(root.IssueID)
	if got := readCanonicalFixture(t, s, root.IssueID, 1); !reflect.DeepEqual(got, original) {
		t.Fatalf("old history changed: %+v", got)
	}
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		projection, err := GetIssue(t.Context(), tx.Q(), root.IssueID)
		if err != nil {
			return err
		}
		var revision int64
		var status string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT revision,status FROM work_items WHERE work_item_id=?`, root.IssueID).Scan(&revision, &status); err != nil {
			return err
		}
		if projection.Revision != 2 || revision != 2 || projection.Status != protocol.WorkItemSnoozed || status != "snoozed" || projection.Title != root.Plan.Title {
			t.Fatalf("incoherent legacy projection: %+v (%d,%s)", projection, revision, status)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), path, testkit.NewClock().Now)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := readCanonicalFixture(t, reopened, root.IssueID, 0); !reflect.DeepEqual(got, root) {
		t.Fatalf("reopen lost aggregate: %+v", got)
	}
}

func TestCanonicalIssueLegacySaveCannotMutateAdoptedIdentity(t *testing.T) {
	s, _ := openTest(t)
	issue := canonicalIssueFixture("wi_00000000000000000000000003")
	saveCanonicalFixture(t, s, issue, 0)
	err := s.Write(t.Context(), func(tx *Tx) error {
		projection, err := GetIssue(t.Context(), tx.Q(), issue.IssueID)
		if err != nil {
			return err
		}
		projection.Title = "wrong legacy write"
		projection.Revision++
		return tx.SaveIssue(t.Context(), projection, 1, "legacy update")
	})
	if protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		t.Fatalf("legacy write error=%v", err)
	}
	if got := readCanonicalFixture(t, s, issue.IssueID, 0); !reflect.DeepEqual(got, issue) {
		t.Fatalf("legacy write escaped: %+v", got)
	}
	// Unadopted compatibility identities remain writable and cannot be read as canonical.
	legacy := protocol.WorkItem{WorkItemPlan: issue.Plan, WorkItemID: "wi_00000000000000000000000004", Revision: 1, Status: protocol.WorkItemOpen, CreatedAt: 1, UpdatedAt: 1}
	if err := s.Write(t.Context(), func(tx *Tx) error { return tx.SaveIssue(t.Context(), legacy, 0, "legacy") }); err != nil {
		t.Fatal(err)
	}
	err = s.Read(t.Context(), func(tx *ReadTx) error {
		_, err := GetCanonicalIssue(t.Context(), tx.Q(), legacy.WorkItemID)
		return err
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("legacy row fabricated canonical ownership: %v", err)
	}
	// An update cannot adopt an old identity through the canonical primitive either.
	adopted := canonicalIssueFixture(legacy.WorkItemID)
	adopted.Revision = 2
	if err := s.Write(t.Context(), func(tx *Tx) error { return tx.SaveCanonicalIssue(t.Context(), adopted, 1, issueProvenance()) }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("implicit adoption=%v", err)
	}
}

func TestFault_CanonicalIssueAggregateSaveRollsBackEveryPhase(t *testing.T) {
	for _, phase := range []struct{ name, trigger string }{
		{"plan_snapshot", `BEFORE INSERT ON work_item_revisions`},
		{"control_current", `BEFORE UPDATE ON issue_controls`},
		{"dependency", `BEFORE INSERT ON issue_dependencies`},
		{"control_snapshot", `BEFORE INSERT ON issue_control_revisions`},
	} {
		t.Run(phase.name, func(t *testing.T) {
			s, _ := openTest(t)
			issue := canonicalIssueFixture("wi_00000000000000000000000005")
			dependency := canonicalIssueFixture("wi_00000000000000000000000006")
			previousDependency := canonicalIssueFixture("wi_00000000000000000000000099")
			saveCanonicalFixture(t, s, previousDependency, 0)
			saveCanonicalFixture(t, s, dependency, 0)
			issue.DependsOn = []string{previousDependency.IssueID}
			saveCanonicalFixture(t, s, issue, 0)
			mustExec(t, s, `CREATE TRIGGER fail_issue_save `+phase.trigger+` BEGIN SELECT RAISE(ABORT,'injected issue save fault'); END`)
			next := issue
			next.Revision, next.UpdatedAt = 2, 2
			next.State = protocol.IssuePaused
			next.Plan.Title = "must roll back"
			next.DependsOn = []string{dependency.IssueID}
			err := s.Write(t.Context(), func(tx *Tx) error { return tx.SaveCanonicalIssue(t.Context(), next, 1, issueProvenance()) })
			if err == nil {
				t.Fatal("save survived injected failure")
			}
			if got := readCanonicalFixture(t, s, issue.IssueID, 0); !reflect.DeepEqual(got, issue) {
				t.Fatalf("partial aggregate survived: %+v", got)
			}
			if err := s.Read(t.Context(), func(tx *ReadTx) error {
				for _, table := range []string{"work_item_revisions", "issue_control_revisions"} {
					var count int
					column := "work_item_id"
					if table == "issue_control_revisions" {
						column = "issue_id"
					}
					if err := tx.Q().QueryRowContext(t.Context(), `SELECT count(*) FROM `+table+` WHERE `+column+`=?`, issue.IssueID).Scan(&count); err != nil {
						return err
					}
					if count != 1 {
						t.Fatalf("%s leaked revision: %d", table, count)
					}
				}
				var count int
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT count(*) FROM issue_dependencies WHERE issue_id=?`, issue.IssueID).Scan(&count); err != nil {
					return err
				}
				if count != 1 {
					t.Fatal("previous dependency was not restored")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
	// Creation must roll back its base row when the last snapshot fails as well.
	t.Run("initial_create", func(t *testing.T) {
		s, _ := openTest(t)
		mustExec(t, s, `CREATE TRIGGER fail_initial_snapshot BEFORE INSERT ON issue_control_revisions BEGIN SELECT RAISE(ABORT,'injected'); END`)
		err := s.Write(t.Context(), func(tx *Tx) error {
			return tx.SaveCanonicalIssue(t.Context(), canonicalIssueFixture("wi_00000000000000000000000007"), 0, issueProvenance())
		})
		if err == nil {
			t.Fatal("create survived injected failure")
		}
		if err := s.Read(t.Context(), func(tx *ReadTx) error {
			_, err := GetIssue(t.Context(), tx.Q(), "wi_00000000000000000000000007")
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("partial base row: %v", err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCanonicalIssueRejectsStaleRevisionAndIncompleteProvenance(t *testing.T) {
	s, _ := openTest(t)
	issue := canonicalIssueFixture("wi_00000000000000000000000008")
	saveCanonicalFixture(t, s, issue, 0)
	next := issue
	next.Revision = 2
	saveCanonicalFixture(t, s, next, 1)
	err := s.Write(t.Context(), func(tx *Tx) error { return tx.SaveCanonicalIssue(t.Context(), next, 1, issueProvenance()) })
	if protocol.CodeOf(err) != protocol.CodeStaleRevision {
		t.Fatalf("stale=%v", err)
	}
	for _, p := range []IssueRevisionProvenance{
		{},
		{Actor: issueProvenance().Actor, RequestID: "request", EffectSeq: 1},
		{Actor: issueProvenance().Actor, RequestID: "request", CoordinationExecutionID: "execution"},
	} {
		next.Revision = 3
		if err := s.Write(t.Context(), func(tx *Tx) error { return tx.SaveCanonicalIssue(t.Context(), next, 2, p) }); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
			t.Fatalf("incomplete provenance accepted: %v", err)
		}
	}
}

func TestCanonicalIssueConstraintsAndImmutableSnapshots(t *testing.T) {
	s, _ := openTest(t)
	issue := canonicalIssueFixture("wi_00000000000000000000000009")
	saveCanonicalFixture(t, s, issue, 0)
	for _, query := range []string{
		`UPDATE issue_control_revisions SET snapshot_json='{}'`,
		`DELETE FROM issue_control_revisions`,
		`UPDATE issue_controls SET assignment_epoch=0`,
		`UPDATE issue_controls SET state='invented'`,
		`UPDATE issue_controls SET parent_issue_id='wi_00000000000000000000000010'`,
		`UPDATE issue_controls SET parent_issue_id=issue_id`,
		`UPDATE issue_controls SET assignment_state='handoff'`,
		`INSERT INTO issue_dependencies VALUES('wi_00000000000000000000000009','wi_00000000000000000000000010')`,
		`INSERT INTO issue_dependencies VALUES('wi_00000000000000000000000009','wi_00000000000000000000000009')`,
	} {
		if err := exec(t, s, query); err == nil {
			t.Fatalf("constraint permitted %s", query)
		}
	}
	// A legacy identity cannot serve as a canonical parent or prerequisite.
	mustExec(t, s, `INSERT INTO work_items VALUES('wi_00000000000000000000000004','default',1,'open','{}',1,1)`)
	if err := exec(t, s, `INSERT INTO issue_dependencies VALUES('wi_00000000000000000000000009','wi_00000000000000000000000004')`); err == nil {
		t.Fatal("legacy prerequisite accepted")
	}
	if err := exec(t, s, `UPDATE issue_controls SET parent_issue_id='wi_00000000000000000000000004'`); err == nil {
		t.Fatal("legacy parent accepted")
	}
}

func TestIssueControlsMigrationPreservesLegacyBytesAndPrimary(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ledger.db")
	clock := testkit.NewClock()
	write, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		t.Fatal(err)
	}
	old := &Store{write: write, read: write, now: clock.Now}
	if err := old.migrate(ctx, 53); err != nil {
		write.Close()
		t.Fatal(err)
	}
	legacy := protocol.WorkItem{WorkItemPlan: protocol.WorkItemPlan{Title: "Original", Project: "default", Intent: "Original intent", Stages: []protocol.WorkItemStage{}}, WorkItemID: "wi_00000000000000000000000011", Revision: 1, Status: protocol.WorkItemOpen, CreatedAt: 1, UpdatedAt: 1}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, old, `INSERT INTO work_items VALUES(?,'default',1,'open',?,1,1)`, legacy.WorkItemID, string(raw))
	mustExec(t, old, `INSERT INTO work_item_revisions VALUES(?,1,?,'{}',1)`, legacy.WorkItemID, string(raw))
	insertTask(t, old, "task_historical")
	mustExec(t, old, `INSERT INTO task_specs(task_id,spec_version,body_json,created_at) VALUES('task_historical',1,'{}',1)`)
	if err := old.Write(ctx, func(tx *Tx) error { return tx.InsertTaskIssueOwner(ctx, "task_historical", legacy.WorkItemID) }); err != nil {
		t.Fatal(err)
	}
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(ctx, path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if err := upgraded.Read(ctx, func(tx *ReadTx) error {
		var got string
		if err := tx.Q().QueryRowContext(ctx, `SELECT body_json FROM work_items WHERE work_item_id=?`, legacy.WorkItemID).Scan(&got); err != nil {
			return err
		}
		if got != string(raw) {
			t.Fatal("migration rewrote historical plan")
		}
		owner, err := GetTaskIssueOwner(ctx, tx.Q(), "task_historical")
		if err != nil {
			return err
		}
		if owner != legacy.WorkItemID {
			t.Fatal("migration changed primary provenance")
		}
		for _, table := range []string{"issue_controls", "issue_dependencies", "issue_control_revisions", "issue_assignment_handoffs"} {
			var count int
			if err := tx.Q().QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				t.Fatalf("migration fabricated %s: %d", table, count)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalIssueReadRequiresMatchingImmutableRevision(t *testing.T) {
	s, _ := openTest(t)
	issue := canonicalIssueFixture("wi_00000000000000000000000999")
	saveCanonicalFixture(t, s, issue, 0)
	// Model an accidental internal writer bypassing the canonical saver. A read
	// must not hide the missing half of this aggregate revision with a default.
	mustExec(t, s, `UPDATE work_items SET revision=2,body_json=json_set(body_json,'$.revision',2) WHERE work_item_id=?`, issue.IssueID)
	if err := s.Read(t.Context(), func(tx *ReadTx) error { _, err := GetCanonicalIssue(t.Context(), tx.Q(), issue.IssueID); return err }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing control snapshot accepted: %v", err)
	}
}

func TestIssueAssignmentHandoffStorageKeepsIssueIdentity(t *testing.T) {
	s, _ := openTest(t)
	a := canonicalIssueFixture("wi_00000000000000000000000888")
	b := canonicalIssueFixture("wi_00000000000000000000000889")
	saveCanonicalFixture(t, s, a, 0)
	saveCanonicalFixture(t, s, b, 0)
	// This is a storage fixture only. No app endpoint resolves a successor or
	// activates assignment in this slice.
	mustExec(t, s, `INSERT INTO issue_assignment_handoffs(handoff_id,issue_id,from_epoch,to_epoch,from_assignee_json,target_assignee_json,status,obligations_json,request_id,reason,created_at,updated_at) VALUES('handoff_a',?,1,2,'{}','{}','fenced','[]','req','fixture',1,1)`, a.IssueID)
	if err := exec(t, s, `UPDATE issue_controls SET assignment_epoch=2,assignment_state='handoff',pending_assignee_kind='owner',pending_assignee_ref='successor',pending_handoff_id='handoff_a' WHERE issue_id=?`, b.IssueID); err == nil {
		t.Fatal("handoff for another issue was accepted")
	}
	mustExec(t, s, `UPDATE issue_controls SET assignment_epoch=2,assignment_state='handoff',pending_assignee_kind='owner',pending_assignee_ref='successor',pending_handoff_id='handoff_a' WHERE issue_id=?`, a.IssueID)
	if err := exec(t, s, `INSERT INTO issue_assignment_handoffs(handoff_id,issue_id,from_epoch,to_epoch,from_assignee_json,target_assignee_json,status,obligations_json,request_id,reason,created_at,updated_at) VALUES('handoff_second',?,2,3,'{}','{}','fenced','[]','req2','fixture',2,2)`, a.IssueID); err == nil {
		t.Fatal("two pending handoffs on one issue")
	}
}
