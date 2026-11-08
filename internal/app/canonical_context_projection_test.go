package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

// These are explicit read-model fixtures, not native completion evidence.
func contextProjectionTurn(t *testing.T, h *harness, logical, conversation, hostedIssue string, items ...string) string {
	t.Helper()
	message, request := "msg_"+logical, "req_"+logical
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT OR IGNORE INTO agent_conversations(conversation_id,created_at) VALUES(?,1)`, conversation); err != nil {
			return err
		}
		if _, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: message, ConversationID: conversation, Author: "owner", Body: "original scoped input"}); err != nil {
			return err
		}
		if err := tx.PutMainTurn(t.Context(), store.MainTurn{RequestID: request, MessageID: message, ThreadID: logical, Status: store.TurnInterrupted}, 1); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO logical_turns(logical_turn_id,message_id,status,updated_at) VALUES(?,?,'recovering',1)`, logical, message); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO logical_turn_executions(request_id,logical_turn_id) VALUES(?,?)`, request, logical); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO logical_turn_messages(logical_turn_id,message_id) VALUES(?,?)`, logical, message); err != nil {
			return err
		}
		if hostedIssue != "" {
			if _, err := tx.Exec(t.Context(), `INSERT INTO runner_coordination_executions(execution_id,conversation_id,issue_id,assignment_epoch,role,configuration_revision,agent_ref,private_snapshot,created_at) VALUES(?,?,?,1,'issue_manager',0,'fixture/owner','{}',1)`, request, conversation, hostedIssue); err != nil {
				return err
			}
		}
		for _, item := range items {
			if _, err := tx.Exec(t.Context(), `INSERT OR IGNORE INTO logical_turn_contexts(logical_turn_id,work_item_id) VALUES(?,?)`, logical, item); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return request
}

func canonicalContextProjection(t *testing.T, h *harness, issue string) protocol.IssueContextView {
	t.Helper()
	var got protocol.IssueContextView
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		got, err = views.CanonicalIssueContext(t.Context(), tx.Q(), issue, 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestCanonicalContextProjectionDoesNotInheritGlobalRecovery(t *testing.T) {
	h := newHarness(t)
	issue, other := createCanonicalOwnerTestIssue(t, h), createCanonicalOwnerTestIssue(t, h)
	contextProjectionTurn(t, h, "global_recovery", "issue:"+other.IssueID, "", issue.IssueID, other.IssueID)
	got := canonicalContextProjection(t, h, issue.IssueID)
	if got.Prepared || !got.DispatchReady || got.SyncStatus != "synced" || got.CoordinationState != "idle" || len(got.DispatchBlockers) != 0 {
		t.Fatalf("unrelated recovery or legacy baseline leaked: %+v", got)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		legacy, err := views.IssueContext(t.Context(), tx.Q(), issue.IssueID, 0)
		if err != nil {
			return err
		}
		if legacy.DispatchReady || legacy.SyncStatus != "initializing" || legacy.CoordinationState != "recovering" {
			t.Fatalf("legacy gate changed: %+v", legacy)
		}
		var status string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT status FROM logical_turns WHERE logical_turn_id='global_recovery'`).Scan(&status); err != nil {
			return err
		}
		if status != "recovering" {
			t.Fatal("read settled uncertain history")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	otherView := canonicalContextProjection(t, h, other.IssueID)
	if otherView.DispatchReady || otherView.CoordinationState != "recovering" {
		t.Fatalf("actual conversation responsibility lost: %+v", otherView)
	}
}

func TestCanonicalContextProjectionKeepsOwnedNativeAndSourceDuties(t *testing.T) {
	h := newHarness(t)
	issue, other := createCanonicalOwnerTestIssue(t, h), createCanonicalOwnerTestIssue(t, h)
	request := contextProjectionTurn(t, h, "owned_recovery", "main", issue.IssueID, other.IssueID)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO native_turn_receipts_pending(request_id) VALUES(?)`, request); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO message_context_obligations(message_id,work_item_id,needs_review) VALUES('msg_owned_recovery',?,1)`, issue.IssueID); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got := canonicalContextProjection(t, h, issue.IssueID)
	seen := map[string]bool{}
	for _, b := range got.DispatchBlockers {
		seen[b.Code] = true
	}
	if got.Prepared || got.DispatchReady || got.SyncStatus != "needs_review" || got.CoordinationState != "recovering" || !seen["native_receipt"] || !seen["source_correction"] || !seen["logical_turn_state"] {
		t.Fatalf("own obligations hidden: %+v", got)
	}
	otherView := canonicalContextProjection(t, h, other.IssueID)
	if !otherView.DispatchReady || otherView.CoordinationState != "idle" || len(otherView.DispatchBlockers) != 0 {
		t.Fatalf("shared membership granted execution ownership: %+v", otherView)
	}
}

func TestCanonicalContextProjectionHostedIdentityPrecedesConversation(t *testing.T) {
	h := newHarness(t)
	issue, other := createCanonicalOwnerTestIssue(t, h), createCanonicalOwnerTestIssue(t, h)
	contextProjectionTurn(t, h, "explicit_owner", "issue:"+other.IssueID, issue.IssueID, other.IssueID)
	got := canonicalContextProjection(t, h, other.IssueID)
	if got.CoordinationState != "idle" || !got.DispatchReady {
		t.Fatalf("conversation overrode stored execution identity: %+v", got)
	}
}

func TestCanonicalContextProjectionReviewedProcessingAndRepairChain(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	contextProjectionTurn(t, h, "processing_owner", "main", issue.IssueID)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE logical_turns SET status='processing' WHERE logical_turn_id='processing_owner'`); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO logical_turn_context_reviews(logical_turn_id,work_item_id,revision) SELECT 'processing_owner',work_item_id,revision FROM work_item_contexts WHERE work_item_id=?`, issue.IssueID); err != nil {
			return err
		}
		// A previous assignment epoch is still responsible for its real receipt.
		_, err := tx.Exec(t.Context(), `UPDATE runner_coordination_executions SET assignment_epoch=99 WHERE execution_id='req_processing_owner'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	got := canonicalContextProjection(t, h, issue.IssueID)
	if got.Prepared || !got.DispatchReady || got.CoordinationState != "processing" {
		t.Fatalf("reviewed processing became a false gate: %+v", got)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.PutMainTurn(t.Context(), store.MainTurn{RequestID: "req_repair_reader", MessageID: "msg_processing_owner", ThreadID: "repair", Status: store.TurnInterrupted}, 2); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO logical_turn_executions(request_id,logical_turn_id,repair) VALUES('req_repair_reader','processing_owner',1)`); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO native_turn_receipts_pending(request_id) VALUES('req_repair_reader')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	got = canonicalContextProjection(t, h, issue.IssueID)
	if got.DispatchReady || len(got.DispatchBlockers) != 1 || got.DispatchBlockers[0].ObjectID != "req_repair_reader" {
		t.Fatalf("old-epoch repair receipt lost: %+v", got)
	}
}

func TestCanonicalContextProjectionRemovedAndQueuedSourceObligations(t *testing.T) {
	h := newHarness(t)
	issue, sibling := createCanonicalOwnerTestIssue(t, h), createCanonicalOwnerTestIssue(t, h)
	contextProjectionTurn(t, h, "planner_shared", "main", "", issue.IssueID, sibling.IssueID)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		for _, item := range []string{issue.IssueID, sibling.IssueID} {
			if _, err := tx.Exec(t.Context(), `INSERT INTO message_context_obligations(message_id,work_item_id,needs_review) VALUES('msg_planner_shared',?,1)`, item); err != nil {
				return err
			}
		}
		// No current association: removal must not erase the admitted correction.
		if _, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: "msg_queued_successor", Author: "owner", Body: "queued successor"}); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO message_context_obligations(message_id,work_item_id,needs_review) VALUES('msg_queued_successor',?,1)`, issue.IssueID); err != nil {
			return err
		}
		return tx.InsertOutbox(t.Context(), store.OutboxRow{OutboxID: "ob_queued_successor", RequestID: "req_queued_successor", Destination: "main_thread", Operation: "main_thread.handle_message", PayloadJSON: `{"message_id":"msg_queued_successor"}`})
	}); err != nil {
		t.Fatal(err)
	}
	for _, item := range []string{issue.IssueID, sibling.IssueID} {
		got := canonicalContextProjection(t, h, item)
		if got.DispatchReady || got.SyncStatus != "needs_review" || got.CoordinationState != "idle" || len(got.DispatchBlockers) != 1 || got.DispatchBlockers[0].ObjectID != "msg_planner_shared" {
			t.Fatalf("shared source duty or queued admission changed: %+v", got)
		}
	}
}

func TestCanonicalContextProjectionKeepsEffectiveFactsAndAncestorTaskBindings(t *testing.T) {
	h := newHarness(t)
	parent := createCanonicalOwnerTestIssue(t, h)
	create := canonicalOwnerIssueCreate{ParentIssueID: parent.IssueID, Plan: protocol.IssuePlan{Title: "Child", Project: "default", Intent: "bounded execution", Stages: []protocol.IssueStage{}}}
	op := h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, create, nil), CanonicalOwnerIssueCommand{Action: "create"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var child protocol.Issue
	if err := json.Unmarshal(op.Result, &child); err != nil {
		t.Fatal(err)
	}
	task := canonicalTaskReadFixture(t, h, child)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		digest, _, err := store.EffectiveContext(t.Context(), tx.Q(), parent.IssueID)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(map[string]any{"shared_context": []protocol.TaskWorkContext{{WorkItemID: parent.IssueID, ContextDigest: digest}}})
		if err != nil {
			return err
		}
		if err := tx.InsertSpec(t.Context(), task, 2, string(raw), "", 1); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE tasks SET spec_version=2 WHERE task_id=?`, task); err != nil {
			return err
		}
		if _, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: "msg_effective_review", Author: "system", Body: "effective source correction"}); err != nil {
			return err
		}
		for _, scope := range []string{"global", "project"} {
			if _, err := tx.Exec(t.Context(), `INSERT INTO work_item_context_facts(fact_id,project,scope,kind,body,source_message_id,attribution,needs_review,created_at) VALUES(?,'default',?,'constraint','Recheck effective input','msg_effective_review','owner',1,1)`, "fact_"+scope, scope); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got := canonicalContextProjection(t, h, parent.IssueID)
	facts, agreement := 0, false
	for _, b := range got.DispatchBlockers {
		if b.Code == "fact_review" {
			facts++
		}
		if b.Code == "task_agreement" && b.ObjectID == task && b.SpecVersion == 2 {
			agreement = true
		}
	}
	if got.DispatchReady || got.SyncStatus != "needs_review" || facts != 2 || agreement {
		t.Fatalf("source facts should remain visible without a new Task permission: %+v", got)
	}
}
