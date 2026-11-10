package app

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func preparationFixture(t *testing.T, role protocol.RunRole) (*harness, protocol.Assignment, string) {
	t.Helper()
	h, as, issue := observationFixture(t, role)
	hash, manifest := strings.Repeat("a", 64), strings.Repeat("b", 64)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.UpsertHost(t.Context(), store.HostRow{HostID: "linux-2", OS: "linux", Arch: "amd64", Slots: 2}, h.clock.Now().UnixMilli()); err != nil {
			return err
		}
		batch := store.UpgradeRow{View: protocol.UpgradeView{UpgradeID: "installed-batch", ReleaseID: "installed-release", Revision: 7, Status: "completed", Phase: "completed", CreatedAt: h.clock.Now().Add(-time.Minute), UpdatedAt: h.clock.Now()}, ServerInstance: "fixed-service", ServerHash: hash, ManifestSHA256: manifest}
		for _, host := range []string{"linux-1", "linux-2"} {
			rt := testRuntime("current-" + host)
			if host == "linux-1" {
				old, err := store.GetHostRuntime(t.Context(), tx.Q(), host)
				if err != nil {
					return err
				}
				rt = &old.Runtime
			}
			rt.Build = protocol.RunnerBuild{Version: "installed-release", Commit: strings.Repeat("d", 40), BinarySHA256: hash}
			rt.UpdaterReceipt = &protocol.UpdaterReceipt{Binding: protocol.UpgradeBinding{UpgradeID: "installed-batch", InstanceID: host, ProcessInstanceID: "original-" + host, ReleaseID: "installed-release", ManifestSHA256: manifest, ArtifactSHA256: hash}, Phase: "committed", CurrentProcessInstanceID: rt.InstanceID, CurrentSHA256: hash, CommittedConfirmed: true}
			batch.Hosts = append(batch.Hosts, store.UpgradeHost{HostID: host, CandidateInstance: "original-" + host, PreviousHash: hash, TargetHash: hash, Committed: rt.UpdaterReceipt})
			batch.View.Hosts = append(batch.View.Hosts, protocol.UpgradeHostView{HostID: host, Status: "completed", Phase: "completed"})
			if err := tx.PutHostRuntime(t.Context(), host, store.HostRuntimeRow{Runtime: *rt, Online: true, LastHeartbeatAt: h.clock.Now().UnixMilli()}); err != nil {
				return err
			}
		}
		return tx.PutUpgrade(t.Context(), batch)
	}); err != nil {
		t.Fatal(err)
	}
	h.app.observeDeployment = func(context.Context) (protocol.ObservedDeployment, error) {
		return protocol.ObservedDeployment{Preparation: &protocol.ObservedDeploymentPreparation{ServerInstance: "fixed-service"}, ActualServer: &protocol.ObservedServer{BinarySHA256: hash, ProcessInstanceID: "server-current", PID: 123}, ProofStatus: "confirmed", CommittedProof: &protocol.UpdaterReceipt{Binding: protocol.UpgradeBinding{InstanceID: "fixed-service", ProcessInstanceID: "server-original", ArtifactSHA256: hash}, Phase: "committed", CurrentProcessInstanceID: "server-current", CurrentSHA256: hash, CommittedConfirmed: true}}, nil
	}
	return h, as, issue
}

func preparationHost(t *testing.T, p *protocol.ObservedDeploymentPreparation, id string) protocol.ObservedDeploymentHost {
	t.Helper()
	for _, h := range p.Hosts {
		if h.HostID == id {
			return h
		}
	}
	t.Fatalf("target host %s missing: %+v", id, p)
	return protocol.ObservedDeploymentHost{}
}

func TestDeploymentPreparationIncludesCompleteCurrentTargetSet(t *testing.T) {
	h, as, _ := observationFixture(t, protocol.RoleImplement)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.UpsertHost(t.Context(), store.HostRow{HostID: "linux-2", OS: "linux", Arch: "amd64", Slots: 2}, h.clock.Now().UnixMilli()); err != nil {
			return err
		}
		return tx.PutHostRuntime(t.Context(), "linux-2", store.HostRuntimeRow{Runtime: *testRuntime("second-process"), Online: true, LastHeartbeatAt: h.clock.Now().UnixMilli()})
	}); err != nil {
		t.Fatal(err)
	}
	view, err := h.app.ReadRunObservation(t.Context(), runPrincipal(as.RunID, as.Role))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(view.Deployment)
	if err != nil {
		t.Fatal(err)
	}
	var deployment struct {
		Preparation *struct {
			HostIDs []string `json:"host_ids"`
			Hosts   []any    `json:"hosts"`
		} `json:"preparation"`
	}
	if err := json.Unmarshal(raw, &deployment); err != nil {
		t.Fatal(err)
	}
	if deployment.Preparation == nil || len(deployment.Preparation.HostIDs) != 2 || len(deployment.Preparation.Hosts) != 2 {
		t.Fatalf("authorized preparation cannot see the complete current target set: %s", raw)
	}
}

func TestDeploymentPreparationProjectsExplicitControlledExecutionEntry(t *testing.T) {
	h, as, _ := preparationFixture(t, protocol.RoleImplement)
	issue := createCanonicalOwnerTestIssue(t, h)
	observe := h.app.observeDeployment
	entry := &protocol.DeploymentExecutionEntry{Transport: "tailscale_ssh", Target: "service.example.invalid", User: "operator", CLIPath: "/controlled/current/tend", ControlSocket: "/controlled/upgrades.sock"}
	h.app.observeDeployment = func(ctx context.Context) (protocol.ObservedDeployment, error) {
		d, err := observe(ctx)
		d.Preparation.ExecutionEntry = entry
		return d, err
	}
	ownerView, err := h.app.ReadIssueDeploymentPreparation(t.Context(), issue.IssueID)
	if err != nil || !reflect.DeepEqual(ownerView.Preparation.ExecutionEntry, entry) {
		t.Fatal("Issue owner lost the configured deployment route", ownerView, err)
	}
	runView, err := h.app.ReadRunObservation(t.Context(), runPrincipal(as.RunID, as.Role))
	if err != nil || !reflect.DeepEqual(runView.Deployment.Preparation.ExecutionEntry, entry) {
		t.Fatal("worker lost the configured deployment route", runView, err)
	}
}

func TestDeploymentPreparationKeepsInstallationProofSeparateFromHeartbeat(t *testing.T) {
	for _, change := range []string{"none", "missing", "wrong_host", "old_process", "wrong_hash", "rolled_back", "batch_missing", "stale", "offline"} {
		t.Run(change, func(t *testing.T) {
			h, as, _ := preparationFixture(t, protocol.RoleImplement)
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				rt, err := store.GetHostRuntime(t.Context(), tx.Q(), "linux-2")
				if err != nil {
					return err
				}
				switch change {
				case "missing":
					rt.Runtime.UpdaterReceipt = nil
				case "wrong_host":
					rt.Runtime.UpdaterReceipt.Binding.InstanceID = "not-this-target"
				case "old_process":
					rt.Runtime.UpdaterReceipt.CurrentProcessInstanceID = "old-process"
				case "wrong_hash":
					rt.Runtime.UpdaterReceipt.CurrentSHA256 = strings.Repeat("c", 64)
				case "rolled_back":
					rt.Runtime.UpdaterReceipt.Phase = "rolled_back"
					rt.Runtime.UpdaterReceipt.CommittedConfirmed = false
					rt.Runtime.UpdaterReceipt.RollbackConfirmed = true
				case "batch_missing":
					rt.Runtime.UpdaterReceipt.Binding.UpgradeID = "bounded-install-without-upgrade-row"
				case "stale":
					rt.LastHeartbeatAt = h.clock.Now().Add(-2 * RunnerOfflineAfter).UnixMilli()
				case "offline":
					rt.Online = false
				}
				return tx.PutHostRuntime(t.Context(), "linux-2", rt)
			}); err != nil {
				t.Fatal(err)
			}
			beforeEvents, beforeRequests, beforeOutbox := h.count("events"), h.count("request_records"), h.count("outbox")
			view, err := h.app.ReadRunObservation(t.Context(), runPrincipal(as.RunID, as.Role))
			if err != nil {
				t.Fatal(err)
			}
			p := view.Deployment.Preparation
			if p == nil || !p.TargetComplete || !slices.Equal(p.HostIDs, []string{"linux-1", "linux-2"}) || p.ServerInstance != "fixed-service" || p.Seq != view.Seq || p.ObservedAt != view.ObservedAt || p.DeliveryTargetAvailable == nil || !*p.DeliveryTargetAvailable || view.DeliveryAuthorization != nil {
				t.Fatalf("incorrect preparation scope or authority: %+v", view)
			}
			other := preparationHost(t, p, "linux-2")
			if change == "none" {
				if other.InstallationStatus != "confirmed" || !p.Complete || len(p.Missing) != 0 {
					t.Fatalf("valid same-version successor proof lost: %+v", p)
				}
			} else if other.InstallationStatus == "confirmed" || p.Complete || other.InstallationReason == "" {
				t.Fatalf("%s promoted heartbeat or stale receipt to installation: %+v", change, other)
			}
			if change != "missing" && other.UpdaterReceipt == nil {
				t.Fatal("original mismatched/unknown receipt was discarded")
			}
			if h.count("events") != beforeEvents || h.count("request_records") != beforeRequests || h.count("outbox") != beforeOutbox {
				t.Fatal("read scheduled a provider query or wrote a business effect")
			}
		})
	}
}

func TestDeploymentPreparationCanonicalDeliveriesStayWithinAllowedIssue(t *testing.T) {
	h, as, legacy := preparationFixture(t, protocol.RoleAccept)
	issue := createCanonicalOwnerTestIssue(t, h)
	issue.Plan.Deliveries = []protocol.IssueDelivery{{DeliveryID: "own-pr", Provider: "github", Repository: "example/own", Number: 7, URL: "https://example.invalid/own/pull/7"}}
	issue = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: issue.IssueID}, canonicalOwnerIssueUpdate{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "update"}))
	sibling := createCanonicalOwnerTestIssue(t, h)
	sibling.Plan.Deliveries = []protocol.IssueDelivery{{DeliveryID: "private-pr", Provider: "github", Repository: "PRIVATE_SIBLING_REPO", Number: 99, URL: "https://example.invalid/private/pull/99"}}
	canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: sibling.IssueID}, canonicalOwnerIssueUpdate{Plan: sibling.Plan, ExpectedAssignmentEpoch: sibling.AssignmentEpoch}, rev(sibling.Revision)), CanonicalOwnerIssueCommand{Action: "update"}))
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return putDeliveryObservationForTest(tx, t.Context(), store.DeliveryObservationRow{Observation: protocol.DeliveryObservation{WorkItemID: issue.IssueID, DeliveryID: "own-pr", Generation: 13, Observer: "server", CheckedAt: h.clock.Now().UnixMilli(), DeliverySnapshot: protocol.DeliverySnapshot{State: "merged", HeadSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40), TargetSHA: strings.Repeat("c", 40), MergeSHA: strings.Repeat("d", 40), TargetContainsMerge: true}}})
	}); err != nil {
		t.Fatal(err)
	}
	ownerView, err := h.app.ReadIssueDeploymentPreparation(t.Context(), issue.IssueID)
	if err != nil || ownerView.Preparation == nil || len(ownerView.Preparation.Deliveries) != 1 || len(ownerView.Preparation.Deliveries[0].Items) != 1 || ownerView.Preparation.Deliveries[0].Items[0].Observation.Generation != 13 {
		t.Fatalf("canonical registered delivery missing: %+v %v", ownerView, err)
	}
	// Use the same frozen observation boundary as an existing historical Task;
	// canonical facts must not require delivery execution/merge authority.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		spec := h.storedSpec(as.TaskID, as.SpecVersion)
		spec.Observation.WorkItemIDs = []string{issue.IssueID}
		raw, err := json.Marshal(spec)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `UPDATE task_specs SET body_json=? WHERE task_id=? AND spec_version=?`, string(raw), as.TaskID, as.SpecVersion)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	workerView, err := h.app.ReadRunObservation(t.Context(), runPrincipal(as.RunID, as.Role))
	if err != nil || !reflect.DeepEqual(ownerView.Preparation.Deliveries, workerView.Deployment.Preparation.Deliveries) || workerView.DeliveryAuthorization != nil {
		t.Fatalf("owner/worker facts diverged or authority leaked: %+v %v", workerView, err)
	}
	raw, _ := json.Marshal(workerView)
	if strings.Contains(string(raw), "PRIVATE_SIBLING") || strings.Contains(string(raw), sibling.IssueID) || strings.Contains(string(raw), legacy) {
		t.Fatalf("unrelated Issue leaked: %s", raw)
	}
}

func TestDeploymentPreparationRechecksScopedIssueAfterProcessRead(t *testing.T) {
	h := newHarness(t)
	root := createCanonicalOwnerTestIssue(t, h)
	child := createCanonicalOwnerTestIssue(t, h)
	child = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, child, "reparent", protocol.ReparentIssuePayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: child.AssignmentEpoch}, ParentIssueID: root.IssueID, ExpectedParentRevision: rev(root.Revision)}))
	called := 0
	h.app.observeDeployment = func(ctx context.Context) (protocol.ObservedDeployment, error) {
		called++
		child = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, child, "reparent", protocol.ReparentIssuePayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: child.AssignmentEpoch}}))
		return protocol.ObservedDeployment{ActualServer: &protocol.ObservedServer{Commit: "SHOULD_NOT_ESCAPE"}}, nil
	}
	view, err := h.app.ReadScopedIssueDeploymentPreparation(t.Context(), root.IssueID, child.IssueID)
	if protocol.CodeOf(err) != protocol.CodeForbidden || called != 1 || view.ActualServer != nil || view.Preparation != nil {
		t.Fatalf("reparent during process observation escaped scope: %+v %v calls=%d", view, err, called)
	}
	_, err = h.app.ReadScopedIssueDeploymentPreparation(t.Context(), root.IssueID, child.IssueID)
	if protocol.CodeOf(err) != protocol.CodeForbidden || called != 1 {
		t.Fatalf("already forbidden read observed the process: %v calls=%d", err, called)
	}
}

func TestDeploymentPreparationRecognizesActualBoundedInstallationWithoutUpgradeRow(t *testing.T) {
	for _, change := range []string{"none", "same_version_restart", "missing_receipt", "uncommitted", "wrong_binding", "wrong_current_process", "wrong_current_hash", "wrong_authority"} {
		t.Run(change, func(t *testing.T) {
			h, as, release := boundedRunnerAdmissionFixture(t)
			issue := createCanonicalOwnerTestIssue(t, h)
			beforeRun, beforeLease := h.run(as.RunID), h.lease(as.RunID)
			if h.count("upgrades") != 0 {
				t.Fatal("bounded fixture unexpectedly relies on an ordinary upgrade")
			}
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				r, err := store.GetHostRuntime(t.Context(), tx.Q(), release.HostID)
				if err != nil {
					return err
				}
				r.Online, r.LastHeartbeatAt = true, h.clock.Now().UnixMilli()
				switch change {
				case "same_version_restart":
					r.Runtime.InstanceID = "confirmed-same-version-successor"
					r.Runtime.UpdaterReceipt.CurrentProcessInstanceID = r.Runtime.InstanceID
				case "missing_receipt":
					r.Runtime.UpdaterReceipt = nil
				case "uncommitted":
					r.Runtime.UpdaterReceipt.CommittedConfirmed = false
				case "wrong_binding":
					r.Runtime.UpdaterReceipt.Binding.ProcessInstanceID = "foreign-install"
				case "wrong_current_process":
					r.Runtime.UpdaterReceipt.CurrentProcessInstanceID = "previous-runtime"
				case "wrong_current_hash":
					r.Runtime.UpdaterReceipt.CurrentSHA256 = strings.Repeat("d", 64)
				case "wrong_authority":
					r.Runtime.BoundedRecoveryAuthorizationID = h.ids.New(protocol.PrefixRequest)
				}
				return tx.PutHostRuntime(t.Context(), release.HostID, r)
			}); err != nil {
				t.Fatal(err)
			}
			view, err := h.app.ReadIssueDeploymentPreparation(t.Context(), issue.IssueID)
			if err != nil {
				t.Fatal(err)
			}
			host := preparationHost(t, view.Preparation, release.HostID)
			if change == "none" || change == "same_version_restart" {
				if host.InstallationStatus != "confirmed" || host.UpdaterReceipt == nil || host.Runner.PendingOperations == nil || *host.Runner.PendingOperations != 3 {
					t.Fatalf("actual bounded installation or retained unknown evidence lost: %+v", host)
				}
			} else if host.InstallationStatus == "confirmed" || host.InstallationReason == "" {
				t.Fatalf("unbound bounded-install receipt became proof: %+v", host)
			}
			if !reflect.DeepEqual(beforeRun, h.run(as.RunID)) || !reflect.DeepEqual(beforeLease, h.lease(as.RunID)) {
				t.Fatal("installation observation changed original uncertain work")
			}
		})
	}
}

func TestDeploymentPreparationPreservesKnownUnknownWorkWithoutGlobalZeroGate(t *testing.T) {
	h, as, _ := preparationFixture(t, protocol.RoleImplement)
	_, task := h.createTask(owner, createTaskPayload{Title: "PRIVATE unrelated work", Spec: testSpec})
	var runtime store.HostRuntimeRow
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		runtime, err = store.GetHostRuntime(t.Context(), tx.Q(), "linux-1")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	other, ok := h.claimInputRun(&runtime.Runtime)
	if !ok || other.TaskID != task.TaskID {
		t.Fatalf("unrelated run not claimed: %+v", other)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return tx.UpdateRunObserved(t.Context(), other.RunID, protocol.RunUnknown, h.clock.Now().UnixMilli())
	}); err != nil {
		t.Fatal(err)
	}
	view, err := h.app.ReadRunObservation(t.Context(), runPrincipal(as.RunID, as.Role))
	if err != nil {
		t.Fatal(err)
	}
	p := view.Deployment.Preparation
	host := preparationHost(t, p, "linux-1")
	if !p.Complete || host.UnknownRuns != 1 || p.DeliveryTargetAvailable == nil || !*p.DeliveryTargetAvailable {
		t.Fatalf("known unrelated uncertainty imposed a global zero gate: %+v", p)
	}
	raw, _ := json.Marshal(view)
	if strings.Contains(string(raw), "PRIVATE unrelated") || strings.Contains(string(raw), other.RunID) || h.run(other.RunID).ObservedState != protocol.RunUnknown {
		t.Fatalf("read leaked or settled unrelated work: %s", raw)
	}
}

func TestDeploymentPreparationKeepsLatestWindowSeparateFromInstalledSource(t *testing.T) {
	h, as, _ := preparationFixture(t, protocol.RoleImplement)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return tx.PutUpgrade(t.Context(), store.UpgradeRow{View: protocol.UpgradeView{UpgradeID: "runner-only-unsettled", ReleaseID: "other-release", Revision: 9, Status: "attention", Phase: "runners", CreatedAt: h.clock.Now(), UpdatedAt: h.clock.Now()}, ServerInstance: "fixed-service"})
	}); err != nil {
		t.Fatal(err)
	}
	view, err := h.app.ReadRunObservation(t.Context(), runPrincipal(as.RunID, as.Role))
	if err != nil {
		t.Fatal(err)
	}
	d, p := view.Deployment, view.Deployment.Preparation
	if d.ProofStatus != "confirmed" || d.ActualServer.ProcessInstanceID != "server-current" || d.LatestUpgrade.UpgradeID != "runner-only-unsettled" || p.SettledBaseline.UpgradeID != "installed-batch" || len(p.UnsettledUpgrades) != 1 || p.UnsettledUpgrades[0].Revision != 9 || p.DeliveryTargetAvailable == nil || *p.DeliveryTargetAvailable {
		t.Fatalf("current installation and unfinished window were conflated: %+v", d)
	}
	if preparationHost(t, p, "linux-2").InstallationStatus != "confirmed" || !p.Complete {
		t.Fatal("known blocking window hid independently installed bytes", p)
	}
}

func TestDeploymentPreparationFalseScopeAndUnavailableConfiguration(t *testing.T) {
	h, as, _ := observationFixture(t, protocol.RoleImplement)
	spec := h.storedSpec(as.TaskID, as.SpecVersion)
	spec.Observation.Deployment = false
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE task_specs SET body_json=? WHERE task_id=? AND spec_version=?`, string(raw), as.TaskID, as.SpecVersion)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	called := false
	h.app.observeDeployment = func(context.Context) (protocol.ObservedDeployment, error) {
		called = true
		return protocol.ObservedDeployment{}, nil
	}
	view, err := h.app.ReadRunObservation(t.Context(), runPrincipal(as.RunID, as.Role))
	if err != nil || view.Deployment != nil || called {
		t.Fatalf("deployment=false expanded read authority: %+v %v", view, err)
	}
	issue := createCanonicalOwnerTestIssue(t, h)
	h.app.observeDeployment = nil
	d, err := h.app.ReadIssueDeploymentPreparation(t.Context(), issue.IssueID)
	if err != nil || d.Preparation == nil || d.Preparation.TargetComplete || d.Preparation.Complete || d.Preparation.DeliveryTargetAvailable != nil || !slices.Contains(d.Preparation.Missing, "configured_server_instance") || d.ProofStatus != "unavailable" {
		t.Fatalf("missing configuration was invented or read failed: %+v %v", d, err)
	}
}

func TestDeploymentPreparationPendingBoundedGrantKeepsCurrentInstallation(t *testing.T) {
	for _, change := range []string{"none", "expired_pending", "wrong_authority", "wrong_host_event"} {
		t.Run(change, func(t *testing.T) {
			h, _, release := boundedRunnerAdmissionFixture(t)
			issue := createCanonicalOwnerTestIssue(t, h)
			var grant protocol.BoundedRunnerMaintenanceGrant
			var runtime store.HostRuntimeRow
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				var raw string
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT json_extract(body_json,'$.grant') FROM events WHERE source='app' AND object_type='host' AND object_id=? AND request_id=? AND kind=?`, release.HostID, release.RecoveryAuthorizationID, boundedRunnerAuthorizedEvent).Scan(&raw); err != nil {
					return err
				}
				if err := json.Unmarshal([]byte(raw), &grant); err != nil {
					return err
				}
				var err error
				runtime, err = store.GetHostRuntime(t.Context(), tx.Q(), release.HostID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			runtime.Online, runtime.LastHeartbeatAt = true, h.clock.Now().UnixMilli()
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.PutHostRuntime(t.Context(), release.HostID, runtime) }); err != nil {
				t.Fatal(err)
			}
			before, err := h.app.ReadIssueDeploymentPreparation(t.Context(), issue.IssueID)
			if err != nil || preparationHost(t, before.Preparation, release.HostID).InstallationStatus != "confirmed" {
				t.Fatalf("original actual installation is not confirmed: %+v %v", before, err)
			}
			pending := grant.Plan
			pending.ExpectedControlRevision = grant.ControlRevision
			pending.RunnerInstanceID, pending.RunnerGeneration = runtime.Runtime.InstanceID, runtime.Runtime.Generation
			pending.Source = protocol.RecoveryIdentity{PID: int(runtime.Runtime.Build.PID), StartTicks: 789, LaunchID: runtime.Runtime.InstanceID, SHA256: runtime.Runtime.Build.BinarySHA256}
			pending.Journal.Version = runtime.Runtime.JournalVersion
			pending.Target.UpgradeID, pending.Target.ProcessInstanceID = "next-not-started", "next-not-started-process"
			pending.Target.ReleaseID, pending.Target.ManifestSHA256 = "next-candidate", strings.Repeat("e", 64)
			pending.Target.ArtifactSHA256 = strings.Repeat("d", 64)
			pending.Authorization = protocol.BoundedMaintenanceAuthorization{RequestID: h.ids.New(protocol.PrefixRequest), Role: "runner", Source: pending.Source, Target: pending.Target, DataIdentity: &pending.Journal, ExpectedGeneration: 8, ExpectedPhase: "maintenance", Timeouts: protocol.DefaultMaintenanceTimeoutPolicy(), ExpiresAtMillis: h.clock.Now().Add(6 * time.Minute).UnixMilli(), Reason: "Prepare a future replacement while preserving current installation evidence"}
			pending.Authorization.PlanSHA256, err = protocol.RunnerBoundedMaintenancePlanSHA256(pending)
			if err != nil {
				t.Fatal(err)
			}
			future, err := h.app.AuthorizeRunnerBoundedMaintenance(t.Context(), owner, pending)
			if err != nil || future.RecoveryAuthorizationID != pending.Authorization.RequestID || future.ControlRevision != grant.ControlRevision+1 {
				t.Fatalf("second real pending authority failed: %+v %v", future, err)
			}
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				switch change {
				case "expired_pending":
					h.clock.Advance(7 * time.Minute)
					runtime.LastHeartbeatAt = h.clock.Now().UnixMilli()
					return tx.PutHostRuntime(t.Context(), release.HostID, runtime)
				case "wrong_authority":
					runtime.Runtime.BoundedRecoveryAuthorizationID = future.RecoveryAuthorizationID
					return tx.PutHostRuntime(t.Context(), release.HostID, runtime)
				case "wrong_host_event":
					_, err := tx.Exec(t.Context(), `UPDATE events SET object_id='another-host' WHERE source='app' AND object_type='host' AND object_id=? AND request_id=? AND kind=?`, release.HostID, release.RecoveryAuthorizationID, boundedRunnerAuthorizedEvent)
					return err
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			after, err := h.app.ReadIssueDeploymentPreparation(t.Context(), issue.IssueID)
			if err != nil {
				t.Fatal(err)
			}
			host := preparationHost(t, after.Preparation, release.HostID)
			latest := host.LatestBoundedAuthorization
			if latest == nil || latest.RequestID != pending.Authorization.RequestID || latest.RecoveryAuthorizationID != future.RecoveryAuthorizationID || latest.ControlRevision != future.ControlRevision || latest.EventSeq == 0 || latest.CurrentInstallation || latest.ExpiresAtMillis != pending.Authorization.ExpiresAtMillis {
				t.Fatalf("new registration is hidden or presented as installed: %+v", latest)
			}
			if change == "none" || change == "expired_pending" {
				if host.InstallationStatus != "confirmed" || !reflect.DeepEqual(host.UpdaterReceipt, runtime.Runtime.UpdaterReceipt) || host.Runner.BoundedRecoveryAuthorizationID != release.RecoveryAuthorizationID || host.Runner.ControlRevision != future.ControlRevision {
					t.Fatalf("pending authorization replaced current physical installation: %+v", host)
				}
			} else if host.InstallationStatus == "confirmed" {
				t.Fatalf("unbound authority became current installation evidence: %+v", host)
			}
			if len(after.Preparation.Blockers) != 0 {
				t.Fatal("registration or expiry invented an installation blocker", after.Preparation.Blockers)
			}
			raw, _ := json.Marshal(host)
			if strings.Contains(string(raw), pending.ReleaseDir) || strings.Contains(string(raw), "protections") || strings.Contains(string(raw), "plan_sha256") {
				t.Fatalf("private bounded plan escaped registration summary: %s", raw)
			}
			if h.count("upgrades") != 0 {
				t.Fatal("observation invented an ordinary upgrade")
			}
		})
	}
}

func TestDeploymentPreparationIncludesRetiredHostUpgradeBlocker(t *testing.T) {
	h, as, _ := preparationFixture(t, protocol.RoleImplement)
	issue := createCanonicalOwnerTestIssue(t, h)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.UpdateRunObserved(t.Context(), as.RunID, protocol.RunUnknown, h.clock.Now().UnixMilli()); err != nil {
			return err
		}
		return tx.RetireHostsExcept(t.Context(), []string{"linux-2"}, h.clock.Now().UnixMilli())
	}); err != nil {
		t.Fatal(err)
	}
	view, err := h.app.ReadIssueDeploymentPreparation(t.Context(), issue.IssueID)
	if err != nil {
		t.Fatal(err)
	}
	p := view.Preparation
	if !slices.Equal(p.HostIDs, []string{"linux-2"}) || len(p.Hosts) != 1 || !p.Complete {
		t.Fatalf("retired work changed active targets or imposed a report gate: %+v", p)
	}
	found := false
	for _, b := range p.Blockers {
		if b.Kind == "retired_hosts_open_runs" && b.Count == 1 && b.Source != "" {
			found = true
		}
	}
	if !found || h.run(as.RunID).ObservedState != protocol.RunUnknown {
		t.Fatalf("actual upgrade blocker was hidden or settled: %+v", p)
	}
	raw, _ := json.Marshal(view)
	if strings.Contains(string(raw), as.RunID) || strings.Contains(string(raw), as.TaskID) {
		t.Fatalf("retired-run business identity escaped aggregate read: %s", raw)
	}
}

func TestDeploymentPreparationCancelledBaselineMatchesPrepare(t *testing.T) {
	for _, completedFirst := range []bool{false, true} {
		name := "only_cancelled"
		if completedFirst {
			name = "completed_then_cancelled"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			issue := createCanonicalOwnerTestIssue(t, h)
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				if completedFirst {
					if err := tx.PutUpgrade(t.Context(), store.UpgradeRow{View: protocol.UpgradeView{UpgradeID: "prior-completed", ReleaseID: "old-release", Revision: 9, Status: "completed", Phase: "completed", CreatedAt: h.clock.Now().Add(-time.Minute), UpdatedAt: h.clock.Now().Add(-time.Second)}}); err != nil {
						return err
					}
				}
				return tx.PutUpgrade(t.Context(), store.UpgradeRow{View: protocol.UpgradeView{UpgradeID: "current-cancelled", ReleaseID: "cancelled-release", Revision: 12, Status: "cancelled", Phase: "cancelled", CreatedAt: h.clock.Now(), UpdatedAt: h.clock.Now()}})
			}); err != nil {
				t.Fatal(err)
			}
			view, err := h.app.ReadIssueDeploymentPreparation(t.Context(), issue.IssueID)
			if err != nil {
				t.Fatal(err)
			}
			baseline := view.Preparation.SettledBaseline
			if baseline == nil || baseline.UpgradeID != "current-cancelled" || baseline.Revision != 12 || baseline.Status != "cancelled" || len(view.Preparation.UnsettledUpgrades) != 0 {
				t.Fatalf("advertised settled baseline differs from actual prepare baseline: %+v", view.Preparation)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				candidate := protocol.IssueDeliveryCandidate{Deploy: true, Deployment: &protocol.IssueDeliveryDeployment{BaselineUpgradeID: baseline.UpgradeID, BaselineUpgradeRevision: baseline.Revision}}
				return checkDeliveryBaseline(t.Context(), tx.Q(), candidate)
			}); err != nil {
				t.Fatalf("advertised baseline immediately fails prepare: %v", err)
			}
		})
	}
}
