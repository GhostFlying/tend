package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestFreshSessionReconstructsOwnerContextFromLedger(t *testing.T) {
	h, _, issue, as, source := reviewManagerAssignment(t)
	if op := h.associate(owner, source, issue.IssueID); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// No native thread/turn has been recorded. All required input is retrieved
	// with this real Run's credentials rather than a prior model transcript.
	current, err := h.app.ReadRunContext(t.Context(), runPrincipal(as.RunID, as.Role), protocol.IssueMaterialsQuery{})
	if err != nil || current.ExecutionContext == nil || !strings.Contains(strings.Join(current.Materials.IssueIDs, ","), issue.IssueID) {
		t.Fatal(current, err)
	}
	original, err := h.app.ReadRunContext(t.Context(), runPrincipal(as.RunID, as.Role), protocol.IssueMaterialsQuery{Kind: "messages", ID: source})
	raw, _ := json.Marshal(original.Materials)
	if err != nil || !strings.Contains(string(raw), "Coordinate this intent") {
		t.Fatal(string(raw), err)
	}
}

func TestFreshSessionReconstructsImplementationContextFromLedger(t *testing.T) {
	h := newDeliverableHarness(t)
	created := h.createReportTask("standalone original goal", temporaryReportSpec())
	as, ok, _ := h.claimDeliverableRun()
	if !ok {
		t.Fatal("claim")
	}
	if as.TaskID != created.TaskID || !strings.Contains(string(as.Spec), "investigate") || !strings.Contains(string(as.Spec), "reconstructed from the ledger") {
		t.Fatal(as)
	}
	current, err := h.app.ReadRunContext(t.Context(), runPrincipal(as.RunID, as.Role), protocol.IssueMaterialsQuery{Kind: "tasks", ID: created.TaskID})
	if err != nil || current.ExecutionContext == nil || len(current.Materials.Tasks) != 1 || !strings.Contains(string(current.Materials.Tasks[0].Spec), "evidence independently checked") {
		t.Fatal(current, err)
	}
}

func TestFreshSessionReconstructsReviewerContextFromLedger(t *testing.T) {
	h, runtime, issue, source, c, _ := sharedReviewFixture(t)
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
	task := createSharedReviewerTaskForTest(t, h, issue.IssueID)
	r := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: task}))
	as, ok := h.claimInputRun(runtime)
	if !ok || as.TaskID != task {
		t.Fatal(as)
	}
	if !strings.Contains(string(as.Spec), r.ReviewID) || !strings.Contains(string(as.Spec), c.HeadSHA) {
		t.Fatal("fresh reviewer lacks actual target", string(as.Spec))
	}
	current, err := h.app.ReadRunContext(t.Context(), runPrincipal(as.RunID, as.Role), protocol.IssueMaterialsQuery{Kind: "reviews", ID: "pr"})
	if err != nil || current.Materials.SharedReviews == nil || len(current.Materials.SharedReviews.ExistingEvidence) != 1 || current.Materials.SharedReviews.ExistingEvidence[0].Contributions[0].ArtifactID == "" {
		t.Fatal(current, err)
	}
}

func TestFreshSessionReconstructsBoundDeliveryAuthorityFromLedger(t *testing.T) {
	h, _, issue, as, candidate := liveReviewedDeliveryFixture(t)
	prepareLiveDeliveryForTest(t, h, issue.IssueID, as.TaskID, candidate)
	p := protocol.UpdateIssueDeliveryPayload{Action: "bind", TaskID: as.TaskID, ExecutionTaskID: as.TaskID, ExpectedTaskControlRevision: rev(h.task(as.TaskID).ControlRevision), IssueControlPayload: protocol.IssueControlPayload{Reason: "Continue the real original delivery without session memory"}}
	if op := updateDeliveryForTest(t, h, issue.IssueID, p); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	current, err := h.app.ReadRunContext(t.Context(), runPrincipal(as.RunID, as.Role), protocol.IssueMaterialsQuery{Kind: "delivery_authorization"})
	if err != nil || current.Materials.DeliveryAuthorization == nil || !current.Materials.DeliveryAuthorization.Valid || current.Materials.DeliveryAuthorization.SourceTaskID != as.TaskID {
		t.Fatal(current, err)
	}
	if len(as.Spec) == 0 || !strings.Contains(string(as.Spec), "reconstructed from the ledger") {
		t.Fatal("delivery spec depends on old session")
	}
}
