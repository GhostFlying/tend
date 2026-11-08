package app

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"testing"
	"time"
)

func TestSelfDownloadFailureRetry(t *testing.T) {
	h, _, o := selfUpdaterOnlyReady(t)
	rt := upgradeAck(h, "linux-1", protocol.RunnerReady)
	rt.Sequence++
	rt.UpgradeError = "release download or updater confirmation failed"
	if _, err := h.app.RuntimeHeartbeat(t.Context(), runner("linux-1"), protocol.HeartbeatPayload{Runtime: &rt}); err != nil {
		t.Fatal(err)
	}
	v := upgradeTick(h, o)
	if v.Status != "attention" {
		t.Fatal(v)
	}
	op := h.exec(owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: v.UpgradeID}, protocol.UpgradeControlPayload{Action: "retry"}, rev(v.Revision)), ControlUpgrade{})
	if op.Error != nil {
		t.Fatal(op)
	}
	if v = upgradeTick(h, o); v.Status != "running" {
		t.Fatalf("retry consumed the previous failure without a new heartbeat: status=%s phase=%s reason=%s", v.Status, v.Phase, v.Reason)
	}
}
func TestSelfDownloadFailureCancel(t *testing.T) {
	h, _, o := selfUpdaterOnlyReady(t)
	rt := upgradeAck(h, "linux-1", protocol.RunnerReady)
	rt.Sequence++
	rt.UpgradeError = "release download or updater confirmation failed"
	if _, err := h.app.RuntimeHeartbeat(t.Context(), runner("linux-1"), protocol.HeartbeatPayload{Runtime: &rt}); err != nil {
		t.Fatal(err)
	}
	v := upgradeTick(h, o)
	op, err := h.app.Execute(t.Context(), owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: v.UpgradeID}, protocol.UpgradeControlPayload{Action: "cancel"}, rev(v.Revision)), ControlUpgrade{})
	if err != nil || op.Error != nil {
		t.Fatalf("cannot cancel when release download failed before contacting updater: op=%+v err=%v", op, err)
	}
}

func TestSelfRejectionVisibleToOwner(t *testing.T) {
	h, _, o := selfUpdaterOnlyReady(t)
	row := upgradeRead(h)
	rt := upgradeAck(h, "linux-1", protocol.RunnerReady)
	rt.Sequence++
	rt.UpgradeError = "release download or updater confirmation failed"
	rt.Updater = &protocol.UpdaterSelfObservation{InstanceID: "linux-1", Rejection: &protocol.UpdaterSelfRejection{RequestID: row.StartRequestID, UpgradeID: row.View.UpgradeID, ReleaseSetSHA256: row.ReleaseSetSHA256, InstanceID: "linux-1", RejectedAtMillis: h.clock.Now().UnixMilli()}}
	if _, err := h.app.RuntimeHeartbeat(t.Context(), runner("linux-1"), protocol.HeartbeatPayload{Runtime: &rt}); err != nil {
		t.Fatal(err)
	}
	v := upgradeTick(h, o)
	if v.Hosts[0].Updater == nil || v.Hosts[0].Updater.Rejection == nil {
		t.Fatalf("definitive preparation rejection discarded from owner view; host=%+v ctl=%+v", v.Hosts[0], upgradeCtl(h, "linux-1"))
	}
}
func TestSelfUpdaterOnlyRenewsNextHostPhase(t *testing.T) {
	h, _, o := selfUpdaterOnlyReady(t)
	h.clock.Advance(5*time.Minute + 40*time.Second)
	upgradeAck(h, "linux-2", protocol.RunnerNormal)
	selfRuntimeProof(t, h, "linux-1", protocol.RunnerReady)
	if v := upgradeTick(h, o); v.Hosts[0].Phase != "updater_only_restored" {
		t.Fatal(v)
	}
	h.clock.Advance(15 * time.Second)
	upgradeAck(h, "linux-2", protocol.RunnerNormal)
	upgradeAck(h, "linux-1", protocol.RunnerNormal)
	v := upgradeTick(h, o)
	if v.Hosts[0].Phase != "verified" {
		t.Fatal(v)
	}
	h.clock.Advance(10 * time.Second)
	upgradeAck(h, "linux-1", protocol.RunnerNormal)
	upgradeAck(h, "linux-2", protocol.RunnerNormal)
	if v = upgradeTick(h, o); v.Status == "attention" {
		t.Fatalf("first host finished in time but next host inherited its expired phase deadline: %+v", v)
	}
}
