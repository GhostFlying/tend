package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

// The fixture uses the existing real shared review, policy grant, execution
// creation and runner claim flow; only the external provider is a test double.
func mergeSettlementFixture(t *testing.T) (*harness, protocol.Assignment, *protocol.DeliveryExecutionAuthority) {
	t.Helper()
	h, runtime, issue, source, candidate, acceptance := sharedReviewFixture(t)
	importSharedReviewForTest(t, h, issue, source, acceptance)
	h.app.serverDeliveryObservations = true
	issue = setPRPolicyForTest(t, h, issue.IssueID, "auto")
	duty := prepareDeliveryForTest(t, h, issue.IssueID, source, candidate)
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	spec := protocol.OwnerTaskSpec{Goal: "Settle only the authorized reviewed merge", Acceptance: []string{"Verify the original head's server merge and ancestry"}, Deliverables: []string{domain.DeliveryReport}, Workspace: protocol.OwnerTaskWorkspace{Mode: domain.WorkspaceTemporary}, AgentSelection: &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer", AgentID: "linux-1/alpha"}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer"}}}
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.CreateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Settle approved integration", Spec: spec, DeliveryAuthorization: &protocol.IssueDeliveryAuthorization{TaskID: source, Revision: duty.Revision}}, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var execution taskResult
	if err := json.Unmarshal(op.Result, &execution); err != nil {
		t.Fatal(err)
	}
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityWorkerObservation)
	runtime.Sequence++
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	run, ok := h.claimInputRun(runtime)
	if !ok || run.TaskID != execution.TaskID {
		t.Fatal(run)
	}
	if op := h.runAs(run, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(run, protocol.OpRunsReportWorkspace, protocol.WorkspaceReport{BackendID: run.Workspace.BackendID, Path: t.TempDir(), Baseline: json.RawMessage(`{"files":[]}`)}, ReportWorkspace{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	view, err := h.app.ReadRunObservation(t.Context(), runPrincipal(run.RunID, run.Role))
	if err != nil || view.DeliveryAuthorization == nil || !view.DeliveryAuthorization.Valid {
		t.Fatal(view.DeliveryAuthorization, err)
	}
	return h, run, view.DeliveryAuthorization
}

func settlementSnapshot(candidate protocol.IssueDeliveryCandidate) protocol.DeliverySnapshot {
	return protocol.DeliverySnapshot{State: "merged", HeadSHA: candidate.HeadSHA, BaseSHA: candidate.BaseSHA, TargetSHA: strings.Repeat("e", 40), MergeSHA: strings.Repeat("d", 40), TargetContainsMerge: true, URL: candidate.URL}
}

func recordSettlementSnapshot(t *testing.T, h *harness, snapshot protocol.DeliverySnapshot) {
	t.Helper()
	query, err := h.app.ClaimServerDelivery(t.Context())
	if err != nil || query == nil {
		t.Fatal(query, err)
	}
	if err = h.app.RecordServerDelivery(t.Context(), query.ID, &snapshot, ""); err != nil {
		t.Fatal(err)
	}
}

func refreshSettlement(t *testing.T, h *harness, authority *protocol.DeliveryExecutionAuthority) {
	t.Helper()
	issue := canonicalTaskIssue(t, h, authority.IssueID)
	op := h.exec(owner, h.env(protocol.OpRefreshDelivery, protocol.Target{Type: "work_item", ID: issue.IssueID}, protocol.RefreshDeliveryPayload{DeliveryID: authority.Candidate.DeliveryID}, rev(issue.Revision)), RefreshDelivery{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func TestDeliveryMergeSettlementKeepsOriginalExecutionWhenVerifiedTargetAdvances(t *testing.T) {
	h, run, authority := mergeSettlementFixture(t)
	original := deliveryDutyForTest(t, h, authority.IssueID, authority.SourceTaskID)
	var binding string
	readBinding := func() string {
		var value string
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			return tx.Q().QueryRowContext(t.Context(), `SELECT json_array(issue_id,source_task_id,duty_revision,candidate_json) FROM issue_delivery_executions WHERE task_id=?`, run.TaskID).Scan(&value)
		}); err != nil {
			t.Fatal(err)
		}
		return value
	}
	binding = readBinding()
	snapshot := settlementSnapshot(authority.Candidate)
	recordSettlementSnapshot(t, h, snapshot)
	assertCurrent := func(workerActive bool) {
		t.Helper()
		duty := deliveryDutyForTest(t, h, authority.IssueID, authority.SourceTaskID)
		if !reflect.DeepEqual(duty, original) || readBinding() != binding {
			t.Fatal("observed target advancement rewrote the original duty, grant or execution", duty)
		}
		var current *protocol.DeliveryExecutionAuthority
		if workerActive {
			view, err := h.app.ReadRunObservation(t.Context(), runPrincipal(run.RunID, run.Role))
			if err != nil || view.DeliveryAuthorization == nil {
				t.Fatal(view.DeliveryAuthorization, err)
			}
			current = view.DeliveryAuthorization
		} else {
			// Completion still ends worker read authority. Owner settlement can
			// inspect its original binding without reviving that finished lease.
			if _, err := h.app.ReadRunObservation(t.Context(), runPrincipal(run.RunID, run.Role)); protocol.CodeOf(err) != protocol.CodeForbidden {
				t.Fatal("finished worker unexpectedly regained observation access", err)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				var err error
				current, err = views.DeliveryExecutionAuthority(t.Context(), tx.Q(), run.TaskID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}
		if !current.Valid || current.Provider == nil || !current.Provider.TargetContainsMerge || current.Provider.LineageErrorKind != "" || current.Provider.TargetSHA != snapshot.TargetSHA || current.Provider.MergeSHA != snapshot.MergeSHA || current.Candidate != authority.Candidate || !reflect.DeepEqual(current.Grant, authority.Grant) {
			t.Fatal("worker lost the exact authorization or real server ancestry", current)
		}
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			valid, err := deliveryTaskCurrent(t.Context(), tx.Q(), run.TaskID)
			if err == nil && !valid {
				t.Fatal("legal completed merge invalidated the original execution")
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	assertCurrent(true)
	if op := h.runAs(run, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "The reviewed head was merged; server proved its merge remains in the target"}}, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.task(run.TaskID).Lifecycle != protocol.TaskCompleted {
		t.Fatal(h.task(run.TaskID))
	}
	// A further unrelated merge can land after the execution report but before
	// the owner settles the same duty; it still cannot require another merge.
	refreshSettlement(t, h, authority)
	snapshot.TargetSHA = strings.Repeat("f", 40)
	recordSettlementSnapshot(t, h, snapshot)
	assertCurrent(false)
	op := updateDeliveryForTest(t, h, authority.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: authority.SourceTaskID, Action: "complete", IssueControlPayload: protocol.IssueControlPayload{Reason: "Settle the original server-verified merge without authorizing any new source"}})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if duty := deliveryDutyForTest(t, h, authority.IssueID, authority.SourceTaskID); duty.Status != "complete" || duty.ExecutionTaskID != run.TaskID || !reflect.DeepEqual(duty.Candidate, original.Candidate) || !reflect.DeepEqual(duty.Authorization, original.Authorization) {
		t.Fatal(duty)
	}
	if h.count("acceptances") != 1 {
		t.Fatal("settlement created another formal review", h.count("acceptances"))
	}
}

func TestDeliveryMergeSettlementRejectsUnverifiedAdvancedTarget(t *testing.T) {
	for _, name := range []string{"wrong_head", "wrong_base", "missing_merge", "missing_target", "not_contained"} {
		t.Run(name, func(t *testing.T) {
			h, run, authority := mergeSettlementFixture(t)
			original := deliveryDutyForTest(t, h, authority.IssueID, authority.SourceTaskID)
			snapshot := settlementSnapshot(authority.Candidate)
			switch name {
			case "wrong_head":
				snapshot.HeadSHA = strings.Repeat("c", 40)
			case "wrong_base":
				snapshot.BaseSHA = strings.Repeat("c", 40)
			case "missing_merge":
				snapshot.MergeSHA = ""
			case "missing_target":
				snapshot.TargetSHA = ""
			case "not_contained":
				snapshot.TargetContainsMerge = false
			}
			recordSettlementSnapshot(t, h, snapshot)
			duty := deliveryDutyForTest(t, h, authority.IssueID, authority.SourceTaskID)
			if duty.Status != "preparing" || duty.Revision != original.Revision+1 {
				t.Fatal("unverified fact retained authorization", duty)
			}
			view, err := h.app.ReadRunObservation(t.Context(), runPrincipal(run.RunID, run.Role))
			if err != nil || view.DeliveryAuthorization == nil || view.DeliveryAuthorization.Valid {
				t.Fatal(view.DeliveryAuthorization, err)
			}
			provider := view.DeliveryAuthorization.Provider
			if provider == nil || provider.TargetContainsMerge != snapshot.TargetContainsMerge || provider.LineageErrorKind != snapshot.LineageErrorKind {
				t.Fatal("worker proof fields were dropped or fabricated", provider)
			}
			o := protocol.DeliveryObservation{DeliverySnapshot: snapshot, Observer: "server"}
			if _, err := deliveryDeploymentSource(authority.Candidate, o); protocol.CodeOf(err) != protocol.CodeStaleRevision {
				t.Fatal("source helper accepted unverified ancestry", err)
			}
			op := updateDeliveryForTest(t, h, authority.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: authority.SourceTaskID, Action: "complete", IssueControlPayload: protocol.IssueControlPayload{Reason: "An unverified original merge cannot settle responsibility"}})
			expectWorkCode(t, op, protocol.CodeStaleRevision)
		})
	}
}

func TestDeliveryMergeSettlementPreservesLegacyAndDeploymentSourceBoundaries(t *testing.T) {
	candidate := protocol.IssueDeliveryCandidate{ProviderState: "open", HeadSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40)}
	for _, tc := range []struct {
		name, observer            string
		deploy, advanced, allowed bool
	}{
		{"legacy exact", "", false, false, true},
		{"runner exact", "runner", false, false, true},
		{"legacy advanced", "", false, true, false},
		{"runner advanced", "runner", false, true, false},
		{"server advanced", "server", false, true, true},
		{"deployment exact", "server", true, false, true},
		{"deployment advanced", "server", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := candidate
			c.Deploy = tc.deploy
			snapshot := settlementSnapshot(c)
			if !tc.advanced {
				snapshot.TargetSHA = snapshot.MergeSHA
			}
			got, err := deliveryDeploymentSource(c, protocol.DeliveryObservation{DeliverySnapshot: snapshot, Observer: tc.observer})
			if (err == nil) != tc.allowed || (err == nil && got != snapshot.MergeSHA) {
				t.Fatal("source authority changed", got, err)
			}
		})
	}
	c := candidate
	c.ProviderState = "merged"
	c.Deploy = true
	c.MergeSHA = strings.Repeat("d", 40)
	c.TargetSHA = c.MergeSHA
	snapshot := settlementSnapshot(c)
	if _, err := deliveryDeploymentSource(c, protocol.DeliveryObservation{DeliverySnapshot: snapshot, Observer: "server"}); protocol.CodeOf(err) != protocol.CodeStaleRevision {
		t.Fatal("frozen deployment target drift became authorized", err)
	}
}

func TestDeliveryMergeSettlementDoesNotAuthorizeUnboundCandidate(t *testing.T) {
	h, _, issue, source, candidate, acceptance := sharedReviewFixture(t)
	importSharedReviewForTest(t, h, issue, source, acceptance)
	h.app.serverDeliveryObservations = true
	issue = setPRPolicyForTest(t, h, issue.IssueID, "auto")
	before := prepareDeliveryForTest(t, h, issue.IssueID, source, candidate)
	if before.Status != "authorized" || before.ExecutionTaskID != "" {
		t.Fatal(before)
	}
	recordSettlementSnapshot(t, h, settlementSnapshot(candidate))
	after := deliveryDutyForTest(t, h, issue.IssueID, source)
	if after.Status != "preparing" || after.Revision != before.Revision+1 || after.ExecutionTaskID != "" {
		t.Fatal("a merged observation granted an unbound execution", after)
	}
}

func TestDeliveryMergeSettlementRecoversOriginalCompletedExecutionAfterUnknownProof(t *testing.T) {
	for _, failure := range []string{"lineage_unknown", "provider_timeout"} {
		t.Run(failure, func(t *testing.T) {
			h, run, authority := mergeSettlementFixture(t)
			original := deliveryDutyForTest(t, h, authority.IssueID, authority.SourceTaskID)
			snapshot := settlementSnapshot(authority.Candidate)
			recordSettlementSnapshot(t, h, snapshot)
			if op := h.runAs(run, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Completed the original exact reviewed merge; no deployment"}}, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if h.task(run.TaskID).Lifecycle != protocol.TaskCompleted {
				t.Fatal(h.task(run.TaskID))
			}
			tasks, runs := h.count("tasks"), h.count("runs")
			refreshSettlement(t, h, authority)
			if failure == "lineage_unknown" {
				unknown := snapshot
				unknown.LineageErrorKind = "timeout"
				recordSettlementSnapshot(t, h, unknown)
			} else {
				query, err := h.app.ClaimServerDelivery(t.Context())
				if err != nil || query == nil {
					t.Fatal(query, err)
				}
				if err = h.app.RecordServerDelivery(t.Context(), query.ID, nil, "timeout"); err != nil {
					t.Fatal(err)
				}
			}
			if duty := deliveryDutyForTest(t, h, authority.IssueID, authority.SourceTaskID); !reflect.DeepEqual(duty, original) {
				t.Fatal("temporary unknown irreversibly expired the original binding", duty)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				view, err := views.DeliveryExecutionAuthority(t.Context(), tx.Q(), run.TaskID)
				if err != nil {
					return err
				}
				if view.Valid || view.InvalidReason != "provider_observation_unavailable" || view.Provider == nil {
					t.Fatal("unknown proof became worker authority", view)
				}
				if failure == "lineage_unknown" && view.Provider.LineageErrorKind != "timeout" {
					t.Fatal("worker lost the actual lineage failure", view.Provider)
				}
				if failure == "provider_timeout" && view.Provider.ErrorKind != "timeout" {
					t.Fatal("worker lost the actual provider failure", view.Provider)
				}
				current, err := deliveryTaskCurrent(t.Context(), tx.Q(), run.TaskID)
				if err == nil && current {
					t.Fatal("unknown proof permitted execution")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			blocked := updateDeliveryForTest(t, h, authority.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: authority.SourceTaskID, Action: "complete", IssueControlPayload: protocol.IssueControlPayload{Reason: "Unknown ancestry must not be interpreted as a successful settlement"}})
			if blocked.Status != protocol.OpFailed || (blocked.Error.Code != protocol.CodeStaleRevision && blocked.Error.Code != protocol.CodeInvalidTransition) {
				t.Fatal(blocked)
			}
			refreshSettlement(t, h, authority)
			snapshot.TargetSHA = strings.Repeat("f", 40)
			recordSettlementSnapshot(t, h, snapshot)
			if duty := deliveryDutyForTest(t, h, authority.IssueID, authority.SourceTaskID); !reflect.DeepEqual(duty, original) {
				t.Fatal("restored proof changed original authorization", duty)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				view, err := views.DeliveryExecutionAuthority(t.Context(), tx.Q(), run.TaskID)
				if err == nil && (!view.Valid || view.Provider.LineageErrorKind != "" || view.Provider.ErrorKind != "" || !view.Provider.TargetContainsMerge) {
					t.Fatal("bounded read did not restore original authority", view)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			complete := updateDeliveryForTest(t, h, authority.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: authority.SourceTaskID, Action: "complete", IssueControlPayload: protocol.IssueControlPayload{Reason: "A bounded server read restored the original merge proof; settle its existing execution"}})
			if complete.Status != protocol.OpSucceeded {
				t.Fatal(complete)
			}
			duty := deliveryDutyForTest(t, h, authority.IssueID, authority.SourceTaskID)
			if duty.Status != "complete" || duty.ExecutionTaskID != run.TaskID || !reflect.DeepEqual(duty.Authorization, original.Authorization) || h.count("tasks") != tasks || h.count("runs") != runs {
				t.Fatal("recovery replaced or repeated the original work", duty)
			}
		})
	}
}
