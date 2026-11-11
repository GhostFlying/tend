package views

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

// Effects are immutable execution positions, not retained transport responses.
// Only real unclosed executions and pending business delivery block handoff.
func TestManagerControlHistoryUsesActualExecutionFacts(t *testing.T) {
	ctx := t.Context()
	clock := testkit.NewClock()
	path := filepath.Join(t.TempDir(), "ledger.db")
	db, err := viewFixture.Open(ctx, path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	issue, other := "wi_00000000000000000000000001", "wi_00000000000000000000000002"
	if err := db.Write(ctx, func(tx *store.Tx) error {
		for _, id := range []string{issue, other} {
			i := protocol.Issue{IssueControl: protocol.IssueControl{IssueID: id, State: protocol.IssueActive, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: "fixture-owner"}, AssignmentEpoch: 1, AssignmentState: "active"}, Revision: 1, Plan: protocol.IssuePlan{Title: "Query fixture", Project: "default", Intent: "Preserve actual execution facts", Stages: []protocol.IssueStage{}}, DependsOn: []string{}, CreatedAt: 1, UpdatedAt: 1}
			if err := tx.SaveCanonicalIssue(ctx, i, 0, store.IssueRevisionProvenance{Actor: protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "fixture-owner", Client: "fixture"}, RequestID: "req_fixture", Reason: "query fixture"}); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hosted_configuration_versions(revision,environment_id,private_hash,private_json,public_json,main_agent_id,request_id,accepted_at) VALUES(1,'fixture','hash','{}','{}','fixture','req_config',1)`); err != nil {
			return err
		}
		for _, v := range []struct {
			id, issue, table, status string
			epoch, config            int
		}{
			{"local", issue, "local_hosted_executions", "completed", 1, 1}, {"runner", issue, "runner_coordination_executions", "unknown", 1, 0}, {"other", other, "runner_coordination_executions", "unknown", 1, 0}, {"next", issue, "runner_coordination_executions", "unknown", 2, 0},
		} {
			if _, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: "source-" + v.id, Author: "system", Body: "Actual execution source", CreatedAt: 1}); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO `+v.table+`(execution_id,conversation_id,issue_id,assignment_epoch,role,configuration_revision,agent_ref,private_snapshot,created_at) VALUES(?,'fixture',?,?,'issue_manager',?,'fixture','{}',1)`, v.id, v.issue, v.epoch, v.config); err != nil {
				return err
			}
			effects := "runner_coordination_effects"
			if v.config == 1 {
				effects = "local_hosted_coordination_effects"
			}
			for seq := 1; seq <= 2; seq++ {
				if _, err := tx.Exec(ctx, `INSERT INTO `+effects+`(execution_id,effect_seq) VALUES(?,?)`, v.id, seq); err != nil {
					return err
				}
			}
			if err := tx.PutMainTurn(ctx, store.MainTurn{RequestID: v.id, MessageID: "source-" + v.id, ThreadID: "native", TurnID: "turn-" + v.id, Status: v.status}, 1); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	read := func() []protocol.IssueBlocker {
		t.Helper()
		var blockers []protocol.IssueBlocker
		if err := db.Read(ctx, func(tx *store.ReadTx) error {
			var err error
			blockers, err = ManagerHandoffBlockers(ctx, tx.Q(), issue, 2)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return blockers
	}
	want := []protocol.IssueBlocker{{Code: "old_manager_execution", ObjectID: "runner"}}
	if got := read(); !reflect.DeepEqual(got, want) {
		t.Fatal("completed effects or unrelated epochs became blockers", got)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(ctx, path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	if got := read(); !reflect.DeepEqual(got, want) {
		t.Fatal("restart lost actual unknown execution", got)
	}
	if err := db.Write(ctx, func(tx *store.Tx) error {
		return tx.PutMainTurn(ctx, store.MainTurn{RequestID: "runner", MessageID: "source-runner", ThreadID: "native", TurnID: "turn-runner", Status: store.TurnCompleted}, 2)
	}); err != nil {
		t.Fatal(err)
	}
	if got := read(); len(got) != 0 {
		t.Fatal("settled execution still blocked by old effect positions", got)
	}
	if err := db.Read(ctx, func(tx *store.ReadTx) error {
		var n int
		if err := tx.Q().QueryRowContext(ctx, `SELECT COUNT(*) FROM hosted_coordination_effects`).Scan(&n); err != nil {
			return err
		}
		if n != 8 {
			t.Fatal("business effect history was discarded", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
