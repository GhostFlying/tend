package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func reviewManagerAssignment(t *testing.T) (*harness, *protocol.RunnerRuntime, protocol.Issue, protocol.Assignment, string) {
	t.Helper()
	h, runtime := managerClaimHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	source := queueMessage(t, h, "Coordinate this intent")
	result := h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{SourceMessageID: source, ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Continuing responsibility"}, rev(issue.Revision)), RequestIssueManager{})
	if result.Status != protocol.OpSucceeded {
		t.Fatal(result)
	}
	assignment := reviewClaimManager(t, h, runtime)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		issue, err = store.GetCanonicalIssue(t.Context(), tx.Q(), issue.IssueID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return h, runtime, issue, assignment, source
}

func reviewClaimManager(t *testing.T, h *harness, runtime *protocol.RunnerRuntime) protocol.Assignment {
	t.Helper()
	op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: runtime, WorkspaceProtocol: 1}, nil), Claim{})
	var result struct {
		Assignment *protocol.Assignment `json:"assignment"`
	}
	if err := json.Unmarshal(op.Result, &result); err != nil || result.Assignment == nil {
		t.Fatal(op, err)
	}
	return *result.Assignment
}

func TestFault_ManagerQueuedNativeTurnRechecksFenceBeforeClaim(t *testing.T) {
	for _, fence := range []string{"replacement", "withdrawal", "pause"} {
		t.Run(fence, func(t *testing.T) {
			h, _, issue, as, source := reviewManagerAssignment(t)
			id, err := h.app.QueueManagerNative(t.Context(), as.RunID, "turn", json.RawMessage(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			switch fence {
			case "replacement":
				if result := h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{Replace: true, ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Fence before native dequeue"}, rev(issue.Revision)), RequestIssueManager{}); result.Status != protocol.OpSucceeded {
					t.Fatal(result)
				}
			case "withdrawal":
				withdrawOwnerSource(t, h, source)
			case "pause":
				if result := h.exec(owner, h.env("issues.pause", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Pause native responsibility"}, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "pause"}); result.Status != protocol.OpSucceeded {
					t.Fatal(result)
				}
			}
			op := h.runAs(as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
			var next protocol.ManagerNativeRequest
			if err := json.Unmarshal(op.Result, &next); err != nil || next.ID != "" {
				t.Fatal("fenced native turn was claimed", op, err)
			}
			reply, ready, err := h.app.ManagerNativeResult(t.Context(), as.RunID, id)
			if err != nil || !ready || !reply.Rejected {
				t.Fatal("dequeue did not retain a definite non-execution receipt", reply, ready, err)
			}
			read, err := h.app.QueueManagerNative(t.Context(), as.RunID, "find", json.RawMessage(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			op = h.runAs(as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
			if err := json.Unmarshal(op.Result, &next); err != nil || next.ID != read {
				t.Fatal("fence blocked original receipt reading", op, err)
			}
		})
	}
}

func TestFault_ManagerStoppedEventsRetainOriginalReplayAndToolResult(t *testing.T) {
	h, runtime, _, as, _ := reviewManagerAssignment(t)
	resultPayload := json.RawMessage(`{"thread_id":"native","turn_id":"turn","call_id":"call","tool_result":{"Success":true,"Text":"ORIGINAL_RESULT"}}`)
	if _, err := h.app.QueueManagerNative(t.Context(), as.RunID, "tool_result", resultPayload); err != nil {
		t.Fatal(err)
	}
	for index, body := range []string{`{"thread_id":"native","native":{"Kind":"request_usage"}}`, `{"thread_id":"native","call":{"ThreadID":"native","TurnID":"turn","CallID":"call"}}`} {
		if op := h.runAs(as, protocol.OpManagerNativeEvent, protocol.ManagerNativeEvent{Sequence: int64(index + 1), Body: json.RawMessage(body)}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	if op := h.runAs(as, protocol.OpManagerNativeStopped, protocol.ManagerStoppedPayload{Reason: "Original native process exited", LastSequence: 2}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeStopped}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// Restart the server before any proxy has applied these uploaded events.
	var path string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var seq int
		var name string
		return tx.Q().QueryRowContext(t.Context(), `PRAGMA database_list`).Scan(&seq, &name, &path)
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(t.Context(), path, h.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	h.store = reopened
	h.app = New(reopened, h.ids, h.clock.Now)
	reader := reviewClaimManager(t, h, runtime)
	if reader.Manager == nil || !reader.Manager.ReceiptOnly || reader.TaskID != as.TaskID {
		t.Fatal("old events did not create a pinned receipt reader", reader)
	}
	events, err := h.app.ManagerReplayEvents(t.Context(), reader.RunID, 0)
	if err != nil || len(events) != 2 || events[0].RunID != as.RunID || events[0].Sequence != 1 || events[1].Sequence != 2 {
		t.Fatal("old upload identity disappeared", events, err)
	}
	retained, err := h.app.ManagerRetainedToolResult(t.Context(), reader.RunID, as.RunID, "native", "turn", "call")
	if err != nil || string(retained) != `{"Success":true,"Text":"ORIGINAL_RESULT"}` {
		t.Fatal("original tool result changed", string(retained), err)
	}
	other := createCanonicalOwnerTestIssue(t, h)
	if op := h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: other.IssueID}, protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: other.AssignmentEpoch, Reason: "Separate responsibility"}, rev(other.Revision)), RequestIssueManager{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	unrelated := reviewClaimManager(t, h, runtime)
	if leak, err := h.app.ManagerReplayEvents(t.Context(), unrelated.RunID, 0); err != nil || len(leak) != 0 {
		t.Fatal("another Issue received the old event stream", leak, err)
	}
	if leak, err := h.app.ManagerRetainedToolResult(t.Context(), unrelated.RunID, as.RunID, "native", "turn", "call"); err != nil || len(leak) != 0 {
		t.Fatal("another Issue received old tool output", string(leak), err)
	}
	for _, event := range events {
		if err := h.app.AcknowledgeManagerEvent(t.Context(), event.RunID, event.Sequence, "native", "turn", "", false); err != nil {
			t.Fatal(err)
		}
	}
	if replay, err := h.app.ManagerReplayEvents(t.Context(), reader.RunID, 0); err != nil || len(replay) != 0 {
		t.Fatal("applied facts replayed again", replay, err)
	}
	if op, err := h.app.Execute(t.Context(), runPrincipal(as.RunID, as.Role), h.env(protocol.OpManagerNativeEvent, runTarget(as.RunID), protocol.ManagerNativeEvent{Sequence: 3, Body: json.RawMessage(`{}`)}, nil), ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent}); err == nil && op.Status != protocol.OpFailed {
		t.Fatal("stopped process appended new facts", op, err)
	}
}

func TestManagerSchedulingUsesProjectWeight(t *testing.T) {
	h, _ := managerClaimHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	if op := h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Use shared scheduling policy"}, rev(issue.Revision)), RequestIssueManager{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE projects SET weight=10 WHERE project=?`, issue.Plan.Project)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		candidates, err := views.IssueManagerCandidates(t.Context(), tx.Q())
		if err == nil && (len(candidates) != 1 || candidates[0].Weight != 10) {
			t.Fatal(candidates)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestManagerPendingOwnerTakebackCancelsFutureClaim(t *testing.T) {
	h, runtime := managerClaimHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	if op := h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Delegate before selecting an executor"}, rev(issue.Revision)), RequestIssueManager{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op, err := h.app.MigrateManager(t.Context(), owner, h.env(OpManagerMigration, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.ManagerMigrationPayload{Phase: "cancel", ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Owner takes back the pending responsibility"}, rev(issue.Revision))); err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
	op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: runtime, WorkspaceProtocol: 1}, nil), Claim{})
	var result struct {
		Assignment *protocol.Assignment `json:"assignment"`
	}
	if err := json.Unmarshal(op.Result, &result); err != nil || result.Assignment != nil {
		t.Fatal("runner acquired a cancelled pending responsibility", op, err)
	}
}

func TestFault_NestedManagerSourceMustStayInAdmittedScope(t *testing.T) {
	h := hostedManagerHarness(t)
	inventoryCategory(t, h, protocol.DefaultIssueManagerCategory, 0, "active")
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	turn, principal := startHostedIssueReview(t, h, issue)
	checkNestedManagerSource(t, h, issue, turn, principal)
}

func TestFault_RunnerNestedManagerSourceMustStayInAdmittedScope(t *testing.T) {
	h, _, issue, as, _ := reviewManagerAssignment(t)
	turn, _ := startHostedIssueReview(t, h, issue)
	principal := runPrincipal(as.RunID, protocol.RoleIssueManager)
	principal.Thread = turn.ThreadID
	principal.ConversationID = "issue:" + issue.IssueID
	checkNestedManagerSource(t, h, issue, turn, principal)
}

func checkNestedManagerSource(t *testing.T, h *harness, issue protocol.Issue, turn store.MainTurn, principal protocol.Principal) {
	t.Helper()
	outside := queueMessage(t, h, "Independent owner input")
	payload := protocol.CreateIssuePayload{ParentIssueID: issue.IssueID, ExpectedParentRevision: rev(issue.Revision), Plan: protocol.IssuePlan{Title: "Child", Project: "default", Intent: "Only inherit authorized scope"}, Manager: &protocol.RequestIssueManagerPayload{CategoryID: protocol.DefaultIssueManagerCategory, SourceMessageID: outside, Reason: "Delegate child"}}
	for _, state := range []string{"queued", "admitted_outside", "admitted_shared"} {
		if state != "queued" {
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				if _, err := tx.Exec(t.Context(), `UPDATE outbox SET status='delivered' WHERE json_extract(payload_json,'$.message_id')=?`, outside); err != nil {
					return err
				}
				if state == "admitted_shared" {
					return tx.BindMessage(t.Context(), outside, "work_item", issue.IssueID, "shared_authorization")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		before := h.count("issue_controls")
		op, err := h.app.Execute(t.Context(), principal, h.env("issues.create", protocol.Target{Type: "issue"}, payload, nil), HostedCoordinationCommand{Command: CanonicalOwnerIssueCommand{Action: "create"}, ExecutionID: turn.RequestID, EffectSeq: 1})
		if state == "admitted_shared" {
			if err != nil || op.Status != protocol.OpSucceeded {
				t.Fatal("admitted inherited source was rejected", op, err)
			}
		} else {
			if protocol.CodeOf(err) != protocol.CodeForbidden {
				t.Fatal("nested source must be rejected", state, op, err)
			}
			if h.count("issue_controls") != before {
				t.Fatal("rejected source created a child Issue")
			}
		}
	}
}

// A persisted open run is also cleanup work when no delivery remains. This is
// the restart window between the last FinishDelivery and native close enqueue.
func TestFault_ManagerLastDeliveryCompletionRetainsTeardownWakeup(t *testing.T) {
	h, _, _, as, _ := reviewManagerAssignment(t)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE outbox SET status='delivered' WHERE destination=?`, "background_thread:"+as.Manager.ConversationID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE runs SET observed_state='running' WHERE run_id=?`, as.RunID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// A new App has no in-memory pool attachment or pending delivery to rely on.
	restarted := New(h.store, h.ids, h.clock.Now)
	ids, err := restarted.PendingAgentConversations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, id := range ids {
		found = found || id == as.Manager.ConversationID
	}
	if !found {
		t.Fatal("open manager was omitted from startup cleanup", ids)
	}
	if _, err := restarted.QueueManagerNative(t.Context(), as.RunID, "close", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
}

func TestFault_ManagerCloseSettlesTrailingStartupRPC(t *testing.T) {
	h, _, _, as, _ := reviewManagerAssignment(t)
	closeID, err := h.app.QueueManagerNative(t.Context(), as.RunID, "close", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if op := h.runAs(as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// A replacement server attachment queues Models behind the claimed close.
	modelsID, err := h.app.QueueManagerNative(t.Context(), as.RunID, "models", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if op := h.runAs(as, protocol.OpManagerNativeReply, protocol.ManagerNativeReply{ID: closeID, Result: json.RawMessage(`null`)}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	reply, ready, err := h.app.ManagerNativeResult(t.Context(), as.RunID, modelsID)
	if err != nil || !ready || !reply.Rejected {
		t.Fatal("unclaimed RPC did not receive its non-execution receipt", reply, ready, err)
	}
}

func TestFault_ManagerClosingAttachmentUsesConfirmedMetadataToDrainEvents(t *testing.T) {
	h, _, _, as, _ := reviewManagerAssignment(t)
	completed := func(method string, result json.RawMessage) {
		t.Helper()
		id, err := h.app.QueueManagerNative(t.Context(), as.RunID, method, json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		if op := h.runAs(as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		if op := h.runAs(as, protocol.OpManagerNativeReply, protocol.ManagerNativeReply{ID: id, Result: result}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	catalog := json.RawMessage(`[{"ID":"model","Name":"native-model"}]`)
	thread := json.RawMessage(`{"ID":"native","Permissions":{"Sandbox":"read-only","ApprovalPolicy":"never"}}`)
	completed("models", catalog)
	completed("start", thread)
	if raw, err := h.app.ManagerClosingAttachment(t.Context(), as.RunID, "models", ""); err != nil || len(raw) != 0 {
		t.Fatal("ordinary startup reused a closing receipt", string(raw), err)
	}
	if _, err := h.app.QueueManagerNative(t.Context(), as.RunID, "close", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	selection, named, err := h.app.HostedRuntimeSelection(t.Context(), as.Manager.ConversationID)
	if err != nil || !named || !selection.ReceiptOnly {
		t.Fatal("closing attachment was offered new business", selection, named, err)
	}
	if _, err := h.app.QueueManagerNative(t.Context(), as.RunID, "turn", json.RawMessage(`{}`)); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("closing attachment accepted a new native turn", err)
	}
	for _, read := range []struct {
		method, thread string
		want           json.RawMessage
	}{{"models", "", catalog}, {"resume", "native", thread}, {"resume", "unrelated", nil}, {"start", "native", nil}, {"turn", "native", nil}} {
		raw, err := h.app.ManagerClosingAttachment(t.Context(), as.RunID, read.method, read.thread)
		if err != nil || string(raw) != string(read.want) {
			t.Fatal("closing metadata read changed identity", read.method, string(raw), err)
		}
	}
	// Reattaching does not enqueue another native RPC behind close. A final
	// uploaded event can now be applied before the original close is committed.
	if op := h.runAs(as, protocol.OpManagerNativeEvent, protocol.ManagerNativeEvent{Sequence: 1, Body: json.RawMessage(`{"thread_id":"native","native":{"Kind":"request_usage"}}`)}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.app.AcknowledgeManagerEvent(t.Context(), as.RunID, 1, "native", "", "", false); err != nil {
		t.Fatal(err)
	}
	closeOp := h.runAs(as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
	var closeRequest protocol.ManagerNativeRequest
	if err := json.Unmarshal(closeOp.Result, &closeRequest); err != nil || closeRequest.Method != "close" {
		t.Fatal(closeOp, err)
	}
	if op := h.runAs(as, protocol.OpManagerNativeReply, protocol.ManagerNativeReply{ID: closeRequest.ID, Result: json.RawMessage(`null`), LastSequence: 1}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func TestFault_CancelledMigrationRetainsHostedReceiptReader(t *testing.T) {
	h, _ := managerClaimHarness(t)
	inventoryCategory(t, h, "planner", 0, "active")
	_, prepared := acceptHostedFixture(t, h, hostedConfigurationFixture(), 0)
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(prepared))
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	old, _ := startHostedIssueReview(t, h, issue)
	if err := h.app.AwaitNativeReceipts(t.Context(), old.ThreadID); err != nil {
		t.Fatal(err)
	}
	env := h.env(OpManagerMigration, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.ManagerMigrationPayload{Phase: "switch", ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Migrate"}, rev(issue.Revision))
	if op, err := h.app.MigrateManager(t.Context(), owner, env); err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var e error
		issue, e = store.GetCanonicalIssue(t.Context(), tx.Q(), issue.IssueID)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	op := h.exec(owner, h.env("issues.cancel", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Cancel while old receipt remains"}, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "cancel"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if ready, err := h.app.AgentConversationClaimed(t.Context(), "issue:"+issue.IssueID); err != nil || !ready {
		t.Fatalf("old unknown hosted receipt reader lost after cancellation: ready=%v err=%v", ready, err)
	}
}

func TestFault_ConsultationPromotionRetainsSelectedManagerSource(t *testing.T) {
	h := hostedManagerHarness(t)
	inventoryCategory(t, h, protocol.DefaultIssueManagerCategory, 0, "active")
	c := createConsultationFixture(t, h)
	intent, turn, principal := consultationTurn(t, h, c, "Investigate my selected request")
	outside := queueMessage(t, h, "Unrelated queued private owner input")
	p := protocol.PromoteConsultationPayload{
		IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: intent, ExpectedSourceRevision: rev(1)},
		Plan:                 protocol.IssuePlan{Title: "Selected goal", Project: "default", Intent: "Only the consultation scope"},
		Manager:              &protocol.RequestIssueManagerPayload{SourceMessageID: outside, Reason: "Use unrelated source"},
	}
	op, err := h.app.Execute(t.Context(), principal, h.env("conversation.promote", protocol.Target{Type: "conversation", ID: c.ID}, p, nil), HostedCoordinationCommand{Command: PromoteConsultation{}, ExecutionID: turn.RequestID, EffectSeq: 1})
	if err != nil {
		t.Logf("rejected as expected: %v", err)
		return
	}
	if op.Status != protocol.OpSucceeded {
		t.Logf("rejected as expected: %+v", op)
		return
	}
	var issue protocol.Issue
	if err := json.Unmarshal(op.Result, &issue); err != nil {
		t.Fatal(err)
	}
	var source string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		m, err := store.CurrentIssueManagerTask(t.Context(), tx.Q(), issue.IssueID)
		source = m.SourceMessageID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Fatalf("consultation accepted outside source %s (want rejection), manager source %s, associations %+v", outside, source, h.associations(outside))
}

func TestManagerBoardOmitsCoordinationAndStatusDoesNotSelfWake(t *testing.T) {
	h, _, issue, as, _ := reviewManagerAssignment(t)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		page, err := views.CanonicalIssues(t.Context(), tx.Q(), views.CanonicalIssueFilter{Board: true})
		if err != nil {
			return err
		}
		if len(page.Items) != 1 || page.Items[0].Board == nil || page.Items[0].Board.Execution != (protocol.ExecutionCounts{}) {
			t.Fatal("manager counted as a delivery task on board", page)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	turn, _ := startHostedIssueReview(t, h, issue)
	principal := runPrincipal(as.RunID, protocol.RoleIssueManager)
	principal.Thread = turn.ThreadID
	principal.ConversationID = as.Manager.ConversationID
	result := hostedEffect(t, h, principal, turn, 1, protocol.Target{Type: "issue", ID: issue.IssueID}, rev(issue.Revision), protocol.SetIssueStatusPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Status: protocol.IssueInProgress}, CanonicalOwnerIssueCommand{Action: "set_status"})
	if result.Status != protocol.OpSucceeded {
		t.Fatal(result)
	}
	if _, ok := statusNotice(t, h, issue.IssueID); ok {
		t.Fatal("runner manager generated its own status notification")
	}
}

func cancellationManager(t *testing.T) (*harness, *protocol.RunnerRuntime, protocol.Issue, protocol.Assignment) {
	t.Helper()
	h, runtime := managerClaimHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	if op := h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Retain cancellation settlement responsibility"}, rev(issue.Revision)), RequestIssueManager{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	as := reviewClaimManager(t, h, runtime)
	return h, runtime, canonicalTaskIssue(t, h, issue.IssueID), as
}

func closeCancellationRound(t *testing.T, h *harness, as protocol.Assignment) {
	t.Helper()
	id, err := h.app.QueueManagerNative(t.Context(), as.RunID, "close", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if op := h.runAs(as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(as, protocol.OpManagerNativeReply, protocol.ManagerNativeReply{ID: id, Result: json.RawMessage(`null`)}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func TestManagerRetainsRestrictedAuthorityUntilCancellationSettles(t *testing.T) {
	h, _, issue, as := cancellationManager(t)
	turn, _ := startHostedIssueReview(t, h, issue)
	principal := runPrincipal(as.RunID, protocol.RoleIssueManager)
	principal.Thread = turn.ThreadID
	principal.ConversationID = as.Manager.ConversationID
	op := hostedEffect(t, h, principal, turn, 1, protocol.Target{Type: "issue", ID: issue.IssueID}, rev(issue.Revision), protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Cancel and settle existing commitments"}, CanonicalOwnerIssueCommand{Action: "cancel"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	// Cancellation retains cleanup, without permitting more task production.
	_, err := h.app.Execute(t.Context(), principal, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.CreateIssueTaskPayload{}, rev(issue.Revision)), HostedCoordinationCommand{Command: CanonicalOwnerTaskCreate{}, ExecutionID: turn.RequestID, EffectSeq: 2})
	if protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("cancelling manager could create more work", err)
	}
	if op := hostedEffect(t, h, principal, turn, 2, protocol.Target{Type: "issue", ID: issue.IssueID}, nil, hostedReviewPayload(t, h, issue), ReviewHostedIssueContext{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := commitHostedReview(t, h, principal, turn, 3); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	op = hostedEffect(t, h, principal, turn, 4, protocol.Target{Type: "issue", ID: issue.IssueID}, rev(issue.Revision), protocol.IssueEndpointPayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Existing responsibilities are accounted for"}}, CanonicalOwnerIssueCommand{Action: "finish_cancel"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if canonicalTaskIssue(t, h, issue.IssueID).State != protocol.IssueCancelled {
		t.Fatal("cancellation did not settle")
	}
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	closeCancellationRound(t, h, as)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		task, err := store.GetTask(t.Context(), tx.Q(), as.TaskID)
		if err == nil && task.Lifecycle != protocol.TaskCancelled {
			t.Fatal("manager ended before or after the wrong fact", task)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestManagerWaitingCancellationWakesAndReclaimsItsPinnedRunner(t *testing.T) {
	h, runtime, issue, as := cancellationManager(t)
	// Account for the first round's batch before its confirmed native close.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE background_batches SET status='frozen' WHERE work_item_id=?`, issue.IssueID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE outbox SET status='delivered' WHERE destination=?`, "background_thread:"+as.Manager.ConversationID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	closeCancellationRound(t, h, as)
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "pause", protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Pause this Issue"}))
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "cancel", protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Cancel and finish the existing responsibility"}))
	if issue.State != protocol.IssueCancelling {
		t.Fatal(issue)
	}
	next := reviewClaimManager(t, h, runtime)
	if next.TaskID != as.TaskID || next.Execution == nil || next.Manager.ReceiptOnly || *next.Execution != *as.Execution {
		t.Fatal("cancellation replaced the responsible execution", as, next)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var sources int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM background_sources s JOIN background_batches b USING(batch_id) WHERE s.kind='issue.cancelling' AND b.work_item_id=?`, issue.IssueID).Scan(&sources); err != nil {
			return err
		}
		if sources != 1 {
			t.Fatal("owner cancellation was not durably delivered", sources)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
