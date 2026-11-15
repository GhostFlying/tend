package app

import (
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestPredeploymentPrepareBeforeContinueRetainsBinding(t *testing.T) {
	h, r, i, as, next, stage := predeploymentFixture(t)
	original := deliveryDutyForTest(t, h, i.IssueID, as.TaskID)
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, stage, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	acceptStageForTest(t, h, r, as.TaskID)
	reviewNextPredeploymentForTest(t, h, r, i, as.TaskID, next)
	next.Deploy, next.Deployment = false, nil
	op := updateDeliveryForTest(t, h, i.IssueID, protocol.UpdateIssueDeliveryPayload{Action: "prepare", TaskID: as.TaskID, Candidate: &next, ExpectedTaskControlRevision: rev(h.task(as.TaskID).ControlRevision), IssueControlPayload: protocol.IssueControlPayload{Reason: "Retain deployment even when prepare precedes continue"}})
	expectWorkCode(t, op, protocol.CodeInvalidTransition)
	if !reflect.DeepEqual(deliveryDutyForTest(t, h, i.IssueID, as.TaskID), original) || h.count("issue_delivery_execution_history") != 0 {
		t.Fatal("prepare discarded the stage's outstanding deployment")
	}
	if op := continueTaskForTest(t, h, as.TaskID, "Continue the accepted stage without losing its binding"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func TestPredeploymentPreparationClaimAfterMainAdvances(t *testing.T) {
	h, r, i, as, _, stage := predeploymentFixture(t)
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, stage, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	acceptStageForTest(t, h, r, as.TaskID)
	original := *deliveryDutyForTest(t, h, i.IssueID, as.TaskID).Candidate
	original.TargetSHA = strings.Repeat("e", 40)
	observeDeliveryForTest(t, h, i.IssueID, original, "merged", strings.Repeat("c", 40))
	if op := continueTaskForTest(t, h, as.TaskID, "Prepare this accepted candidate on current main"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		o, err := store.GetDeliveryObservation(t.Context(), tx.Q(), i.IssueID, original.DeliveryID)
		if err != nil {
			return err
		}
		o.Observation.LineageErrorKind = "unavailable"
		return putDeliveryObservationForTest(tx, t.Context(), o)
	}); err != nil {
		t.Fatal(err)
	}
	if claimed, ok := h.claimInputRun(r); ok {
		t.Fatal("unknown lineage admitted preparation", claimed)
	}
	observeDeliveryForTest(t, h, i.IssueID, original, "merged", strings.Repeat("c", 40))
	if next, ok := h.claimInputRun(r); !ok || next.TaskID != as.TaskID {
		t.Fatal("the preparation Run cannot start", next, ok)
	}
	d := deliveryDutyForTest(t, h, i.IssueID, as.TaskID)
	currentIssue := canonicalTaskIssue(t, h, i.IssueID)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		// Scheduling preparation must not make the old candidate installable.
		_, err := deliveryExecutionAuthorized(t.Context(), tx.Q(), currentIssue, protocol.IssueDeliveryAuthorization{TaskID: as.TaskID, Revision: d.Revision})
		if protocol.CodeOf(err) != protocol.CodeStaleRevision {
			t.Fatal("old installation authority was widened", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPredeploymentCompleteHostScopeUsesSetSemantics(t *testing.T) {
	h, r, i, as, next, stage := predeploymentFixture(t)
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, stage, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	acceptStageForTest(t, h, r, as.TaskID)
	frozen := *deliveryDutyForTest(t, h, i.IssueID, as.TaskID).Candidate
	frozen.Deployment.HostIDs = []string{"linux-1", "linux-2"}
	next.Deployment = &protocol.IssueDeliveryDeployment{ServerInstance: "server", HostIDs: []string{"linux-2", "linux-1"}}
	task := h.task(as.TaskID)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		o, err := store.GetDeliveryObservation(t.Context(), tx.Q(), i.IssueID, frozen.DeliveryID)
		if err != nil {
			return err
		}
		for _, hosts := range [][]string{{"linux-2", "linux-1"}, {"linux-1"}, {"linux-1", "linux-2", "other"}, {"linux-1", "linux-1"}} {
			next.Deployment.HostIDs = hosts
			err := mergedPredeploymentContinuation(t.Context(), tx.Q(), task, frozen, o.Observation, &next)
			if reflect.DeepEqual(hosts, []string{"linux-2", "linux-1"}) {
				if err != nil {
					t.Fatal("same complete host scope was rejected", err)
				}
				if hosts[0] != "linux-2" || frozen.Deployment.HostIDs[0] != "linux-1" {
					t.Fatal("scope comparison changed its inputs")
				}
			} else if protocol.CodeOf(err) != protocol.CodeForbidden {
				t.Fatal("changed or duplicate host scope was admitted", hosts, err)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPredeploymentPreparationOfOriginalCandidateAfterMainAdvances(t *testing.T) {
	h, r, i, as, _, stage := predeploymentFixture(t)
	original := *deliveryDutyForTest(t, h, i.IssueID, as.TaskID).Candidate
	stage.Mounts[0].CommitSHA = original.HeadSHA
	stage.Report.Predeployment = &protocol.PredeploymentResult{DeliveryID: original.DeliveryID, HeadSHA: original.HeadSHA, BaseSHA: original.BaseSHA}
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, stage, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	acceptStageForTest(t, h, r, as.TaskID)
	original.TargetSHA = strings.Repeat("e", 40)
	observeDeliveryForTest(t, h, i.IssueID, original, "merged", strings.Repeat("c", 40))
	if op := continueTaskForTest(t, h, as.TaskID, "Prepare a correction from this accepted original candidate"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if claimed, ok := h.claimInputRun(r); !ok || claimed.TaskID != as.TaskID {
		t.Fatal("original candidate preparation cannot start", claimed, ok)
	}
}

func TestPredeploymentUnchangedInstallationRequiresIndependentStageAcceptance(t *testing.T) {
	h, _, _, as, _, stage := predeploymentFixture(t)
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, stage, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	stage.Report = &protocol.ResultReport{Body: "Installation output cannot borrow an unaccepted candidate", Evidence: []string{"upgrade:invented"}}
	stage.Mounts[0].Changed = false
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		allowed, err := unchangedDeliveryMountsAllowed(t.Context(), tx.Q(), as.RunID, stage)
		if allowed || protocol.CodeOf(err) != protocol.CodeInvalidTransition {
			t.Fatal("unaccepted commit output reused", allowed, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
