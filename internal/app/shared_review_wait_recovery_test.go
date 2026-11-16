package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestSharedReviewWaitPreservesNoVerdictAndContinuesSameReviewerTask(t *testing.T) {
	h, runtime, issue, source, candidate, _ := sharedReviewFixture(t)
	linked := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
	task := createSharedReviewerTaskForTest(t, h, issue.IssueID)
	input := protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: task, ExpectedHeadSHA: linked.HeadSHA, ExpectedBaseSHA: linked.BaseSHA, ExpectedRequirementsDigest: linked.RequirementsDigest}
	candidate.HeadSHA = strings.Repeat("e", 40)
	observeDeliveryForTest(t, h, issue.IssueID, candidate, "open", "")
	if op := sharedReviewAction(t, h, issue.IssueID, input); protocol.CodeOf(op.Error) != protocol.CodeStaleRevision {
		t.Fatal("start silently rebound the review to an unexamined refreshed candidate", op)
	}
	input.ExpectedHeadSHA = candidate.HeadSHA
	r := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, input))
	as, ok := h.claimInputRun(runtime)
	if !ok || as.TaskID != task {
		t.Fatal(as)
	}
	wait := protocol.UpdateSharedDeliveryReviewPayload{Action: "wait", DeliveryID: "pr", ExpectedReviewRevision: rev(r.Revision), IssueControlPayload: protocol.IssueControlPayload{Reason: "The real coordination-only report reached no quality verdict"}}
	i := canonicalTaskIssue(t, h, issue.IssueID)
	wait.ExpectedAssignmentEpoch = i.AssignmentEpoch
	op, err := h.app.Execute(t.Context(), owner, h.env("issues.review_delivery", protocol.Target{Type: "issue", ID: i.IssueID}, wait, rev(i.Revision)), ReviewIssueDelivery{})
	if technicalWaitErrorCode(op, err) != protocol.CodeUnknownOutcome {
		t.Fatal("live reviewer was released without stop", op, err)
	}
	// A real coordination-only report is not a quality verdict. The normal
	// report submission ends this Run; its frozen review round stays intact.
	if op := h.runAs(as, protocol.OpRunsReportWorkspace, protocol.WorkspaceReport{BackendID: as.Workspace.BackendID, Path: t.TempDir(), Baseline: json.RawMessage(`{"files":[]}`)}, ReportWorkspace{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "The candidate changed; no independent review conclusion was reached"}}, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	resource := h.resourceForTask(task)
	if !h.releaseResource(task, resource.WorkspaceID) {
		t.Fatal("resource not released")
	}
	waiting := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, wait))
	if waiting.Status != "waiting" || waiting.Valid || !reflect.DeepEqual(waiting.Rounds, r.Rounds) || waiting.Rounds[0].Result != nil {
		t.Fatal("coordination recovery fabricated or erased quality facts", waiting)
	}
	if op := continueTaskForTest(t, h, task, "Review the current frozen candidate after the coordination-only report"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	input.Action = "recheck"
	r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, input))
	executeSharedReviewForTest(t, h, runtime, r, task, "passed")
	r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
	if !r.Valid || r.Status != "passed" || len(r.Rounds) != 2 || r.Rounds[0].Result != nil || r.Rounds[0].TaskID != r.Rounds[1].TaskID || r.Rounds[1].ImplementGeneration <= r.Rounds[0].ImplementGeneration {
		t.Fatal("normal same Task reviewer continuation failed", r)
	}
}

func TestSharedReviewWaitCannotHideAnActualVerdictOrStaleCoordination(t *testing.T) {
	h, runtime, issue, source, _, _ := sharedReviewFixture(t)
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
	task := createSharedReviewerTaskForTest(t, h, issue.IssueID)
	r := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: task}))
	executeSharedReviewForTest(t, h, runtime, r, task, "changes_requested")
	resource := h.resourceForTask(task)
	h.releaseResource(task, resource.WorkspaceID)
	for _, revision := range []int64{r.Revision - 1, r.Revision} {
		op := sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "wait", DeliveryID: "pr", ExpectedReviewRevision: rev(revision)})
		if op.Status != protocol.OpFailed {
			t.Fatal("waiting concealed the actual reviewer finding", op)
		}
	}
	r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
	if r.Status != "changes_requested" || r.Rounds[0].Result == nil || len(r.Rounds[0].Result.Findings) == 0 {
		t.Fatal("actual finding not retained", r)
	}
}
