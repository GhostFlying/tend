package app

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"strings"
	"testing"
	"time"
)

func selfUpdaterOnlyReady(t *testing.T) (*harness, StartUpgrade, ServerObservation) {
	t.Helper()
	h, c := selfUpgradeFixture(t)
	selfUpgradeHosts(t, h, true)
	c.ServerUpdaterSupported = true
	c.Manifest.Artifacts[1].SHA256 = strings.Repeat("a", 64)
	c.ReleaseSet.Artifacts[1].SHA256 = c.Manifest.Artifacts[1].SHA256
	c.Manifest.Storage.RunnerJournal.Max = 9
	if op := startTestUpgrade(h, "updater-only", c); op.Error != nil {
		t.Fatal(op)
	}
	row := upgradeRead(h)
	o := ServerObservation{MainDrained: true, BinarySHA256: c.ServerHash, ProcessInstanceID: "original-server"}
	o.Updater = selfUpgradeObservation(row, "server", o.ProcessInstanceID, o.BinarySHA256)
	if v := upgradeTick(h, o); v.Phase != "server_draining" {
		t.Fatal(v)
	}
	if v := upgradeTick(h, o); v.Phase != "runners" {
		t.Fatal(v)
	}
	upgradeTick(h, o)
	upgradeAck(h, "linux-1", protocol.RunnerReady)
	upgradeTick(h, o)
	if v := upgradeRead(h).View.Hosts[0]; v.Phase != "ready" {
		t.Fatal(v)
	}
	return h, c, o
}
func selfRuntimeProof(t *testing.T, h *harness, host, mode string) {
	t.Helper()
	rt := upgradeAck(h, host, mode)
	rt.Sequence++
	rt.Build.PID = 91
	rt.Updater = selfUpgradeObservation(upgradeRead(h), host, rt.InstanceID, rt.Build.BinarySHA256)
	if _, err := h.app.RuntimeHeartbeat(t.Context(), runner(host), protocol.HeartbeatPayload{Runtime: &rt}); err != nil {
		t.Fatal(err)
	}
}
func TestSelfUpdaterOnlyNextHostAcknowledgment(t *testing.T) {
	h, _, o := selfUpdaterOnlyReady(t)
	selfRuntimeProof(t, h, "linux-1", protocol.RunnerReady)
	v := upgradeTick(h, o)
	if v.Hosts[0].Phase != "updater_only_restored" {
		t.Fatal(v)
	}
	upgradeAck(h, "linux-1", protocol.RunnerNormal)
	upgradeAck(h, "linux-2", protocol.RunnerNormal)
	v = upgradeTick(h, o)
	if v.Status == "attention" {
		t.Fatalf("normal per-host acknowledgment gap paused batch: %+v; first control=%+v", v, upgradeCtl(h, "linux-1"))
	}
}
func TestSelfUpdaterOnlyRetryPendingHandoff(t *testing.T) {
	h, _, o := selfUpdaterOnlyReady(t)
	grantSelfRunner(t, h, "linux-1", o)
	h.clock.Advance(7 * time.Minute)
	upgradeAck(h, "linux-1", protocol.RunnerReady)
	upgradeAck(h, "linux-2", protocol.RunnerNormal)
	if v := upgradeTick(h, o); v.Status != "attention" {
		t.Fatal(v)
	}
	v := upgradeRead(h).View
	op := h.exec(owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: v.UpgradeID}, protocol.UpgradeControlPayload{Action: "retry"}, rev(v.Revision)), ControlUpgrade{})
	if op.Error != nil {
		t.Fatal(op)
	}
	selfRuntimeProof(t, h, "linux-1", protocol.RunnerReady)
	for range 3 {
		v = upgradeTick(h, o)
	}
	if v.Hosts[0].Phase == "queued" {
		t.Fatalf("retry discarded active handoff phase; verified original target remains stuck queued: %+v; control=%+v", v, upgradeCtl(h, "linux-1"))
	}
}

func grantSelfRunner(t *testing.T, h *harness, host string, o ServerObservation) {
	t.Helper()
	rt := upgradeAck(h, host, protocol.RunnerReady)
	rt.Sequence++
	rt.UpgradePhase = "updater_ready"
	if _, err := h.app.RuntimeHeartbeat(t.Context(), runner(host), protocol.HeartbeatPayload{Runtime: &rt}); err != nil {
		t.Fatal(err)
	}
	upgradeTick(h, o)
	ctl := upgradeCtl(h, host)
	if ctl.Upgrade == nil || ctl.Upgrade.Updater == nil || !ctl.Upgrade.Updater.Authorized {
		t.Fatalf("download readiness did not receive dispatch admission ctl=%+v target=%+v runtime=%+v view=%+v", ctl, ctl.Upgrade.Updater, rt, upgradeRead(h).View)
	}
}
