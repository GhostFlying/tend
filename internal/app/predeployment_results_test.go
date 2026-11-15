package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
)

// All Task/Run/approval/result transitions use commands. Provider and installer
// observations are doubles at their external boundary, never fake completions.
func predeploymentFixture(t *testing.T) (*harness, *protocol.RunnerRuntime, protocol.Issue, protocol.Assignment, protocol.IssueDeliveryCandidate, protocol.ArtifactPayload) {
	t.Helper()
	h, r, i, as, old := liveReviewedDeliveryFixture(t)
	i.Plan.PRPolicy.Deployment = &protocol.IssueDeploymentScope{ServerInstance: "server", HostIDs: []string{"linux-1"}}
	i = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, i, "update", protocol.UpdateIssuePayload{Plan: i.Plan, ExpectedAssignmentEpoch: i.AssignmentEpoch}))
	old.Deploy = true
	old.Deployment = &protocol.IssueDeliveryDeployment{ServerInstance: "server", HostIDs: []string{"linux-1"}}
	old.DeploymentScope = "Install the current reviewed source on the retained server and host"
	prepareLiveDeliveryForTest(t, h, i.IssueID, as.TaskID, old)
	bindSelfCandidateForTest(t, h, i.IssueID, as.TaskID)
	merged := old
	merged.TargetSHA = strings.Repeat("c", 40)
	observeDeliveryForTest(t, h, i.IssueID, merged, "merged", merged.TargetSHA)
	// A failed main check has no installation. Its necessary correction is a new PR.
	i = canonicalTaskIssue(t, h, i.IssueID)
	i.Plan.Deliveries = append(i.Plan.Deliveries, protocol.IssueDelivery{DeliveryID: "next", Provider: "github", Repository: "example/repo", Number: 18, URL: "https://example.invalid/repo/pull/18"})
	i = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, i, "update", protocol.UpdateIssuePayload{Plan: i.Plan, ExpectedAssignmentEpoch: i.AssignmentEpoch}))
	next := old
	next.DeliveryID = "next"
	next.URL = i.Plan.Deliveries[1].URL
	next.HeadSHA = strings.Repeat("d", 40)
	next.BaseSHA = merged.TargetSHA
	next.TargetSHA = merged.TargetSHA
	observeDeliveryForTest(t, h, i.IssueID, next, "open", "")
	artifact := testArtifact(h.task(as.TaskID).WorkspaceName)
	artifact.Mounts[0].CommitSHA = next.HeadSHA
	artifact.Report = &protocol.ResultReport{Body: "The new candidate is checked; no deployment has started.", Predeployment: &protocol.PredeploymentResult{DeliveryID: next.DeliveryID, HeadSHA: next.HeadSHA, BaseSHA: next.BaseSHA}}
	return h, r, i, as, next, artifact
}

func acceptStageForTest(t *testing.T, h *harness, r *protocol.RunnerRuntime, task string) {
	t.Helper()
	as, ok := h.claimInputRun(r)
	if !ok || as.TaskID != task || as.Role != protocol.RoleAccept {
		t.Fatal("independent acceptance unavailable", as)
	}
	if op := h.submitVerdict(as.RunID, protocol.VerdictPassed); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	resource := h.resourceForTask(task)
	if !h.releaseResource(task, resource.WorkspaceID) {
		t.Fatal("resource was not released")
	}
}

func reviewNextPredeploymentForTest(t *testing.T, h *harness, r *protocol.RunnerRuntime, i protocol.Issue, task string, next protocol.IssueDeliveryCandidate) {
	t.Helper()
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		row, err := store.GetDeliveryObservation(t.Context(), tx.Q(), i.IssueID, next.DeliveryID)
		if err != nil {
			return err
		}
		row.Observation.CI = &protocol.DeliveryCI{HeadSHA: next.HeadSHA, State: "success", Checks: []protocol.DeliveryCheck{{Name: "required", State: "completed", Conclusion: "success"}}}
		return putDeliveryObservationForTest(tx, t.Context(), row)
	}); err != nil {
		t.Fatal(err)
	}
	sharedReviewResult(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: next.DeliveryID, TaskID: task}))
	i = canonicalTaskIssue(t, h, i.IssueID)
	spec := testSpec
	spec.Delivery = ""
	spec.Deliverables = []string{domain.DeliveryReport}
	spec.Workspace = domain.Workspace{Mode: domain.WorkspaceTemporary}
	spec.ReviewDeliveryID = next.DeliveryID
	spec.AgentSelection = &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "reviewer", AgentID: "linux-1/beta"}}
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: i.IssueID}, canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Title: "Review the corrected candidate", Spec: spec, IndependentAcceptance: &protocol.IndependentAcceptanceRequest{Required: false, Reason: "Independent reviewer reports once"}}, rev(i.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var created taskResult
	if err := json.Unmarshal(op.Result, &created); err != nil {
		t.Fatal(err)
	}
	review := sharedReviewResult(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: next.DeliveryID, ReviewerTaskID: created.TaskID}))
	executeSharedReviewForTest(t, h, r, review, created.TaskID, "passed")
	sharedReviewResult(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: next.DeliveryID}))
}

func TestFault_PredeploymentResultContinuesMergedDeliveryWithoutInventingInstallation(t *testing.T) {
	h, r, i, as, next, stage := predeploymentFixture(t)
	original := deliveryDutyForTest(t, h, i.IssueID, as.TaskID)
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, stage, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal("stage blocked by absent upgrade", op)
	}
	if d := deliveryDutyForTest(t, h, i.IssueID, as.TaskID); !reflect.DeepEqual(d, original) {
		t.Fatal("stage settled or changed the frozen delivery", d)
	}
	acceptStageForTest(t, h, r, as.TaskID)
	if h.task(as.TaskID).Lifecycle != protocol.TaskCompleted {
		t.Fatal("stage was not independently accepted")
	}
	if op := updateDeliveryForTest(t, h, i.IssueID, protocol.UpdateIssueDeliveryPayload{Action: "complete", TaskID: as.TaskID, IssueControlPayload: protocol.IssueControlPayload{Reason: "A candidate is not an installation"}}); op.Status == protocol.OpSucceeded {
		t.Fatal("stage completed deployment", op)
	}
	if op := continueTaskForTest(t, h, as.TaskID, "Continue the same delivery after its checked stage"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.count("issue_delivery_execution_history") != 0 || !reflect.DeepEqual(original, deliveryDutyForTest(t, h, i.IssueID, as.TaskID)) {
		t.Fatal("continuation discarded the old binding")
	}
	current, ok := h.claimInputRun(r)
	if !ok || current.TaskID != as.TaskID || current.RunID == as.RunID {
		t.Fatal(current)
	}
	if op := h.runAs(current, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// Missing the new independent whole-PR review still rejects and rolls back.
	before := h.count("issue_delivery_execution_history")
	if op := updateDeliveryForTest(t, h, i.IssueID, protocol.UpdateIssueDeliveryPayload{Action: "prepare", TaskID: as.TaskID, Candidate: &next, ExpectedTaskControlRevision: rev(h.task(as.TaskID).ControlRevision), IssueControlPayload: protocol.IssueControlPayload{Reason: "New candidate needs its own review"}}); op.Status == protocol.OpSucceeded || h.count("issue_delivery_execution_history") != before {
		t.Fatal("unreviewed replacement admitted", op)
	}
	reviewNextPredeploymentForTest(t, h, r, i, as.TaskID, next)
	downgrade := next
	downgrade.Deploy, downgrade.Deployment = false, nil
	op, err := h.app.Execute(t.Context(), owner, h.env("issues.update_delivery", protocol.Target{Type: "issue", ID: i.IssueID}, protocol.UpdateIssueDeliveryPayload{Action: "prepare", TaskID: as.TaskID, Candidate: &downgrade, ExpectedTaskControlRevision: rev(h.task(as.TaskID).ControlRevision), IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Reason: "Do not discard remaining deployment"}}, rev(canonicalTaskIssue(t, h, i.IssueID).Revision)), UpdateIssueDelivery{})
	if err == nil {
		err = op.Error
	}
	if protocol.CodeOf(err) != protocol.CodeForbidden || h.count("issue_delivery_execution_history") != 0 {
		t.Fatal("deployment scope was silently downgraded", op, err)
	}
	prepareLiveDeliveryForTest(t, h, i.IssueID, as.TaskID, next)
	if h.count("issue_delivery_execution_history") != 1 {
		t.Fatal("old merged binding not archived")
	}
	bindSelfCandidateForTest(t, h, i.IssueID, as.TaskID)
	final := testArtifact(h.task(as.TaskID).WorkspaceName)
	final.Mounts[0].BaseSHA = next.BaseSHA
	final.Mounts[0].CommitSHA = next.HeadSHA
	final.Report = &protocol.ResultReport{Body: "Final deployment requires its actual upgrade"}
	expectWorkCode(t, h.runAs(current, protocol.OpRunsSubmitArtifact, final, SubmitArtifact{}), protocol.CodeInvalidRequest)
	final.Report.Evidence = []string{"upgrade:invented"}
	if op := h.runAs(current, protocol.OpRunsSubmitArtifact, final, SubmitArtifact{}); op.Status == protocol.OpSucceeded {
		t.Fatal("invented upgrade accepted")
	}
	// Installer facts are an explicit external double, matching the real target.
	hash := strings.Repeat("e", 64)
	merge := strings.Repeat("f", 40)
	next.TargetSHA = merge
	observeDeliveryForTest(t, h, i.IssueID, next, "merged", merge)
	batch := store.UpgradeRow{DeliveryTaskID: as.TaskID, View: protocol.UpgradeView{UpgradeID: "actual-next-install", ReleaseID: "next-release", SourceCommit: merge, Revision: 22, Status: "completed", Phase: "completed", CreatedAt: h.clock.Now(), UpdatedAt: h.clock.Now(), Hosts: []protocol.UpgradeHostView{{HostID: "linux-1", Status: "skipped"}}}, Manifest: release.Manifest{ReleaseID: "next-release"}, ManifestSHA256: hash, ServerHash: hash, ServerInstance: "server", ServerProcess: "new-process", ServerHealthy: 2, Hosts: []store.UpgradeHost{{HostID: "linux-1", PreviousHash: hash, TargetHash: hash}}}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.PutUpgrade(t.Context(), batch) }); err != nil {
		t.Fatal(err)
	}
	final.Report.Evidence = []string{"upgrade:" + batch.View.UpgradeID}
	if op := h.runAs(current, protocol.OpRunsSubmitArtifact, final, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	acceptStageForTest(t, h, r, as.TaskID)
	if op := updateDeliveryForTest(t, h, i.IssueID, protocol.UpdateIssueDeliveryPayload{Action: "complete", TaskID: as.TaskID, UpgradeID: batch.View.UpgradeID, IssueControlPayload: protocol.IssueControlPayload{Reason: "Settle only the final actual independently verified installation"}}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.count("tasks") < 1 || h.count("issue_delivery_execution_history") != 1 || deliveryDutyForTest(t, h, i.IssueID, as.TaskID).Status != "complete" {
		t.Fatal("delivery history or final settlement lost")
	}
}

func TestPredeploymentResultRejectsMismatchedOrUncertainExecution(t *testing.T) {
	for _, scenario := range []string{"candidate", "mount", "upgrade-claim", "unknown", "expired", "old-run", "unattached-upgrade"} {
		t.Run(scenario, func(t *testing.T) {
			h, _, _, as, _, stage := predeploymentFixture(t)
			switch scenario {
			case "candidate":
				stage.Report.Predeployment.HeadSHA = strings.Repeat("e", 40)
			case "mount":
				stage.Mounts[0].CommitSHA = strings.Repeat("e", 40)
			case "upgrade-claim":
				stage.Report.Evidence = []string{"upgrade:invented"}
			case "expired":
				h.clock.Advance(2 * h.app.LeaseTTL())
			case "old-run":
				if op := h.runAs(as, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "Stop the original execution"}, Release{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			case "unknown":
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					_, err := tx.Exec(t.Context(), `INSERT INTO manager_native_requests(request_id,run_id,method,payload_json,status,created_at) VALUES(?,?,'turn','{}','unknown',?)`, h.ids.New(protocol.PrefixRequest), as.RunID, h.clock.Now().UnixMilli())
					return err
				}); err != nil {
					t.Fatal(err)
				}
			case "unattached-upgrade":
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					return tx.PutUpgrade(t.Context(), store.UpgradeRow{DeliveryTaskID: as.TaskID, View: protocol.UpgradeView{UpgradeID: "unsettled-install", Status: "attention", Phase: "server_reconcile", CreatedAt: h.clock.Now(), UpdatedAt: h.clock.Now()}})
				}); err != nil {
					t.Fatal(err)
				}
			}
			artifacts := h.count("artifacts")
			op, err := h.app.Execute(t.Context(), runPrincipal(as.RunID, as.Role), h.env(protocol.OpRunsSubmitArtifact, runTarget(as.RunID), stage, nil), SubmitArtifact{})
			if err == nil {
				err = op.Error
			}
			want := protocol.CodeInvalidRequest
			switch scenario {
			case "candidate":
				want = protocol.CodeStaleRevision
			case "expired", "old-run":
				want = protocol.CodeForbidden
			case "unknown", "unattached-upgrade":
				want = protocol.CodeUnknownOutcome
			}
			if protocol.CodeOf(err) != want || h.count("artifacts") != artifacts {
				t.Fatal("invalid stage admitted or wrong failure", scenario, op, err)
			}
		})
	}
}

func TestIssueOwnerPredeploymentResultRetainsActualProducer(t *testing.T) {
	h, i, producer, candidate, _ := ownerBoundDeliveryFixture(t, true)
	artifact := testArtifact(h.task(producer.TaskID).WorkspaceName)
	artifact.Mounts[0].BaseSHA = candidate.BaseSHA
	artifact.Mounts[0].CommitSHA = candidate.HeadSHA
	artifact.Report = &protocol.ResultReport{Body: "The actual owner records candidate checks, not an installation.", Predeployment: &protocol.PredeploymentResult{DeliveryID: candidate.DeliveryID, HeadSHA: candidate.HeadSHA, BaseSHA: candidate.BaseSHA}}
	original := deliveryDutyForTest(t, h, i.IssueID, producer.TaskID)
	if op := updateDeliveryForTest(t, h, i.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: producer.TaskID, RunID: producer.RunID, Action: "submit_result", Result: &artifact, IssueControlPayload: protocol.IssueControlPayload{Reason: "Keep the actual owner producer and its existing deployment duty"}}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	d := deliveryDutyForTest(t, h, i.IssueID, producer.TaskID)
	if d.Status != "executing" || d.Revision != original.Revision || d.ArtifactID == original.ArtifactID || !reflect.DeepEqual(d.Candidate, original.Candidate) {
		t.Fatal("stage rewrote the frozen binding", d)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		a, err := store.GetArtifact(t.Context(), tx.Q(), d.ArtifactID)
		if err != nil {
			return err
		}
		if a.RunID != producer.RunID || a.TaskID != producer.TaskID {
			t.Fatal("stage invented a producer", a)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPredeploymentAcceptanceRetainsInstallationDrainBoundary(t *testing.T) {
	h, r, i, as, _, stage := predeploymentFixture(t)
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, stage, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	batch := store.UpgradeRow{DeliveryTaskID: as.TaskID, View: protocol.UpgradeView{UpgradeID: "concurrent-real-install", Status: "attention", Phase: "server_reconcile", CreatedAt: h.clock.Now(), UpdatedAt: h.clock.Now()}}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.PutUpgrade(t.Context(), batch) }); err != nil {
		t.Fatal(err)
	}
	if claimed, ok := h.claimInputRun(r); ok {
		t.Fatal("candidate acceptance occupied an installation target", claimed)
	}
	batch.View.Status, batch.View.Phase = "completed", "completed"
	batch.View.Revision++
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.PutUpgrade(t.Context(), batch) }); err != nil {
		t.Fatal(err)
	}
	acceptStageForTest(t, h, r, as.TaskID)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		v, err := store.DeliveryDeploymentSettlement(t.Context(), tx.Q(), as.TaskID, h.clock.Now().UnixMilli())
		if protocol.CodeOf(err) != protocol.CodeInvalidTransition || v == nil || v.Eligible || v.InvalidReason != "predeployment_result" {
			t.Fatal("an independently checked stage became installation evidence", v, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if deliveryDutyForTest(t, h, i.IssueID, as.TaskID).Status != "executing" {
		t.Fatal("stage settled deployment")
	}
}
