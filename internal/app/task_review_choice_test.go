package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestActiveTaskCanWithdrawOptionalReviewWithoutInventingVerdict(t *testing.T) {
	h := newDeliverableHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	if issue.AcceptancePolicy == nil || issue.AcceptancePolicy.Required {
		t.Fatal("review is not optional by default", issue.AcceptancePolicy)
	}
	spec := testSpec
	spec.Delivery, spec.Deliverables = "", []string{domain.DeliveryReport}
	input := canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Investigate", Spec: spec, IndependentAcceptance: &protocol.IndependentAcceptanceRequest{Required: true, Reason: "Initially request a second read"}}
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, input, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var created taskResult
	if err := json.Unmarshal(op.Result, &created); err != nil {
		t.Fatal(err)
	}
	as, ok, _ := h.claimDeliverableRun()
	if !ok || as.TaskID != created.TaskID {
		t.Fatal(as)
	}
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Source-grounded findings"}}, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	before := h.task(created.TaskID)
	if before.Lifecycle != protocol.TaskAwaitingAcceptance {
		t.Fatal(before)
	}
	change := protocol.UpdateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, IndependentAcceptance: &protocol.IndependentAcceptanceRequest{Required: false, Reason: "The bounded source report is sufficient; withdraw the optional review"}}
	op = h.exec(owner, h.env("issues.update_task", taskTarget(created.TaskID), change, rev(before.ControlRevision)), CanonicalOwnerTaskUpdate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	after := h.task(created.TaskID)
	if after.Lifecycle != protocol.TaskCompleted || after.CurrentArtifactID != before.CurrentArtifactID || h.count("acceptances") != 0 || h.count("task_review_choices") != 1 {
		t.Fatal("choice lost the result or fabricated review", after)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		evidence, err := store.CompletedTaskEvidence(t.Context(), tx.Q(), created.TaskID, "Actual result")
		if err == nil && evidence.Kind != protocol.WorkItemEvidenceResult {
			t.Fatal(evidence)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if op := h.exec(owner, h.env("issues.update_task", taskTarget(created.TaskID), change, rev(after.ControlRevision)), CanonicalOwnerTaskUpdate{}); op.Status != protocol.OpFailed {
		t.Fatal("historical result policy was rewritten", op)
	}
}

func TestReviewHumanTaskRequirementSurvivesAgentEndorsement(t *testing.T) {
	h := newDeliverableHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	spec := testSpec
	spec.Delivery = ""
	spec.Deliverables = []string{domain.DeliveryReport}
	input := canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Human-required review", Spec: spec, IndependentAcceptance: &protocol.IndependentAcceptanceRequest{Required: true, Reason: "Human explicitly requires independent review"}}
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, input, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var task taskResult
	if err := json.Unmarshal(op.Result, &task); err != nil {
		t.Fatal(err)
	}
	var kind string
	err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		row, current, err := loadTask(t.Context(), tx.Q(), task.TaskID)
		if err != nil {
			return err
		}
		identity := protocol.AgentIdentity{ExecutionEnvironmentID: "server-hosted", LocalID: "planner"}
		actor := protocol.Principal{Kind: protocol.PrincipalMainAgent}
		x := &Exec{Principal: owner, Actor: &actor, actorAgent: &identity, Now: h.clock.Now(), Env: h.env("issues.update_task", taskTarget(task.TaskID), map[string]any{"independent_acceptance": protocol.IndependentAcceptanceRequest{Required: true, Reason: "Agent agrees that review is needed"}}, rev(row.ControlRevision))}
		if err := updateTaskReviewChoice(t.Context(), x, tx, row, current, &protocol.IndependentAcceptanceRequest{Required: true, Reason: "Agent agrees that review is needed"}); err != nil {
			return err
		}
		previous, err := store.TaskAcceptancePolicy(t.Context(), tx.Q(), task.TaskID)
		if err != nil {
			return err
		}
		kind = previous.DecidedBy.Kind
		row.ControlRevision++
		x.Env = h.env("issues.update_task", taskTarget(task.TaskID), map[string]any{"independent_acceptance": protocol.IndependentAcceptanceRequest{Required: false, Reason: "Agent now prefers self-check"}}, rev(row.ControlRevision))
		err = updateTaskReviewChoice(t.Context(), x, tx, row, current, &protocol.IndependentAcceptanceRequest{Required: false, Reason: "Agent now prefers self-check"})
		if protocol.CodeOf(err) != protocol.CodeForbidden {
			t.Errorf("human requirement became %q authority, then Agent waived it: %v", kind, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
