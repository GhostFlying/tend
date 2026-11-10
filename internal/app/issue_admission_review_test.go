package app

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
	"testing"
)

func TestIssueAdmissionRegressionRetainedOwnerPolicyOnReopen(t *testing.T) {
	h, _, i := issueOwnerHarness(t)
	// Before this patch the policy was stored separately; new admission fields are absent.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO issue_ownership_policies(issue_id,policy,request_id,created_at) VALUES(?,?,?,?)`, i.IssueID, protocol.IssueOwnerPolicy, "original-owner-request", 1); err != nil {
			return err
		}
		old := i.Revision
		i.OwnerSelection = nil
		i.Revision++
		return tx.SaveCanonicalIssue(t.Context(), i, old, store.IssueRevisionProvenance{AdmissionDefinition: true, Actor: owner, RequestDomain: "owner", RequestID: "fixture-history"})
	}); err != nil {
		t.Fatal(err)
	}
	i = admissionCurrent(t, h, i.IssueID)
	closed := canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, i, "close", canonicalLifecycleClosure(i, protocol.WorkItemClosureCompleted)))
	reopened := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.reopen", protocol.Target{Type: "issue", ID: i.IssueID}, canonicalOwnerControl{ExpectedAssignmentEpoch: closed.AssignmentEpoch}, rev(closed.Revision)), CanonicalOwnerIssueCommand{Action: "reopen"}))
	if reopened.OwnerSelection == nil || reopened.OwnerSelection.OwnershipPolicy != protocol.IssueOwnerPolicy {
		t.Fatalf("lost retained owner policy: %+v", reopened.OwnerSelection)
	}
}

func TestIssueAdmissionRegressionRetainedSourceWithdrawalAfterResume(t *testing.T) {
	h, r := managerClaimHarness(t)
	i := createCanonicalOwnerTestIssue(t, h)
	source := queueMessage(t, h, "Delegate only while this source is current")
	op := h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: i.IssueID}, protocol.RequestIssueManagerPayload{SourceMessageID: source, ExpectedAssignmentEpoch: i.AssignmentEpoch, Reason: "Retain source-bounded authorization"}, rev(i.Revision)), retainedManagerCommand{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	i = issueAdmissionStatus(t, h, i, protocol.IssueBacklog, nil)
	i = issueAdmissionStatus(t, h, i, protocol.IssueTodo, nil)
	withdrawOwnerSource(t, h, source)
	if next := admissionClaim(t, h, r); next != nil {
		t.Fatalf("withdrawn responsibility restarted: %+v, selection=%+v", next, admissionCurrent(t, h, i.IssueID).OwnerSelection)
	}
}

func TestIssueAdmissionRegressionPauseProgressExcludesFinishedLegacyRun(t *testing.T) {
	h, r, i := issueOwnerHarness(t)
	if op := createOwnerExecution(t, h, i, "implementer"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	as := ownerWorkerClaim(t, h, r, protocol.RoleImplement)
	i = issueAdmissionStatus(t, h, admissionCurrent(t, h, i.IssueID), protocol.IssueBacklog, nil)
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, testArtifact(h.task(as.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var view protocol.IssueView
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		view, err = views.CanonicalIssue(t.Context(), tx.Q(), i.IssueID, 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if view.Pause == nil {
		t.Fatal("missing pause view")
	}
	if view.Pause.Unsupported != 0 {
		t.Fatalf("finished run still reported as unsupported slot holder: %+v lease=%s", view.Pause, h.lease(as.RunID).Status)
	}
}
