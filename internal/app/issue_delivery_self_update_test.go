package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

// The worker's Git operation is real and local. Only process replacement and
// fleet observations are simulated; the normal task, owner answer, upgrade
// admission, grants, health reconciliation and delivery commands are real.
func TestDeliveryApprovedWorkerMergesAndSelfUpdatesThroughControlledUpgrade(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "behavior.txt"), []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "behavior.txt")
	git("commit", "-qm", "fixture base")
	base := git("rev-parse", "HEAD")
	git("checkout", "-qb", "candidate")
	if err := os.WriteFile(filepath.Join(dir, "behavior.txt"), []byte("fixed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("commit", "-qam", "fixture fix")
	head := git("rev-parse", "HEAD")
	git("checkout", "-q", "main")
	h, upgrade := upgradeHarness(t)
	_, i, original, candidate := deliveryFixtureOnHarness(t, h)
	candidate.HeadSHA, candidate.BaseSHA = head, base
	candidate.Deploy = true
	candidate.DeploymentScope = "controlled self-update of server and complete isolated fleet; verify original behavior"
	candidate.Deployment = &protocol.IssueDeliveryDeployment{ServerInstance: "server", HostIDs: []string{"linux-2", "linux-1"}}
	observeDeliveryForTest(t, h, i.IssueID, candidate, "open", "")
	duty := prepareDeliveryForTest(t, h, i.IssueID, original, candidate)
	expectWorkCode(t, createDeliveryExecutionForTest(t, h, i.IssueID, duty), protocol.CodeForbidden)
	if git("rev-parse", "main") != base || h.count("upgrades") != 0 {
		t.Fatal("execution before approval")
	}
	answerDeliveryForTest(t, h, duty, "approve", "")
	op := createDeliveryExecutionForTest(t, h, i.IssueID, duty)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var created taskResult
	if err := json.Unmarshal(op.Result, &created); err != nil {
		t.Fatal(err)
	}
	worker, ok, claimEnv := h.claim("linux-1")
	if !ok || worker.TaskID != created.TaskID {
		t.Fatal(worker, claimEnv)
	}
	if op := h.runAs(worker, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	git("merge", "--ff-only", head)
	// A reconciled replay does not create another merge commit.
	git("merge", "--ff-only", head)
	merged := git("rev-parse", "main")
	if merged != head || git("rev-list", "--count", "main") != "2" {
		t.Fatal("unexpected merge identity")
	}
	observeDeliveryForTest(t, h, i.IssueID, candidate, "merged", merged)
	body, err := os.ReadFile(filepath.Join(dir, "behavior.txt"))
	if err != nil || string(body) != "fixed\n" {
		t.Fatal("original behavior readback", err)
	}
	sum := sha256.Sum256(body)
	upgrade.ServerHash = hex.EncodeToString(sum[:])
	upgrade.Manifest.ReleaseID = "self-update-" + merged
	for index := range upgrade.Manifest.Artifacts {
		a := &upgrade.Manifest.Artifacts[index]
		if a.Role == "server" {
			a.SHA256 = upgrade.ServerHash
		}
	}
	env := h.env(upgrade.Operation(), protocol.Target{Type: "upgrade", ID: "delivery-self-update"}, map[string]string{"release_id": upgrade.Manifest.ReleaseID}, rev(0))
	system := protocol.Principal{Kind: protocol.PrincipalSystem}
	for range 2 {
		if op := h.exec(system, env, upgrade); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	if h.count("upgrades") != 1 {
		t.Fatal("upgrade request replay created another batch")
	}
	// The deployment worker is itself on a target. Persist its started batch
	// before releasing the run, so rolling maintenance can drain this host.
	artifact := testArtifact(h.task(created.TaskID).WorkspaceName)
	artifact.Mounts[0].CommitSHA = merged
	artifact.Report = &protocol.ResultReport{Body: "Merged " + merged + "; verified fixed source behavior; controlled self-update delivery-self-update started; server hash " + upgrade.ServerHash + "; complete host set linux-1,linux-2; installed readbacks pending independent acceptance", Evidence: []string{"upgrade:delivery-self-update"}}
	missing := artifact
	missing.Report = &protocol.ResultReport{Body: artifact.Report.Body}
	expectWorkCode(t, h.runAs(worker, protocol.OpRunsSubmitArtifact, missing, SubmitArtifact{}), protocol.CodeInvalidRequest)
	duplicate := artifact
	duplicate.Report = &protocol.ResultReport{Body: artifact.Report.Body, Evidence: []string{"upgrade:", "upgrade:delivery-self-update"}}
	expectWorkCode(t, h.runAs(worker, protocol.OpRunsSubmitArtifact, duplicate, SubmitArtifact{}), protocol.CodeInvalidRequest)
	if op := h.runAs(worker, protocol.OpRunsSubmitArtifact, artifact, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.run(worker.RunID).ObservedState != protocol.RunFinished || h.task(created.TaskID).Lifecycle != protocol.TaskAwaitingAcceptance || deliveryDutyForTest(t, h, i.IssueID, original).Status != "executing" {
		t.Fatal("started batch fabricated deployment success or retained its implementation run")
	}
	// Acceptance must not race the updater's first tick while controls are
	// still normal, or it would reacquire this target's drain lease.
	if early, ok, _ := h.claim("linux-1"); ok {
		t.Fatal("premature acceptance blocks its own update", early)
	}
	pausedBatch := upgradeRead(h)
	pausedBatch.View.Status = "attention"
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.PutUpgrade(t.Context(), pausedBatch) }); err != nil {
		t.Fatal(err)
	}
	if early, ok, _ := h.claim("linux-1"); ok {
		t.Fatal("uncertain batch admitted acceptance", early)
	}
	pausedBatch.View.Status = "cancelled"
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.PutUpgrade(t.Context(), pausedBatch) }); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		ready, err := deliveryAcceptanceReady(t.Context(), tx.Q(), created.TaskID)
		if err == nil && !ready {
			t.Fatal("terminal failure cannot be independently reviewed")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	pausedBatch.View.Status = "running"
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.PutUpgrade(t.Context(), pausedBatch) }); err != nil {
		t.Fatal(err)
	}
	upgradeTick(h, ServerObservation{MainDrained: true, BinarySHA256: strings.Repeat("a", 64)})
	for _, host := range []string{"linux-1", "linux-2"} {
		upgradeAck(h, host, protocol.RunnerMaintenance)
	}
	binding := protocol.UpgradeBinding{UpgradeID: "delivery-self-update", ReleaseID: upgrade.Manifest.ReleaseID, ManifestSHA256: upgrade.ManifestSHA256, ArtifactSHA256: upgrade.ServerHash, InstanceID: "server", ProcessInstanceID: "new-self-server"}
	grant, err := h.app.ServerUpgradeAuthorize(t.Context(), binding)
	if err != nil || !grant.Allowed {
		t.Fatal(grant, err)
	}
	replay, err := h.app.ServerUpgradeAuthorize(t.Context(), binding)
	if err != nil || !replay.ExpiresAt.Equal(grant.ExpiresAt) {
		t.Fatal("grant replay extended authority", err)
	}
	// Unknown/wrong installed process cannot establish success or start again.
	upgradeTick(h, ServerObservation{MainDrained: true, Healthy: true, ProcessInstanceID: "unknown", BinarySHA256: strings.Repeat("f", 64)})
	if upgradeRead(h).ServerHealthy != 0 || h.count("upgrades") != 1 {
		t.Fatal("unknown replacement was treated as success/retry")
	}
	current := ServerObservation{InstanceID: "server", ProcessInstanceID: binding.ProcessInstanceID, BinarySHA256: upgrade.ServerHash, Healthy: true, MainDrained: true}
	upgradeTick(h, current)
	h.clock.Advance(5 * time.Second)
	upgradeTick(h, current)
	for _, host := range []string{"linux-1", "linux-2"} {
		upgradeAck(h, host, protocol.RunnerNormal)
	}
	// Execute both runner installations through the real grant, hello, health,
	// committed receipt and normal acknowledgement transitions. Neither host
	// is skipped, including the host that ran the implementation above.
	for _, host := range upgradeRead(h).View.Hosts {
		upgradeTick(h, ServerObservation{})
		upgradeAck(h, host.HostID, protocol.RunnerReady)
		upgradeTick(h, ServerObservation{})
		upgradeAck(h, host.HostID, protocol.RunnerReady)
		ctl := upgradeCtl(h, host.HostID)
		runnerBinding := protocol.UpgradeBinding{UpgradeID: binding.UpgradeID, ReleaseID: binding.ReleaseID, ManifestSHA256: binding.ManifestSHA256, ArtifactSHA256: strings.Repeat("b", 64), InstanceID: host.HostID, ProcessInstanceID: "new-" + host.HostID}
		op := h.exec(runner(host.HostID), h.env("runner.upgrade_authorize", protocol.Target{Type: "host", ID: host.HostID}, runnerBinding, rev(ctl.Revision)), AuthorizeRunnerUpgrade{})
		var granted protocol.UpgradeGrant
		if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &granted) != nil || !granted.Allowed {
			t.Fatal(op, granted)
		}
		next := testRuntime(runnerBinding.ProcessInstanceID)
		next.JournalVersion = upgrade.Manifest.Storage.RunnerJournal.Max
		next.Capabilities = append(next.Capabilities, capabilityUpdater)
		next.Build.BinarySHA256 = runnerBinding.ArtifactSHA256
		next.Build.Version = upgrade.Manifest.ReleaseID
		next.Mode = protocol.RunnerReady
		next.UpgradeID = runnerBinding.UpgradeID
		next.AppliedControlRevision = ctl.Revision
		if op := h.versionedHello(host.HostID, next); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		upgradeTick(h, ServerObservation{})
		h.clock.Advance(8 * time.Second)
		upgradeAck(h, host.HostID, protocol.RunnerReady)
		upgradeTick(h, ServerObservation{})
		upgradeCommitAck(h, runnerBinding)
		upgradeTick(h, ServerObservation{})
		upgradeAck(h, host.HostID, protocol.RunnerNormal)
		upgradeTick(h, ServerObservation{})
		upgradeAck(h, host.HostID, protocol.RunnerNormal)
		upgradeTick(h, ServerObservation{})
	}
	installed := upgradeTick(h, ServerObservation{})
	if installed.Status != "completed" || installed.ReleaseID != upgrade.Manifest.ReleaseID {
		t.Fatal(installed)
	}
	for _, host := range installed.Hosts {
		if host.Status != "completed" || host.CurrentVersion != upgrade.Manifest.ReleaseID {
			t.Fatal("host installation not read back", host)
		}
	}
	health, err := h.app.ServerUpgradeHealth(t.Context(), binding)
	if err != nil || !health.Healthy || !health.ServerConfirmed {
		t.Fatal(health, err)
	}
	reviewer, ok, claimEnv := h.claim("linux-1")
	if !ok || reviewer.TaskID != created.TaskID || reviewer.Role != protocol.RoleAccept {
		t.Fatal(reviewer, claimEnv)
	}
	var reviewed protocol.ArtifactPayload
	if err := json.Unmarshal(reviewer.Artifact, &reviewed); err != nil || reviewed.Report == nil || !strings.Contains(reviewed.Report.Body, merged) || !strings.Contains(reviewed.Report.Body, "delivery-self-update") {
		t.Fatal("independent acceptance did not receive its required execution evidence", err)
	}
	// Independent role reads the actual batch and original Issue before verdict.
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		originalView, err := views.CanonicalIssue(t.Context(), tx.Q(), i.IssueID, 0)
		if err != nil {
			return err
		}
		if originalView.IssueID != i.IssueID || len(originalView.DeliveryDuties) != 1 || originalView.DeliveryDuties[0].Status != "executing" {
			t.Fatal(originalView)
		}
		proof, err := deliveryUpgradeEvidence(t.Context(), &Exec{Now: h.clock.Now()}, tx.Q(), *candidate.Deployment, "delivery-self-update", h.task(created.TaskID).CreatedAt)
		if err != nil {
			return err
		}
		if proof.ServerSHA256 != upgrade.ServerHash || proof.ServerProcessInstanceID != binding.ProcessInstanceID || len(proof.Hosts) != 2 {
			t.Fatal(proof)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if op := h.submitVerdict(reviewer.RunID, protocol.VerdictPassed); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	request := protocol.UpdateIssueDeliveryPayload{TaskID: original, Action: "complete", UpgradeID: "delivery-self-update", IssueControlPayload: protocol.IssueControlPayload{Reason: "Independently verified actual merge, controlled self-update, server/host installation and original behavior readbacks"}}
	i = canonicalTaskIssue(t, h, i.IssueID)
	request.ExpectedAssignmentEpoch = i.AssignmentEpoch
	completion := h.env("issues.update_delivery", protocol.Target{Type: "issue", ID: i.IssueID}, request, rev(i.Revision))
	for range 2 {
		if op := h.exec(owner, completion, UpdateIssueDelivery{}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	settled := deliveryDutyForTest(t, h, i.IssueID, original)
	if settled.Status != "complete" || len(settled.Evidence) != 3 || settled.Evidence[2].Upgrade == nil || h.task(original).Lifecycle != protocol.TaskCompleted || h.count("upgrades") != 1 {
		t.Fatal(settled)
	}
}

func TestDeliveryDeploymentCannotSettleFromUnrelatedOrUnverifiedUpgrade(t *testing.T) {
	h, i, original, c := deliveryFixture(t)
	c.Deploy = true
	c.Deployment = &protocol.IssueDeliveryDeployment{ServerInstance: "server", HostIDs: []string{"linux-1"}}
	c.DeploymentScope = "controlled server update and verified retained host"
	duty := prepareDeliveryForTest(t, h, i.IssueID, original, c)
	answerDeliveryForTest(t, h, duty, "approve", "")
	op := createDeliveryExecutionForTest(t, h, i.IssueID, duty)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var created taskResult
	if err := json.Unmarshal(op.Result, &created); err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("e", 64)
	fixture := func() store.UpgradeRow {
		return store.UpgradeRow{View: protocol.UpgradeView{UpgradeID: "batch", ReleaseID: "release", Revision: 5, Status: "completed", Phase: "completed", CreatedAt: h.clock.Now(), UpdatedAt: h.clock.Now(), Hosts: []protocol.UpgradeHostView{{HostID: "linux-1", Status: "skipped"}}}, Manifest: release.Manifest{ReleaseID: "release"}, ManifestSHA256: hash, ServerHash: hash, ServerInstance: "server", ServerProcess: "installed-server", ServerHealthy: 2, Hosts: []store.UpgradeHost{{HostID: "linux-1", PreviousHash: hash, TargetHash: hash}}}
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.PutUpgrade(t.Context(), fixture()) }); err != nil {
		t.Fatal(err)
	}
	executeAcceptedDeliveryTaskForTest(t, h, created.TaskID, func() protocol.ArtifactPayload {
		return protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Controlled update receipt", Evidence: []string{"upgrade:batch"}}}
	})
	observeDeliveryForTest(t, h, i.IssueID, c, "merged", strings.Repeat("c", 40))
	expectWorkCode(t, updateDeliveryForTest(t, h, i.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: original, Action: "complete", UpgradeID: "unrelated-batch", IssueControlPayload: protocol.IssueControlPayload{Reason: "Try to substitute another installation"}}), protocol.CodeInvalidTransition)
	for _, invalid := range []string{"old-batch", "running", "unknown", "server-instance", "server-health", "server-process", "host-set", "installation", "valid"} {
		t.Run(invalid, func(t *testing.T) {
			batch := fixture()
			switch invalid {
			case "old-batch":
				batch.View.CreatedAt = time.UnixMilli(h.task(created.TaskID).CreatedAt - 1)
			case "running":
				batch.View.Status = "running"
			case "unknown":
				batch.View.Status = "attention"
				batch.View.Reason = "replacement result unknown"
			case "server-instance":
				batch.ServerInstance = "other"
			case "server-health":
				batch.ServerHealthy = 1
			case "server-process":
				batch.ServerProcess = ""
			case "host-set":
				batch.Hosts[0].HostID = "other"
				batch.View.Hosts[0].HostID = "other"
			case "installation":
				batch.View.Hosts[0].Status = "completed"
			}
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.PutUpgrade(t.Context(), batch) }); err != nil {
				t.Fatal(err)
			}
			op := updateDeliveryForTest(t, h, i.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: original, Action: "complete", UpgradeID: "batch", IssueControlPayload: protocol.IssueControlPayload{Reason: "Read back controlled self-update"}})
			if invalid != "valid" {
				if op.Status == protocol.OpSucceeded || deliveryDutyForTest(t, h, i.IssueID, original).Status != "executing" {
					t.Fatal("invalid deployment settled responsibility", op)
				}
				return
			}
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			d := deliveryDutyForTest(t, h, i.IssueID, original)
			if d.Evidence[2].Upgrade.ServerProcessInstanceID != "installed-server" {
				t.Fatal(d)
			}
			// Later observations cannot rewrite the receipt frozen at this endpoint.
			batch.View.Status = "cancelled"
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.PutUpgrade(t.Context(), batch) }); err != nil {
				t.Fatal(err)
			}
			if d = deliveryDutyForTest(t, h, i.IssueID, original); d.Status != "complete" || d.Evidence[2].Upgrade.Revision != 5 {
				t.Fatal(d)
			}
		})
	}
}

func TestDeliveryExecutionReportIsRequiredAndVisibleToIndependentAcceptance(t *testing.T) {
	h, i, original, c := deliveryFixture(t)
	duty := prepareDeliveryForTest(t, h, i.IssueID, original, c)
	answerDeliveryForTest(t, h, duty, "approve", "")
	i = canonicalTaskIssue(t, h, i.IssueID)
	p := protocol.CreateIssueTaskPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Title: "Execute reviewed integration", Spec: protocol.OwnerTaskSpec{Goal: "Execute approved integration and retain evidence", Acceptance: []string{"Verify original candidate and result"}, Delivery: "commit", Workspace: protocol.OwnerTaskWorkspace{Mounts: []protocol.OwnerTaskMount{{Repo: "tend"}}}}, DeliveryAuthorization: &protocol.IssueDeliveryAuthorization{TaskID: original, Revision: duty.Revision}}
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: i.IssueID}, p, rev(i.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var created taskResult
	if err := json.Unmarshal(op.Result, &created); err != nil {
		t.Fatal(err)
	}
	worker, ok, claimEnv := h.claim("linux-1")
	if !ok || worker.TaskID != created.TaskID {
		t.Fatal(worker, claimEnv)
	}
	h.runAs(worker, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	artifact := testArtifact(h.task(created.TaskID).WorkspaceName)
	expectWorkCode(t, h.runAs(worker, protocol.OpRunsSubmitArtifact, artifact, SubmitArtifact{}), protocol.CodeInvalidRequest)
	artifact.Report = &protocol.ResultReport{Body: "Required integration evidence for the approved candidate"}
	if op := h.runAs(worker, protocol.OpRunsSubmitArtifact, artifact, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	reviewer, ok, claimEnv := h.claim("linux-1")
	if !ok || reviewer.TaskID != created.TaskID || reviewer.Role != protocol.RoleAccept {
		t.Fatal(reviewer, claimEnv)
	}
	var actual protocol.ArtifactPayload
	if err := json.Unmarshal(reviewer.Artifact, &actual); err != nil || actual.Report == nil || actual.Report.Body != artifact.Report.Body {
		t.Fatal("required evidence absent from independent assignment", err)
	}
}
