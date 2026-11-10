package app

import (
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestIssueDeliveryScopeAuthorizesDeploymentWithoutCandidateQuestion(t *testing.T) {
	h, _, issue, task, candidate, acceptance := sharedReviewFixture(t)
	importSharedReviewForTest(t, h, issue, task, acceptance)
	issue = setPRPolicyForTest(t, h, issue.IssueID, "auto")
	issue.Plan.PRPolicy.Deployment = &protocol.IssueDeploymentScope{ServerInstance: "server", HostIDs: []string{"linux-1"}}
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "update", protocol.UpdateIssuePayload{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}))
	candidate.Deploy = true
	candidate.DeploymentScope = "Install this Issue's reviewed delivery on its authorized fixed service"
	candidate.Deployment = &protocol.IssueDeliveryDeployment{ServerInstance: "server", HostIDs: []string{"linux-1"}}
	duty := prepareDeliveryForTest(t, h, issue.IssueID, task, candidate)
	if duty.Status != "authorized" || duty.AttentionID != "" || duty.Authorization == nil || duty.Authorization.RequestID != issue.Plan.PRPolicy.Authority.RequestID || h.count("attentions") != 0 {
		t.Fatal("explicit scope still required transcription as a candidate answer", duty)
	}
	if op := createDeliveryExecutionForTest(t, h, issue.IssueID, duty); op.Status != protocol.OpSucceeded {
		t.Fatal("automatic deployment could not bind a real execution", op)
	}
}

func TestIssueDeliveryScopeKeepsOriginalAuthorityAcrossOwnerRequirementJudgment(t *testing.T) {
	h, _, issue, task, candidate, acceptance := sharedReviewFixture(t)
	importSharedReviewForTest(t, h, issue, task, acceptance)
	issue = setPRPolicyForTest(t, h, issue.IssueID, "auto")
	duty := prepareDeliveryForTest(t, h, issue.IssueID, task, candidate)
	before := *issue.Plan.PRPolicy.Authority
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	issue.Plan.CompletionCriteria = append(issue.Plan.CompletionCriteria, "Owner clarified presentation of the already authorized result")
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "update", protocol.UpdateIssuePayload{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}))
	if !reflect.DeepEqual(*issue.Plan.PRPolicy.Authority, before) {
		t.Fatal("ordinary Issue edit manufactured replacement authorization", issue.Plan.PRPolicy)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error { return store.DeliveryGrantValid(t.Context(), tx.Q(), issue, duty) }); err != nil {
		t.Fatal("owner judgment was replaced by a requirement-digest approval gate", err)
	}
	// A new physical target is an actual authority enlargement, not a rebase.
	duty.Candidate.Deploy = true
	duty.Candidate.Deployment = &protocol.IssueDeliveryDeployment{ServerInstance: "other-service", HostIDs: []string{}}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error { return store.DeliveryGrantValid(t.Context(), tx.Q(), issue, duty) }); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("merge-only grant permitted an unrelated installation", err)
	}
}

func TestIssueDeploymentScopeRestrictsTargetsAndPreservesMergeOnlyDefault(t *testing.T) {
	policy := &protocol.IssuePRPolicy{Deployment: &protocol.IssueDeploymentScope{ServerInstance: "service", HostIDs: []string{"runner-one"}}}
	for _, tc := range []struct {
		name      string
		candidate protocol.IssueDeliveryCandidate
		allowed   bool
	}{
		{"merge", protocol.IssueDeliveryCandidate{}, true},
		{"server only", protocol.IssueDeliveryCandidate{Deploy: true, Deployment: &protocol.IssueDeliveryDeployment{ServerInstance: "service", HostIDs: []string{}}}, true},
		{"runner", protocol.IssueDeliveryCandidate{Deploy: true, Deployment: &protocol.IssueDeliveryDeployment{ServerInstance: "service", HostIDs: []string{"runner-one"}}}, true},
		{"other server", protocol.IssueDeliveryCandidate{Deploy: true, Deployment: &protocol.IssueDeliveryDeployment{ServerInstance: "other", HostIDs: []string{}}}, false},
		{"other runner", protocol.IssueDeliveryCandidate{Deploy: true, Deployment: &protocol.IssueDeliveryDeployment{ServerInstance: "service", HostIDs: []string{"runner-two"}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := store.IssuePolicyAuthorizesDeployment(policy, &tc.candidate); got != tc.allowed {
				t.Fatal(got)
			}
			if tc.candidate.Deploy && store.IssuePolicyAuthorizesDeployment(&protocol.IssuePRPolicy{}, &tc.candidate) {
				t.Fatal("old merge-only policy acquired deployment authority")
			}
		})
	}
}
