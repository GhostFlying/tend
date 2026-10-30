package app

import (
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

func TestUpgradeServerStopGrantReservesConservativeRestartCoverage(t *testing.T) {
	h, c := upgradeHarness(t)
	if op := startTestUpgrade(h, "coverage", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	upgradeTick(h, ServerObservation{MainDrained: true, BinarySHA256: strings.Repeat("a", 64)})
	var earliest int64
	for _, host := range []string{"linux-1", "linux-2"} {
		rt := upgradeAck(h, host, protocol.RunnerMaintenance)
		// Include an actual response delay on one host in its acknowledged bound.
		if host == "linux-2" {
			rt.Sequence++
			rt.AuthorizationDeadlineMillis -= 20000
			if _, err := h.app.RuntimeHeartbeat(t.Context(), runner(host), protocol.HeartbeatPayload{Runtime: &rt}); err != nil {
				t.Fatal(err)
			}
		}
		if earliest == 0 || rt.AuthorizationDeadlineMillis < earliest {
			earliest = rt.AuthorizationDeadlineMillis
		}
	}
	b := protocol.UpgradeBinding{UpgradeID: "coverage", ReleaseID: c.Manifest.ReleaseID, ManifestSHA256: c.ManifestSHA256, ArtifactSHA256: c.ServerHash, InstanceID: "server", ProcessInstanceID: "candidate"}
	g, err := h.app.ServerUpgradeAuthorize(t.Context(), b)
	if err != nil || !g.Allowed {
		t.Fatalf("grant %+v %v", g, err)
	}
	if g.ExpiresAt.UnixMilli() != earliest-120000 {
		t.Fatalf("stop cutoff %d, coverage %d", g.ExpiresAt.UnixMilli(), earliest)
	}
	if got := upgradeRead(h).Deadline; got != earliest {
		t.Fatalf("candidate deadline %d != coverage %d", got, earliest)
	}
	h.clock.Advance(time.Duration(g.RemainingMillis) * time.Millisecond)
	if late := h.app.RefreshUpgradeGrant(g); late.Allowed {
		t.Fatal("server can stop after latest safe cutoff", late)
	}
}

func TestUpgradeDownloadFailureRetryIgnoresPreviousControlError(t *testing.T) {
	h, c := upgradeHarness(t)
	upgradeToRunners(h, c)
	upgradeTick(h, ServerObservation{})
	upgradeAck(h, "linux-1", protocol.RunnerReady)
	upgradeTick(h, ServerObservation{})
	rt := upgradeAck(h, "linux-1", protocol.RunnerReady)
	rt.Sequence++
	rt.UpgradeError = "download_failed"
	if _, err := h.app.RuntimeHeartbeat(t.Context(), runner("linux-1"), protocol.HeartbeatPayload{Runtime: &rt}); err != nil {
		t.Fatal(err)
	}
	v := upgradeTick(h, ServerObservation{})
	if v.Status != "attention" {
		t.Fatal(v)
	}
	op := h.exec(owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: v.UpgradeID}, map[string]string{"action": "retry"}, rev(v.Revision)), ControlUpgrade{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// The old heartbeat is still in the database until the next runner exchange.
	if v = upgradeTick(h, ServerObservation{}); v.Status != "running" {
		t.Fatal("old failed revision immediately paused retry", v)
	}
	upgradeAck(h, "linux-1", protocol.RunnerNormal)
	upgradeTick(h, ServerObservation{})
	upgradeAck(h, "linux-1", protocol.RunnerReady)
	if v = upgradeTick(h, ServerObservation{}); v.Status != "running" || upgradeCtl(h, "linux-1").Upgrade == nil {
		t.Fatal("retry cannot issue a fresh download", v)
	}
}

func TestUpgradeRunnerRollbackReceiptReconcilesAfterTimeout(t *testing.T) {
	for _, invalid := range []string{"confirmed", "healthy-before-rollback", "missing", "wrong-candidate", "wrong-current", "wrong-hash", "busy"} {
		t.Run(invalid, func(t *testing.T) {
			h, c := upgradeHarness(t)
			upgradeToRunners(h, c)
			upgradeTick(h, ServerObservation{})
			upgradeAck(h, "linux-1", protocol.RunnerReady)
			upgradeTick(h, ServerObservation{})
			upgradeAck(h, "linux-1", protocol.RunnerReady)
			ctl := upgradeCtl(h, "linux-1")
			b := protocol.UpgradeBinding{UpgradeID: "upgrade-one", ReleaseID: c.Manifest.ReleaseID, ManifestSHA256: c.ManifestSHA256, ArtifactSHA256: strings.Repeat("b", 64), InstanceID: "linux-1", ProcessInstanceID: "failed-candidate"}
			op := h.exec(runner("linux-1"), h.env("runner.upgrade_authorize", protocol.Target{Type: "host", ID: "linux-1"}, b, rev(ctl.Revision)), AuthorizeRunnerUpgrade{})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if invalid == "healthy-before-rollback" {
				candidate := testRuntime(b.ProcessInstanceID)
				candidate.Build.BinarySHA256 = b.ArtifactSHA256
				candidate.JournalVersion = c.Manifest.Storage.RunnerJournal.Max
				candidate.Mode = protocol.RunnerReady
				candidate.UpgradeID = b.UpgradeID
				candidate.AppliedControlRevision = ctl.Revision
				if op := h.versionedHello(b.InstanceID, candidate); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				upgradeTick(h, ServerObservation{})
				h.clock.Advance(8 * time.Second)
				upgradeAck(h, b.InstanceID, protocol.RunnerReady)
				upgradeTick(h, ServerObservation{})
				if upgradeRead(h).Hosts[0].Healthy < 2 {
					t.Fatal("candidate never reached commit-ready")
				}
			}
			h.clock.Advance(31 * time.Minute)
			if v := upgradeTick(h, ServerObservation{}); v.Status != "attention" {
				t.Fatal(v)
			}
			ctl = upgradeCtl(h, "linux-1")
			restored := testRuntime("restored-old")
			restored.Build.BinarySHA256 = strings.Repeat("a", 64)
			restored.Mode = protocol.RunnerReady
			restored.AppliedControlRevision = ctl.Revision
			restored.UpgradeID = b.UpgradeID
			restored.UpdaterReceipt = &protocol.UpdaterReceipt{Binding: b, Phase: "rolled_back", RollbackConfirmed: true, CurrentProcessInstanceID: restored.InstanceID, CurrentSHA256: restored.Build.BinarySHA256}
			switch invalid {
			case "missing":
				restored.UpdaterReceipt = nil
			case "wrong-candidate":
				restored.UpdaterReceipt.Binding.ProcessInstanceID = "other"
			case "wrong-current":
				restored.UpdaterReceipt.CurrentProcessInstanceID = "other"
			case "wrong-hash":
				restored.UpdaterReceipt.CurrentSHA256 = strings.Repeat("e", 64)
			case "busy":
				restored.PendingOperations = 1
			}
			if op := h.versionedHello("linux-1", restored); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			upgradeTick(h, ServerObservation{})
			if invalid != "confirmed" && invalid != "healthy-before-rollback" {
				if upgradeRead(h).Hosts[0].Rollback != nil || upgradeCtl(h, "linux-1").Mode != protocol.RunnerMaintenance {
					t.Fatal("unproven rollback released maintenance")
				}
				return
			}
			r := upgradeRead(h)
			if r.Hosts[0].Rollback == nil || r.Hosts[0].CandidateInstance != b.ProcessInstanceID || upgradeCtl(h, "linux-1").Mode != protocol.RunnerNormal {
				t.Fatal("confirmed old process remains stuck", r)
			}
			notices := h.count("message_notices")
			upgradeTick(h, ServerObservation{})
			if h.count("message_notices") != notices {
				t.Fatal("duplicate rollback notice")
			}
			op = h.exec(owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: b.UpgradeID}, map[string]string{"action": "cancel"}, rev(r.View.Revision)), ControlUpgrade{})
			if op.Status != protocol.OpSucceeded {
				t.Fatal("confirmed rollback cannot cancel", op)
			}
		})
	}
}

func TestUpgradeServerRollbackRestoresMainAndAllowsCancellation(t *testing.T) {
	h, c := upgradeHarness(t)
	if op := startTestUpgrade(h, "server-rollback", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	old := strings.Repeat("a", 64)
	upgradeTick(h, ServerObservation{MainDrained: true, BinarySHA256: old})
	for _, host := range []string{"linux-1", "linux-2"} {
		upgradeAck(h, host, protocol.RunnerMaintenance)
	}
	b := protocol.UpgradeBinding{UpgradeID: "server-rollback", ReleaseID: c.Manifest.ReleaseID, ManifestSHA256: c.ManifestSHA256, ArtifactSHA256: c.ServerHash, InstanceID: "server", ProcessInstanceID: "failed-server"}
	if g, err := h.app.ServerUpgradeAuthorize(t.Context(), b); err != nil || !g.Allowed {
		t.Fatal(g, err)
	}
	o := ServerObservation{InstanceID: "server", ProcessInstanceID: "restored-server", BinarySHA256: old, Healthy: true, MainDrained: true, UpdaterReceipt: &protocol.UpdaterReceipt{Binding: b, Phase: "rolled_back", RollbackConfirmed: true, CurrentProcessInstanceID: "restored-server", CurrentSHA256: old}}
	v := upgradeTick(h, o)
	if v.Status != "attention" || upgradeRead(h).ServerRollback == nil {
		t.Fatal(v)
	}
	if paused, err := h.app.UpgradePausesMain(t.Context()); err != nil || paused {
		t.Fatal("old main remains paused", paused, err)
	}
	for _, host := range []string{"linux-1", "linux-2"} {
		if upgradeCtl(h, host).Mode != protocol.RunnerNormal {
			t.Fatal("rollback left fleet draining")
		}
	}
	op := h.exec(owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: b.UpgradeID}, map[string]string{"action": "cancel"}, rev(v.Revision)), ControlUpgrade{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func TestUpgradeHostConfigFrozenUntilBatchEnds(t *testing.T) {
	h, c := upgradeHarness(t)
	cfg := testConfig()
	other := cfg.Hosts[0]
	other.HostID = "linux-2"
	cfg.Hosts = append(cfg.Hosts, other)
	if op := startTestUpgrade(h, "freeze", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.syncConfig(cfg); op.Status != protocol.OpSucceeded {
		t.Fatal("candidate cannot sync unchanged config", op)
	}
	cfg.Hosts[0].Slots++
	if op := h.syncConfig(cfg); op.Status != protocol.OpFailed {
		t.Fatal("in-flight host mutation accepted", op)
	}
	r := upgradeRead(h)
	if op := h.exec(owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: r.View.UpgradeID}, map[string]string{"action": "cancel"}, rev(r.View.Revision)), ControlUpgrade{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.syncConfig(cfg); op.Status != protocol.OpSucceeded {
		t.Fatal("cancelled batch still freezes config", op)
	}
}

func TestUpgradeManifestCannotInventInstalledVersion(t *testing.T) {
	for _, kind := range []string{"protocol", "server-schema", "runner-journal"} {
		t.Run(kind, func(t *testing.T) {
			h, c := upgradeHarness(t)
			switch kind {
			case "protocol":
				c.Manifest.Protocol = release.Range{Min: protocol.CurrentRunnerProtocol, Max: protocol.CurrentRunnerProtocol + 1}
			case "server-schema":
				c.Manifest.Storage.ServerSchema.Max++
			case "runner-journal":
				c.Manifest.Storage.RunnerJournal.Max++
				c.Manifest.Artifacts[1].SHA256 = strings.Repeat("a", 64)
			}
			if op := startTestUpgrade(h, "metadata", c); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			v := upgradeTick(h, ServerObservation{MainDrained: true, BinarySHA256: c.ServerHash})
			if kind == "runner-journal" {
				v = upgradeTick(h, ServerObservation{})
			}
			if v.Status != "attention" {
				t.Fatal("accepted falsely labeled installed version", v)
			}
		})
	}
}

func TestUpgradeRetiredHostWithOpenRunCannotEscapePreflight(t *testing.T) {
	h, c := upgradeHarness(t)
	cfg := testConfig()
	second := cfg.Hosts[0]
	second.HostID = "linux-2"
	third := cfg.Hosts[0]
	third.HostID = "linux-3"
	cfg.Hosts = append(cfg.Hosts, second, third)
	if op := h.syncConfig(cfg); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	rt := testRuntime("retiring-runner")
	if op := h.versionedHello("linux-3", rt); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.createTask(owner, createTaskPayload{Title: "work on retired host", Spec: testSpec})
	if _, ok := h.claimWith("linux-3", h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-3"}, protocol.ClaimPayload{Worker: h.worker("linux-3"), WorkspaceProtocol: 1, Runtime: rt}, nil)); !ok {
		t.Fatal("missing run")
	}
	cfg.Hosts = cfg.Hosts[:2]
	if op := h.syncConfig(cfg); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := startTestUpgrade(h, "retired-host", c); op.Status != protocol.OpFailed {
		t.Fatal("upgrade omitted an unfinished retired-host run", op)
	}
}

func TestUpgradeCancelledIdentityCannotOverwriteHistory(t *testing.T) {
	h, c := upgradeHarness(t)
	if op := startTestUpgrade(h, "history", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	r := upgradeRead(h)
	if op := h.exec(owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: r.View.UpgradeID}, map[string]string{"action": "cancel"}, rev(r.View.Revision)), ControlUpgrade{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	before := upgradeRead(h)
	_, err := h.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalSystem}, h.env(c.Operation(), protocol.Target{Type: "upgrade", ID: "history"}, map[string]string{"release_id": c.Manifest.ReleaseID}, rev(0)), c)
	if protocol.CodeOf(err) != protocol.CodeConflictPayload {
		t.Fatal("reused upgrade identity overwrote history", err)
	}
	after := upgradeRead(h)
	if before.View.Revision != after.View.Revision || after.View.Status != "cancelled" {
		t.Fatal("historical batch changed")
	}
}

func TestUpgradeCompletedHostGrantDoesNotBlockLaterRetry(t *testing.T) {
	h, c := upgradeHarness(t)
	upgradeToRunners(h, c)
	upgradeTick(h, ServerObservation{})
	upgradeAck(h, "linux-1", protocol.RunnerReady)
	upgradeTick(h, ServerObservation{})
	upgradeAck(h, "linux-1", protocol.RunnerReady)
	ctl := upgradeCtl(h, "linux-1")
	b := protocol.UpgradeBinding{UpgradeID: "upgrade-one", ReleaseID: c.Manifest.ReleaseID, ManifestSHA256: c.ManifestSHA256, ArtifactSHA256: strings.Repeat("b", 64), InstanceID: "linux-1", ProcessInstanceID: "completed-candidate"}
	op := h.exec(runner("linux-1"), h.env("runner.upgrade_authorize", protocol.Target{Type: "host", ID: "linux-1"}, b, rev(ctl.Revision)), AuthorizeRunnerUpgrade{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	candidate := testRuntime(b.ProcessInstanceID)
	candidate.Build.BinarySHA256 = b.ArtifactSHA256
	candidate.JournalVersion = c.Manifest.Storage.RunnerJournal.Max
	candidate.Mode = protocol.RunnerReady
	candidate.AppliedControlRevision = ctl.Revision
	candidate.UpgradeID = b.UpgradeID
	if op := h.versionedHello("linux-1", candidate); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	upgradeTick(h, ServerObservation{})
	h.clock.Advance(8 * time.Second)
	upgradeAck(h, "linux-1", protocol.RunnerReady)
	upgradeTick(h, ServerObservation{})
	upgradeCommitAck(h, b)
	upgradeTick(h, ServerObservation{})
	upgradeAck(h, "linux-1", protocol.RunnerNormal)
	upgradeTick(h, ServerObservation{})
	upgradeAck(h, "linux-1", protocol.RunnerNormal)
	upgradeTick(h, ServerObservation{})
	if r := upgradeRead(h); r.View.Hosts[0].Status != "completed" || r.View.Hosts[1].Phase != "draining" {
		t.Fatal(r.View)
	}
	h.clock.Advance(31 * time.Minute)
	v := upgradeTick(h, ServerObservation{})
	if v.Status != "attention" {
		t.Fatal(v)
	}
	upgradeAck(h, "linux-1", protocol.RunnerNormal)
	upgradeAck(h, "linux-2", protocol.RunnerNormal)
	op = h.exec(owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: v.UpgradeID}, map[string]string{"action": "retry"}, rev(v.Revision)), ControlUpgrade{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal("completed host's historical grant blocked retry", op)
	}
	if v = upgradeTick(h, ServerObservation{}); v.Status != "running" || v.Hosts[0].Status != "completed" || v.Hosts[1].Phase != "draining" {
		t.Fatal(v)
	}
}

func TestUpgradeCancelMarksStagedSupervisorsAfterAttentionReleasedControl(t *testing.T) {
	h, c := upgradeHarness(t)
	c.DrainMillis = 1000
	upgradeToRunners(h, c)
	upgradeTick(h, ServerObservation{})
	h.clock.Advance(time.Second)
	v := upgradeTick(h, ServerObservation{})
	if v.Status != "attention" || upgradeCtl(h, "linux-1").UpgradeID != "" {
		t.Fatal(v)
	}
	op := h.exec(owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: v.UpgradeID}, map[string]string{"action": "cancel"}, rev(v.Revision)), ControlUpgrade{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	for _, host := range []string{"linux-1", "linux-2"} {
		ctl := upgradeCtl(h, host)
		if ctl.CancelledUpgradeID != v.UpgradeID || ctl.Mode != protocol.RunnerNormal {
			t.Fatal("cancel was not durably propagated", ctl)
		}
	}
}

func TestUpgradeWaitsForCancellationReceiptsBeforeNextBatch(t *testing.T) {
	h, c := upgradeHarness(t)
	if op := startTestUpgrade(h, "cancel-first", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	r := upgradeRead(h)
	if op := h.exec(owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: r.View.UpgradeID}, map[string]string{"action": "cancel"}, rev(r.View.Revision)), ControlUpgrade{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := startTestUpgrade(h, "too-early", c); op.Status != protocol.OpFailed {
		t.Fatal("overwrote unacknowledged cancellation", op)
	}
	upgradeAck(h, "linux-1", protocol.RunnerNormal)
	if op := startTestUpgrade(h, "one-receipt", c); op.Status != protocol.OpFailed {
		t.Fatal("ignored other host's staged operation", op)
	}
	upgradeAck(h, "linux-2", protocol.RunnerNormal)
	if op := startTestUpgrade(h, "both-receipts", c); op.Status != protocol.OpSucceeded {
		t.Fatal("next batch blocked after confirmed cancellation", op)
	}
	ids, err := h.app.ListCancelledServerUpgrades(t.Context())
	if err != nil || len(ids) != 1 || ids[0] != "cancel-first" {
		t.Fatal("new batch hid the server's old cancellation intent", ids, err)
	}
}
