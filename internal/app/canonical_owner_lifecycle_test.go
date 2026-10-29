package app

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func canonicalLifecycleControl(t *testing.T, h *harness, current protocol.Issue, action string, payload any) protocol.Operation {
	t.Helper()
	return h.exec(owner, h.env("issues."+action, protocol.Target{Type: "issue", ID: current.IssueID}, payload, rev(current.Revision)), CanonicalOwnerIssueCommand{Action: action})
}
func canonicalLifecycleIssue(t *testing.T, op protocol.Operation) protocol.Issue {
	t.Helper()
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var issue protocol.Issue
	if err := json.Unmarshal(op.Result, &issue); err != nil {
		t.Fatal(err)
	}
	return issue
}
func TestCanonicalLifecycleOwnerEmptyIssueCancellationEndsAtBaselineZero(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "cancel", canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch}))
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "finish_cancel", canonicalOwnerEndpoint{canonicalOwnerControl: canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "No remaining work"}}))
	if issue.State != protocol.IssueCancelled || issue.Closure != nil || issue.Revision != 3 {
		t.Fatal(issue)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var baseline int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT baseline_revision FROM work_item_contexts WHERE work_item_id=?`, issue.IssueID).Scan(&baseline); err != nil {
			return err
		}
		if baseline != 0 {
			t.Fatal("endpoint fabricated execution readiness")
		}
		saved, err := store.GetCanonicalIssue(t.Context(), tx.Q(), issue.IssueID)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(saved)
		if err != nil {
			return err
		}
		actual, _ := json.Marshal(issue)
		if !bytes.Equal(encoded, actual) {
			t.Fatal("current/receipt disagree")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestCanonicalLifecycleCancellationRequiresEachActualFollowOnDisposition(t *testing.T) {
	h := newHarness(t)
	p := canonicalOwnerIssueCreate{Plan: protocol.IssuePlan{Title: "Report", Project: "default", Intent: "Produce report", Stages: []protocol.IssueStage{}, FollowOns: []protocol.IssueFollowOn{{FollowOnID: "report", Kind: protocol.WorkItemFollowOnCommitment, Text: "Send report", Source: "Owner request", Status: protocol.WorkItemFollowOnOpen}}}}
	issue := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, p, nil), CanonicalOwnerIssueCommand{Action: "create"}))
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "cancel", canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch}))
	payload := canonicalOwnerEndpoint{canonicalOwnerControl: canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Owner ends the scope"}}
	before := canonicalGuardBusinessSnapshot(t, h)
	expectWorkCode(t, canonicalLifecycleControl(t, h, issue, "finish_cancel", payload), protocol.CodeInvalidTransition)
	if !bytes.Equal(before, canonicalGuardBusinessSnapshot(t, h)) {
		t.Fatal("rejected finish leaked business writes")
	}
	payload.FollowOnDispositions = []canonicalOwnerFollowOnDisposition{{FollowOnID: "report", Reason: "Owner no longer needs report", Evidence: []protocol.IssueEvidence{{Kind: protocol.WorkItemEvidenceOwnerDecision, Reference: "current_request", Summary: "Explicitly end report promise"}}}}
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "finish_cancel", payload))
	if issue.State != protocol.IssueCancelled || issue.Plan.FollowOns[0].Status != protocol.WorkItemFollowOnResolved || issue.Plan.FollowOns[0].Evidence[0].Reference == "current_request" {
		t.Fatal(issue)
	}
}
func TestCanonicalLifecycleIssueEndpointDoesNotStopSharedMainOrOtherIssue(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	legacy := h.createWork(workPlan())
	source, turn := startReviewTurn(t, h)
	if op := h.associate(owner, source, issue.IssueID, legacy.WorkItemID); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "cancel", canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch}))
	associations := h.associations(source)
	var contextRevision int64
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT revision FROM work_item_contexts WHERE work_item_id=?`, issue.IssueID).Scan(&contextRevision)
	}); err != nil {
		t.Fatal(err)
	}
	payload := canonicalOwnerEndpoint{canonicalOwnerControl: canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Owner ends this issue while main continues"}, ExpectedContextRevision: rev(contextRevision), SourceDispositions: []canonicalOwnerSourceDisposition{{MessageID: source, ContentRevision: associations.ContentRevision, AssociationRevision: associations.Revision, Reason: "This issue has no remaining action from this source"}}}
	beforeOutbox := h.count("outbox")
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "finish_cancel", payload))
	if h.count("outbox") != beforeOutbox {
		t.Fatal("endpoint queued a main/native stop")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var canonical, other int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT needs_review FROM message_context_obligations WHERE work_item_id=? AND message_id=?`, issue.IssueID, source).Scan(&canonical); err != nil {
			return err
		}
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT needs_review FROM message_context_obligations WHERE work_item_id=? AND message_id=?`, legacy.WorkItemID, source).Scan(&other); err != nil {
			return err
		}
		if canonical != 0 || other != 1 {
			t.Fatal("endpoint changed the wrong source responsibility")
		}
		native, err := store.GetMainTurn(t.Context(), tx.Q(), turn.RequestID)
		if err != nil {
			return err
		}
		if native.Status != store.TurnStarted {
			t.Fatal("shared main was stopped")
		}
		logical, err := store.LogicalTurn(t.Context(), tx.Q(), turn.RequestID)
		if err != nil {
			return err
		}
		if logical.Status == "completed" || logical.Status == "stopped" {
			t.Fatal("endpoint completed whole logical turn")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Existing main still reviews the other Issue. Ended C's reviewed share must
	// no longer strand the main on baseline0, without granting C dispatch readiness.
	h.prepareAgent(legacy.WorkItemID)
	commitFixtureTurn(t, h, turn.RequestID)
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	logical, err := h.app.LogicalTurn(t.Context(), turn.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if logical.Status != "completed" {
		t.Fatal(logical)
	}
}
func TestCanonicalLifecycleOwnerReparentRetainsSourceShareWithoutExecutionOwnership(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	parent := createCanonicalOwnerTestIssue(t, h)
	source, _ := startReviewTurn(t, h)
	if op := h.associate(owner, source, issue.IssueID); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	p := struct {
		canonicalOwnerControl
		ParentIssueID          string `json:"parent_issue_id"`
		ExpectedParentRevision *int64 `json:"expected_parent_revision"`
	}{canonicalOwnerControl: canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch}, ParentIssueID: parent.IssueID, ExpectedParentRevision: rev(parent.Revision)}
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "reparent", p))
	if issue.ParentIssueID != parent.IssueID {
		t.Fatal(issue)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var pending int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT needs_review FROM message_context_obligations WHERE work_item_id=? AND message_id=?`, issue.IssueID, source).Scan(&pending); err != nil {
			return err
		}
		if pending != 1 {
			t.Fatal("reparent erased source responsibility")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestCanonicalLifecycleCloseReopenKeepsFrozenEndpointHistory(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	closePayload := canonicalOwnerEndpoint{canonicalOwnerControl: canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch}, Closure: &protocol.IssueClosure{Kind: protocol.WorkItemClosureCompleted, Reason: "Owner confirms result", Evidence: []protocol.IssueEvidence{{Kind: protocol.WorkItemEvidenceOwnerDecision, Reference: "current_request", Summary: "Owner accepts responsibility endpoint"}}}}
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "close", closePayload))
	closedRevision := issue.Revision
	if issue.Closure == nil || issue.Closure.Evidence[0].Reference == "current_request" {
		t.Fatal(issue)
	}
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "reopen", canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch}))
	if issue.State != protocol.IssueActive || issue.Closure != nil {
		t.Fatal(issue)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		closed, err := store.GetCanonicalIssueRevision(t.Context(), tx.Q(), issue.IssueID, closedRevision)
		if err != nil {
			return err
		}
		if closed.State != protocol.IssueClosed || closed.Closure == nil {
			t.Fatal("reopen erased immutable endpoint")
		}
		var n int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT count(*) FROM events WHERE kind='issue.closed' AND object_id=?`, issue.IssueID).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Fatal("wrong close event identity")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func canonicalLifecycleContextRevision(t *testing.T, h *harness, issue string) int64 {
	t.Helper()
	var revision int64
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT revision FROM work_item_contexts WHERE work_item_id=?`, issue).Scan(&revision)
	}); err != nil {
		t.Fatal(err)
	}
	return revision
}
func canonicalLifecycleSourceEndpoint(t *testing.T, h *harness, issue protocol.Issue, source string) canonicalOwnerEndpoint {
	t.Helper()
	a := h.associations(source)
	return canonicalOwnerEndpoint{canonicalOwnerControl: canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Owner ends this exact responsibility"}, ExpectedContextRevision: rev(canonicalLifecycleContextRevision(t, h, issue.IssueID)), SourceDispositions: []canonicalOwnerSourceDisposition{{MessageID: source, ContentRevision: a.ContentRevision, AssociationRevision: a.Revision, Reason: "No remaining action from this exact source"}}}
}
func canonicalLifecycleClosure(issue protocol.Issue, kind protocol.IssueClosureKind) canonicalOwnerEndpoint {
	return canonicalOwnerEndpoint{canonicalOwnerControl: canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch}, Closure: &protocol.IssueClosure{Kind: kind, Reason: "Owner confirms responsibility endpoint", Evidence: []protocol.IssueEvidence{{Kind: protocol.WorkItemEvidenceOwnerDecision, Reference: "current_request", Summary: "Explicit owner endpoint"}}}}
}
func TestCanonicalLifecycleSourceDispositionRejectsChangedVersionsAtomically(t *testing.T) {
	for _, field := range []string{"content", "association", "context"} {
		t.Run(field, func(t *testing.T) {
			h := newHarness(t)
			issue := createCanonicalOwnerTestIssue(t, h)
			legacy := h.createWork(workPlan())
			input, turn := startReviewTurn(t, h)
			report := recordAgentMessagePayload{Body: "Report", ReplyTo: input, TurnID: turn.TurnID, ItemID: "source-report", Status: protocol.AssistantStreaming}
			message := h.exec(mainAgent, h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, report, nil), RecordAgentMessage{})
			if message.Status != protocol.OpSucceeded {
				t.Fatal(message)
			}
			var source struct {
				MessageID string `json:"message_id"`
			}
			if err := json.Unmarshal(message.Result, &source); err != nil {
				t.Fatal(err)
			}
			if op := h.associate(owner, source.MessageID, issue.IssueID); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "cancel", canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch}))
			payload := canonicalLifecycleSourceEndpoint(t, h, issue, source.MessageID)
			if field == "content" {
				report.Body = "Completed report"
				report.Status = protocol.AssistantCompleted
				op := h.exec(mainAgent, h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, report, nil), RecordAgentMessage{})
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			} else {
				if op := h.associate(owner, source.MessageID, issue.IssueID, legacy.WorkItemID); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			if field != "context" {
				payload.ExpectedContextRevision = rev(canonicalLifecycleContextRevision(t, h, issue.IssueID))
			}
			before := canonicalGuardBusinessSnapshot(t, h)
			expectWorkCode(t, canonicalLifecycleControl(t, h, issue, "finish_cancel", payload), protocol.CodeStaleRevision)
			if !bytes.Equal(before, canonicalGuardBusinessSnapshot(t, h)) {
				t.Fatal("stale disposition leaked source/history changes")
			}
			fresh := canonicalLifecycleSourceEndpoint(t, h, issue, source.MessageID)
			issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "finish_cancel", fresh))
			if issue.State != protocol.IssueCancelled {
				t.Fatal(issue)
			}
		})
	}
}
func TestCanonicalLifecycleRejectedParentEndpointRollsBackSourceDisposition(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	childPayload := canonicalOwnerIssueCreate{Plan: issue.Plan, ParentIssueID: issue.IssueID, ExpectedParentRevision: rev(issue.Revision)}
	child := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, childPayload, nil), CanonicalOwnerIssueCommand{Action: "create"}))
	source, _ := startReviewTurn(t, h)
	if op := h.associate(owner, source, issue.IssueID); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "cancel", canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch}))
	payload := canonicalLifecycleSourceEndpoint(t, h, issue, source)
	before := canonicalGuardBusinessSnapshot(t, h)
	failed := canonicalLifecycleControl(t, h, issue, "finish_cancel", payload)
	expectWorkCode(t, failed, protocol.CodeInvalidTransition)
	if !bytes.Equal(before, canonicalGuardBusinessSnapshot(t, h)) {
		t.Fatal("later child blocker leaked earlier source disposition")
	}
	child = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, child, "cancel", canonicalOwnerControl{ExpectedAssignmentEpoch: child.AssignmentEpoch}))
	child = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, child, "finish_cancel", canonicalOwnerEndpoint{canonicalOwnerControl: canonicalOwnerControl{ExpectedAssignmentEpoch: child.AssignmentEpoch, Reason: "Owner ends child"}}))
	if child.State != protocol.IssueCancelled {
		t.Fatal(child)
	}
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "finish_cancel", payload))
	if issue.State != protocol.IssueCancelled {
		t.Fatal(issue)
	}
}
func TestCanonicalLifecyclePauseResumeAndSameOwnerReceipt(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	outbox, tasks := h.count("outbox"), h.count("tasks")
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "pause", canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch}))
	if issue.State != protocol.IssuePaused {
		t.Fatal(issue)
	}
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "resume", canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch}))
	if issue.State != protocol.IssueActive {
		t.Fatal(issue)
	}
	if h.count("outbox") != outbox || h.count("tasks") != tasks {
		t.Fatal("pause/resume changed execution or dispatch")
	}
	p := struct {
		canonicalOwnerControl
		Assignee protocol.IssueAssignee `json:"assignee"`
	}{canonicalOwnerControl: canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch}, Assignee: issue.Assignee}
	envelope := h.env("issues.assign", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision))
	events := h.count("events")
	first := h.exec(owner, envelope, CanonicalOwnerIssueCommand{Action: "assign"})
	original := canonicalLifecycleIssue(t, first)
	if original.Revision != issue.Revision || original.AssignmentEpoch != issue.AssignmentEpoch || h.count("events") != events {
		t.Fatal("same-owner no-op changed business state")
	}
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "pause", canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch}))
	events = h.count("events")
	replay := h.exec(owner, envelope, CanonicalOwnerIssueCommand{Action: "assign"})
	if replay.Status != protocol.OpSucceeded || !bytes.Equal(first.Result, replay.Result) || h.count("events") != events {
		t.Fatal("owner no-op replay lost its original receipt", replay)
	}
}
func TestCanonicalLifecycleCompletionDependencyRequiresAchievedEndpoint(t *testing.T) {
	for _, endpoint := range []string{"completed", "declined", "cancelled"} {
		t.Run(endpoint, func(t *testing.T) {
			h := newHarness(t)
			prerequisite := createCanonicalOwnerTestIssue(t, h)
			p := canonicalOwnerIssueCreate{Plan: prerequisite.Plan, DependsOn: []string{prerequisite.IssueID}}
			issue := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, p, nil), CanonicalOwnerIssueCommand{Action: "create"}))
			if endpoint == "cancelled" {
				prerequisite = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, prerequisite, "cancel", canonicalOwnerControl{ExpectedAssignmentEpoch: prerequisite.AssignmentEpoch}))
				prerequisite = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, prerequisite, "finish_cancel", canonicalOwnerEndpoint{canonicalOwnerControl: canonicalOwnerControl{ExpectedAssignmentEpoch: prerequisite.AssignmentEpoch, Reason: "Owner cancels prerequisite"}}))
			} else {
				prerequisite = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, prerequisite, "close", canonicalLifecycleClosure(prerequisite, protocol.IssueClosureKind(endpoint))))
			}
			closePayload := canonicalLifecycleClosure(issue, protocol.WorkItemClosureCompleted)
			closed := canonicalLifecycleControl(t, h, issue, "close", closePayload)
			if endpoint == "completed" {
				issue = canonicalLifecycleIssue(t, closed)
				if issue.State != protocol.IssueClosed {
					t.Fatal(issue)
				}
			} else {
				expectWorkCode(t, closed, protocol.CodeInvalidTransition)
				empty := []string{}
				replacement := struct {
					canonicalOwnerControl
					DependsOn *[]string `json:"depends_on"`
				}{canonicalOwnerControl: canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch}, DependsOn: &empty}
				issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "dependencies", replacement))
				issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "close", closePayload))
				if issue.State != protocol.IssueClosed {
					t.Fatal(issue)
				}
			}
		})
	}
}

func TestCanonicalLifecycleGraphAndAncestorGates(t *testing.T) {
	h := newHarness(t)
	parent := createCanonicalOwnerTestIssue(t, h)
	childPayload := canonicalOwnerIssueCreate{Plan: parent.Plan, ParentIssueID: parent.IssueID, ExpectedParentRevision: rev(parent.Revision)}
	first := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, childPayload, nil), CanonicalOwnerIssueCommand{Action: "create"}))
	second := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, childPayload, nil), CanonicalOwnerIssueCommand{Action: "create"}))
	replace := func(issue protocol.Issue, ids []string) protocol.Operation {
		p := struct {
			canonicalOwnerControl
			DependsOn *[]string `json:"depends_on"`
		}{canonicalOwnerControl: canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch}, DependsOn: &ids}
		return canonicalLifecycleControl(t, h, issue, "dependencies", p)
	}
	// Parent completion already waits for children; explicitly depending on its
	// child is legal, and harmless sibling ordering is also legal.
	parent = canonicalLifecycleIssue(t, replace(parent, []string{first.IssueID}))
	first = canonicalLifecycleIssue(t, replace(first, []string{second.IssueID}))
	before := canonicalGuardBusinessSnapshot(t, h)
	expectWorkCode(t, replace(second, []string{first.IssueID}), protocol.CodeInvalidRequest)
	if !bytes.Equal(before, canonicalGuardBusinessSnapshot(t, h)) {
		t.Fatal("dependency cycle leaked graph changes")
	}
	p := struct {
		canonicalOwnerControl
		ParentIssueID string `json:"parent_issue_id"`
	}{canonicalOwnerControl: canonicalOwnerControl{ExpectedAssignmentEpoch: parent.AssignmentEpoch}, ParentIssueID: first.IssueID}
	expectWorkCode(t, canonicalLifecycleControl(t, h, parent, "reparent", p), protocol.CodeInvalidRequest)
	second = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, second, "close", canonicalLifecycleClosure(second, protocol.WorkItemClosureDeclined)))
	first = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, first, "close", canonicalLifecycleClosure(first, protocol.WorkItemClosureDeclined)))
	parent = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, parent, "close", canonicalLifecycleClosure(parent, protocol.WorkItemClosureDeclined)))
	before = canonicalGuardBusinessSnapshot(t, h)
	expectWorkCode(t, canonicalLifecycleControl(t, h, first, "reopen", canonicalOwnerControl{ExpectedAssignmentEpoch: first.AssignmentEpoch}), protocol.CodeInvalidTransition)
	childPayload.ExpectedParentRevision = rev(parent.Revision)
	created := h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, childPayload, nil), CanonicalOwnerIssueCommand{Action: "create"})
	expectWorkCode(t, created, protocol.CodeInvalidTransition)
	if !bytes.Equal(before, canonicalGuardBusinessSnapshot(t, h)) {
		t.Fatal("terminal ancestor admitted an active child")
	}
}
func TestCanonicalLifecycleParentEndpointSerializesWithChildActivation(t *testing.T) {
	for _, activation := range []string{"create", "reopen"} {
		t.Run(activation, func(t *testing.T) {
			h := newHarness(t)
			parent := createCanonicalOwnerTestIssue(t, h)
			closeEnv := h.env("issues.close", protocol.Target{Type: "issue", ID: parent.IssueID}, canonicalLifecycleClosure(parent, protocol.WorkItemClosureCompleted), rev(parent.Revision))
			childPayload := canonicalOwnerIssueCreate{Plan: parent.Plan, ParentIssueID: parent.IssueID, ExpectedParentRevision: rev(parent.Revision)}
			var childEnv *protocol.Envelope
			childAction := "create"
			if activation == "create" {
				childEnv = h.env("issues.create", protocol.Target{Type: "issue"}, childPayload, nil)
			} else {
				child := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, childPayload, nil), CanonicalOwnerIssueCommand{Action: "create"}))
				child = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, child, "close", canonicalLifecycleClosure(child, protocol.WorkItemClosureCompleted)))
				childAction = "reopen"
				childEnv = h.env("issues.reopen", protocol.Target{Type: "issue", ID: child.IssueID}, canonicalOwnerControl{ExpectedAssignmentEpoch: child.AssignmentEpoch}, rev(child.Revision))
			}
			type result struct {
				op  protocol.Operation
				err error
			}
			results := make(chan result, 2)
			start := make(chan struct{})
			for _, request := range []struct {
				env    *protocol.Envelope
				action string
			}{{closeEnv, "close"}, {childEnv, childAction}} {
				go func(env *protocol.Envelope, action string) {
					<-start
					op, err := h.app.Execute(t.Context(), owner, env, CanonicalOwnerIssueCommand{Action: action})
					results <- result{op: op, err: err}
				}(request.env, request.action)
			}
			close(start)
			succeeded := 0
			for range 2 {
				r := <-results
				if r.err != nil {
					t.Fatal(r.err)
				}
				if r.op.Status == protocol.OpSucceeded {
					succeeded++
				} else if r.op.Status != protocol.OpFailed || r.op.Error == nil || (r.op.Error.Code != protocol.CodeInvalidTransition && r.op.Error.Code != protocol.CodeStaleRevision) {
					t.Fatal(r.op)
				}
			}
			if succeeded != 1 {
				t.Fatalf("%d endpoints succeeded; exactly one may win", succeeded)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				current, err := store.GetCanonicalIssue(t.Context(), tx.Q(), parent.IssueID)
				if err != nil {
					return err
				}
				var active int
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT count(*) FROM issue_controls WHERE parent_issue_id=? AND state IN ('active','paused','cancelling')`, parent.IssueID).Scan(&active); err != nil {
					return err
				}
				if current.State == protocol.IssueClosed && active != 0 {
					t.Fatal("active child escaped under closed parent")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCanonicalLifecycleParentRetainsLateTerminalChildSourceDuty(t *testing.T) {
	h := newHarness(t)
	parent := createCanonicalOwnerTestIssue(t, h)
	cp := canonicalOwnerIssueCreate{Plan: parent.Plan, ParentIssueID: parent.IssueID, ExpectedParentRevision: rev(parent.Revision)}
	child := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, cp, nil), CanonicalOwnerIssueCommand{Action: "create"}))
	input, turn := startReviewTurn(t, h)
	report := recordAgentMessagePayload{Body: "Preliminary report", ReplyTo: input, TurnID: turn.TurnID, ItemID: "late-child-report", Status: protocol.AssistantStreaming}
	op := h.exec(mainAgent, h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, report, nil), RecordAgentMessage{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var source struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(op.Result, &source); err != nil {
		t.Fatal(err)
	}
	if op := h.associate(owner, source.MessageID, child.IssueID); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	endpoint := canonicalLifecycleSourceEndpoint(t, h, child, source.MessageID)
	endpoint.Closure = canonicalLifecycleClosure(child, protocol.WorkItemClosureCompleted).Closure
	child = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, child, "close", endpoint))
	report.Body = "Completed report contains additional facts"
	report.Status = protocol.AssistantCompleted
	op = h.exec(mainAgent, h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, report, nil), RecordAgentMessage{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	before := canonicalGuardBusinessSnapshot(t, h)
	blocked := canonicalLifecycleControl(t, h, parent, "close", canonicalLifecycleClosure(parent, protocol.WorkItemClosureCompleted))
	expectWorkCode(t, blocked, protocol.CodeInvalidTransition)
	if !bytes.Contains(blocked.Error.Details, []byte("child_source_review")) || !bytes.Contains(blocked.Error.Details, []byte(child.IssueID)) {
		t.Fatal("missing concrete child source blocker", blocked)
	}
	if !bytes.Equal(before, canonicalGuardBusinessSnapshot(t, h)) {
		t.Fatal("parent endpoint discarded late child duty")
	}
	child = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, child, "reopen", canonicalOwnerControl{ExpectedAssignmentEpoch: child.AssignmentEpoch}))
	endpoint = canonicalLifecycleSourceEndpoint(t, h, child, source.MessageID)
	endpoint.Closure = canonicalLifecycleClosure(child, protocol.WorkItemClosureCompleted).Closure
	child = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, child, "close", endpoint))
	if child.State != protocol.IssueClosed {
		t.Fatal(child)
	}
	parent = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, parent, "close", canonicalLifecycleClosure(parent, protocol.WorkItemClosureCompleted)))
	if parent.State != protocol.IssueClosed {
		t.Fatal(parent)
	}
}

func TestCanonicalLifecycleCancellingParentAllowsTerminalChildSourceReview(t *testing.T) {
	for _, childState := range []string{"closed", "cancelled"} {
		t.Run(childState, func(t *testing.T) {
			h := newHarness(t)
			parent := createCanonicalOwnerTestIssue(t, h)
			cp := canonicalOwnerIssueCreate{Plan: parent.Plan, ParentIssueID: parent.IssueID, ExpectedParentRevision: rev(parent.Revision)}
			child := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, cp, nil), CanonicalOwnerIssueCommand{Action: "create"}))
			input, turn := startReviewTurn(t, h)
			report := recordAgentMessagePayload{Body: "Preliminary child report", ReplyTo: input, TurnID: turn.TurnID, ItemID: "terminal-child-source", Status: protocol.AssistantStreaming}
			op := h.exec(mainAgent, h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, report, nil), RecordAgentMessage{})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			var source struct {
				MessageID string `json:"message_id"`
			}
			if err := json.Unmarshal(op.Result, &source); err != nil {
				t.Fatal(err)
			}
			if op := h.associate(owner, source.MessageID, child.IssueID); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if childState == "cancelled" {
				child = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, child, "cancel", canonicalOwnerControl{ExpectedAssignmentEpoch: child.AssignmentEpoch}))
			}
			endpoint := canonicalLifecycleSourceEndpoint(t, h, child, source.MessageID)
			action := "finish_cancel"
			if childState == "closed" {
				action = "close"
				endpoint.Closure = canonicalLifecycleClosure(child, protocol.WorkItemClosureCompleted).Closure
			}
			child = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, child, action, endpoint))
			terminal := child
			parent = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, parent, "cancel", canonicalOwnerControl{ExpectedAssignmentEpoch: parent.AssignmentEpoch}))
			report.Body = "Completed child report has additional facts"
			report.Status = protocol.AssistantCompleted
			op = h.exec(mainAgent, h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, report, nil), RecordAgentMessage{})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			parentFinish := canonicalOwnerEndpoint{canonicalOwnerControl: canonicalOwnerControl{ExpectedAssignmentEpoch: parent.AssignmentEpoch, Reason: "Owner finishes parent cancellation"}}
			before := canonicalGuardBusinessSnapshot(t, h)
			blocked := canonicalLifecycleControl(t, h, parent, "finish_cancel", parentFinish)
			expectWorkCode(t, blocked, protocol.CodeInvalidTransition)
			if !bytes.Contains(blocked.Error.Details, []byte("child_source_review")) {
				t.Fatal("missing child source blocker", blocked)
			}
			expectWorkCode(t, canonicalLifecycleControl(t, h, child, "reopen", canonicalOwnerControl{ExpectedAssignmentEpoch: child.AssignmentEpoch}), protocol.CodeInvalidTransition)
			if !bytes.Equal(before, canonicalGuardBusinessSnapshot(t, h)) {
				t.Fatal("rejected parent finish/reopen changed responsibility")
			}
			reviewPayload := canonicalLifecycleSourceEndpoint(t, h, child, source.MessageID)
			reviewEnv := h.env("issues.review_sources", protocol.Target{Type: "issue", ID: child.IssueID}, reviewPayload, rev(child.Revision))
			reviewed := h.exec(owner, reviewEnv, CanonicalOwnerIssueCommand{Action: "review_sources"})
			child = canonicalLifecycleIssue(t, reviewed)
			if child.State != terminal.State || child.Revision != terminal.Revision+1 || child.AssignmentEpoch != terminal.AssignmentEpoch || child.Assignee != terminal.Assignee {
				t.Fatal("source review changed terminal responsibility", child)
			}
			oldGoal, _ := json.Marshal(struct {
				Plan    protocol.IssuePlan
				Closure *protocol.IssueClosure
			}{terminal.Plan, terminal.Closure})
			newGoal, _ := json.Marshal(struct {
				Plan    protocol.IssuePlan
				Closure *protocol.IssueClosure
			}{child.Plan, child.Closure})
			if !bytes.Equal(oldGoal, newGoal) {
				t.Fatal("source review rewrote plan or original closure")
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				old, err := store.GetCanonicalIssueRevision(t.Context(), tx.Q(), child.IssueID, terminal.Revision)
				if err != nil {
					return err
				}
				actual, _ := json.Marshal(old)
				expected, _ := json.Marshal(terminal)
				if !bytes.Equal(actual, expected) {
					t.Fatal("original terminal history changed")
				}
				var reason string
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT reason_json FROM issue_control_revisions WHERE issue_id=? AND revision=?`, child.IssueID, child.Revision).Scan(&reason); err != nil {
					return err
				}
				if !bytes.Contains([]byte(reason), []byte(source.MessageID)) {
					t.Fatal("new owner source decision absent from revision")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			replay := h.exec(owner, reviewEnv, CanonicalOwnerIssueCommand{Action: "review_sources"})
			if replay.Status != protocol.OpSucceeded || !bytes.Equal(replay.Result, reviewed.Result) {
				t.Fatal("source review lost exact receipt", replay)
			}
			parent = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, parent, "finish_cancel", parentFinish))
			if parent.State != protocol.IssueCancelled {
				t.Fatal(parent)
			}
		})
	}
}

func TestCanonicalLifecycleTerminalSourceReviewCanSettleNamedBatches(t *testing.T) {
	h := newHarness(t)
	parent := createCanonicalOwnerTestIssue(t, h)
	legacy := h.createWork(workPlan())
	cp := canonicalOwnerIssueCreate{Plan: parent.Plan, ParentIssueID: parent.IssueID, ExpectedParentRevision: rev(parent.Revision)}
	child := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, cp, nil), CanonicalOwnerIssueCommand{Action: "create"}))
	child = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, child, "close", canonicalLifecycleClosure(child, protocol.WorkItemClosureCompleted)))
	parent = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, parent, "cancel", canonicalOwnerControl{ExpectedAssignmentEpoch: parent.AssignmentEpoch}))
	first, turn := startReviewTurn(t, h)
	op := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{Body: "A second source for the ended scope"}, nil), SubmitMessage{})
	if op.Status != protocol.OpAccepted {
		t.Fatal(op)
	}
	var second struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(op.Result, &second); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{first, second.MessageID} {
		if op := h.associate(owner, source, child.IssueID, legacy.WorkItemID); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	p := canonicalLifecycleSourceEndpoint(t, h, child, first)
	envelope := h.env("issues.review_sources", protocol.Target{Type: "issue", ID: child.IssueID}, p, rev(child.Revision))
	reviewed := h.exec(owner, envelope, CanonicalOwnerIssueCommand{Action: "review_sources"})
	child = canonicalLifecycleIssue(t, reviewed)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var pending int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT needs_review FROM message_context_obligations WHERE work_item_id=? AND message_id=?`, child.IssueID, second.MessageID).Scan(&pending); err != nil {
			return err
		}
		if pending != 1 {
			t.Fatal("unmentioned source was cleared")
		}
		var currentReview bool
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT EXISTS(SELECT 1 FROM logical_turn_context_reviews r JOIN work_item_contexts c ON c.work_item_id=r.work_item_id WHERE r.logical_turn_id=? AND r.work_item_id=? AND r.revision=c.revision)`, turn.RequestID, child.IssueID).Scan(&currentReview); err != nil {
			return err
		}
		if currentReview {
			t.Fatal("partial source batch certified the whole current context")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	finish := canonicalOwnerEndpoint{canonicalOwnerControl: canonicalOwnerControl{ExpectedAssignmentEpoch: parent.AssignmentEpoch, Reason: "Finish parent after child sources"}}
	expectWorkCode(t, canonicalLifecycleControl(t, h, parent, "finish_cancel", finish), protocol.CodeInvalidTransition)
	child = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, child, "review_sources", canonicalLifecycleSourceEndpoint(t, h, child, second.MessageID)))
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var pending int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT count(*) FROM message_context_obligations WHERE work_item_id=? AND needs_review=1`, legacy.WorkItemID).Scan(&pending); err != nil {
			return err
		}
		if pending != 2 {
			t.Fatal("source batches changed the other Issue")
		}
		var revision int64
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT revision FROM work_item_contexts WHERE work_item_id=?`, child.IssueID).Scan(&revision); err != nil {
			return err
		}
		done, err := canonicalTerminalContextReviewed(t.Context(), tx.Q(), turn.RequestID, child.IssueID, revision)
		if err != nil {
			return err
		}
		if !done {
			t.Fatal("complete source batches did not review the current terminal share")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	replay := h.exec(owner, envelope, CanonicalOwnerIssueCommand{Action: "review_sources"})
	if replay.Status != protocol.OpSucceeded || !bytes.Equal(replay.Result, reviewed.Result) {
		t.Fatal("source batch replay lost its older receipt", replay)
	}
	parent = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, parent, "finish_cancel", finish))
	if parent.State != protocol.IssueCancelled || child.State != protocol.IssueClosed {
		t.Fatal(parent, child)
	}
}
