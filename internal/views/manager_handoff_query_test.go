package views

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

// The old relation is kept only as an equivalence oracle on a small mixed
// fixture. It must not reintroduce the historical fanout in production.
const priorManagerDeliveriesSQL = `SELECT DISTINCT o.request_id FROM outbox o JOIN logical_turn_executions e ON e.request_id=o.request_id JOIN logical_turn_executions original ON original.logical_turn_id=e.logical_turn_id JOIN hosted_executions h ON h.execution_id=original.request_id WHERE h.issue_id=? AND h.assignment_epoch<? AND o.operation IN ('conversation.repair_turn','conversation.recover_agent','main_thread.handle_message','main_thread.handle_event') AND o.status IN ('sending','unknown') UNION SELECT o.request_id FROM agent_conversations c JOIN outbox o ON o.destination='background_thread:'||c.conversation_id WHERE c.work_item_id=? AND o.operation IN ('main_thread.handle_message','main_thread.handle_event') AND o.status IN ('sending','unknown') ORDER BY 1`

func TestManagerHandoffDeliveriesDeduplicateLogicalHistory(t *testing.T) {
	ctx := t.Context()
	clock := testkit.NewClock()
	db, err := viewFixture.Open(ctx, filepath.Join(t.TempDir(), "ledger.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	issue, other := "wi_00000000000000000000000001", "wi_00000000000000000000000002"
	if err := db.Write(ctx, func(tx *store.Tx) error {
		for _, id := range []string{issue, other} {
			i := protocol.Issue{IssueControl: protocol.IssueControl{IssueID: id, State: protocol.IssueActive, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: "fixture-owner"}, AssignmentEpoch: 1, AssignmentState: "active"}, Revision: 1, Plan: protocol.IssuePlan{Title: "Query fixture", Project: "default", Intent: "Preserve blocker facts", Stages: []protocol.IssueStage{}}, DependsOn: []string{}, CreatedAt: 1, UpdatedAt: 1}
			if err := tx.SaveCanonicalIssue(ctx, i, 0, store.IssueRevisionProvenance{Actor: protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "fixture-owner", Client: "fixture"}, RequestID: "req_fixture", Reason: "query fixture"}); err != nil {
				return err
			}
		}
		if _, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: "msg_fixture", ConversationID: "main", Author: "owner", Body: "Query fixture"}); err != nil {
			return err
		}
		for _, id := range []string{"shared", "new-epoch", "other-issue", "orphan"} {
			if _, err := tx.Exec(ctx, `INSERT INTO logical_turns(logical_turn_id,message_id,updated_at) VALUES(?,'msg_fixture',1)`, id); err != nil {
				return err
			}
		}
		for _, v := range []struct {
			id, logical, issue string
			epoch              int
		}{{"old-a", "shared", issue, 1}, {"old-b", "shared", issue, 1}, {"new-owner", "new-epoch", issue, 2}, {"other-owner", "other-issue", other, 1}} {
			if _, err := tx.Exec(ctx, `INSERT INTO runner_coordination_executions(execution_id,conversation_id,issue_id,assignment_epoch,role,configuration_revision,agent_ref,private_snapshot,created_at) VALUES(?,'fixture',?,?,'issue_manager',0,'fixture-agent','{}',1)`, v.id, v.issue, v.epoch); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO logical_turn_executions(request_id,logical_turn_id) VALUES(?,?)`, v.id, v.logical); err != nil {
				return err
			}
		}
		// A main execution has no issue or assignment epoch. Its logical delivery
		// must not acquire the requested issue through the UNION view.
		if _, err := tx.Exec(ctx, `INSERT INTO hosted_configuration_versions(revision,environment_id,private_hash,private_json,public_json,main_agent_id,request_id,accepted_at) VALUES(1,'fixture','hash','{}','{}','fixture','req_config',1)`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO local_hosted_executions(execution_id,conversation_id,issue_id,assignment_epoch,role,configuration_revision,agent_ref,private_snapshot,created_at) VALUES('main-owner','main',NULL,NULL,'main',1,'fixture','{}',1)`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO logical_turn_executions(request_id,logical_turn_id) VALUES('main-owner','orphan')`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO agent_conversations(conversation_id,work_item_id,created_at) VALUES('fixture-conversation',?,1)`, issue); err != nil {
			return err
		}
		cases := []struct{ id, logical, status, operation, destination string }{
			{"old-a", "", "unknown", "conversation.repair_turn", "background_thread:fixture-conversation"},
			{"repair", "shared", "sending", "conversation.repair_turn", "native"},
			{"event", "shared", "unknown", "main_thread.handle_event", "background_thread:fixture-conversation"},
			{"done", "shared", "delivered", "conversation.repair_turn", "native"},
			{"pending", "shared", "pending", "conversation.repair_turn", "native"},
			{"wrong-method", "shared", "unknown", "unrelated.operation", "native"},
			{"new", "new-epoch", "unknown", "conversation.repair_turn", "native"},
			{"other", "other-issue", "unknown", "conversation.repair_turn", "native"},
			{"orphan-delivery", "orphan", "unknown", "conversation.repair_turn", "native"},
			{"conversation-only", "", "unknown", "main_thread.handle_message", "background_thread:fixture-conversation"},
		}
		for n, v := range cases {
			if v.logical != "" {
				if _, err := tx.Exec(ctx, `INSERT INTO logical_turn_executions(request_id,logical_turn_id) VALUES(?,?)`, v.id, v.logical); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `INSERT INTO outbox(outbox_id,request_id,destination,operation,payload_json,status,next_attempt_at,created_at,updated_at,seq) VALUES(?,?,?,?,'{}',?,0,1,1,?)`, v.id, v.id, v.destination, v.operation, v.status, n+1); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Read(ctx, func(tx *store.ReadTx) error {
		expected, err := store.FollowupIDs(ctx, tx.Q(), priorManagerDeliveriesSQL, issue, 2, issue)
		if err != nil {
			return err
		}
		blockers, err := ManagerHandoffBlockers(ctx, tx.Q(), issue, 2)
		if err != nil {
			return err
		}
		var actual []string
		for _, v := range blockers {
			if v.Code == "old_manager_delivery" {
				actual = append(actual, v.ObjectID)
			}
		}
		if !reflect.DeepEqual(actual, expected) || !reflect.DeepEqual(actual, []string{"conversation-only", "event", "old-a", "repair"}) {
			t.Fatalf("changed delivery blockers: got %v previous %v", actual, expected)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
