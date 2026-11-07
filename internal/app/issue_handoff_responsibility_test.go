package app

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
	"testing"
)

func TestIssueHandoffUsesFrozenIndependentReviewResponsibility(t *testing.T) {
	h, runtime, issue := issueOwnerHarness(t)
	if op := createOwnerExecution(t, h, issue, "implementer"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	impl := ownerWorkerClaim(t, h, runtime, protocol.RoleImplement)
	if op := h.runAs(impl, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, testArtifact(h.task(impl.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	accept := ownerWorkerClaim(t, h, runtime, protocol.RoleAccept)
	if accept.Responsibility == nil || accept.Responsibility.IssueID == issue.IssueID {
		t.Fatal(accept)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		parent, parentErr := views.IssueHandoffPrompt(t.Context(), tx.Q(), issue.IssueID, accept.RunID)
		child, childErr := views.IssueHandoffPrompt(t.Context(), tx.Q(), accept.Responsibility.IssueID, accept.RunID)
		t.Logf("implementation parent error=%v parent run=%s; actual review child error=%v child run=%s", parentErr, parent.RunID, childErr, child.RunID)
		if childErr != nil {
			t.Errorf("actual responsible review child cannot hand off its run: %v", childErr)
		}
		if parentErr == nil {
			t.Error("implementation parent was accepted as controller of its independent review child's run")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
