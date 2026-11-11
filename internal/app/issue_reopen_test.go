package app

import (
	"sync"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func publishReopenManager(t *testing.T, h *harness, r *protocol.RunnerRuntime, i protocol.Issue) (store.IssueManagerTask, protocol.Assignment) {
	t.Helper()
	op := h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: i.IssueID}, protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Reason: "Coordinate continuing responsibility"}, rev(i.Revision)), RequestIssueManager{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	as := reviewClaimManager(t, h, r)
	var m store.IssueManagerTask
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		m, err = store.GetIssueManagerTask(t.Context(), tx.Q(), as.TaskID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return m, as
}
func TestIssueReopenRestoresNewCoordinatorAndPreservesTerminalHistory(t *testing.T) {
	h, r := managerClaimHarness(t)
	i := createCanonicalOwnerTestIssue(t, h)
	task := createCanonicalTaskForTest(t, h, i)
	acceptDeliveryTaskForTest(t, h, task)
	endDeliveryForTest(t, h, i.IssueID, task)
	i = canonicalTaskIssue(t, h, i.IssueID)
	d := deliveryDutyForTest(t, h, i.IssueID, task)
	i = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, i, "review_completions", map[string]any{"expected_assignment_epoch": i.AssignmentEpoch, "reviews": []protocol.CompletionReviewAcknowledgement{{EventID: d.EventID, Reason: "Explicit owner endpoint reviewed"}}}))
	previous, as := publishReopenManager(t, h, r, i)
	stopOwnerClaim(t, h, as)
	i = canonicalTaskIssue(t, h, i.IssueID)
	closed := canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, i, "close", canonicalLifecycleClosure(i, protocol.WorkItemClosureCompleted)))
	envelope := h.env("issues.reopen", protocol.Target{Type: "issue", ID: i.IssueID}, canonicalOwnerControl{ExpectedAssignmentEpoch: closed.AssignmentEpoch}, rev(closed.Revision))
	op := h.exec(owner, envelope, CanonicalOwnerIssueCommand{Action: "reopen"})
	restored := canonicalLifecycleIssue(t, op)
	if restored.State != protocol.IssueActive || restored.Closure != nil || restored.BoardStatus() != protocol.IssueBacklog || restored.OwnerSelection == nil {
		t.Fatal(restored)
	}
	before := h.count("tasks")
	expectWorkCode(t, h.exec(owner, envelope, CanonicalOwnerIssueCommand{Action: "reopen"}), protocol.CodeStaleRevision)
	if h.count("tasks") != before || canonicalTaskIssue(t, h, i.IssueID).Revision != restored.Revision {
		t.Fatal("stale reopen allocated execution or changed the active responsibility")
	}
	if got := admissionClaim(t, h, r); got != nil {
		t.Fatal("reopen started backlog", got)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		old, err := store.GetIssueManagerTask(t.Context(), tx.Q(), previous.TaskID)
		if err != nil {
			return err
		}
		history, err := store.GetCanonicalIssueRevision(t.Context(), tx.Q(), i.IssueID, closed.Revision)
		if err == nil && (old.State != "completed" || history.Closure == nil || history.State != protocol.IssueClosed) {
			t.Fatal(old, history)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	conversation := previous.ConversationID
	submitHostedAdmissionInput(t, h, conversation)
	if ready, err := h.app.AgentConversationClaimed(t.Context(), conversation); err != nil || ready {
		t.Fatal("backlog admitted", ready, err)
	}
	restored = issueAdmissionStatus(t, h, restored, protocol.IssueTodo, nil)
	assignment := reviewClaimManager(t, h, r)
	if assignment.TaskID == previous.TaskID || assignment.Manager == nil || assignment.Manager.ConversationID != conversation || h.count("tasks") != before+1 {
		t.Fatal(assignment)
	}
	if h.task(task).Lifecycle != protocol.TaskCompleted || h.task(previous.TaskID).Lifecycle != protocol.TaskCompleted {
		t.Fatal("terminal Task resurrected")
	}
	if ready, err := h.app.AgentConversationClaimed(t.Context(), conversation); err != nil || !ready {
		t.Fatal("claimed successor not admitted", ready, err)
	}
	if h.count("issue_delivery_executions") != 0 || h.count("acceptances") != 1 {
		t.Fatal("reopen rewrote delivery/acceptance")
	}
}
func TestIssueReopenConcurrentRequestsPublishOneSuccessor(t *testing.T) {
	h, r := managerClaimHarness(t)
	i := createCanonicalOwnerTestIssue(t, h)
	_, as := publishReopenManager(t, h, r, i)
	stopOwnerClaim(t, h, as)
	i = canonicalTaskIssue(t, h, i.IssueID)
	closed := canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, i, "close", canonicalLifecycleClosure(i, protocol.WorkItemClosureCompleted)))
	var results [2]protocol.Operation
	var wg sync.WaitGroup
	for j := range results {
		env := h.env("issues.reopen", protocol.Target{Type: "issue", ID: i.IssueID}, canonicalOwnerControl{ExpectedAssignmentEpoch: closed.AssignmentEpoch}, rev(closed.Revision))
		wg.Add(1)
		go func() { defer wg.Done(); results[j] = h.exec(owner, env, CanonicalOwnerIssueCommand{Action: "reopen"}) }()
	}
	wg.Wait()
	succeeded := 0
	for _, op := range results {
		if op.Status == protocol.OpSucceeded {
			succeeded++
		} else if op.Error == nil || op.Error.Code != protocol.CodeStaleRevision {
			t.Fatal(op)
		}
	}
	if succeeded != 1 || h.count("issue_manager_tasks") != 1 {
		t.Fatal(results)
	}
	current := canonicalTaskIssue(t, h, i.IssueID)
	issueAdmissionStatus(t, h, current, protocol.IssueTodo, nil)
	reviewClaimManager(t, h, r)
	if h.count("issue_manager_tasks") != 2 {
		t.Fatal("explicit todo did not create exactly one successor")
	}
}
func TestIssueReopenUnknownCoordinatorCannotStartAnother(t *testing.T) {
	h, r := managerClaimHarness(t)
	i := createCanonicalOwnerTestIssue(t, h)
	previous, as := publishReopenManager(t, h, r, i)
	i = canonicalTaskIssue(t, h, i.IssueID)
	closed := canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, i, "close", canonicalLifecycleClosure(i, protocol.WorkItemClosureCompleted)))
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE runs SET observed_state='unknown' WHERE run_id=?`, as.RunID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before := h.count("issue_manager_tasks")
	op, err := h.app.Execute(t.Context(), owner, h.env("issues.reopen", protocol.Target{Type: "issue", ID: i.IssueID}, canonicalOwnerControl{ExpectedAssignmentEpoch: closed.AssignmentEpoch}, rev(closed.Revision)), CanonicalOwnerIssueCommand{Action: "reopen"})
	if err == nil {
		err = op.Error
	}
	if protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
		t.Fatal(op, err)
	}
	if h.count("issue_manager_tasks") != before || canonicalTaskIssue(t, h, i.IssueID).State != protocol.IssueClosed || h.run(as.RunID).ObservedState != protocol.RunUnknown || h.task(previous.TaskID).Lifecycle == protocol.TaskCompleted {
		t.Fatal("unknown original was replaced")
	}
}
func TestNamedMainReopensOnlyCurrentAdmittedOwnerSource(t *testing.T) {
	for _, sourceKind := range []string{"owner", "system", "unadmitted", "withdrawn", "edited"} {
		t.Run(sourceKind, func(t *testing.T) {
			h := hostedManagerHarness(t)
			i := createCanonicalOwnerTestIssue(t, h)
			closed := canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, i, "close", canonicalLifecycleClosure(i, protocol.WorkItemClosureCompleted)))
			source, delivery := h.say("Reopen this responsibility and restore coordination")
			turn := startHostedReview(t, h, "main", delivery)
			if sourceKind == "unadmitted" {
				source = queueMessage(t, h, "Another owner message outside this turn")
			}
			if sourceKind == "system" {
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE conversation_messages SET author='system' WHERE message_id=?`, source)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			if sourceKind == "withdrawn" {
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE conversation_message_controls SET withdrawn_at=1 WHERE message_id=?`, source)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			version := int64(1)
			if sourceKind == "edited" {
				version = 2
			}
			payload := protocol.IssueControlPayload{IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: source, ExpectedSourceRevision: &version}, ExpectedAssignmentEpoch: closed.AssignmentEpoch}
			cmd := HostedCoordinationCommand{Command: CanonicalOwnerIssueCommand{Action: "reopen"}, ExecutionID: turn.RequestID, EffectSeq: 1}
			op, err := h.app.Execute(t.Context(), mainAgent, h.env(cmd.Operation(), protocol.Target{Type: "issue", ID: i.IssueID}, payload, rev(closed.Revision)), cmd)
			if sourceKind == "owner" {
				if err != nil || op.Status != protocol.OpSucceeded {
					t.Fatal(op, err)
				}
			} else if err == nil && op.Status == protocol.OpSucceeded {
				t.Fatal("noncurrent owner source reopened Issue", sourceKind)
			}
		})
	}
}
