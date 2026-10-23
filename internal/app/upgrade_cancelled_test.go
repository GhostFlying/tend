package app

import (
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestUpgradeLostServerGrantRestoresOnlyWithNoStartProof(t *testing.T) {
	h, c := upgradeHarness(t)
	if op := startTestUpgrade(h, "lost-grant", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	old := ServerObservation{MainDrained: true, Healthy: true, ProcessInstanceID: "old-server", BinarySHA256: strings.Repeat("a", 64), InstanceID: "server"}
	upgradeTick(h, old)
	for _, host := range []string{"linux-1", "linux-2"} {
		upgradeAck(h, host, protocol.RunnerMaintenance)
	}
	b := protocol.UpgradeBinding{UpgradeID: "lost-grant", ReleaseID: c.Manifest.ReleaseID, ManifestSHA256: c.ManifestSHA256, ArtifactSHA256: c.ServerHash, InstanceID: c.ServerInstance, ProcessInstanceID: "never-started"}
	if g, err := h.app.ServerUpgradeAuthorize(t.Context(), b); err != nil || !g.Allowed {
		t.Fatal(g, err)
	}
	before := upgradeRead(h)
	h.clock.Advance(4 * time.Minute)
	for _, host := range []string{"linux-1", "linux-2"} {
		upgradeAck(h, host, protocol.RunnerMaintenance)
	}
	upgradeTick(h, old)
	for _, bad := range []string{"missing", "binding", "current", "hash", "rollback-flag"} {
		o := old
		o.UpdaterReceipt = &protocol.UpdaterReceipt{Binding: b, Phase: "cancelled", CurrentProcessInstanceID: old.ProcessInstanceID, CurrentSHA256: old.BinarySHA256}
		switch bad {
		case "missing":
			o.UpdaterReceipt = nil
		case "binding":
			o.UpdaterReceipt.Binding.ProcessInstanceID = "other"
		case "current":
			o.UpdaterReceipt.CurrentProcessInstanceID = "other"
		case "hash":
			o.UpdaterReceipt.CurrentSHA256 = strings.Repeat("e", 64)
		case "rollback-flag":
			o.UpdaterReceipt.RollbackConfirmed = true
		}
		upgradeTick(h, o)
		paused, err := h.app.UpgradePausesMain(t.Context())
		if err != nil || !paused || upgradeRead(h).ServerRollback != nil {
			t.Fatal("unconfirmed no-start restored", bad, err)
		}
	}
	old.UpdaterReceipt = &protocol.UpdaterReceipt{Binding: b, Phase: "cancelled", CurrentProcessInstanceID: old.ProcessInstanceID, CurrentSHA256: old.BinarySHA256}
	v := upgradeTick(h, old)
	after := upgradeRead(h)
	if v.Status != "attention" || after.ServerRollback == nil || after.ServerRollback.Phase != "cancelled" || after.ServerProcess != b.ProcessInstanceID || after.ServerGrantDeadline != before.ServerGrantDeadline {
		t.Fatal("lost grant not safely restored", after)
	}
	if paused, err := h.app.UpgradePausesMain(t.Context()); err != nil || paused {
		t.Fatal("main stayed paused", err)
	}
	for _, host := range []string{"linux-1", "linux-2"} {
		if upgradeCtl(h, host).Mode != protocol.RunnerNormal {
			t.Fatal("runner stayed fenced")
		}
	}
	if g, err := h.app.ServerUpgradeAuthorize(t.Context(), b); err == nil || g.Allowed {
		t.Fatal("restored batch renewed grant", g, err)
	}
	notices := h.count("message_notices")
	upgradeTick(h, old)
	if h.count("message_notices") != notices {
		t.Fatal("duplicate no-start notice")
	}
	r := upgradeRead(h)
	if op := h.exec(owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: b.UpgradeID}, map[string]string{"action": "cancel"}, rev(r.View.Revision)), ControlUpgrade{}); op.Status != protocol.OpSucceeded {
		t.Fatal("no-start could not cancel", op)
	}
}

func TestUpgradeRunnerAttentionWaitsForNoStartReceipt(t *testing.T) {
	h, c := upgradeHarness(t)
	upgradeToRunners(h, c)
	upgradeTick(h, ServerObservation{})
	upgradeAck(h, "linux-1", protocol.RunnerReady)
	upgradeTick(h, ServerObservation{})
	upgradeAck(h, "linux-1", protocol.RunnerReady)
	ctl := upgradeCtl(h, "linux-1")
	b := protocol.UpgradeBinding{UpgradeID: "upgrade-one", ReleaseID: c.Manifest.ReleaseID, ManifestSHA256: c.ManifestSHA256, ArtifactSHA256: strings.Repeat("b", 64), InstanceID: "linux-1", ProcessInstanceID: "never-started"}
	if op := h.exec(runner(b.InstanceID), h.env("runner.upgrade_authorize", protocol.Target{Type: "host", ID: b.InstanceID}, b, rev(ctl.Revision)), AuthorizeRunnerUpgrade{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	before := upgradeRead(h)
	h.clock.Advance(31 * time.Minute)
	upgradeTick(h, ServerObservation{})
	ctl = upgradeCtl(h, b.InstanceID)
	if ctl.CancelledUpgradeID != b.UpgradeID || ctl.Mode != protocol.RunnerMaintenance || ctl.UpgradeID != b.UpgradeID {
		t.Fatal("attention released without proof", ctl)
	}
	rt := upgradeAck(h, b.InstanceID, protocol.RunnerReady)
	upgradeTick(h, ServerObservation{})
	if upgradeCtl(h, b.InstanceID).Mode != protocol.RunnerMaintenance {
		t.Fatal("ack alone restored")
	}
	rt.Sequence++
	rt.UpdaterReceipt = &protocol.UpdaterReceipt{Binding: b, Phase: "cancelled", CurrentProcessInstanceID: rt.InstanceID, CurrentSHA256: rt.Build.BinarySHA256}
	if _, err := h.app.RuntimeHeartbeat(t.Context(), runner(b.InstanceID), protocol.HeartbeatPayload{Runtime: &rt}); err != nil {
		t.Fatal(err)
	}
	v := upgradeTick(h, ServerObservation{})
	r := upgradeRead(h)
	if r.Hosts[0].Rollback == nil || r.Hosts[0].Rollback.Phase != "cancelled" || upgradeCtl(h, b.InstanceID).Mode != protocol.RunnerNormal || r.Hosts[0].GrantDeadline != before.Hosts[0].GrantDeadline || v.Hosts[0].Phase != "cancelled" {
		t.Fatal("no-start not reconciled", r)
	}
	if strings.Contains(v.Reason, "回退") || strings.Contains(v.Hosts[0].Reason, "恢复旧版本") {
		t.Fatal("no-start pretends rollback", v)
	}
	if op := h.exec(owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: b.UpgradeID}, map[string]string{"action": "cancel"}, rev(v.Revision)), ControlUpgrade{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func TestUpgradeProgressCountsWholeDownloadedBundle(t *testing.T) {
	h, c := upgradeHarness(t)
	if op := startTestUpgrade(h, "bytes", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	total := c.ManifestSize
	for _, a := range c.Manifest.Artifacts {
		total += a.Size
	}
	for _, host := range upgradeRead(h).View.Hosts {
		if host.TotalBytes != total {
			t.Fatalf("download total=%d want manifest+all artifacts %d", host.TotalBytes, total)
		}
	}
}
