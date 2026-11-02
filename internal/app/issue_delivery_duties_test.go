package app

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func deliveryDutyForTest(t *testing.T, h *harness, issue, task string) protocol.IssueDeliveryDuty {
	t.Helper()
	var d protocol.IssueDeliveryDuty
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		d, err = store.GetIssueDeliveryDuty(t.Context(), tx.Q(), issue, task)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return d
}
func updateDeliveryForTest(t *testing.T, h *harness, issue string, p protocol.UpdateIssueDeliveryPayload) protocol.Operation {
	t.Helper()
	i := canonicalTaskIssue(t, h, issue)
	p.ExpectedAssignmentEpoch = i.AssignmentEpoch
	return h.exec(owner, h.env("issues.update_delivery", protocol.Target{Type: "issue", ID: issue}, p, rev(i.Revision)), UpdateIssueDelivery{})
}
func endDeliveryForTest(t *testing.T, h *harness, issue, task string) {
	t.Helper()
	op := updateDeliveryForTest(t, h, issue, protocol.UpdateIssueDeliveryPayload{TaskID: task, Action: "end", IssueControlPayload: protocol.IssueControlPayload{Reason: "Owner explicitly ends remaining delivery responsibility for this bounded test"}})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}
func acceptDeliveryTaskForTest(t *testing.T, h *harness, task string) {
	t.Helper()
	executeAcceptedDeliveryTaskForTest(t, h, task, nil)
}
func executeAcceptedDeliveryTaskForTest(t *testing.T, h *harness, task string, execute func() protocol.ArtifactPayload) {
	t.Helper()
	impl, ok, op := h.claim("linux-1")
	if !ok || impl.TaskID != task {
		t.Fatal("implementation", impl, op)
	}
	h.runAs(impl, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	artifact := testArtifact(h.task(task).WorkspaceName)
	artifact.Report = &protocol.ResultReport{Body: "Frozen delivery result with merge, authorized scope and verification evidence"}
	if execute != nil {
		artifact = execute()
	}
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, artifact, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	accept, ok, op := h.claim("linux-1")
	if !ok || accept.TaskID != task {
		t.Fatal("acceptance", accept, op)
	}
	env := h.env(protocol.OpAcceptancesSubmit, runTarget(accept.RunID), protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, nil)
	verdict := h.exec(runPrincipal(accept.RunID, accept.Role), env, SubmitAcceptance{})
	if verdict.Status != protocol.OpSucceeded {
		t.Fatal(verdict)
	}
	h.exec(runPrincipal(accept.RunID, accept.Role), env, SubmitAcceptance{})
	resource := h.resourceForTask(task)
	if !h.releaseResource(task, resource.WorkspaceID) {
		t.Fatal("release accepted resource")
	}
}
func deliveryFixture(t *testing.T) (*harness, protocol.Issue, string, protocol.IssueDeliveryCandidate) {
	t.Helper()
	return deliveryFixtureOnHarness(t, newHarness(t))
}
func deliveryFixtureOnHarness(t *testing.T, h *harness) (*harness, protocol.Issue, string, protocol.IssueDeliveryCandidate) {
	t.Helper()
	i := createCanonicalOwnerTestIssue(t, h)
	i.Plan.Deliveries = []protocol.IssueDelivery{{DeliveryID: "pr", Provider: "github", Repository: "example/repo", Number: 17, URL: "https://example.invalid/repo/pull/17"}}
	i = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: i.IssueID}, canonicalOwnerIssueUpdate{Plan: i.Plan, ExpectedAssignmentEpoch: i.AssignmentEpoch}, rev(i.Revision)), CanonicalOwnerIssueCommand{Action: "update"}))
	task := createCanonicalTaskForTest(t, h, i)
	acceptDeliveryTaskForTest(t, h, task)
	i = canonicalTaskIssue(t, h, i.IssueID)
	c := protocol.IssueDeliveryCandidate{DeliveryID: "pr", URL: i.Plan.Deliveries[0].URL, HeadSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40), ProviderState: "open", Validation: "checks and independent review on this head", DeploymentScope: "none; merge only"}
	observeDeliveryForTest(t, h, i.IssueID, c, "open", "")
	return h, i, task, c
}
func observeDeliveryForTest(t *testing.T, h *harness, issue string, c protocol.IssueDeliveryCandidate, state, merge string) {
	t.Helper()
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return tx.PutDeliveryObservation(t.Context(), store.DeliveryObservationRow{Observation: protocol.DeliveryObservation{WorkItemID: issue, DeliveryID: c.DeliveryID, CheckedAt: h.clock.Now().UnixMilli(), DeliverySnapshot: protocol.DeliverySnapshot{URL: c.URL, HeadSHA: c.HeadSHA, BaseSHA: c.BaseSHA, State: state, MergeSHA: merge}}})
	}); err != nil {
		t.Fatal(err)
	}
}
func prepareDeliveryForTest(t *testing.T, h *harness, issue, task string, c protocol.IssueDeliveryCandidate) protocol.IssueDeliveryDuty {
	t.Helper()
	op := updateDeliveryForTest(t, h, issue, protocol.UpdateIssueDeliveryPayload{TaskID: task, Action: "prepare", Candidate: &c, IssueControlPayload: protocol.IssueControlPayload{Reason: "Present actual candidate and requested scope in the current IM"}})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	return deliveryDutyForTest(t, h, issue, task)
}
func answerDeliveryForTest(t *testing.T, h *harness, d protocol.IssueDeliveryDuty, option, text string) {
	t.Helper()
	var a protocol.Attention
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		a, err = store.GetAttention(t.Context(), tx.Q(), d.AttentionID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	op := h.exec(owner, h.env("attentions.respond", protocol.Target{Type: "attention", ID: a.AttentionID}, protocol.RespondAttentionPayload{Version: 1, Option: option, Text: text}, rev(a.Revision)), AttentionCommand{Action: "respond"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}
func createDeliveryExecutionForTest(t *testing.T, h *harness, issue string, d protocol.IssueDeliveryDuty) protocol.Operation {
	t.Helper()
	i := canonicalTaskIssue(t, h, issue)
	spec := protocol.OwnerTaskSpec{Goal: "Merge the approved candidate and verify only its authorized scope", Acceptance: []string{"Verify provider merge receipt and all approved post-deployment checks"}, Deliverables: []string{domain.DeliveryReport}, Workspace: protocol.OwnerTaskWorkspace{Mounts: []protocol.OwnerTaskMount{{Repo: "tend"}}}}
	p := protocol.CreateIssueTaskPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Title: "Execute approved delivery", Spec: spec, DeliveryAuthorization: &protocol.IssueDeliveryAuthorization{TaskID: d.TaskID, Revision: d.Revision}}
	op, err := h.app.Execute(t.Context(), owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue}, p, rev(i.Revision)), CanonicalOwnerTaskCreate{})
	if err != nil {
		pe, ok := err.(*protocol.Error)
		if !ok {
			t.Fatal(err)
		}
		return protocol.Operation{Status: protocol.OpFailed, Error: pe}
	}
	return op
}
func TestDeliveryAcceptedOutputCannotDisappearWithCompletionReviewOrEmptyPlan(t *testing.T) {
	h, i, task, _ := deliveryFixture(t)
	d := deliveryDutyForTest(t, h, i.IssueID, task)
	if d.Status != "preparing" || d.AcceptanceID == "" || h.count("issue_delivery_duties") != 1 {
		t.Fatal(d)
	}
	op := canonicalLifecycleControl(t, h, i, "review_completions", map[string]any{"expected_assignment_epoch": i.AssignmentEpoch, "reviews": []protocol.CompletionReviewAcknowledgement{{EventID: d.EventID, Reason: "Review accepted implementation"}}})
	i = canonicalLifecycleIssue(t, op)
	expectWorkCode(t, canonicalLifecycleControl(t, h, i, "close", canonicalLifecycleClosure(i, protocol.WorkItemClosureCompleted)), protocol.CodeInvalidTransition)
	if h.task(task).Lifecycle != protocol.TaskCompleted || h.count("issue_delivery_executions") != 0 {
		t.Fatal("completion changed task or scheduled delivery")
	}
	// A report-only endpoint cannot be claimed for accepted code.
	expectWorkCode(t, updateDeliveryForTest(t, h, i.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: task, Action: "report", IssueControlPayload: protocol.IssueControlPayload{Reason: "Try to reinterpret accepted code as report"}}), protocol.CodeInvalidTransition)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		v, err := views.CanonicalIssue(t.Context(), tx.Q(), i.IssueID, 0)
		if err == nil && (len(v.DeliveryDuties) != 1 || len(v.PendingReviews) != 0) {
			t.Fatal(v)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
func TestDeliveryApprovalScopesExecutionAndIndependentAcceptedEndpoint(t *testing.T) {
	h, i, task, c := deliveryFixture(t)
	d := prepareDeliveryForTest(t, h, i.IssueID, task, c)
	before := h.count("plan_actions")
	expectWorkCode(t, createDeliveryExecutionForTest(t, h, i.IssueID, d), protocol.CodeForbidden)
	if h.count("plan_actions") != before || h.count("issue_delivery_executions") != 0 {
		t.Fatal("unapproved delivery scheduled")
	}
	answerDeliveryForTest(t, h, d, "approve", "")
	op := createDeliveryExecutionForTest(t, h, i.IssueID, d)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var result taskResult
	if err := json.Unmarshal(op.Result, &result); err != nil {
		t.Fatal(err)
	}
	frozen := h.storedSpec(result.TaskID, 1)
	if !frozen.RequiresDelivery(domain.DeliveryReport) || !strings.Contains(strings.Join(frozen.Constraints, "\n"), deliveryCandidateQuestion(c)) {
		t.Fatal("approved scope not frozen")
	}
	expectWorkCode(t, createDeliveryExecutionForTest(t, h, i.IssueID, d), protocol.CodeInvalidTransition)
	acceptDeliveryTaskForTest(t, h, result.TaskID)
	if h.count("issue_delivery_duties") != 1 {
		t.Fatal("recursive approval obligation")
	}
	expectWorkCode(t, updateDeliveryForTest(t, h, i.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: task, Action: "complete", IssueControlPayload: protocol.IssueControlPayload{Reason: "Verify delivery"}}), protocol.CodeInvalidTransition)
	observeDeliveryForTest(t, h, i.IssueID, c, "merged", strings.Repeat("c", 40))
	op = updateDeliveryForTest(t, h, i.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: task, Action: "complete", IssueControlPayload: protocol.IssueControlPayload{Reason: "Independently accepted provider receipt"}})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	d = deliveryDutyForTest(t, h, i.IssueID, task)
	if d.Status != "complete" || len(d.Evidence) != 2 || d.Evidence[1].Observation == nil || h.task(task).Lifecycle != protocol.TaskCompleted {
		t.Fatal(d)
	}
}
func TestDeliveryCandidateChangeAndOwnerTextCannotReuseApproval(t *testing.T) {
	for _, change := range []string{"head", "base", "state", "free-text", "question", "withdraw-source", "unknown-provider", "edited-source", "defer"} {
		t.Run(change, func(t *testing.T) {
			h, i, task, c := deliveryFixture(t)
			d := prepareDeliveryForTest(t, h, i.IssueID, task, c)
			text := ""
			if change == "free-text" {
				text = "Wait for another verification before merging"
			}
			option := "approve"
			if change == "defer" {
				option = "defer"
			}
			answerDeliveryForTest(t, h, d, option, text)
			switch change {
			case "head":
				c.HeadSHA = strings.Repeat("d", 40)
				observeDeliveryForTest(t, h, i.IssueID, c, "open", "")
			case "base":
				c.BaseSHA = strings.Repeat("d", 40)
				observeDeliveryForTest(t, h, i.IssueID, c, "open", "")
			case "state":
				observeDeliveryForTest(t, h, i.IssueID, c, "closed", "")
			case "question":
				c.Validation = "new candidate checks"
				d = prepareDeliveryForTest(t, h, i.IssueID, task, c)
			case "unknown-provider":
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					row, err := store.GetDeliveryObservation(t.Context(), tx.Q(), i.IssueID, c.DeliveryID)
					if err != nil {
						return err
					}
					row.Observation.ErrorKind = "timeout"
					return tx.PutDeliveryObservation(t.Context(), row)
				}); err != nil {
					t.Fatal(err)
				}
			case "edited-source":
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE message_dispositions SET content_revision=content_revision+1 WHERE message_id IN (SELECT message_id FROM issue_delivery_answers)`)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			case "withdraw-source":
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE conversation_message_controls SET withdrawn_at=1 WHERE message_id IN (SELECT message_id FROM issue_delivery_answers)`)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			op := createDeliveryExecutionForTest(t, h, i.IssueID, d)
			if op.Status == protocol.OpSucceeded {
				t.Fatal("stale or conditional answer authorized execution", change)
			}
			if h.count("issue_delivery_executions") != 0 {
				t.Fatal("stale approval created execution")
			}
		})
	}
}
func TestDeliveryDuplicateAndConcurrentPreparationRetainOneQuestion(t *testing.T) {
	h, i, task, c := deliveryFixture(t)
	p := protocol.UpdateIssueDeliveryPayload{TaskID: task, Action: "prepare", Candidate: &c, IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Reason: "Prepare one exact candidate"}}
	env := h.env("issues.update_delivery", protocol.Target{Type: "issue", ID: i.IssueID}, p, rev(i.Revision))
	var results [2]protocol.Operation
	var wg sync.WaitGroup
	for j := range results {
		wg.Add(1)
		go func() { defer wg.Done(); results[j] = h.exec(owner, env, UpdateIssueDelivery{}) }()
	}
	wg.Wait()
	if results[0].Status != protocol.OpSucceeded || string(results[0].Result) != string(results[1].Result) || h.count("issue_delivery_duties") != 1 || h.count("attentions") != 1 {
		t.Fatal(results)
	}
	d := deliveryDutyForTest(t, h, i.IssueID, task)
	prepareDeliveryForTest(t, h, i.IssueID, task, c)
	if h.count("attentions") != 1 || deliveryDutyForTest(t, h, i.IssueID, task).Revision != d.Revision {
		t.Fatal("same candidate duplicated confirmation")
	}
}
func TestDeliveryQueuedExecutionStopsWhenProviderCandidateChanges(t *testing.T) {
	h, i, task, c := deliveryFixture(t)
	d := prepareDeliveryForTest(t, h, i.IssueID, task, c)
	answerDeliveryForTest(t, h, d, "approve", "")
	op := createDeliveryExecutionForTest(t, h, i.IssueID, d)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	c.BaseSHA = strings.Repeat("e", 40)
	observeDeliveryForTest(t, h, i.IssueID, c, "open", "")
	if a, ok, _ := h.claim("linux-1"); ok {
		t.Fatal("stale queued execution claimed", a)
	}
}
func TestDeliveryBoundedReportHasExplicitAcceptedEndpoint(t *testing.T) {
	h := newDeliverableHarness(t)
	i := createCanonicalOwnerTestIssue(t, h)
	spec := testSpec
	spec.Deliverables = []string{domain.DeliveryReport}
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: i.IssueID}, canonicalOwnerTaskPayload{Title: "Bounded report", ExpectedAssignmentEpoch: i.AssignmentEpoch, Spec: spec}, rev(i.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var result taskResult
	_ = json.Unmarshal(op.Result, &result)
	impl, ok, _ := h.claimDeliverableRun()
	if !ok {
		t.Fatal("no report execution")
	}
	h.runAs(impl, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	resultArtifact := testArtifact(h.task(result.TaskID).WorkspaceName)
	resultArtifact.Report = &protocol.ResultReport{Body: "Bounded report"}
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, resultArtifact, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	accept, ok, _ := h.claimDeliverableRun()
	if !ok {
		t.Fatal("no report acceptance")
	}
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	op = updateDeliveryForTest(t, h, i.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: result.TaskID, Action: "report", IssueControlPayload: protocol.IssueControlPayload{Reason: "Independent report delivered; no code integration in this bounded contract"}})
	if op.Status != protocol.OpSucceeded || h.count("attentions") != 0 {
		t.Fatal(op)
	}
}

func TestDeliveryChangedCandidateRecoversOnlyAfterOriginalExecutionSettles(t *testing.T) {
	h, i, task, c := deliveryFixture(t)
	d := prepareDeliveryForTest(t, h, i.IssueID, task, c)
	answerDeliveryForTest(t, h, d, "approve", "")
	op := createDeliveryExecutionForTest(t, h, i.IssueID, d)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var execution taskResult
	if err := json.Unmarshal(op.Result, &execution); err != nil {
		t.Fatal(err)
	}
	c.HeadSHA = strings.Repeat("f", 40)
	observeDeliveryForTest(t, h, i.IssueID, c, "open", "")
	payload := protocol.UpdateIssueDeliveryPayload{TaskID: task, Action: "prepare", Candidate: &c, IssueControlPayload: protocol.IssueControlPayload{Reason: "Prepare changed candidate only after reconciling the original execution"}}
	expectWorkCode(t, updateDeliveryForTest(t, h, i.IssueID, payload), protocol.CodeInvalidTransition)
	cancel := h.exec(owner, h.env("tasks.cancel", protocol.Target{Type: "task", ID: execution.TaskID}, map[string]any{}, rev(execution.ControlRevision)), CancelTask{})
	if cancel.Status != protocol.OpSucceeded {
		t.Fatal(cancel)
	}
	next := prepareDeliveryForTest(t, h, i.IssueID, task, c)
	if next.Revision != d.Revision+1 || next.AttentionID == d.AttentionID || next.ExecutionTaskID != "" || h.task(execution.TaskID).Lifecycle != protocol.TaskCancelled {
		t.Fatal(next)
	}
	expectWorkCode(t, createDeliveryExecutionForTest(t, h, i.IssueID, d), protocol.CodeStaleRevision)
	expectWorkCode(t, createDeliveryExecutionForTest(t, h, i.IssueID, next), protocol.CodeForbidden)
	if h.count("issue_delivery_executions") != 1 {
		t.Fatal("original execution authorization history changed")
	}
}

func TestDeliveryDeferralRetainsResponsibilityAndExplicitEndUsesOwnerEvidence(t *testing.T) {
	for _, option := range []string{"defer", "end_delivery"} {
		t.Run(option, func(t *testing.T) {
			h, i, task, c := deliveryFixture(t)
			d := prepareDeliveryForTest(t, h, i.IssueID, task, c)
			answerDeliveryForTest(t, h, d, option, "")
			d = deliveryDutyForTest(t, h, i.IssueID, task)
			if option == "defer" && d.Status != "deferred" {
				t.Fatal("deferral was not durable", d)
			}
			prepareDeliveryForTest(t, h, i.IssueID, task, c)
			if h.count("attentions") != 1 {
				t.Fatal("deferral was asked again")
			}
			expectWorkCode(t, createDeliveryExecutionForTest(t, h, i.IssueID, d), map[string]protocol.Code{"defer": protocol.CodeStaleRevision, "end_delivery": protocol.CodeForbidden}[option])
			// Only the actual owner can directly end a duty. Agent settlement must
			// resolve the exact end_delivery option, rather than reinterpret defer.
			if option == "defer" {
				current := canonicalTaskIssue(t, h, i.IssueID)
				expectWorkCode(t, canonicalLifecycleControl(t, h, current, "close", canonicalLifecycleClosure(current, protocol.WorkItemClosureCompleted)), protocol.CodeInvalidTransition)
			}
			if option == "defer" {
				op := updateDeliveryForTest(t, h, i.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: task, Action: "resume", IssueControlPayload: protocol.IssueControlPayload{Reason: "Owner explicitly resumes this deferred delivery for current confirmation"}})
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				next := deliveryDutyForTest(t, h, i.IssueID, task)
				if next.AttentionID == d.AttentionID || next.Status != "awaiting_owner" {
					t.Fatal("deferral could not resume without changing the candidate", next)
				}
				expectWorkCode(t, createDeliveryExecutionForTest(t, h, i.IssueID, next), protocol.CodeForbidden)
			}
			endDeliveryForTest(t, h, i.IssueID, task)
			settled := deliveryDutyForTest(t, h, i.IssueID, task)
			if settled.Status != "ended" || len(settled.Evidence) != 1 || settled.Evidence[0].Kind != protocol.WorkItemEvidenceOwnerDecision || settled.Evidence[0].Reference == "" {
				t.Fatal(settled)
			}
		})
	}
	t.Run("manager-owner-cancellation-source-without-PR", func(t *testing.T) {
		h := hostedManagerHarness(t)
		i := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
		task := createCanonicalTaskForTest(t, h, i)
		acceptDeliveryTaskForTest(t, h, task)
		i = canonicalTaskIssue(t, h, i.IssueID)
		submitted := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{ConversationID: "issue:" + i.IssueID, Body: "End the remaining code delivery; retain the accepted implementation as history"}, nil), SubmitMessage{})
		if submitted.Status != protocol.OpAccepted {
			t.Fatal(submitted)
		}
		var source struct {
			MessageID string `json:"message_id"`
		}
		if err := json.Unmarshal(submitted.Result, &source); err != nil {
			t.Fatal(err)
		}
		delivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:issue:"+i.IssueID)
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		turn := startHostedReview(t, h, "issue:"+i.IssueID, delivery)
		principal := protocol.Principal{Kind: protocol.PrincipalBackgroundAgent, Subject: "hosted-" + i.IssueID, Thread: turn.ThreadID, ConversationID: "issue:" + i.IssueID}
		if op := hostedEffect(t, h, principal, turn, 1, protocol.Target{Type: "issue", ID: i.IssueID}, nil, hostedReviewPayload(t, h, i), ReviewHostedIssueContext{}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		i = canonicalTaskIssue(t, h, i.IssueID)
		payload := protocol.UpdateIssueDeliveryPayload{TaskID: task, Action: "end", IssueControlPayload: protocol.IssueControlPayload{IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: source.MessageID, ExpectedSourceRevision: rev(1)}, ExpectedAssignmentEpoch: i.AssignmentEpoch, Reason: "The current admitted owner source explicitly ends this delivery scope"}}
		unsourced := payload
		unsourced.SourceMessageID = ""
		unsourced.ExpectedSourceRevision = nil
		denied := HostedCoordinationCommand{Command: UpdateIssueDelivery{}, ExecutionID: turn.RequestID, EffectSeq: 2}
		rejected, err := h.app.Execute(t.Context(), principal, h.env(denied.Operation(), protocol.Target{Type: "issue", ID: i.IssueID}, unsourced, rev(i.Revision)), denied)
		if err == nil {
			err = rejected.Error
		}
		if protocol.CodeOf(err) != protocol.CodeForbidden {
			t.Fatal("manager ended delivery without an owner decision", rejected, err)
		}
		op := hostedEffect(t, h, principal, turn, 3, protocol.Target{Type: "issue", ID: i.IssueID}, rev(i.Revision), payload, UpdateIssueDelivery{})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		d := deliveryDutyForTest(t, h, i.IssueID, task)
		if d.Status != "ended" || d.Evidence[0].Reference != source.MessageID || h.count("attentions") != 0 || h.task(task).Lifecycle != protocol.TaskCompleted {
			t.Fatal("owner cancellation required unwanted PR work or revived implementation", d)
		}
	})

}

func TestDeliveryObservedDriftCannotReviveOldApprovalAfterProviderReturns(t *testing.T) {
	for _, change := range []string{"head", "base", "closed", "unknown"} {
		t.Run(change, func(t *testing.T) {
			h, i, task, c := deliveryFixture(t)
			d := prepareDeliveryForTest(t, h, i.IssueID, task, c)
			answerDeliveryForTest(t, h, d, "approve", "")
			observerHello(t, h, "linux-1", "candidate-observer")
			report := func(snapshot *protocol.DeliverySnapshot, failure string) {
				t.Helper()
				h.clock.Advance(5 * time.Minute)
				assignments := pollDelivery(t, h, "linux-1", "candidate-observer")
				if len(assignments) != 1 || !assignments[0].CandidateIdentity {
					t.Fatal(assignments)
				}
				a := assignments[0]
				p := protocol.DeliveryReportPayload{InstanceID: "candidate-observer", WorkItemID: i.IssueID, DeliveryID: c.DeliveryID, Generation: a.Generation, Nonce: a.Nonce, Snapshot: snapshot, ErrorKind: failure}
				op := h.exec(runner("linux-1"), h.env(protocol.OpDeliveriesReport, protocol.Target{Type: "host", ID: "linux-1"}, p, nil), ReportDelivery{})
				if !accepted(t, op) {
					t.Fatal(op)
				}
			}
			snapshot := protocol.DeliverySnapshot{State: "open", HeadSHA: c.HeadSHA, BaseSHA: c.BaseSHA, URL: c.URL}
			changed := snapshot
			failure := ""
			var facts *protocol.DeliverySnapshot = &changed
			switch change {
			case "head":
				changed.HeadSHA = strings.Repeat("e", 40)
			case "base":
				changed.BaseSHA = strings.Repeat("e", 40)
			case "closed":
				changed.State = "closed"
			case "unknown":
				facts = nil
				failure = "timeout"
			}
			report(facts, failure)
			expired := deliveryDutyForTest(t, h, i.IssueID, task)
			if expired.Revision != d.Revision+1 || expired.Status != "preparing" {
				t.Fatal(expired)
			}
			if change == "closed" {
				// A terminal reference is refreshed only by an explicit owner refresh.
				current := canonicalTaskIssue(t, h, i.IssueID)
				op := h.exec(owner, h.env("issues.refresh_delivery", protocol.Target{Type: "issue", ID: i.IssueID}, protocol.IssueRefreshDeliveryPayload{ExpectedAssignmentEpoch: current.AssignmentEpoch, DeliveryID: c.DeliveryID}, rev(current.Revision)), IssueRefreshDelivery{})
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			report(&snapshot, "")
			expectWorkCode(t, createDeliveryExecutionForTest(t, h, i.IssueID, d), protocol.CodeStaleRevision)
			next := prepareDeliveryForTest(t, h, i.IssueID, task, c)
			if next.AttentionID == d.AttentionID {
				t.Fatal("old approval reused after observed drift")
			}
			expectWorkCode(t, createDeliveryExecutionForTest(t, h, i.IssueID, next), protocol.CodeForbidden)
		})
	}
}
