package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func independentReviewFailureFixture(t *testing.T, assigneeAgent bool) (*harness, *protocol.RunnerRuntime, protocol.Issue, string) {
	t.Helper()
	h := hostedManagerHarness(t)
	runtime := testRuntime("independent-review")
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	i := createCanonicalOwnerTestIssue(t, h)
	i.Plan.Deliveries = []protocol.IssueDelivery{{DeliveryID: "pr", Provider: "github", Repository: "example/repo", Number: 17, URL: "https://example.invalid/repo/pull/17"}}
	i = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, i, "update", protocol.UpdateIssuePayload{Plan: i.Plan, ExpectedAssignmentEpoch: i.AssignmentEpoch}))
	if assigneeAgent {
		i = assignHostedManager(t, h, i)
	}
	op, task := createIndependentReviewFailureTask(t, h, i)
	if op.Status != protocol.OpSucceeded || task == "" {
		t.Fatal(op)
	}
	return h, runtime, i, task
}

func createIndependentReviewFailureTask(t *testing.T, h *harness, i protocol.Issue) (protocol.Operation, string) {
	t.Helper()
	i = canonicalTaskIssue(t, h, i.IssueID)
	spec := protocol.OwnerTaskSpec{
		Goal: "Independently review the linked PR", ReviewDeliveryID: "pr",
		Acceptance: []string{"Submit actual independent findings"}, Deliverables: []string{domain.DeliveryReport},
		Workspace: protocol.OwnerTaskWorkspace{Mode: domain.WorkspaceTemporary},
	}
	env := h.env("issues.create_task", protocol.Target{Type: "issue", ID: i.IssueID}, protocol.CreateIssueTaskPayload{
		Title: "Review the linked candidate", ExpectedAssignmentEpoch: i.AssignmentEpoch, Spec: spec,
	}, rev(i.Revision))
	op := h.exec(owner, env, CanonicalOwnerTaskCreate{})
	var res taskResult
	if op.Status == protocol.OpSucceeded && json.Unmarshal(op.Result, &res) != nil {
		t.Fatal(op)
	}
	return op, res.TaskID
}

func TestFault_IndependentReviewFailureRequiresCurrentOwnerAndLimitsRetries(t *testing.T) {
	h, runtime, issue, task := independentReviewFailureFixture(t, true)
	reason := protocol.WorkerFailureReason(&protocol.AgentFailure{Code: "server_overloaded", HTTPStatus: 503})
	var failed []string
	for n := 0; n <= independentReviewRetryLimit; n++ {
		run, ok := h.claimInputRun(runtime)
		if !ok || run.TaskID != task || run.Role != protocol.RoleImplement || run.Generation != int64(n+1) {
			t.Fatal("expected the same independent review Task", run, ok)
		}
		failed = append(failed, run.RunID)
		if started := h.runAs(run, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); started.Status != protocol.OpSucceeded {
			t.Fatal(started)
		}
		for range 2 {
			op := h.runAs(run, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: reason}, Release{})
			if op.Status != protocol.OpSucceeded || h.run(run.RunID).ObservedState != protocol.RunFailed || h.lease(run.RunID).Status != store.LeaseReleased {
				t.Fatal("failed Run did not settle once", op, run)
			}
		}
		if _, ok := h.claimInputRun(runtime); ok {
			t.Fatal("failure created an unapproved successor")
		}
		if h.count("independent_review_failures") != n+1 {
			t.Fatal("failure announcement or duplicate changes created another settlement")
		}
		if n == independentReviewRetryLimit {
			break
		}
		if notice := h.countWhere(`SELECT COUNT(*) FROM background_sources s JOIN background_batches b USING(batch_id)
			WHERE s.kind='task.independent_review_failed' AND b.work_item_id=?`, issue.IssueID); notice != n+1 {
			t.Fatal("failure did not route once to its Issue", notice)
		}
		p := protocol.CreateActionPayload{Kind: protocol.ActionImplement, Generation: int64(n + 2)}
		revision := h.task(task).ControlRevision
		if op, err := h.app.Execute(t.Context(), owner, h.env("actions.create", taskTarget(task), p, rev(revision)), CreateAction{}); protocol.CodeOf(err) != protocol.CodeForbidden {
			t.Fatal("a principal outside the named responsibility authorized a retry", op, err)
		}
		current := canonicalTaskIssue(t, h, issue.IssueID)
		turn, principal := startHostedIssueReview(t, h, current)
		cmd := HostedCoordinationCommand{Command: CreateAction{}, ExecutionID: turn.RequestID, EffectSeq: 1}
		env := h.env(cmd.Operation(), taskTarget(task), p, rev(revision))
		env.Reason = "Retry after the provider capacity incident recovered"
		if op := h.exec(principal, env, cmd); op.Status != protocol.OpSucceeded {
			t.Fatal("the current named owner could not explicitly retry", op)
		}
		if op := h.exec(principal, env, cmd); op.Error == nil || op.Error.Code != protocol.CodeDuplicateEffect {
			t.Fatal("the original decision receipt did not identify its committed effect", op)
		}
		if got := h.countWhere(`SELECT COUNT(*) FROM independent_review_failures WHERE task_id=? AND retry_action_id IS NOT NULL`, task); got != n+1 {
			t.Fatal("duplicate approval spent retry budget", got)
		}
		future := protocol.CreateActionPayload{Kind: protocol.ActionImplement, Generation: int64(n + 3)}
		if op := hostedEffect(t, h, principal, turn, 2, taskTarget(task), rev(h.task(task).ControlRevision), future, CreateAction{}); op.Error == nil || op.Error.Code != protocol.CodeInvalidTransition {
			t.Fatal("a second approval overtook the active retry", op)
		}
		turn.Status = store.TurnCompleted
		if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
			t.Fatal(err)
		}
		pending, err := h.app.InFlightDeliveries(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range pending {
			if d.RequestID == turn.RequestID {
				if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Delivered}); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if len(failed) != 4 || h.countWhere(`SELECT COUNT(*) FROM conversation_messages WHERE conversation_id='main' AND body LIKE '%重试额度已用完%'`) != 1 {
		t.Fatal("expected exactly one user escalation after three retries", failed)
	}
	p := protocol.CreateActionPayload{Kind: protocol.ActionImplement, Generation: 5}
	current := canonicalTaskIssue(t, h, issue.IssueID)
	turn, principal := startHostedIssueReview(t, h, current)
	if op := hostedEffect(t, h, principal, turn, 1, taskTarget(task), rev(h.task(task).ControlRevision), p, CreateAction{}); op.Error == nil || op.Error.Code != protocol.CodeInvalidTransition {
		t.Fatal("fourth retry was admitted", op)
	}
}

func TestFault_IndependentReviewProviderRejectionRetainsReasonAndBlocksRetry(t *testing.T) {
	h, runtime, _, task := independentReviewFailureFixture(t, false)
	run, ok := h.claimInputRun(runtime)
	if !ok || run.TaskID != task {
		t.Fatal(run, ok)
	}
	reason := protocol.WorkerFailureReason(&protocol.AgentFailure{Code: "cyberPolicy"})
	op := h.runAs(run, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: reason}, Release{})
	if op.Status != protocol.OpSucceeded || h.run(run.RunID).ObservedState != protocol.RunFailed || h.lease(run.RunID).Status != store.LeaseReleased {
		t.Fatal(op)
	}
	if op := h.requestAction(task, protocol.ActionImplement, 2); op.Error == nil || op.Error.Code != protocol.CodeInvalidTransition {
		t.Fatal("provider rejection became retryable", op)
	}
	if _, ok := h.claimInputRun(runtime); ok {
		t.Fatal("provider rejection created a successor")
	}
	if got := h.countWhere(`SELECT COUNT(*) FROM conversation_messages WHERE conversation_id='main' AND body LIKE ?`, "%"+strings.Join([]string{run.RunID, "cyberPolicy"}, "%")+"%"); got != 1 {
		t.Fatal("one owner notice must preserve the original Run and typed rejection", got)
	}
}

func TestFault_IndependentReviewReplacementCannotResetFailedObligation(t *testing.T) {
	h, runtime, i, task := independentReviewFailureFixture(t, false)
	setCandidate := func(head string) {
		t.Helper()
		err := h.store.Write(t.Context(), func(tx *store.Tx) error {
			_, err := tx.PutDeliveryObservationFact(t.Context(), i.Plan.Deliveries[0], protocol.DeliveryObservation{
				State: "open", HeadSHA: head, BaseSHA: strings.Repeat("a", 40),
			})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	setCandidate(strings.Repeat("b", 40))
	run, ok := h.claimInputRun(runtime)
	if !ok || run.TaskID != task {
		t.Fatal(run, ok)
	}
	h.clock.Advance(time.Millisecond)
	op, pending := createIndependentReviewFailureTask(t, h, i)
	if op.Status != protocol.OpSucceeded || pending == "" {
		t.Fatal(op)
	}
	reason := protocol.WorkerFailureReason(&protocol.AgentFailure{Code: "server_overloaded"})
	if op := h.runAs(run, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: reason}, Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if _, ok := h.claimInputRun(runtime); ok {
		t.Fatal("ordinary scheduling started a queued replacement against the failed candidate")
	}
	if op, _ := createIndependentReviewFailureTask(t, h, i); op.Error == nil || op.Error.Code != protocol.CodeInvalidTransition {
		t.Fatal("creation reset the original failure", op)
	}
	setCandidate(strings.Repeat("c", 40))
	replacement, ok := h.claimInputRun(runtime)
	if !ok || replacement.TaskID != pending || replacement.RunID == run.RunID {
		t.Fatal("a distinctly observed candidate did not permit new work", replacement, ok)
	}
}
