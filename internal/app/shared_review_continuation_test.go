package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func completedReviewInput(r protocol.SharedDeliveryReview, task string) protocol.UpdateSharedDeliveryReviewPayload {
	c := r.CurrentCandidate
	return protocol.UpdateSharedDeliveryReviewPayload{
		Action: "record", DeliveryID: "pr", ReviewerTaskID: task,
		ExpectedReviewRevision: rev(r.Revision), ExpectedCandidateRevision: rev(c.Revision),
		ExpectedHeadSHA: c.HeadSHA, ExpectedBaseSHA: c.BaseSHA, ExpectedRequirementsDigest: c.RequirementsDigest,
	}
}

func reviewReceiptForTest(t *testing.T, h *harness, issue, request string) *protocol.SharedReviewRequestReceipt {
	t.Helper()
	var receipt *protocol.SharedReviewRequestReceipt
	err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		out, err := views.SharedDeliveryReviewReceipt(t.Context(), tx.Q(), issue, "pr", request)
		receipt = out.Receipt
		return err
	})
	if err != nil || receipt == nil || receipt.RequestID != request {
		t.Fatal(receipt, err)
	}
	return receipt
}

func confirmReviewerStopForTest(t *testing.T, h *harness, run protocol.Assignment) {
	t.Helper()
	stop := protocol.SessionStopPayload{PhysicalExit: true, EventSequence: 1}
	if op := h.runAs(run, protocol.OpRunsRecordSessionStop, stop, RecordSessionStop{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func TestSharedReviewCompletedReportAfterFailedGenerationAndReminder(t *testing.T) {
	h, runtime, issue, source, finalCandidate, _ := sharedReviewFixture(t)
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityExternalWaits)
	runtime.Sequence++
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	setPRPolicyForTest(t, h, issue.IssueID, "auto")
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
	task := createSharedReportTaskForTest(t, h, issue.IssueID, "pr", protocol.AgentRequirement{CategoryID: "reviewer"})
	initial, ok := h.claimInputRun(runtime)
	if !ok || initial.TaskID != task {
		t.Fatal(initial)
	}
	r := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: task}))
	submitSharedReviewForTest(t, h, initial, r, "changes_requested")
	r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
	firstRound := r.Rounds[0]
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "resolve", DeliveryID: "pr", Dispositions: []protocol.SharedReviewDisposition{{FindingID: "F1", Action: "fixed", Reason: "The corrective candidate addresses the original finding"}}}))
	resource := h.resourceForTask(task)
	if resource.OccupiedBy != task {
		t.Fatal("fixture lost retained workspace", resource)
	}
	beforeStop := h.env("tasks.continue", taskTarget(task), protocol.ContinueTaskPayload{Reason: "Continue the completed reviewer"}, rev(h.task(task).ControlRevision))
	op, err := h.app.Execute(t.Context(), owner, beforeStop, ContinueTask{})
	if technicalWaitErrorCode(op, err) != protocol.CodeUnknownOutcome {
		t.Fatal("the logical report was treated as physical stop", op, err)
	}
	confirmReviewerStopForTest(t, h, initial)
	if op := continueTaskForTest(t, h, task, "Review the corrected candidate in the retained workspace"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	corrected := finalCandidate
	corrected.HeadSHA = strings.Repeat("e", 40)
	observeDeliveryForTest(t, h, issue.IssueID, corrected, "open", "")
	second, ok := h.claimInputRun(runtime)
	if !ok || second.TaskID != task || second.Generation != 2 || second.Workspace.ID != initial.Workspace.ID {
		t.Fatal(second)
	}
	r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "recheck", DeliveryID: "pr", ReviewerTaskID: task}))
	secondRound := r.Rounds[1]
	if op := h.runAs(second, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "Independent reviewer process exited without a report"}, Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	confirmReviewerStopForTest(t, h, second)
	if op := h.requestAction(task, protocol.ActionImplement, 3); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	retry, ok := h.claimInputRun(runtime)
	if !ok || retry.TaskID != task || retry.Generation != 3 {
		t.Fatal(retry)
	}
	op = h.runAs(retry, protocol.OpRunsWaitExternal, protocol.WaitExternalPayload{WaitKey: "same-review", Kind: "review", TargetID: r.ReviewID, IssueID: issue.IssueID, Reason: "Await candidate disposition", Recovery: "Read the original review and current candidate"}, WaitExternal{})
	var wait protocol.ExternalWait
	if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &wait) != nil {
		t.Fatal(op)
	}
	current := canonicalTaskIssue(t, h, issue.IssueID)
	waitInput := protocol.UpdateSharedDeliveryReviewPayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: current.AssignmentEpoch, Reason: "Check the original reviewer stop"}, Action: "wait", DeliveryID: "pr", ExpectedReviewRevision: rev(r.Revision)}
	original := h.env("issues.review_delivery", protocol.Target{Type: "issue", ID: issue.IssueID}, waitInput, rev(current.Revision))
	op, err = h.app.Execute(t.Context(), owner, original, ReviewIssueDelivery{})
	if technicalWaitErrorCode(op, err) != protocol.CodeUnknownOutcome {
		t.Fatal("wait accepted without physical stop", op, err)
	}
	receipt := reviewReceiptForTest(t, h, issue.IssueID, original.RequestID)
	if receipt.Status != "unknown" || receipt.Review != nil {
		t.Fatal("unknown request gained a fabricated outcome", receipt)
	}
	if op := h.runAs(retry, protocol.OpRunsYieldExternalWait, protocol.YieldExternalWaitPayload{WaitID: wait.WaitID, ThreadID: "reviewer-stopped"}, YieldExternalWait{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.clock.Advance(externalWaitReminder)
	if err := h.app.SweepExternalWaits(t.Context()); err != nil {
		t.Fatal(err)
	}
	waits, err := h.app.ExternalWaits(t.Context(), task)
	if err != nil || len(waits) != 1 || waits[0].Status != "continued" || waits[0].WakeReason != "reminder" {
		t.Fatal("reminder did not settle the original wait", waits, err, h.task(task))
	}
	runtime.Sequence++
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	next, ok := h.claimInputRun(runtime)
	if !ok || next.TaskID != task || next.Generation != 3 || next.RunID == retry.RunID || next.Workspace.ID != initial.Workspace.ID || next.ExternalWaitContinuation == nil || next.ExternalWaitContinuation.WakeReason != "reminder" {
		t.Fatal("reminder did not resume the original task and workspace", next, h.executionTask(task).Execution)
	}
	observeDeliveryForTest(t, h, issue.IssueID, finalCandidate, "open", "")
	ctx, err := h.app.ReadRunContext(t.Context(), runPrincipal(next.RunID, next.Role), protocol.IssueMaterialsQuery{Kind: "reviews", ID: "pr"})
	if err != nil || ctx.Materials.SharedReviews == nil || len(ctx.Materials.SharedReviews.Items) != 1 {
		t.Fatal(ctx, err)
	}
	observed := ctx.Materials.SharedReviews.Items[0]
	c := observed.CurrentCandidate
	if observed.Revision != r.Revision || c == nil || c.HeadSHA != finalCandidate.HeadSHA || c.BaseSHA != finalCandidate.BaseSHA || c.RequirementsDigest == "" || c.Revision == r.CurrentCandidate.Revision {
		t.Fatal("the successor could not reconstruct current review input independently", observed)
	}
	report := observed
	report.HeadSHA, report.BaseSHA, report.RequirementsDigest = c.HeadSHA, c.BaseSHA, c.RequirementsDigest
	submitSharedReviewForTest(t, h, next, report, "passed")
	confirmReviewerStopForTest(t, h, next)
	if op := sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}); protocol.CodeOf(op.Error) != protocol.CodeInvalidTransition {
		t.Fatal("the legacy exact binding must not silently accept a different generation", op)
	}
	if op := sharedReviewAction(t, h, issue.IssueID, completedReviewInput(r, task)); protocol.CodeOf(op.Error) != protocol.CodeStaleRevision {
		t.Fatal("the stale candidate must not accept the actual new report", op)
	}
	current = canonicalTaskIssue(t, h, issue.IssueID)
	input := completedReviewInput(observed, task)
	input.ExpectedAssignmentEpoch, input.Reason = current.AssignmentEpoch, "Register the actual reviewer report for the observed candidate"
	record := h.env("issues.review_delivery", protocol.Target{Type: "issue", ID: issue.IssueID}, input, rev(current.Revision))
	settled := sharedReviewResult(t, h.exec(owner, record, ReviewIssueDelivery{}))
	if !settled.Valid || settled.Status != "passed" || len(settled.Rounds) != 3 || !reflect.DeepEqual(settled.Rounds[0], firstRound) || !reflect.DeepEqual(settled.Rounds[1], secondRound) {
		t.Fatal("settlement changed original reports or discarded historical failure", settled)
	}
	final := settled.Rounds[2]
	if final.RunID != next.RunID || final.ArtifactID != h.task(task).CurrentArtifactID || final.Reviewer != "linux-1/beta" || final.HeadSHA != finalCandidate.HeadSHA {
		t.Fatal("registration failed to bind actual production provenance", final)
	}
	if got := reviewReceiptForTest(t, h, issue.IssueID, record.RequestID); got.Status != "committed" || got.Review.Revision != settled.Revision || !reflect.DeepEqual(got.Review.Rounds, settled.Rounds) {
		t.Fatal("request read-back lost the committed result", got)
	}
	if got := reviewReceiptForTest(t, h, issue.IssueID, original.RequestID); got.Status != "unknown" || got.Review != nil {
		t.Fatal("registering a report rewrote the original unknown", got)
	}
	if replay := h.exec(owner, record, ReviewIssueDelivery{}); protocol.CodeOf(replay.Error) != protocol.CodeStaleRevision {
		t.Fatal("original registration repeated its effect", replay)
	}
}

func TestSharedReviewRegisterCompletedReportWithoutPrebinding(t *testing.T) {
	h, runtime, issue, source, finalCandidate, _ := sharedReviewFixture(t)
	setPRPolicyForTest(t, h, issue.IssueID, "auto")
	linked := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
	task := createSharedReportTaskForTest(t, h, issue.IssueID, "pr")
	expectWorkCode(t, sharedReviewAction(t, h, issue.IssueID, completedReviewInput(linked, task)), protocol.CodeInvalidTransition)
	reviewer, ok := h.claimInputRun(runtime)
	if !ok || reviewer.TaskID != task {
		t.Fatal(reviewer)
	}
	submitSharedReviewForTest(t, h, reviewer, linked, "passed")
	confirmReviewerStopForTest(t, h, reviewer)

	input := completedReviewInput(linked, task)
	stale := input
	stale.ExpectedCandidateRevision = rev(*input.ExpectedCandidateRevision - 1)
	expectWorkCode(t, sharedReviewAction(t, h, issue.IssueID, stale), protocol.CodeStaleRevision)
	request := h.ids.New(protocol.PrefixRequest)
	run := h.executionTask(task).Runs[0].RunID
	// This fault boundary represents a native write whose receipt is lost.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO manager_native_requests(request_id,run_id,method,payload_json,status,created_at) VALUES(?,?,'start','{}','unknown',?)`, request, run, h.clock.Now().UnixMilli())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	current := canonicalTaskIssue(t, h, issue.IssueID)
	input.ExpectedAssignmentEpoch, input.Reason = current.AssignmentEpoch, "Register the completed report"
	target := protocol.Target{Type: "issue", ID: issue.IssueID}
	op, err := h.app.Execute(t.Context(), owner, h.env("issues.review_delivery", target, input, rev(current.Revision)), ReviewIssueDelivery{})
	if technicalWaitErrorCode(op, err) != protocol.CodeUnknownOutcome {
		t.Fatal("unknown native operation allowed review registration", op, err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE manager_native_requests SET status='completed',result_json='{}' WHERE request_id=?`, request)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, input))
	if r.Status != "passed" || !r.Valid || len(r.Rounds) != 1 || r.Rounds[0].RunID != run {
		t.Fatal(r)
	}
	duty := prepareDeliveryForTest(t, h, issue.IssueID, source, finalCandidate)
	if duty.Status != "authorized" || duty.Authorization == nil || duty.Authorization.Mode != "auto" {
		t.Fatal("protected delivery rejected verified independent evidence", duty)
	}
}

func TestSharedReviewWaitAndReceiptPreserveStoppedWorkspace(t *testing.T) {
	h, runtime, issue, source, _, _ := sharedReviewFixture(t)
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
	task := createSharedReportTaskForTest(t, h, issue.IssueID, "pr")
	r := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: task}))
	as, ok := h.claimInputRun(runtime)
	if !ok || as.TaskID != task {
		t.Fatal(as)
	}
	if op := h.runAs(as, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "Reviewer exited before reaching a conclusion"}, Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	confirmReviewerStopForTest(t, h, as)
	before := h.resourceForTask(task)
	if before.OccupiedBy != task {
		t.Fatal(before)
	}
	i := canonicalTaskIssue(t, h, issue.IssueID)
	wait := protocol.UpdateSharedDeliveryReviewPayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Reason: "The original Run stopped without a verdict"}, Action: "wait", DeliveryID: "pr", ExpectedReviewRevision: rev(r.Revision)}
	env := h.env("issues.review_delivery", protocol.Target{Type: "issue", ID: i.IssueID}, wait, rev(i.Revision))
	waiting := sharedReviewResult(t, h.exec(owner, env, ReviewIssueDelivery{}))
	if waiting.Status != "waiting" || !reflect.DeepEqual(waiting.Rounds, r.Rounds) || h.resourceForTask(task) != before {
		t.Fatal("review waiting wrote a verdict or altered the workspace reservation", waiting)
	}
	if got := reviewReceiptForTest(t, h, issue.IssueID, env.RequestID); got.Status != "committed" || got.Review.Status != "waiting" || got.Review.Revision != waiting.Revision {
		t.Fatal("lost response cannot be read by original identity", got)
	}
}

func TestSharedReviewRegistersLateOriginalReportAfterWaiting(t *testing.T) {
	h, runtime, issue, source, moved, _ := sharedReviewFixture(t)
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityExternalWaits)
	runtime.Sequence++
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
	task := createSharedReportTaskForTest(t, h, issue.IssueID, "pr")
	frozen := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: task}))
	first, ok := h.claimInputRun(runtime)
	if !ok || first.TaskID != task {
		t.Fatal(first)
	}
	op := h.runAs(first, protocol.OpRunsWaitExternal, protocol.WaitExternalPayload{WaitKey: "reviewer-wait", Kind: "review", TargetID: frozen.ReviewID, IssueID: issue.IssueID, Reason: "Retain the original candidate", Recovery: "Resume this independent review"}, WaitExternal{})
	var wait protocol.ExternalWait
	if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &wait) != nil {
		t.Fatal(op)
	}
	if op := h.runAs(first, protocol.OpRunsYieldExternalWait, protocol.YieldExternalWaitPayload{WaitID: wait.WaitID}, YieldExternalWait{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "wait", DeliveryID: "pr", ExpectedReviewRevision: rev(frozen.Revision)}))
	moved.HeadSHA = strings.Repeat("e", 40)
	observeDeliveryForTest(t, h, issue.IssueID, moved, "open", "")
	h.clock.Advance(externalWaitReminder)
	if err := h.app.SweepExternalWaits(t.Context()); err != nil {
		t.Fatal(err)
	}
	runtime.Sequence++
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	next, ok := h.claimInputRun(runtime)
	if !ok || next.TaskID != task || next.Generation != first.Generation || next.RunID == first.RunID {
		t.Fatal(next)
	}
	submitSharedReviewForTest(t, h, next, frozen, "changes_requested")
	confirmReviewerStopForTest(t, h, next)
	original := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
	if original.Valid || original.Status != "changes_requested" || original.HeadSHA != frozen.HeadSHA ||
		original.CurrentCandidate == nil || original.CurrentCandidate.HeadSHA != moved.HeadSHA ||
		original.Rounds[0].RunID != next.RunID || original.Rounds[0].ArtifactID != h.task(task).CurrentArtifactID || original.Rounds[0].Result.Findings[0].ID != "F1" {
		t.Fatal("the late report must settle its original candidate only", original)
	}
}

func TestSharedReviewRegistersAnotherLegalTaskAfterOriginalStops(t *testing.T) {
	h, runtime, issue, source, _, _ := sharedReviewFixture(t)
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
	first := createSharedReportTaskForTest(t, h, issue.IssueID, "pr")
	frozen := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: first}))
	as, ok := h.claimInputRun(runtime)
	if !ok || as.TaskID != first {
		t.Fatal(as)
	}
	if op := h.runAs(as, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "Reviewer exited before the original report"}, Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	second := createSharedReportTaskForTest(t, h, issue.IssueID, "pr")
	successor, ok := h.claimInputRun(runtime)
	if !ok || successor.TaskID != second {
		t.Fatal(successor)
	}
	submitSharedReviewForTest(t, h, successor, frozen, "passed")
	confirmReviewerStopForTest(t, h, successor)
	i := canonicalTaskIssue(t, h, issue.IssueID)
	input := completedReviewInput(frozen, second)
	input.ExpectedAssignmentEpoch, input.Reason = i.AssignmentEpoch, "Register this reviewer's actual successor report"
	op, err := h.app.Execute(t.Context(), owner, h.env("issues.review_delivery", protocol.Target{Type: "issue", ID: i.IssueID}, input, rev(i.Revision)), ReviewIssueDelivery{})
	if technicalWaitErrorCode(op, err) != protocol.CodeUnknownOutcome {
		t.Fatal("a new report concealed the missing workspace stop receipt of the original Task", op, err)
	}
	confirmReviewerStopForTest(t, h, as)
	r := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, input))
	if !r.Valid || len(r.Rounds) != 2 || !reflect.DeepEqual(r.Rounds[0], frozen.Rounds[0]) || r.Rounds[1].TaskID != second || r.Rounds[1].RunID != successor.RunID || r.Rounds[1].Reviewer != frozen.Reviewer {
		t.Fatal("the legal successor did not retain the original reviewer and unfinished round", r)
	}
}

func TestSharedReviewReceiptSurvivesFinalUnlink(t *testing.T) {
	h, _, issue, source, _, _ := sharedReviewFixture(t)
	linked := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
	i := canonicalTaskIssue(t, h, issue.IssueID)
	input := protocol.UpdateSharedDeliveryReviewPayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Reason: "Unlink the last contribution"}, Action: "unlink", DeliveryID: "pr", TaskID: source}
	env := h.env("issues.review_delivery", protocol.Target{Type: "issue", ID: i.IssueID}, input, rev(i.Revision))
	unlinked := sharedReviewResult(t, h.exec(owner, env, ReviewIssueDelivery{}))
	if got := reviewReceiptForTest(t, h, i.IssueID, env.RequestID); got.Status != "committed" || got.Review.Revision != linked.Revision+1 || len(got.Review.Links) != 0 || got.Review.Revision != unlinked.Revision {
		t.Fatal("the original receipt was lost after removing its final link", got)
	}
}
