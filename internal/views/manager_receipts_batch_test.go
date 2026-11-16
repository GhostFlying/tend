package views

import (
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestManagerReceiptBatchKeepsIssueEpochAndPendingTerminal(t *testing.T) {
	ctx := t.Context()
	db, err := viewFixture.Open(ctx, filepath.Join(t.TempDir(), "ledger.db"), testkit.NewClock().Now)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	issue := "wi_00000000000000000000000001"
	other := "wi_00000000000000000000000002"
	managers := []store.IssueManagerTask{
		{TaskID: "old-manager", IssueID: issue, ExpectedEpoch: 1, AssignmentEpoch: 1},
		{TaskID: "successor", IssueID: issue, ExpectedEpoch: 2, AssignmentEpoch: 2},
		{TaskID: "other-manager", IssueID: other, ExpectedEpoch: 1, AssignmentEpoch: 1},
	}
	if err := db.Write(ctx, func(tx *store.Tx) error {
		for _, id := range []string{issue, other} {
			if err := saveSchedulingIssue(ctx, tx, schedulingIssue(id), 0); err != nil {
				return err
			}
		}
		for _, m := range managers {
			if err := tx.InsertTask(ctx, store.TaskRow{TaskID: m.TaskID, Kind: protocol.TaskKindIssueManager, Project: "default", Title: m.TaskID, Lifecycle: protocol.TaskQueued, ControlMode: protocol.ControlManaged, BlockReason: protocol.BlockNone, ControlRevision: 1, SpecVersion: 1}); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO issue_manager_tasks(task_id,issue_id,category_id,expected_epoch,assignment_epoch,state,source_message_id,request_id,created_at,updated_at,handoff_id) VALUES(?,?,'fixture',?,?,'retiring','','req_fixture',1,1,'')`, m.TaskID, m.IssueID, m.ExpectedEpoch, m.AssignmentEpoch); err != nil {
				return err
			}
		}
		if _, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: "source", Author: "system", Body: "Original execution"}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO runner_coordination_executions(execution_id,conversation_id,issue_id,assignment_epoch,role,configuration_revision,agent_ref,private_snapshot,created_at) VALUES('original','fixture',?,1,'issue_manager',0,'fixture','{}',1)`, issue); err != nil {
			return err
		}
		return tx.PutMainTurn(ctx, store.MainTurn{RequestID: "original", MessageID: "source", ThreadID: "native", TurnID: "turn", Status: "unknown"}, 1)
	}); err != nil {
		t.Fatal(err)
	}
	read := func(want bool) {
		t.Helper()
		if err := db.Read(ctx, func(tx *store.ReadTx) error {
			q := &schedulingQueries{Querier: tx.Q()}
			got, err := managerReceiptsPending(ctx, q, []string{"old-manager", "successor", "other-manager"})
			if err != nil {
				return err
			}
			if q.queries != 1 || got["old-manager"] != want || got["successor"] || got["other-manager"] {
				t.Fatalf("batch receipt scope or query bound: %v queries=%d", got, q.queries)
			}
			for _, m := range managers {
				original, err := ManagerReceiptPending(ctx, tx.Q(), m)
				if err != nil {
					return err
				}
				if got[m.TaskID] != original {
					t.Fatal("batch changed original receipt predicate", m, got, original)
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	read(true)
	if err := db.Write(ctx, func(tx *store.Tx) error {
		return tx.PutMainTurn(ctx, store.MainTurn{RequestID: "original", MessageID: "source", ThreadID: "native", TurnID: "turn", Status: store.TurnCompleted}, 2)
	}); err != nil {
		t.Fatal(err)
	}
	read(false)
	if err := db.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO native_turn_receipts_pending(request_id) VALUES('original')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	read(true)
}
