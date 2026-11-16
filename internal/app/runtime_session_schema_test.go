package app

import (
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestRuntimeSchemaContinuationUsesNormalSessionStopAndTaskContinue(t *testing.T) {
	h, runtime, issue, source, _, _ := sharedReviewFixture(t)
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
	task := createSharedReviewerTaskForTest(t, h, issue.IssueID)
	review := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: task}))
	executeSharedReviewForTest(t, h, runtime, review, task, "passed")
	review = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
	originalRun := review.Rounds[0].RunID
	// The executor's physical-close assertion enters through the normal business
	// command; the fixture does not insert a session stop row or continuation.
	envelope := h.env(protocol.OpRunsRecordSessionStop, protocol.Target{Type: "run", ID: originalRun}, protocol.SessionStopPayload{ThreadID: "original-native-session"}, nil)
	principal := runPrincipal(originalRun, protocol.RoleImplement)
	receipt := h.exec(principal, envelope, RecordSessionStop{})
	if receipt.Status != protocol.OpSucceeded {
		t.Fatal("normal stop receipt failed", receipt)
	}
	replay := h.exec(principal, envelope, RecordSessionStop{})
	if replay.Status != protocol.OpSucceeded {
		t.Fatal("original stop receipt cannot reconcile", replay)
	}
	resource := h.resourceForTask(task)
	if !h.releaseResource(task, resource.WorkspaceID) {
		t.Fatal("completed run did not release its resource")
	}
	if op := continueTaskForTest(t, h, task, "CURRENT_SCHEMA_REVIEW_DELTA"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	next, ok := h.claimInputRun(runtime)
	if !ok || next.TaskID != task || next.RunID == originalRun || next.SessionContinuation == nil || next.SessionContinuation.RunID != originalRun || next.SessionContinuation.ThreadID != "original-native-session" {
		t.Fatal("normal continuation replaced Task or lost stopped Run", next)
	}
	if !strings.Contains(string(next.Spec), "CURRENT_SCHEMA_REVIEW_DELTA") || !strings.Contains(string(next.Spec), review.Rounds[0].ArtifactID) {
		t.Fatal("fresh session cannot reconstruct current task and original report", string(next.Spec))
	}
	predecessor, err := h.app.NativeQuestionPredecessor(t.Context(), next.RunID)
	if err != nil || predecessor != originalRun {
		t.Fatal("native session switch has no validated stopped predecessor", predecessor, err)
	}
}
