package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func hostedManagerHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	if op := h.exec(owner, h.env("categories.put", protocol.Target{Type: "category", ID: "planner"}, protocol.PutAgentCategoryPayload{Title: "Planner", State: "active"}, rev(0)), PutAgentCategory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	_, p := acceptHostedFixture(t, h, hostedConfigurationFixture(), 0)
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(p))
	return h
}
func assignHostedManager(t *testing.T, h *harness, issue protocol.Issue) protocol.Issue {
	t.Helper()
	p := protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeAgent, Ref: "server-hosted/planner"}, Reason: "Delegate investigation to the Issue manager"}
	next := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision)), HostedIssueAssignment{}))
	finish := protocol.FinishIssueAssignmentPayload{ExpectedAssignmentEpoch: next.AssignmentEpoch, HandoffID: next.PendingHandoffID, Reason: "No previous manager execution is outstanding"}
	return canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.finish_assignment", protocol.Target{Type: "issue", ID: issue.IssueID}, finish, rev(next.Revision)), HostedIssueAssignment{Finish: true}))
}
func startHostedReview(t *testing.T, h *harness, conversation string, delivery Delivery) store.MainTurn {
	t.Helper()
	var payload HandleMessage
	if err := json.Unmarshal(delivery.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	selected, named, err := h.app.NamedHostedSelection(t.Context(), conversation)
	if err != nil || !named {
		t.Fatal(selected, named, err)
	}
	thread := mainAgent.Thread
	if conversation != "main" {
		thread = "native-" + conversation
	}
	turn := store.MainTurn{RequestID: delivery.RequestID, MessageID: payload.MessageID, ThreadID: thread, Status: store.TurnStarting, Hosted: &selected, Model: selected.Definition.Model, Effort: selected.Definition.Effort}
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	turn.Status = store.TurnStarted
	turn.TurnID = "turn-" + turn.RequestID
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	return turn
}
func startHostedIssueReview(t *testing.T, h *harness, issue protocol.Issue) (store.MainTurn, protocol.Principal) {
	t.Helper()
	conversation := "issue:" + issue.IssueID
	submitted := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{ConversationID: conversation, Body: "Investigate this vague goal and report what is known"}, nil), SubmitMessage{})
	if submitted.Status != protocol.OpAccepted {
		t.Fatal(submitted)
	}
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	turn := startHostedReview(t, h, conversation, delivery)
	return turn, protocol.Principal{Kind: protocol.PrincipalBackgroundAgent, Subject: "hosted-" + issue.IssueID, Thread: turn.ThreadID, ConversationID: conversation}
}
func hostedReviewPayload(t *testing.T, h *harness, issue protocol.Issue) protocol.IssueContextReview {
	t.Helper()
	p := protocol.IssueContextReview{ExpectedAssignmentEpoch: issue.AssignmentEpoch, ContextRevision: h.agentContext(issue.IssueID).Revision, Summary: "Reviewed goals and constraints; uncertainty remains and investigation is delegated"}
	var ids []string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		ids, err = store.FollowupIDs(t.Context(), tx.Q(), `SELECT message_id FROM message_context_obligations WHERE work_item_id=? AND needs_review=1 ORDER BY message_id`, issue.IssueID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		m := h.associations(id)
		p.Sources = append(p.Sources, protocol.LogicalTurnReview{MessageID: id, ContentRevision: m.ContentRevision, AssociationRevision: m.Revision})
	}
	return p
}
func hostedEffect(t *testing.T, h *harness, principal protocol.Principal, turn store.MainTurn, seq int64, target protocol.Target, expected *int64, payload any, command Command) protocol.Operation {
	t.Helper()
	return h.exec(principal, h.env(command.Operation(), target, payload, expected), HostedCoordinationCommand{Command: command, ExecutionID: turn.RequestID, EffectSeq: seq})
}
func commitHostedReview(t *testing.T, h *harness, principal protocol.Principal, turn store.MainTurn, seq int64) protocol.Operation {
	t.Helper()
	state, err := h.app.LogicalTurn(t.Context(), turn.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	p := protocol.LogicalTurnCommit{Contexts: state.Contexts}
	for _, m := range state.Messages {
		p.Messages = append(p.Messages, protocol.LogicalTurnReview{MessageID: m.MessageID, ContentRevision: m.ContentRevision, AssociationRevision: m.Revision})
	}
	return hostedEffect(t, h, principal, turn, seq, protocol.Target{Type: "logical_turn", ID: turn.RequestID}, nil, p, CommitLogicalTurn{RequestID: turn.RequestID})
}
func TestHostedManagerBaselineZeroInvestigationAndScopedCommit(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	turn, principal := startHostedIssueReview(t, h, issue)
	payload := canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Investigate the unknown goal", Spec: testSpec, Rating: protocol.RatingP1}
	created := hostedEffect(t, h, principal, turn, 1, protocol.Target{Type: "issue", ID: issue.IssueID}, rev(issue.Revision), payload, CanonicalOwnerTaskCreate{})
	if created.Status != protocol.OpSucceeded {
		t.Fatal(created)
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	reviewed := hostedEffect(t, h, principal, turn, 2, protocol.Target{Type: "issue", ID: issue.IssueID}, nil, hostedReviewPayload(t, h, issue), ReviewHostedIssueContext{})
	if reviewed.Status != protocol.OpSucceeded {
		t.Fatal(reviewed)
	}
	if op := commitHostedReview(t, h, principal, turn, 3); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	before, err := h.app.LogicalTurn(t.Context(), turn.RequestID)
	if err != nil || before.Status == "completed" {
		t.Fatal("review fabricated native completion", before, err)
	}
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	state, err := h.app.LogicalTurn(t.Context(), turn.RequestID)
	if err != nil || state.Status != "completed" || state.RepairAttempts != 0 {
		t.Fatal(state, err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var baseline int64
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT baseline_revision FROM work_item_contexts WHERE work_item_id=?`, issue.IssueID).Scan(&baseline); err != nil {
			return err
		}
		if baseline != 0 {
			t.Fatal("manager review fabricated execution baseline")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if run, ok, _ := h.claim("linux-1"); !ok || run.Role != protocol.RoleImplement {
		t.Fatal("investigation was not delegated", run, ok)
	}
}
func TestHostedMainDelegationSettlesBeforeIndependentIssueReviews(t *testing.T) {
	h := hostedManagerHarness(t)
	source, delivery := h.say("Investigate two independent questions")
	first := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	second := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	if op := h.associate(owner, source, first.IssueID, second.IssueID); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	main := startHostedReview(t, h, "main", delivery)
	commitFixtureTurn(t, h, main.RequestID)
	main.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), main); err != nil {
		t.Fatal(err)
	}
	state, err := h.app.LogicalTurn(t.Context(), main.RequestID)
	if err != nil || state.Status != "completed" {
		t.Fatal("main waits for managers", state, err)
	}
	pending := func(issue string) bool {
		t.Helper()
		var p bool
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			return tx.Q().QueryRowContext(t.Context(), `SELECT needs_review FROM message_context_obligations WHERE work_item_id=? AND message_id=?`, issue, source).Scan(&p)
		}); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if !pending(first.IssueID) || !pending(second.IssueID) {
		t.Fatal("main cleared Issue review duties")
	}
	for _, issue := range []protocol.Issue{first, second} {
		turn, principal := startHostedIssueReview(t, h, issue)
		if op := hostedEffect(t, h, principal, turn, 1, protocol.Target{Type: "issue", ID: issue.IssueID}, nil, hostedReviewPayload(t, h, issue), ReviewHostedIssueContext{}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		if issue.IssueID == first.IssueID && !pending(second.IssueID) {
			t.Fatal("first manager reviewed sibling")
		}
		if op := commitHostedReview(t, h, principal, turn, 2); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		turn.Status = store.TurnCompleted
		if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
			t.Fatal(err)
		}
		s, err := h.app.LogicalTurn(t.Context(), turn.RequestID)
		if err != nil || s.Status != "completed" {
			t.Fatal(s, err)
		}
	}
}

func TestHostedManagerSourceCorrectionInvalidatesOnlyActualReviewedVersions(t *testing.T) {
	h := hostedManagerHarness(t)
	ownerSource, delivery := h.say("Review these related source facts independently")
	first := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	second := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	main := startHostedReview(t, h, "main", delivery)
	report := recordAgentMessagePayload{Body: "Initial source facts", ReplyTo: ownerSource, TurnID: main.TurnID, ItemID: "shared-source", Status: protocol.AssistantStreaming}
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
	if op := h.associate(owner, source.MessageID, first.IssueID, second.IssueID); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	turn, principal := startHostedIssueReview(t, h, first)
	review := hostedReviewPayload(t, h, first)
	if op := hostedEffect(t, h, principal, turn, 1, protocol.Target{Type: "issue", ID: first.IssueID}, nil, review, ReviewHostedIssueContext{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	report.Body = "Initial source facts now corrected with another constraint"
	report.Status = protocol.AssistantCompleted
	if op := h.exec(mainAgent, h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, report, nil), RecordAgentMessage{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	expectWorkCode(t, hostedEffect(t, h, principal, turn, 2, protocol.Target{Type: "issue", ID: first.IssueID}, nil, review, ReviewHostedIssueContext{}), protocol.CodeStaleRevision)
	expectWorkCode(t, commitHostedReview(t, h, principal, turn, 3), protocol.CodeStaleRevision)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var count int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM message_context_obligations WHERE message_id=? AND work_item_id IN (?,?) AND needs_review=1`, source.MessageID, first.IssueID, second.IssueID).Scan(&count); err != nil {
			return err
		}
		if count != 2 {
			t.Fatal("late correction lost independent review duties", count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if op := hostedEffect(t, h, principal, turn, 4, protocol.Target{Type: "issue", ID: first.IssueID}, nil, hostedReviewPayload(t, h, first), ReviewHostedIssueContext{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := commitHostedReview(t, h, principal, turn, 5); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func TestHostedPlannerDelegationPersistsActualManagerWake(t *testing.T) {
	h := hostedManagerHarness(t)
	source, delivery := h.say("Please investigate a vague goal")
	turn := startHostedReview(t, h, "main", delivery)
	message := h.associations(source)
	version := message.ContentRevision
	p := protocol.CreateIssuePayload{IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: source, ExpectedSourceRevision: &version}, Plan: protocol.IssuePlan{Title: "Delegate the vague intent", Project: "default", Intent: "Investigate before choosing execution"}, Assignee: &protocol.IssueAssignee{Kind: protocol.IssueAssigneeAgent, Ref: "server-hosted/planner"}}
	created := hostedEffect(t, h, mainAgent, turn, 1, protocol.Target{Type: "issue"}, nil, p, CanonicalOwnerIssueCommand{Action: "create"})
	if created.Status != protocol.OpSucceeded {
		t.Fatal(created)
	}
	issue := canonicalLifecycleIssue(t, created)
	commitFixtureTurn(t, h, turn.RequestID)
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	state, err := h.app.LogicalTurn(t.Context(), turn.RequestID)
	if err != nil || state.Status != "completed" {
		t.Fatal("planner did not finish without manager execution", state, err)
	}
	manager, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:issue:"+issue.IssueID)
	if err != nil || !ok {
		t.Fatal(manager, ok, err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var pending bool
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT needs_review FROM message_context_obligations WHERE message_id=? AND work_item_id=?`, source, issue.IssueID).Scan(&pending); err != nil {
			return err
		}
		if !pending {
			t.Fatal("delegation pretended to review technical source")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func finishHostedReview(t *testing.T, h *harness, turn store.MainTurn) {
	t.Helper()
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	inflight, err := h.app.InFlightDeliveries(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range inflight {
		if d.RequestID == turn.RequestID {
			if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Delivered}); err != nil {
				t.Fatal(err)
			}
		}
	}
}
func TestHostedManagerWorkerAcceptanceCompletionClosureAndParentWake(t *testing.T) {
	testHostedCompletionFacts(t, false)
}
func TestHostedUnavailableManagerCannotRollBackAcceptanceOrChildClosure(t *testing.T) {
	testHostedCompletionFacts(t, true)
}
func TestHostedParentClosesWithVerifiedDescendantAcceptance(t *testing.T) {
	testHostedCompletionFacts(t, false, true)
}
func testHostedCompletionFacts(t *testing.T, unavailable bool, closeParent ...bool) {
	h := hostedManagerHarness(t)
	parent := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	if len(closeParent) == 0 {
		parent.Plan.FollowOns = []protocol.IssueFollowOn{{FollowOnID: "remaining", Kind: protocol.WorkItemFollowOnCommitment, Text: "Parent still needs a separate delivery decision", Source: "owner brief", Status: protocol.WorkItemFollowOnOpen}}
		parent = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: parent.IssueID}, protocol.UpdateIssuePayload{Plan: parent.Plan, ExpectedAssignmentEpoch: parent.AssignmentEpoch}, rev(parent.Revision)), CanonicalOwnerIssueCommand{Action: "update"}))
	}

	childPayload := protocol.CreateIssuePayload{ParentIssueID: parent.IssueID, ExpectedParentRevision: rev(parent.Revision), Plan: protocol.IssuePlan{Title: "Investigate child scope", Project: "default", Intent: "Return a verified result"}}
	child := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, childPayload, nil), CanonicalOwnerIssueCommand{Action: "create"}))
	child = assignHostedManager(t, h, child)
	turn, manager := startHostedIssueReview(t, h, child)
	spec := testSpec
	spec.ImplementationContext = "PRIVATE_MANAGER_EXECUTION_REASONING"
	taskPayload := canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: child.AssignmentEpoch, Title: "Investigate delegated scope", Spec: spec, Rating: protocol.RatingP1}
	op := hostedEffect(t, h, manager, turn, 1, protocol.Target{Type: "issue", ID: child.IssueID}, rev(child.Revision), taskPayload, CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var created struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(op.Result, &created); err != nil {
		t.Fatal(err)
	}
	child = canonicalTaskIssue(t, h, child.IssueID)
	if op := hostedEffect(t, h, manager, turn, 2, protocol.Target{Type: "issue", ID: child.IssueID}, nil, hostedReviewPayload(t, h, child), ReviewHostedIssueContext{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := commitHostedReview(t, h, manager, turn, 3); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	finishHostedReview(t, h, turn)
	implementation, ok, _ := h.claim("linux-1")
	if !ok || implementation.TaskID != created.TaskID || implementation.Role != protocol.RoleImplement {
		t.Fatal(implementation, ok)
	}
	h.runAs(implementation, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	if op := h.runAs(implementation, protocol.OpRunsSubmitArtifact, testArtifact(h.task(created.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	acceptance, ok, _ := h.claim("linux-1")
	if !ok || acceptance.Role != protocol.RoleAccept || acceptance.RunID == implementation.RunID || strings.Contains(string(acceptance.Spec), "PRIVATE_MANAGER_EXECUTION_REASONING") {
		t.Fatal("independent acceptance was not isolated", acceptance, ok)
	}
	if unavailable {
		config := hostedConfigurationFixture()
		config.Revision = 2
		config.Definitions.Agents[0].Revision = 2
		config.Definitions.Agents[0].Purposes = []agentconfig.Purpose{agentconfig.Main}
		config.Definitions.Runtimes[0].Revision = 2
		config.Definitions.Runtimes[0].DeclaredPurposes = []agentconfig.Purpose{agentconfig.Main}
		_, prepared := acceptHostedFixture(t, h, config, 1)
		h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(prepared))
	}
	if op := h.runAs(acceptance, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	resource := h.resourceForTask(created.TaskID)
	if !h.releaseResource(created.TaskID, resource.WorkspaceID) {
		t.Fatal("worker workspace did not release")
	}
	if unavailable {
		original := canonicalTaskIssue(t, h, child.IssueID)
		var pending []protocol.WorkItemCompletionReview
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var err error
			pending, err = store.ListPendingCompletionReviews(t.Context(), tx.Q(), child.IssueID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if len(pending) != 1 || h.task(created.TaskID).Lifecycle != protocol.TaskCompleted {
			t.Fatal("worker acceptance facts did not persist", pending)
		}
		for _, id := range []string{child.IssueID, parent.IssueID} {
			if _, _, err := h.app.NamedHostedSelection(t.Context(), "issue:"+id); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
				t.Fatal("unavailable manager was not blocked", err)
			}
			if _, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:issue:"+id); err != nil || ok {
				t.Fatal("blocked manager consumed pending wake", ok, err)
			}
		}
		endpoint := canonicalLifecycleClosure(original, protocol.WorkItemClosureCompleted)
		endpoint.CompletionReviews = []protocol.CompletionReviewAcknowledgement{{EventID: pending[0].EventID, Reason: "Owner checked independent passed acceptance while the manager is unavailable"}}
		current := hostedReviewPayload(t, h, original)
		if len(current.Sources) > 0 {
			endpoint.ExpectedContextRevision = rev(current.ContextRevision)
		}
		for _, source := range current.Sources {
			endpoint.SourceDispositions = append(endpoint.SourceDispositions, canonicalOwnerSourceDisposition{MessageID: source.MessageID, ContentRevision: source.ContentRevision, AssociationRevision: source.AssociationRevision, Reason: "Owner reviewed this exact source and completed child result"})
		}
		child = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, original, "close", endpoint))
		if child.State != protocol.IssueClosed {
			t.Fatal("child close was blocked by unavailable parent manager", child)
		}
		parentNow := canonicalTaskIssue(t, h, parent.IssueID)
		if parentNow.Assignee != parent.Assignee || parentNow.AssignmentEpoch != parent.AssignmentEpoch || parentNow.State != protocol.IssueActive || parentNow.Plan.FollowOns[0].Status != protocol.WorkItemFollowOnOpen {
			t.Fatal("facts reassigned or disposed the parent", parentNow)
		}
		var notices int
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			return tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM background_sources s JOIN background_batches b ON b.batch_id=s.batch_id WHERE b.work_item_id=? AND s.kind='issue.child_result'`, parent.IssueID).Scan(&notices)
		}); err != nil {
			t.Fatal(err)
		}
		if notices != 1 {
			t.Fatal("parent result was lost instead of retained for its exact assignee", notices)
		}
		return
	}
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:issue:"+child.IssueID)
	if err != nil || !ok {
		t.Fatal("primary completion did not wake responsible manager", ok, err)
	}
	turn = startHostedReview(t, h, "issue:"+child.IssueID, delivery)
	manager.Thread = turn.ThreadID
	child = canonicalTaskIssue(t, h, child.IssueID)
	var pending []protocol.WorkItemCompletionReview
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		pending, err = store.ListPendingCompletionReviews(t.Context(), tx.Q(), child.IssueID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].TaskID != created.TaskID {
		t.Fatal(pending)
	}
	review := protocol.ReviewIssueCompletionsPayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: child.AssignmentEpoch}, Reviews: []protocol.CompletionReviewAcknowledgement{{EventID: pending[0].EventID, Reason: "Read independent passed acceptance and checked remaining child scope"}}}
	child = canonicalLifecycleIssue(t, hostedEffect(t, h, manager, turn, 1, protocol.Target{Type: "issue", ID: child.IssueID}, rev(child.Revision), review, CanonicalOwnerIssueCommand{Action: "review_completions"}))
	fake := canonicalLifecycleClosure(child, protocol.WorkItemClosureCompleted)
	forbidden := HostedCoordinationCommand{Command: CanonicalOwnerIssueCommand{Action: "close"}, ExecutionID: turn.RequestID, EffectSeq: 2}
	if _, err := h.app.Execute(t.Context(), manager, h.env(forbidden.Operation(), protocol.Target{Type: "issue", ID: child.IssueID}, fake, rev(child.Revision)), forbidden); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("manager command became owner decision", err)
	}
	close := protocol.IssueEndpointPayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: child.AssignmentEpoch}, Closure: &protocol.IssueClosure{Kind: protocol.WorkItemClosureCompleted, Reason: "The delegated child deliverable passed independent acceptance", Evidence: []protocol.IssueEvidence{{Kind: protocol.WorkItemEvidenceAcceptance, Reference: pending[0].AcceptanceID, Summary: "Current primary task passed independent acceptance"}}}}
	misnamedDelivery := close
	misnamedDelivery.Closure = &protocol.IssueClosure{Kind: protocol.WorkItemClosureCompleted, Reason: "The task delivered its artifact", Evidence: []protocol.IssueEvidence{{Kind: protocol.WorkItemEvidenceDelivery, Reference: h.task(created.TaskID).CurrentArtifactID, Summary: "Actual task artifact is not a provider delivery observation"}}}
	invalidDelivery, err := h.app.Execute(t.Context(), manager, h.env(forbidden.Operation(), protocol.Target{Type: "issue", ID: child.IssueID}, misnamedDelivery, rev(child.Revision)), forbidden)
	if err == nil {
		err = invalidDelivery.Error
	}
	if protocol.CodeOf(err) != protocol.CodeInvalidRequest || !strings.Contains(err.Error(), "acceptance_id") {
		t.Fatal("artifact mistaken for external delivery did not explain the actual task evidence path", invalidDelivery, err)
	}
	child = canonicalLifecycleIssue(t, hostedEffect(t, h, manager, turn, 3, protocol.Target{Type: "issue", ID: child.IssueID}, rev(child.Revision), close, CanonicalOwnerIssueCommand{Action: "close"}))
	if child.State != protocol.IssueClosed {
		t.Fatal(child)
	}
	if op := hostedEffect(t, h, manager, turn, 4, protocol.Target{Type: "issue", ID: child.IssueID}, nil, hostedReviewPayload(t, h, child), ReviewHostedIssueContext{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := commitHostedReview(t, h, manager, turn, 5); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	finishHostedReview(t, h, turn)
	parent = canonicalTaskIssue(t, h, parent.IssueID)
	if parent.State != protocol.IssueActive || len(closeParent) == 0 && parent.Plan.FollowOns[0].Status != protocol.WorkItemFollowOnOpen {
		t.Fatal("one child result closed or disposed parent's remaining obligation", parent)
	}
	parentDelivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:issue:"+parent.IssueID)
	if err != nil || !ok {
		t.Fatal("child endpoint did not wake parent", ok, err)
	}
	var input HandleMessage
	if err := json.Unmarshal(parentDelivery.Payload, &input); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var count int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM background_sources s JOIN background_batches b USING(batch_id) WHERE b.message_id=? AND b.work_item_id=? AND s.kind='issue.child_result'`, input.MessageID, parent.IssueID).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Fatal("parent wake lost exact child result", count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(closeParent) > 0 {
		parentTurn := startHostedReview(t, h, "issue:"+parent.IssueID, parentDelivery)
		parentManager := protocol.Principal{Kind: protocol.PrincipalBackgroundAgent, Subject: "parent-manager", Thread: parentTurn.ThreadID, ConversationID: "issue:" + parent.IssueID}
		if op := hostedEffect(t, h, parentManager, parentTurn, 1, protocol.Target{Type: "issue", ID: parent.IssueID}, nil, hostedReviewPayload(t, h, parent), ReviewHostedIssueContext{}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		close.IssueControlPayload.ExpectedAssignmentEpoch = parent.AssignmentEpoch
		close.Closure.Reason = "Parent verified the child endpoint and independent accepted deliverable"
		closed := canonicalLifecycleIssue(t, hostedEffect(t, h, parentManager, parentTurn, 2, protocol.Target{Type: "issue", ID: parent.IssueID}, rev(parent.Revision), close, CanonicalOwnerIssueCommand{Action: "close"}))
		if closed.State != protocol.IssueClosed {
			t.Fatal(closed)
		}
	}

}

func TestHostedOwnerReparentWaitsForActualManagerNativeSettlement(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	parent := createCanonicalOwnerTestIssue(t, h)
	turn, manager := startHostedIssueReview(t, h, issue)
	p := protocol.ReparentIssuePayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch}, ParentIssueID: parent.IssueID, ExpectedParentRevision: rev(parent.Revision)}
	expectWorkCode(t, h.exec(owner, h.env("issues.reparent", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "reparent"}), protocol.CodeInvalidTransition)
	if op := hostedEffect(t, h, manager, turn, 1, protocol.Target{Type: "issue", ID: issue.IssueID}, nil, hostedReviewPayload(t, h, issue), ReviewHostedIssueContext{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := commitHostedReview(t, h, manager, turn, 2); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.app.AwaitNativeReceipts(t.Context(), turn.ThreadID); err != nil {
		t.Fatal(err)
	}
	turn.Status = store.TurnInterrupted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	expectWorkCode(t, h.exec(owner, h.env("issues.reparent", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "reparent"}), protocol.CodeInvalidTransition)
	turn.Status = store.TurnCompleted
	turn.NativeReconciled = true
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	next := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.reparent", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "reparent"}))
	if next.ParentIssueID != parent.IssueID || next.Assignee != issue.Assignee || next.AssignmentEpoch != issue.AssignmentEpoch {
		t.Fatal("reparent changed responsibility identity or bypassed inherited scope", next)
	}
}

func TestHostedAssignmentFencesOldManagerAndRequiresNativeUnknownReconcile(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	turn, manager := startHostedIssueReview(t, h, issue)
	request := h.env("issues.pause", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch}, rev(issue.Revision))
	command := HostedCoordinationCommand{Command: CanonicalOwnerIssueCommand{Action: "pause"}, ExecutionID: turn.RequestID, EffectSeq: 1}
	next := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}, Reason: "Owner takes responsibility while the previous manager is reconciled"}, rev(issue.Revision)), HostedIssueAssignment{}))
	if _, err := h.app.Execute(t.Context(), manager, request, command); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("fenced manager still wrote", err)
	}
	finish := protocol.FinishIssueAssignmentPayload{ExpectedAssignmentEpoch: next.AssignmentEpoch, HandoffID: next.PendingHandoffID, Reason: "Owner receives reconciled remaining responsibility"}
	expectWorkCode(t, h.exec(owner, h.env("issues.finish_assignment", protocol.Target{Type: "issue", ID: issue.IssueID}, finish, rev(next.Revision)), HostedIssueAssignment{Finish: true}), protocol.CodeInvalidTransition)
	if err := h.app.AwaitNativeReceipts(t.Context(), turn.ThreadID); err != nil {
		t.Fatal(err)
	}
	turn.Status = store.TurnInterrupted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	expectWorkCode(t, h.exec(owner, h.env("issues.finish_assignment", protocol.Target{Type: "issue", ID: issue.IssueID}, finish, rev(next.Revision)), HostedIssueAssignment{Finish: true}), protocol.CodeInvalidTransition)
	turn.NativeReconciled = true
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	next = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.finish_assignment", protocol.Target{Type: "issue", ID: issue.IssueID}, finish, rev(next.Revision)), HostedIssueAssignment{Finish: true}))
	if next.Assignee.Kind != protocol.IssueAssigneeOwner || next.AssignmentState != "active" {
		t.Fatal(next)
	}
	if _, err := h.app.Execute(t.Context(), manager, request, command); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("finished handoff restored old manager writes", err)
	}
}

func TestHostedAssignmentSettlesOnlyEndedOldManagerRepair(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	turn, _ := startHostedIssueReview(t, h, issue)
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	var repairs int
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM outbox WHERE operation='conversation.repair_turn' AND status='pending' AND request_id IN (SELECT request_id FROM logical_turn_executions WHERE logical_turn_id=(SELECT logical_turn_id FROM logical_turn_executions WHERE request_id=?))`, turn.RequestID).Scan(&repairs)
	}); err != nil {
		t.Fatal(err)
	}
	if repairs != 1 {
		t.Fatalf("fixture did not produce actual unfinished logical repair: %d", repairs)
	}
	before := hostedReviewPayload(t, h, issue)
	next := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}, Reason: "Owner receives remaining Issue review after the old native turn ended"}, rev(issue.Revision)), HostedIssueAssignment{}))
	finish := protocol.FinishIssueAssignmentPayload{ExpectedAssignmentEpoch: next.AssignmentEpoch, HandoffID: next.PendingHandoffID, Reason: "Only unsent repair remains; native execution is definitely complete"}
	// Native completion precedes the dispatcher's receipt. The claimed
	// original delivery must be recorded before the handoff can finish.
	expectWorkCode(t, h.exec(owner, h.env("issues.finish_assignment", protocol.Target{Type: "issue", ID: issue.IssueID}, finish, rev(next.Revision)), HostedIssueAssignment{Finish: true}), protocol.CodeInvalidTransition)
	finishOriginalHostedDelivery(t, h, turn.RequestID)
	next = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.finish_assignment", protocol.Target{Type: "issue", ID: issue.IssueID}, finish, rev(next.Revision)), HostedIssueAssignment{Finish: true}))
	state, err := h.app.LogicalTurn(t.Context(), turn.RequestID)
	if err != nil || state.Status != "stopped" {
		t.Fatalf("ended old manager responsibility still admits fenced repair: %+v %v", state, err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM outbox WHERE operation='conversation.repair_turn' AND status='pending' AND request_id IN (SELECT request_id FROM logical_turn_executions WHERE logical_turn_id=?)`, state.ID).Scan(&repairs)
	}); err != nil {
		t.Fatal(err)
	}
	if repairs != 0 {
		t.Fatalf("old repair remains dispatchable: %d", repairs)
	}
	after := hostedReviewPayload(t, h, next)
	if !reflect.DeepEqual(after.Sources, before.Sources) {
		t.Fatal("handoff changed unresolved Issue sources", before, after)
	}
	next = assignHostedManager(t, h, next)
	nextTurn, manager := startHostedIssueReview(t, h, next)
	frozen, named, err := h.app.HostedExecution(t.Context(), nextTurn.RequestID)
	if err != nil || !named || frozen.AssignmentEpoch != next.AssignmentEpoch {
		t.Fatal("new responsible manager was not admitted", frozen, named, err)
	}
	if op := hostedEffect(t, h, manager, nextTurn, 1, protocol.Target{Type: "issue", ID: next.IssueID}, nil, hostedReviewPayload(t, h, next), ReviewHostedIssueContext{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := commitHostedReview(t, h, manager, nextTurn, 2); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	nextTurn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), nextTurn); err != nil {
		t.Fatal(err)
	}
	advanced, err := h.app.LogicalTurn(t.Context(), nextTurn.RequestID)
	if err != nil || advanced.Status != "completed" {
		t.Fatal("new manager was blocked by retired repair", advanced, err)
	}
}

func TestHostedAssignmentCancelsKnownUnsentRateLimitContinuation(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	turn, _ := startHostedIssueReview(t, h, issue)
	turn.Status = store.TurnFailed
	turn.Failure = &protocol.AgentFailure{Code: "rate_limit_exceeded", HTTPStatus: 429}
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	recovery, found, err := h.app.AgentRecovery(t.Context(), turn.RequestID)
	if err != nil || !found || recovery.Status != "waiting" {
		t.Fatal(recovery, found, err)
	}
	next := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}, Reason: "Owner receives the remaining investigation after rate limit"}, rev(issue.Revision)), HostedIssueAssignment{}))
	finish := protocol.FinishIssueAssignmentPayload{ExpectedAssignmentEpoch: next.AssignmentEpoch, HandoffID: next.PendingHandoffID, Reason: "Failed native execution ended; its continuation was not sent"}
	expectWorkCode(t, h.exec(owner, h.env("issues.finish_assignment", protocol.Target{Type: "issue", ID: issue.IssueID}, finish, rev(next.Revision)), HostedIssueAssignment{Finish: true}), protocol.CodeInvalidTransition)
	finishOriginalHostedDelivery(t, h, turn.RequestID)
	next = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.finish_assignment", protocol.Target{Type: "issue", ID: issue.IssueID}, finish, rev(next.Revision)), HostedIssueAssignment{Finish: true}))
	recovery, found, err = h.app.AgentRecovery(t.Context(), turn.RequestID)
	if err != nil || !found || recovery.Status != "cancelled" {
		t.Fatal(recovery, found, err)
	}
	var pending int
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM outbox WHERE request_id=? AND status IN ('pending','sending','unknown')`, recovery.ActiveRequestID).Scan(&pending)
	}); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatal("fenced continuation still occupies the dispatch queue", pending)
	}
	if len(hostedReviewPayload(t, h, next).Sources) == 0 {
		t.Fatal("handoff discarded Issue source review duty")
	}
}

func TestHostedAssignmentKeepsSendingAndUnknownRepairBlocked(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	conversation := "issue:" + issue.IssueID
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	turn := startHostedReview(t, h, conversation, delivery)
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if err := h.app.FinishDelivery(t.Context(), delivery, DeliveryResult{Outcome: Delivered}); err != nil {
		t.Fatal(err)
	}
	repair, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation)
	if err != nil || !ok || repair.Operation != "conversation.repair_turn" {
		t.Fatal("fixture did not claim real repair delivery", repair, ok, err)
	}
	next := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}, Reason: "Fence old manager while its repair delivery is reconciled"}, rev(issue.Revision)), HostedIssueAssignment{}))
	finish := protocol.FinishIssueAssignmentPayload{ExpectedAssignmentEpoch: next.AssignmentEpoch, HandoffID: next.PendingHandoffID, Reason: "Owner must not assume a sending repair did not execute"}
	expectWorkCode(t, h.exec(owner, h.env("issues.finish_assignment", protocol.Target{Type: "issue", ID: issue.IssueID}, finish, rev(next.Revision)), HostedIssueAssignment{Finish: true}), protocol.CodeInvalidTransition)
	if err := h.app.FinishDelivery(t.Context(), repair, DeliveryResult{Outcome: Unknown}); err != nil {
		t.Fatal(err)
	}
	expectWorkCode(t, h.exec(owner, h.env("issues.finish_assignment", protocol.Target{Type: "issue", ID: issue.IssueID}, finish, rev(next.Revision)), HostedIssueAssignment{Finish: true}), protocol.CodeInvalidTransition)
	state, err := h.app.LogicalTurn(t.Context(), turn.RequestID)
	if err != nil || state.Status == "stopped" {
		t.Fatal("uncertain repair was silently stopped", state, err)
	}
}

func TestHostedCanonicalAdmissionDoesNotFallBackToLegacyWithoutSelection(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	conversation := "issue:" + issue.IssueID
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	var payload HandleMessage
	if err := json.Unmarshal(delivery.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if err := h.app.PutMainTurn(t.Context(), store.MainTurn{RequestID: delivery.RequestID, MessageID: payload.MessageID, ThreadID: "native-issue", Status: store.TurnStarting}); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		t.Fatalf("canonical manager admitted without frozen definition: %v", err)
	}
	message, mainDelivery := h.say("Delegate this next goal")
	if err := h.app.PutMainTurn(t.Context(), store.MainTurn{RequestID: mainDelivery.RequestID, MessageID: message, ThreadID: mainAgent.Thread, Status: store.TurnStarting}); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		t.Fatalf("named main admitted without frozen definition: %v", err)
	}
	restarted := New(h.store, h.ids, h.clock.Now)
	for _, id := range []string{"main", conversation} {
		if _, named, err := restarted.HostedRuntimeSelection(t.Context(), id); protocol.CodeOf(err) != protocol.CodeInvalidTransition || named {
			t.Fatalf("missing configured runtime fell back to legacy for %s: named=%v err=%v", id, named, err)
		}
	}
}

func TestHostedRecoveryUsesFrozenConfigurationRevisionAfterReload(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	turn, _ := startHostedIssueReview(t, h, issue)
	turn.Status = store.TurnFailed
	turn.Failure = &protocol.AgentFailure{Code: "rate_limit_exceeded", HTTPStatus: 429}
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	recovery, found, err := h.app.AgentRecovery(t.Context(), turn.RequestID)
	if err != nil || !found || recovery.Status != "waiting" {
		t.Fatal(recovery, found, err)
	}
	c := hostedConfigurationFixture()
	c.Revision = 2
	c.Definitions.Agents[0].Revision = 2
	c.Definitions.Agents[0].Model = "new-model"
	c.Definitions.Agents[0].Instructions = "NEW_CONFIGURATION_INSTRUCTIONS"
	_, next := acceptHostedFixture(t, h, c, 1)
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(next))
	selected, named, err := h.app.HostedSelectionForAdmission(t.Context(), recovery.ActiveRequestID, "issue:"+issue.IssueID)
	if err != nil || !named || selected.ConfigurationRevision != 1 || selected.Definition.Model == "new-model" {
		t.Fatal("recovery reselected new configuration", selected, named, err)
	}
	h.clock.Advance(time.Minute)
	if yes, err := h.app.AdmitAgentRecovery(t.Context(), recovery.ActiveRequestID, turn.ThreadID, 2); err != nil || yes {
		t.Fatal("current configuration revision replaced the frozen control identity", yes, err)
	}
	if yes, err := h.app.AdmitAgentRecovery(t.Context(), recovery.ActiveRequestID, turn.ThreadID, selected.ConfigurationRevision); err != nil || !yes {
		t.Fatal("frozen named configuration failed admission", yes, err)
	}
	if yes, err := h.app.ConfirmAgentRecoveryStart(t.Context(), recovery.ActiveRequestID, turn.ThreadID, 2); err != nil || yes {
		t.Fatal("confirmation accepted unrelated current configuration", yes, err)
	}
	if yes, err := h.app.ConfirmAgentRecoveryStart(t.Context(), recovery.ActiveRequestID, turn.ThreadID, selected.ConfigurationRevision); err != nil || !yes {
		t.Fatal("frozen named confirmation failed", yes, err)
	}
	continuation := store.MainTurn{RequestID: recovery.ActiveRequestID, MessageID: turn.MessageID, ThreadID: turn.ThreadID, Status: store.TurnStarting, Hosted: &selected, Model: selected.AppliedModel, ModelProvider: selected.AppliedProvider, Effort: selected.AppliedEffort, SettingsRevision: selected.ConfigurationRevision}
	if err := h.app.PutMainTurn(t.Context(), continuation); err != nil {
		t.Fatal(err)
	}
	frozen, ok, err := h.app.HostedExecution(t.Context(), recovery.ActiveRequestID)
	if err != nil || !ok || frozen.Selection.ConfigurationHash != selected.ConfigurationHash || frozen.Selection.Definition.Instructions != selected.Definition.Instructions {
		t.Fatal("continuation did not persist original private pin", frozen, ok, err)
	}
}

func TestHostedPlannerSettlesDelegationToConcreteOwnerWithoutReviewingIssue(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	source, delivery := h.say("This responsibility is mine; keep it in my plan")
	if op := h.associate(owner, source, issue.IssueID); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	turn := startHostedReview(t, h, "main", delivery)
	commitFixtureTurn(t, h, turn.RequestID)
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	state, err := h.app.LogicalTurn(t.Context(), turn.RequestID)
	if err != nil || state.Status != "completed" {
		t.Fatal("main retained the owner's local review responsibility", state, err)
	}
	current := canonicalTaskIssue(t, h, issue.IssueID)
	if current.Assignee != issue.Assignee || current.Revision != issue.Revision {
		t.Fatal("planner acknowledgement changed owner responsibility", current)
	}
	var pending bool
	var baseline int64
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT needs_review FROM message_context_obligations WHERE message_id=? AND work_item_id=?`, source, issue.IssueID).Scan(&pending); err != nil {
			return err
		}
		return tx.Q().QueryRowContext(t.Context(), `SELECT baseline_revision FROM work_item_contexts WHERE work_item_id=?`, issue.IssueID).Scan(&baseline)
	}); err != nil {
		t.Fatal(err)
	}
	if !pending || baseline != 0 {
		t.Fatal("main fabricated the owner's technical review", pending, baseline)
	}
}

// finishOriginalHostedDelivery models the dispatcher's receipt after Deliver
// has persisted a terminal native status; it never rewrites that native fact.
func finishOriginalHostedDelivery(t *testing.T, h *harness, request string) {
	t.Helper()
	deliveries, err := h.app.InFlightDeliveries(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range deliveries {
		if d.RequestID == request {
			if d.Operation != OpHandleMessage && d.Operation != OpHandleEvent {
				t.Fatal("not an ordinary hosted delivery", d)
			}
			if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Delivered}); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("original hosted delivery is not sending", request)
}
