package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

// Only registration and external updater observations are fixtures. Preparation,
// maintenance grants, server commitment and restored controls use real commands.
func upgradeServerWithSilentRunners(t *testing.T, handoff, sweep, fullSet bool) (*harness, StartUpgrade, ServerObservation) {
	t.Helper()
	var h *harness
	var c StartUpgrade
	if fullSet {
		h, c = selfUpgradeFixture(t)
		selfUpgradeHosts(t, h, true)
		c.ServerUpdaterSupported = true
	} else {
		h, c = upgradeHarness(t)
	}
	if handoff {
		c.ServerCapabilities = []string{release.ServerCapabilityMaintenanceHandoff}
		for _, host := range []string{"linux-1", "linux-2"} {
			enterUpgradeMaintenance(t, h, host, protocol.RunnerReady)
		}
	}
	if op := startPreparedTestUpgrade(h, "reconnect", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	old := ServerObservation{InstanceID: c.ServerInstance, ProcessInstanceID: "old-server", MainDrained: true, BinarySHA256: strings.Repeat("a", 64)}
	if fullSet {
		old.Updater = selfUpgradeObservation(upgradeRead(h), c.ServerInstance, old.ProcessInstanceID, old.BinarySHA256)
		if v := upgradeTick(h, old); v.Phase != "server_draining" {
			t.Fatal(v)
		}
	}
	if v := upgradeTick(h, old); v.Phase != "server_ready" {
		t.Fatal(v)
	}
	for _, host := range []string{"linux-1", "linux-2"} {
		upgradeAck(h, host, protocol.RunnerMaintenance)
	}
	b := protocol.UpgradeBinding{UpgradeID: "reconnect", ReleaseID: c.Manifest.ReleaseID, ManifestSHA256: c.ManifestSHA256, ArtifactSHA256: c.ServerHash, InstanceID: c.ServerInstance, ProcessInstanceID: "replacement-server"}
	if g, err := h.app.ServerUpgradeAuthorize(t.Context(), b); err != nil || !g.Allowed {
		t.Fatalf("server grant: %+v %v", g, err)
	}
	// Simulate a server outage at the exact stale-heartbeat boundary, without
	// changing any upgrade state or pretending that runners acknowledged normal.
	h.clock.Advance(RunnerOfflineAfter)
	if sweep {
		if n, err := h.app.SweepRunnerPresence(t.Context()); err != nil || n != 2 {
			t.Fatalf("presence sweep: %d %v", n, err)
		}
	}
	o := ServerObservation{InstanceID: c.ServerInstance, ProcessInstanceID: b.ProcessInstanceID, BinarySHA256: c.ServerHash, Healthy: true, MainDrained: true}
	if fullSet {
		o.Updater = selfUpgradeObservation(upgradeRead(h), c.ServerInstance, o.ProcessInstanceID, o.BinarySHA256)
	}
	if v := upgradeTick(h, o); v.Phase != "server_checking" {
		t.Fatal(v)
	}
	// Readiness alone must leave maintenance in place.
	if ctl := upgradeCtl(h, "linux-1"); ctl.Mode != protocol.RunnerMaintenance || ctl.Authorization == nil {
		t.Fatal("uncommitted server released maintenance", ctl)
	}
	o.UpdaterReceipt = committedServerForTest(b)
	if v := upgradeTick(h, o); v.Phase != "runners" || v.Status != "running" || upgradeRead(h).ServerCommitted == nil {
		t.Fatal(v)
	}
	return h, c, o
}

func completeReconnectRunner(t *testing.T, h *harness, c StartUpgrade, host string) {
	t.Helper()
	ctl := upgradeCtl(h, host)
	if ctl.Mode != protocol.RunnerMaintenance || ctl.Upgrade != nil {
		t.Fatal("missing rolling drain", ctl)
	}
	upgradeAck(h, host, protocol.RunnerReady)
	upgradeTick(h, ServerObservation{})
	upgradeAck(h, host, protocol.RunnerReady)
	ctl = upgradeCtl(h, host)
	b := protocol.UpgradeBinding{UpgradeID: "reconnect", ReleaseID: c.Manifest.ReleaseID, ManifestSHA256: c.ManifestSHA256, ArtifactSHA256: strings.Repeat("b", 64), InstanceID: host, ProcessInstanceID: "candidate-" + host}
	op := h.exec(runner(host), h.env("runner.upgrade_authorize", protocol.Target{Type: "host", ID: host}, b, rev(ctl.Revision)), AuthorizeRunnerUpgrade{})
	var grant protocol.UpgradeGrant
	if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &grant) != nil || !grant.Allowed {
		t.Fatal("runner not authorized", op)
	}
	candidate := testRuntime(b.ProcessInstanceID)
	candidate.Mode, candidate.UpgradeID = protocol.RunnerReady, b.UpgradeID
	candidate.AppliedControlRevision = ctl.Revision
	candidate.Build.BinarySHA256 = b.ArtifactSHA256
	candidate.JournalVersion = c.Manifest.Storage.RunnerJournal.Max
	candidate.Capabilities = append(candidate.Capabilities, capabilityUpdater)
	if op := h.versionedHello(host, candidate); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	upgradeCommitAck(h, b)
	upgradeTick(h, ServerObservation{})
	if ctl := upgradeCtl(h, host); ctl.Mode != protocol.RunnerNormal {
		t.Fatal("committed runner not restored", ctl)
	}
	upgradeAck(h, host, protocol.RunnerNormal)
	upgradeTick(h, ServerObservation{})
}

func TestUpgradeServerCommitWaitsForTargetReconnect(t *testing.T) {
	for _, handoff := range []bool{false, true} {
		for _, sweep := range []bool{false, true} {
			name := "normal"
			if handoff {
				name = "maintenance-handoff"
			}
			if sweep {
				name += "/offline"
			} else {
				name += "/stale"
			}
			t.Run(name, func(t *testing.T) {
				h, c, _ := upgradeServerWithSilentRunners(t, handoff, sweep, false)
				before, control := upgradeRead(h), upgradeCtl(h, "linux-1")
				notices := h.count("message_notices")
				for range 2 {
					v := upgradeTick(h, ServerObservation{})
					if v.Status != "running" || v.Hosts[0].Status != "queued" || v.Hosts[0].Phase != "reconnecting" {
						t.Fatalf("expected target reconnect prematurely paused: %+v", v)
					}
					h.clock.Advance(time.Second)
				}
				after := upgradeRead(h)
				if after.Deadline != before.Deadline || after.View.OverallDeadlineMillis != before.View.OverallDeadlineMillis || !reflect.DeepEqual(control, upgradeCtl(h, "linux-1")) || h.count("message_notices") != notices {
					t.Fatal("waiting changed authority, deadline or notices")
				}
				mode := protocol.RunnerNormal
				if handoff {
					mode = protocol.RunnerMaintenance
				}
				// The second target is still offline; only the current target's
				// real ACK is needed to begin its rolling runner drain.
				upgradeAck(h, "linux-1", mode)
				v := upgradeTick(h, ServerObservation{})
				if v.Status != "running" || v.Hosts[0].Phase != "draining" || v.Hosts[1].Status != "queued" {
					t.Fatal("current target cannot resume independently", v)
				}
				completeReconnectRunner(t, h, c, "linux-1")
				upgradeAck(h, "linux-2", mode)
				upgradeTick(h, ServerObservation{})
				completeReconnectRunner(t, h, c, "linux-2")
				if v := upgradeTick(h, ServerObservation{}); v.Status != "completed" {
					t.Fatal("original batch did not complete", v)
				}
				h.createReportTask("work after reconnect", temporaryReportSpec())
				rt := upgradeAck(h, "linux-1", protocol.RunnerNormal)
				if _, ok := h.claimInputRun(&rt); !ok {
					t.Fatal("business admission did not recover")
				}
			})
		}
	}
}

func TestUpgradeServerReconnectKeepsFixedDeadline(t *testing.T) {
	for _, cutoff := range []string{"stage", "overall"} {
		t.Run(cutoff, func(t *testing.T) {
			h, _, _ := upgradeServerWithSilentRunners(t, false, true, false)
			before := upgradeRead(h)
			deadline := before.Deadline
			if cutoff == "overall" {
				deadline = before.View.OverallDeadlineMillis
			}
			h.clock.Advance(time.UnixMilli(deadline).Sub(h.clock.Now()))
			v := upgradeTick(h, ServerObservation{})
			after := upgradeRead(h)
			if v.Status != "attention" || after.Deadline != before.Deadline || after.View.OverallDeadlineMillis != before.View.OverallDeadlineMillis || after.Hosts[0].CandidateInstance != "" {
				t.Fatal("reconnect wait extended or bypassed cutoff", v)
			}
			// A late ACK is useful evidence, not permission to resume installs.
			upgradeAck(h, "linux-1", protocol.RunnerNormal)
			if v := upgradeTick(h, ServerObservation{}); v.Status != "attention" || v.Hosts[0].Status != "queued" {
				t.Fatal("late reconnect resumed paused batch", v)
			}
		})
	}
}

func TestUpgradeCompleteReleaseWaitsForTargetReconnect(t *testing.T) {
	for _, handoff := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "maintenance-handoff"}[handoff], func(t *testing.T) {
			h, _, o := upgradeServerWithSilentRunners(t, handoff, true, true)
			before := upgradeRead(h)
			if v := upgradeTick(h, o); v.Status != "running" || v.Hosts[0].Phase != "reconnecting" {
				t.Fatal("complete release prematurely paused", v)
			}
			mode := upgradeCtl(h, "linux-1").Mode
			selfRuntimeProof(t, h, "linux-1", mode)
			if v := upgradeTick(h, o); v.Status != "running" || v.Hosts[0].Phase != "draining" {
				t.Fatal("verified supervisor and target ACK did not resume drain", v)
			}
			if upgradeRead(h).View.OverallDeadlineMillis != before.View.OverallDeadlineMillis {
				t.Fatal("complete release changed original batch deadline")
			}
		})
	}
}

func TestUpgradeReconnectDoesNotHideUnexpectedOfflineTarget(t *testing.T) {
	for _, kind := range []string{"restored-ack", "other-batch", "other-hash", "normal-without-ack"} {
		t.Run(kind, func(t *testing.T) {
			h, _, _ := upgradeServerWithSilentRunners(t, false, true, false)
			// A fresh real heartbeat supersedes the pre-restart maintenance
			// observation. Its subsequent loss is not expected server reconnect.
			rt := upgradeAck(h, "linux-1", protocol.RunnerMaintenance)
			rt.Sequence++
			switch kind {
			case "other-batch":
				rt.UpgradeID = "different-batch"
			case "other-hash":
				rt.Build.BinarySHA256 = strings.Repeat("e", 64)
			case "normal-without-ack":
				rt.Mode = protocol.RunnerNormal
				rt.AppliedControlRevision--
			}
			if _, err := h.app.RuntimeHeartbeat(t.Context(), runner("linux-1"), protocol.HeartbeatPayload{Runtime: &rt}); err != nil {
				t.Fatal(err)
			}
			h.clock.Advance(RunnerOfflineAfter)
			if v := upgradeTick(h, ServerObservation{}); v.Status != "attention" || v.Reason != "等待升级的主机离线" {
				t.Fatal("unexpected target hidden by reconnect wait", v)
			}
		})
	}
}

func TestUpgradeWithoutServerReplacementStillRejectsOfflineTarget(t *testing.T) {
	h, c := upgradeHarness(t)
	c.ServerHash = strings.Repeat("a", 64)
	c.Manifest.Artifacts[0].SHA256 = c.ServerHash
	if op := startPreparedTestUpgrade(h, "retained-server", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if v := upgradeTick(h, ServerObservation{MainDrained: true, BinarySHA256: c.ServerHash}); v.Phase != "runners" {
		t.Fatal(v)
	}
	h.clock.Advance(RunnerOfflineAfter)
	if v := upgradeTick(h, ServerObservation{}); v.Status != "attention" || v.Reason != "等待升级的主机离线" {
		t.Fatal("retained server gained reconnect exception", v)
	}
}
