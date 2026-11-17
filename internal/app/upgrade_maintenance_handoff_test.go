package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func enterUpgradeMaintenance(t *testing.T, h *harness, host, mode string) protocol.RunnerRuntime {
	t.Helper()
	ctl := upgradeCtl(h, host)
	op := h.exec(owner, h.env("hosts.maintenance", protocol.Target{Type: "host", ID: host}, protocol.HostMaintenancePayload{Mode: protocol.RunnerMaintenance}, rev(ctl.Revision)), HostMaintenance{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	return upgradeAck(h, host, mode)
}

func assertHandoffBlocksClaim(t *testing.T, h *harness, host string) {
	t.Helper()
	var rt store.HostRuntimeRow
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		rt, err = store.GetHostRuntime(t.Context(), tx.Q(), host)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	env := h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: host}, protocol.ClaimPayload{Worker: h.worker(host), WorkspaceProtocol: 1, Runtime: &rt.Runtime}, nil)
	if as, ok := h.claimWith(host, env); ok {
		t.Fatalf("maintenance handoff allocated run: %+v", as)
	}
	if ctl := upgradeCtl(h, host); ctl.Mode != protocol.RunnerMaintenance {
		t.Fatalf("handoff reopened admission: %+v", ctl)
	}
}

func TestUpgradeMaintenanceHandoffPreservesAdmissionAndWork(t *testing.T) {
	h, c := upgradeHarness(t)
	// An immutable implementation result leaves its independent acceptance queued.
	task := h.createReportTask("original result", temporaryReportSpec())
	rt := upgradeAck(h, "linux-1", protocol.RunnerNormal)
	original, ok := h.claimInputRun(&rt)
	if !ok {
		t.Fatal("original implementation not admitted")
	}
	h.reportDirectory(original, "/tmp/handoff-report")
	if op := h.runAs(original, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Original immutable result"}}, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	artifact := h.task(task.TaskID).CurrentArtifactID
	// A separate already admitted run remains live during the server handoff.
	h.createReportTask("existing live work", temporaryReportSpec())
	existing, ok := h.claimInputRun(&rt)
	if !ok {
		t.Fatal("existing run unavailable")
	}
	before := h.run(existing.RunID)
	maint := enterUpgradeMaintenance(t, h, "linux-1", protocol.RunnerMaintenance)
	revision := maint.AppliedControlRevision
	assertHandoffBlocksClaim(t, h, "linux-1")
	op := startTestUpgrade(h, "maintenance-handoff", c)
	if op.Status != protocol.OpSucceeded {
		t.Fatalf("formal start refused acknowledged standalone maintenance: %+v", op)
	}
	ctl := upgradeCtl(h, "linux-1")
	if ctl.Revision <= revision || ctl.UpgradeID != "maintenance-handoff" || ctl.Upgrade == nil || !ctl.Upgrade.PrepareOnly {
		t.Fatalf("missing fresh preparation responsibility: %+v", ctl)
	}
	assertHandoffBlocksClaim(t, h, "linux-1")
	// Preparation must require the new ACK; the preceding maintenance ACK cannot advance it.
	if v := upgradeTick(h, ServerObservation{}); v.Phase != "preparing" {
		t.Fatal(v)
	}
	for _, host := range []string{"linux-1", "linux-2"} {
		mode := protocol.RunnerNormal
		if host == "linux-1" {
			mode = protocol.RunnerMaintenance
		}
		ack := upgradeAck(h, host, mode)
		ack.Sequence++
		ack.UpgradePhase = "prepared"
		if _, err := h.app.RuntimeHeartbeat(t.Context(), runner(host), protocol.HeartbeatPayload{Runtime: &ack}); err != nil {
			t.Fatal(err)
		}
	}
	upgradeTick(h, ServerObservation{})
	assertHandoffBlocksClaim(t, h, "linux-1")
	if v := upgradeTick(h, ServerObservation{MainDrained: true, BinarySHA256: strings.Repeat("a", 64)}); v.Phase != "server_ready" {
		t.Fatal(v)
	}
	for _, host := range []string{"linux-1", "linux-2"} {
		upgradeAck(h, host, protocol.RunnerMaintenance)
	}
	b := protocol.UpgradeBinding{UpgradeID: "maintenance-handoff", ReleaseID: c.Manifest.ReleaseID, ManifestSHA256: c.ManifestSHA256, ArtifactSHA256: c.ServerHash, InstanceID: c.ServerInstance, ProcessInstanceID: "new-server"}
	grant, err := h.app.ServerUpgradeAuthorize(t.Context(), b)
	if err != nil || !grant.Allowed {
		t.Fatalf("server authority: %+v %v", grant, err)
	}
	assertHandoffBlocksClaim(t, h, "linux-1")
	o := ServerObservation{InstanceID: "server", ProcessInstanceID: b.ProcessInstanceID, BinarySHA256: c.ServerHash, Healthy: true, MainDrained: true, UpdaterReceipt: committedServerForTest(b)}
	upgradeTick(h, o)
	if v := upgradeTick(h, o); v.Phase != "runners" {
		t.Fatal(v)
	}
	assertHandoffBlocksClaim(t, h, "linux-1")
	// The busy host is later in rolling order; server commitment must not reopen it.
	upgradeAck(h, "linux-2", protocol.RunnerNormal)
	upgradeTick(h, ServerObservation{})
	assertHandoffBlocksClaim(t, h, "linux-1")
	after := h.run(existing.RunID)
	if !reflect.DeepEqual(before, after) || h.task(task.TaskID).CurrentArtifactID != artifact || artifact == "" {
		t.Fatal("handoff rewrote original run or immutable result")
	}
	var lease store.LeaseRow
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var e error
		lease, e = store.GetLeaseByRun(t.Context(), tx.Q(), existing.RunID)
		return e
	}); err != nil || lease.Status != store.LeaseActive {
		t.Fatalf("existing lease lost: %+v %v", lease, err)
	}
}

func TestUpgradeMaintenanceHandoffTimeoutAndCancelRestore(t *testing.T) {
	for _, action := range []string{"cancel", "timeout"} {
		t.Run(action, func(t *testing.T) {
			h, c := upgradeHarness(t)
			enterUpgradeMaintenance(t, h, "linux-1", protocol.RunnerReady)
			if op := startTestUpgrade(h, "handoff-recovery", c); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			assertHandoffBlocksClaim(t, h, "linux-1")
			if action == "timeout" {
				h.clock.Advance(20 * time.Minute)
				if v := upgradeTick(h, ServerObservation{}); v.Status != "attention" {
					t.Fatal(v)
				}
			} else {
				v := upgradeRead(h).View
				op := h.exec(owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: v.UpgradeID}, map[string]string{"action": "cancel"}, rev(v.Revision)), ControlUpgrade{})
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			ctl := upgradeCtl(h, "linux-1")
			if ctl.Mode != protocol.RunnerNormal || ctl.UpgradeID != "" || ctl.Authorization != nil || ctl.Upgrade != nil {
				raw, _ := json.Marshal(ctl)
				t.Fatalf("safe recovery did not release unchanged host: %s", raw)
			}
		})
	}
}

func TestUpgradeMaintenanceHandoffRejectsUnconfirmedOrOwnedControl(t *testing.T) {
	for _, kind := range []string{"stale-ack", "stale-runtime", "wrong-runtime-upgrade", "other-upgrade", "installation-grant"} {
		t.Run(kind, func(t *testing.T) {
			h, c := upgradeHarness(t)
			rt := enterUpgradeMaintenance(t, h, "linux-1", protocol.RunnerReady)
			switch kind {
			case "stale-ack":
				ctl := upgradeCtl(h, "linux-1")
				if op := h.exec(owner, h.env("hosts.maintenance", protocol.Target{Type: "host", ID: "linux-1"}, protocol.HostMaintenancePayload{Mode: protocol.RunnerMaintenance}, rev(ctl.Revision)), HostMaintenance{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			case "stale-runtime":
				h.clock.Advance(RunnerOfflineAfter)
			case "wrong-runtime-upgrade":
				rt.Sequence++
				rt.UpgradeID = "different"
				if _, err := h.app.RuntimeHeartbeat(t.Context(), runner("linux-1"), protocol.HeartbeatPayload{Runtime: &rt}); err != nil {
					t.Fatal(err)
				}
			default:
				// Rejection-only fixture: an unrelated controller already owns this host.
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					ctl, err := runnerControl(t.Context(), tx.Q(), "linux-1", h.clock.Now().UnixMilli())
					if err != nil {
						return err
					}
					if kind == "other-upgrade" {
						ctl.UpgradeID = "different"
					} else {
						ctl.Authorization = &protocol.MaintenanceAuthorization{ID: "other-grant", DeadlineMillis: h.clock.Now().Add(time.Minute).UnixMilli()}
					}
					return tx.PutHostControl(t.Context(), "linux-1", ctl, h.clock.Now().UnixMilli())
				}); err != nil {
					t.Fatal(err)
				}
			}
			before := upgradeCtl(h, "linux-1")
			if op := startTestUpgrade(h, "must-reject", c); op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidTransition {
				t.Fatal(op)
			}
			if !reflect.DeepEqual(before, upgradeCtl(h, "linux-1")) || h.count("upgrades") != 0 {
				t.Fatal("rejection changed prior control or created a batch")
			}
		})
	}
}

func TestUpgradeMaintenanceHandoffSameHashCompletesAndResumesQueuedWork(t *testing.T) {
	h, c := upgradeHarness(t)
	c.ServerHash = strings.Repeat("a", 64)
	c.Manifest.Artifacts[0].SHA256 = c.ServerHash
	c.Manifest.Artifacts[1].SHA256 = c.ServerHash
	for _, host := range []string{"linux-1", "linux-2"} {
		rt := upgradeAck(h, host, protocol.RunnerNormal)
		rt.Sequence++
		rt.JournalVersion = c.Manifest.Storage.RunnerJournal.Max
		if _, err := h.app.RuntimeHeartbeat(t.Context(), runner(host), protocol.HeartbeatPayload{Runtime: &rt}); err != nil {
			t.Fatal(err)
		}
		enterUpgradeMaintenance(t, h, host, protocol.RunnerReady)
	}
	h.createReportTask("queued work", temporaryReportSpec())
	if op := startTestUpgrade(h, "same-hash-handoff", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	prepareTestUpgrade(h)
	for _, host := range []string{"linux-1", "linux-2"} {
		assertHandoffBlocksClaim(t, h, host)
	}
	upgradeTick(h, ServerObservation{MainDrained: true, BinarySHA256: c.ServerHash})
	// Each safe same-hash restoration has its own fresh ACK before completion.
	for i := 0; i < 8 && upgradeRead(h).View.Status != "completed"; i++ {
		for _, host := range []string{"linux-1", "linux-2"} {
			ctl := upgradeCtl(h, host)
			if ctl.Mode == protocol.RunnerMaintenance {
				assertHandoffBlocksClaim(t, h, host)
			}
			mode := ctl.Mode
			if mode == protocol.RunnerMaintenance {
				mode = protocol.RunnerReady
			}
			upgradeAck(h, host, mode)
		}
		upgradeTick(h, ServerObservation{})
	}
	if v := upgradeRead(h).View; v.Status != "completed" {
		t.Fatal("same hash handoff stuck", v)
	}
	rt := upgradeAck(h, "linux-1", protocol.RunnerNormal)
	if as, ok := h.claimInputRun(&rt); !ok || as.Role != protocol.RoleImplement {
		t.Fatal("queued business did not resume", as)
	}
}

func TestUpgradeMaintenanceHandoffCandidateCommitAndRollback(t *testing.T) {
	for _, outcome := range []string{"commit", "rollback"} {
		t.Run(outcome, func(t *testing.T) {
			h, c := upgradeHarness(t)
			c.ServerHash = strings.Repeat("a", 64)
			c.Manifest.Artifacts[0].SHA256 = c.ServerHash
			for _, host := range []string{"linux-1", "linux-2"} {
				enterUpgradeMaintenance(t, h, host, protocol.RunnerReady)
			}
			h.createReportTask("queued during install", temporaryReportSpec())
			if op := startTestUpgrade(h, "runner-handoff", c); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			prepareTestUpgrade(h)
			upgradeTick(h, ServerObservation{MainDrained: true, BinarySHA256: c.ServerHash})
			upgradeTick(h, ServerObservation{})
			assertHandoffBlocksClaim(t, h, "linux-1")
			upgradeAck(h, "linux-1", protocol.RunnerReady)
			upgradeTick(h, ServerObservation{})
			upgradeAck(h, "linux-1", protocol.RunnerReady)
			ctl := upgradeCtl(h, "linux-1")
			b := protocol.UpgradeBinding{UpgradeID: "runner-handoff", ReleaseID: c.Manifest.ReleaseID, ManifestSHA256: c.ManifestSHA256, ArtifactSHA256: strings.Repeat("b", 64), InstanceID: "linux-1", ProcessInstanceID: "runner-candidate"}
			env := h.env("runner.upgrade_authorize", protocol.Target{Type: "host", ID: "linux-1"}, b, rev(ctl.Revision))
			op := h.exec(runner("linux-1"), env, AuthorizeRunnerUpgrade{})
			var grant protocol.UpgradeGrant
			if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &grant) != nil || !grant.Allowed {
				t.Fatal("candidate not authorized", op)
			}
			// Unknown installation cannot be cancelled into normal admission.
			v := upgradeRead(h).View
			_, cancelErr := h.app.Execute(t.Context(), owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: v.UpgradeID}, map[string]string{"action": "cancel"}, rev(v.Revision)), ControlUpgrade{})
			if protocol.CodeOf(cancelErr) != protocol.CodeUnknownOutcome {
				t.Fatal(cancelErr)
			}
			assertHandoffBlocksClaim(t, h, "linux-1")
			assertHandoffBlocksClaim(t, h, "linux-2")
			if outcome == "rollback" {
				h.clock.Advance(20 * time.Minute)
				upgradeTick(h, ServerObservation{})
				ctl = upgradeCtl(h, "linux-1")
				restored := testRuntime("restored-old")
				restored.Mode = protocol.RunnerReady
				restored.UpgradeID = b.UpgradeID
				restored.AppliedControlRevision = ctl.Revision
				restored.Build.BinarySHA256 = strings.Repeat("a", 64)
				if op := h.versionedHello("linux-1", restored); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				upgradeTick(h, ServerObservation{})
				assertHandoffBlocksClaim(t, h, "linux-1")
				restored.Sequence++
				restored.UpdaterReceipt = &protocol.UpdaterReceipt{Binding: b, Phase: "rolled_back", RollbackConfirmed: true, CurrentProcessInstanceID: restored.InstanceID, CurrentSHA256: restored.Build.BinarySHA256}
				if _, err := h.app.RuntimeHeartbeat(t.Context(), runner("linux-1"), protocol.HeartbeatPayload{Runtime: restored}); err != nil {
					t.Fatal(err)
				}
				upgradeTick(h, ServerObservation{})
				if upgradeRead(h).Hosts[0].Rollback == nil || upgradeCtl(h, "linux-1").Mode != protocol.RunnerNormal {
					t.Fatal("confirmed rollback did not recover")
				}
			} else {
				candidate := testRuntime(b.ProcessInstanceID)
				candidate.Mode = protocol.RunnerReady
				candidate.UpgradeID = b.UpgradeID
				candidate.AppliedControlRevision = ctl.Revision
				candidate.Build.BinarySHA256 = b.ArtifactSHA256
				candidate.JournalVersion = c.Manifest.Storage.RunnerJournal.Max
				candidate.Capabilities = append(candidate.Capabilities, capabilityUpdater)
				if op := h.versionedHello("linux-1", candidate); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				upgradeTick(h, ServerObservation{})
				assertHandoffBlocksClaim(t, h, "linux-1")
				upgradeCommitAck(h, b)
				upgradeTick(h, ServerObservation{})
				if ctl := upgradeCtl(h, "linux-1"); ctl.Mode != protocol.RunnerNormal {
					t.Fatal("committed candidate did not restore", ctl)
				}
				// The next host still cannot claim while waiting for its own rolling turn.
				assertHandoffBlocksClaim(t, h, "linux-2")
				upgradeAck(h, "linux-1", protocol.RunnerNormal)
				upgradeTick(h, ServerObservation{})
				if v := upgradeRead(h).View; v.Hosts[0].Status != "completed" {
					t.Fatal(v)
				}
			}
			rt := upgradeAck(h, "linux-1", protocol.RunnerNormal)
			if _, ok := h.claimInputRun(&rt); !ok {
				t.Fatal("queued business did not recover")
			}
		})
	}
}
