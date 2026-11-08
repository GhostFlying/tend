package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestIssueOwnerOrdinaryReportDoesNotInheritSystemDefaultReview(t *testing.T) {
	h := newDeliverableHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	task := policyReportTask(t, h, issue)
	as, ok, _ := h.claimDeliverableRun()
	if !ok || as.TaskID != task {
		t.Fatal(as)
	}
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Read the source and recorded the findings"}}, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.task(task).Lifecycle != protocol.TaskCompleted || h.count("acceptances") != 0 {
		t.Fatal("ordinary report did not complete honestly", h.task(task))
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		policy, err := store.TaskAcceptancePolicy(t.Context(), tx.Q(), task)
		if err == nil && (policy == nil || policy.Required || policy.RequestID == "") {
			t.Fatal("Task choice lost its immutable provenance", policy)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIssueOwnerChoosesTaskReviewAndPreservesHumanRequirement(t *testing.T) {
	for _, tc := range []struct {
		name     string
		human    bool
		required bool
		want     protocol.OpStatus
	}{
		{name: "owner_requests_review", required: true, want: protocol.OpSucceeded},
		{name: "owner_chooses_self_check", required: false, want: protocol.OpSucceeded},
		{name: "explicit_human_requirement", human: true, required: false, want: protocol.OpFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newDeliverableHarness(t)
			var issue protocol.Issue
			if tc.human {
				issue = policyIssue(t, h, true)
			} else {
				issue = createCanonicalOwnerTestIssue(t, h)
			}
			spec := testSpec
			spec.Delivery, spec.Deliverables = "", []string{domain.DeliveryReport}
			p := canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Findings", Spec: spec, IndependentAcceptance: &protocol.IndependentAcceptanceRequest{Required: tc.required, Reason: "Responsible owner chose the review useful for this work"}}
			op, err := h.app.Execute(t.Context(), owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
			if tc.human {
				if protocol.CodeOf(err) != protocol.CodeForbidden {
					t.Fatal(op, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if op.Status != tc.want {
				t.Fatal(op)
			}
			var result taskResult
			if err := json.Unmarshal(op.Result, &result); err != nil {
				t.Fatal(err)
			}
			as, ok, _ := h.claimDeliverableRun()
			if !ok || as.TaskID != result.TaskID {
				t.Fatal(as)
			}
			if op := h.runAs(as, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Actual report"}}, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			want := protocol.TaskCompleted
			if tc.required {
				want = protocol.TaskAwaitingAcceptance
			}
			if h.task(result.TaskID).Lifecycle != want {
				t.Fatal(h.task(result.TaskID))
			}
		})
	}
}
