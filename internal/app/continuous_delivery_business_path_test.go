package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
)

// The starting boundary is an actually reviewed, self-bound old candidate.
// Provider facts and process/installer observations are external doubles. The
// new repository commits, result collection, rejection/repair, same Task
// continuation, exact review, Start and every installation transition are real.
func TestFault_ContinuousDeliveryRepairsReviewsInstallsAndSettlesSameTask(t *testing.T) {
	h, issue, initial, command := autoDeploymentLifecycleFixture(t, false)
	var runtime protocol.RunnerRuntime
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		r, err := store.GetHostRuntime(t.Context(), tx.Q(), "linux-1")
		runtime = r.Runtime
		return err
	}); err != nil {
		t.Fatal(err)
	}
	workspace, prepared, stage := predeploymentGitWorkspace(t, initial)
	original := *deliveryDutyForTest(t, h, issue.IssueID, initial.TaskID).Candidate
	advanced := original
	advanced.TargetSHA = stage.Mounts[0].BaseSHA
	observeDeliveryForTest(t, h, issue.IssueID, advanced, "merged", original.MergeSHA)
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	issue.Plan.Deliveries = append(issue.Plan.Deliveries, protocol.IssueDelivery{DeliveryID: "current", Provider: "github", Repository: "example/repo", Number: 18, URL: "https://example.invalid/repo/pull/18"})
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "update", protocol.UpdateIssuePayload{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}))
	next := original
	next.DeliveryID, next.URL = "current", issue.Plan.Deliveries[1].URL
	next.ProviderState, next.MergeSHA = "open", ""
	next.HeadSHA, next.BaseSHA, next.TargetSHA = stage.Mounts[0].CommitSHA, stage.Mounts[0].BaseSHA, stage.Mounts[0].BaseSHA
	observeDeliveryForTest(t, h, issue.IssueID, next, "open", "")
	stage.Report = &protocol.ResultReport{Body: "Actual current-main correction; no installation has started", Predeployment: &protocol.PredeploymentResult{DeliveryID: next.DeliveryID, HeadSHA: next.HeadSHA, BaseSHA: next.BaseSHA}}
	if op := h.runAs(initial, protocol.OpRunsSubmitArtifact, stage, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	reviewer, ok := h.claimInputRun(&runtime)
	if !ok || reviewer.Role != protocol.RoleAccept {
		t.Fatal("stage reviewer unavailable", reviewer)
	}
	if op := h.submitVerdict(reviewer.RunID, protocol.VerdictChangesRequired); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.task(initial.TaskID).CurrentArtifactID != "" {
		t.Fatal("rejected stage retained mutable artifact pointer")
	}
	repair, ok := h.claimInputRun(&runtime)
	if !ok || repair.TaskID != initial.TaskID || repair.Feedback == nil {
		t.Fatal("normal rejected-stage repair blocked after main changed", repair)
	}
	corrected := unchangedInstallationWorkspace(t, workspace, prepared, repair)
	corrected.Report = stage.Report
	if op := h.runAs(repair, protocol.OpRunsSubmitArtifact, corrected, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal("real unchanged stage correction rejected", op)
	}
	acceptStageForTest(t, h, &runtime, initial.TaskID)
	if op := continueTaskForTest(t, h, initial.TaskID, "Continue the accepted actual candidate to its originally authorized installation"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	installation, ok := h.claimInputRun(&runtime)
	if !ok || installation.TaskID != initial.TaskID {
		t.Fatal(installation)
	}
	if op := h.runAs(installation, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	reviewNextPredeploymentForTest(t, h, &runtime, issue, initial.TaskID, next)
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	progress := protocol.UpdateIssueProgressPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Summary: "Current candidate passed its actual independent review", NextStep: "Install once through the original controlled scope"}
	if op := h.exec(owner, h.env("issues.update_progress", protocol.Target{Type: "issue", ID: issue.IssueID}, progress, rev(issue.Revision)), UpdateIssueProgress{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	prepareLiveDeliveryForTest(t, h, issue.IssueID, initial.TaskID, next)
	bindSelfCandidateForTest(t, h, issue.IssueID, initial.TaskID)
	next.TargetSHA = next.HeadSHA
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		row, err := store.GetDeliveryObservation(t.Context(), tx.Q(), issue.IssueID, next.DeliveryID)
		if err != nil {
			return err
		}
		row.Observation.State, row.Observation.MergeSHA = "merged", next.HeadSHA
		row.Observation.TargetSHA, row.Observation.TargetContainsMerge = next.HeadSHA, true
		return putDeliveryObservationForTest(tx, t.Context(), row)
	}); err != nil {
		t.Fatal(err)
	}
	command.ReleaseSet.Commit = next.HeadSHA
	command.ArtifactSources = map[string]release.SourceIdentity{}
	for _, a := range command.Manifest.Artifacts {
		command.ArtifactSources[a.SHA256] = release.SourceIdentity{Commit: next.HeadSHA}
	}
	request := h.env(command.Operation(), protocol.Target{Type: "upgrade", ID: "continuous-install"}, map[string]string{"release_id": command.Manifest.ReleaseID}, rev(0))
	if op := h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, request, command); op.Status != protocol.OpSucceeded {
		t.Fatal("exact reviewed source could not start normal installation", op)
	}
	final := unchangedInstallationWorkspace(t, workspace, prepared, installation)
	final.Report = &protocol.ResultReport{Body: "Actual single controlled installation and exact unchanged repository source", Evidence: []string{"upgrade:continuous-install"}}
	if op := h.runAs(installation, protocol.OpRunsSubmitArtifact, final, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if _, ok := h.claimInputRun(&runtime); ok {
		t.Fatal("final reviewer raced the original installation drain")
	}
	installedRuntime := completeContinuousInstallation(t, h, command, request.Target.ID, next.HeadSHA, runtime)
	accept, ok := h.claimInputRun(&installedRuntime)
	if !ok || accept.TaskID != initial.TaskID || accept.Role != protocol.RoleAccept {
		t.Fatal("installed original Task lost independent final acceptance", accept)
	}
	if op := h.submitVerdict(accept.RunID, protocol.VerdictPassed); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{Action: "complete", TaskID: initial.TaskID, UpgradeID: request.Target.ID, IssueControlPayload: protocol.IssueControlPayload{Reason: "The real current source, single upgrade, committed installations and final independent acceptance converged"}}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	settled := deliveryDutyForTest(t, h, issue.IssueID, initial.TaskID)
	if settled.Status != "complete" || h.task(initial.TaskID).Lifecycle != protocol.TaskCompleted || h.count("upgrades") != 1 || h.count("issue_delivery_execution_history") != 1 {
		t.Fatal("whole delivery did not retain exact single-operation history", settled)
	}
}

func completeContinuousInstallation(t *testing.T, h *harness, c StartUpgrade, id, commit string, retained protocol.RunnerRuntime) protocol.RunnerRuntime {
	t.Helper()
	row := upgradeRead(h)
	if op := h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, h.env("upgrades.authorize_supervisor", protocol.Target{Type: "upgrade", ID: id}, map[string]any{}, rev(row.View.Revision)), AuthorizeServerUpdater{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	selfProof := func(instance, launch, hash string) *protocol.UpdaterSelfObservation {
		proof := selfUpgradeObservation(upgradeRead(h), instance, launch, hash)
		updaterHash := ""
		for _, a := range c.ReleaseSet.Artifacts {
			if a.Role == "updater" {
				updaterHash = a.SHA256
			}
		}
		proof.Process.SHA256, proof.Receipt.Installed.SHA256 = updaterHash, updaterHash
		return proof
	}
	old := ServerObservation{MainDrained: true, BinarySHA256: strings.Repeat("a", 64), ProcessInstanceID: "old-server"}
	old.Updater = selfProof(c.ServerInstance, old.ProcessInstanceID, old.BinarySHA256)
	if view := upgradeTick(h, old); view.Phase != "server_draining" {
		t.Fatal("updater handoff did not preserve the actual old server", view)
	}
	upgradeTick(h, old)
	for _, host := range []string{"linux-1", "linux-2"} {
		upgradeAck(h, host, protocol.RunnerMaintenance)
	}
	binding := protocol.UpgradeBinding{UpgradeID: id, ReleaseID: c.Manifest.ReleaseID, ManifestSHA256: c.ManifestSHA256, ArtifactSHA256: c.ServerHash, InstanceID: c.ServerInstance, ProcessInstanceID: "current-server"}
	if grant, err := h.app.ServerUpgradeAuthorize(t.Context(), binding); err != nil || !grant.Allowed {
		t.Fatal(grant, err)
	}
	current := ServerObservation{InstanceID: binding.InstanceID, ProcessInstanceID: binding.ProcessInstanceID, BinarySHA256: binding.ArtifactSHA256, Commit: commit, Healthy: true, MainDrained: true}
	current.Updater = selfProof(binding.InstanceID, current.ProcessInstanceID, current.BinarySHA256)
	upgradeTick(h, current)
	h.clock.Advance(5 * time.Second)
	upgradeTick(h, current)
	for _, host := range []string{"linux-1", "linux-2"} {
		upgradeAck(h, host, protocol.RunnerNormal)
	}
	var installed protocol.RunnerRuntime
	for _, host := range upgradeRead(h).View.Hosts {
		upgradeTick(h, current)
		upgradeAck(h, host.HostID, protocol.RunnerReady)
		upgradeTick(h, current)
		upgradeAck(h, host.HostID, protocol.RunnerReady)
		grantSelfRunner(t, h, host.HostID, current)
		retainedHost := upgradeAck(h, host.HostID, protocol.RunnerReady)
		retainedHost.Sequence++
		retainedHost.Build.PID = 91
		retainedHost.Updater = selfProof(host.HostID, retainedHost.InstanceID, retainedHost.Build.BinarySHA256)
		if _, err := h.app.RuntimeHeartbeat(t.Context(), runner(host.HostID), protocol.HeartbeatPayload{Runtime: &retainedHost}); err != nil {
			t.Fatal(err)
		}
		upgradeTick(h, current)
		ctl := upgradeCtl(h, host.HostID)
		hash := ""
		for _, a := range c.Manifest.Artifacts {
			if a.Role == "runner" {
				hash = a.SHA256
			}
		}
		b := protocol.UpgradeBinding{UpgradeID: id, ReleaseID: c.Manifest.ReleaseID, ManifestSHA256: c.ManifestSHA256, ArtifactSHA256: hash, InstanceID: host.HostID, ProcessInstanceID: "current-" + host.HostID}
		op := h.exec(runner(host.HostID), h.env("runner.upgrade_authorize", protocol.Target{Type: "host", ID: host.HostID}, b, rev(ctl.Revision)), AuthorizeRunnerUpgrade{})
		var grant protocol.UpgradeGrant
		if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &grant) != nil || !grant.Allowed {
			t.Fatal(op, grant)
		}
		next := *testRuntime(b.ProcessInstanceID)
		if host.HostID == "linux-1" {
			next = retained
			next.InstanceID = b.ProcessInstanceID
			next.Generation++
		}
		next.Updater = selfProof(host.HostID, b.ProcessInstanceID, b.ArtifactSHA256)
		next.Build.PID = 91
		next.Sequence++
		next.JournalVersion = c.Manifest.Storage.RunnerJournal.Max
		next.Capabilities = append(next.Capabilities, capabilityUpdater, protocol.CapabilityCompleteReleaseSet, protocol.CapabilityUpdaterSelfUpdate)
		next.Build.BinarySHA256, next.Build.Version, next.Build.Commit = b.ArtifactSHA256, c.Manifest.ReleaseID, commit
		next.Mode, next.UpgradeID, next.AppliedControlRevision = protocol.RunnerReady, id, ctl.Revision
		if op := h.versionedHello(host.HostID, &next); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		upgradeTick(h, current)
		h.clock.Advance(8 * time.Second)
		upgradeAck(h, host.HostID, protocol.RunnerReady)
		upgradeTick(h, current)
		upgradeCommitAck(h, b)
		upgradeTick(h, current)
		upgradeAck(h, host.HostID, protocol.RunnerNormal)
		upgradeTick(h, current)
		upgradeAck(h, host.HostID, protocol.RunnerNormal)
		upgradeTick(h, current)
		if host.HostID == "linux-1" {
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				r, err := store.GetHostRuntime(t.Context(), tx.Q(), host.HostID)
				installed = r.Runtime
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if done := upgradeTick(h, current); done.Status != "completed" {
		t.Fatal("actual upgrade transitions did not complete", done)
	}
	// Completion clears the original upgrade target metadata under a fresh
	// control revision. The actual final ACK, rather than a stale pre-cleanup
	// runtime snapshot, restores normal claim eligibility.
	installed = upgradeAck(h, "linux-1", protocol.RunnerNormal)
	upgradeAck(h, "linux-2", protocol.RunnerNormal)
	return installed
}
