package app

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"strings"
	"testing"
	"time"
)

func TestUpgradeRetryWaitsForPendingCandidateCancellation(t *testing.T) {
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
	// The other host drops offline while the bound candidate remains staged.
	h.clock.Advance(61 * time.Second)
	upgradeAck(h, b.InstanceID, protocol.RunnerReady)
	v := upgradeTick(h, ServerObservation{})
	if v.Status != "attention" {
		t.Fatal(v)
	}
	ctl = upgradeCtl(h, b.InstanceID)
	if ctl.CancelledUpgradeID != b.UpgradeID {
		t.Fatal("missing cancellation intent", ctl)
	}
	// Owner retries before the next heartbeat delivers the cancellation proof.
	op := h.exec(owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: b.UpgradeID}, map[string]string{"action": "retry"}, rev(v.Revision)), ControlUpgrade{})
	if op.Status != protocol.OpFailed || op.Error == nil || op.Error.Code != protocol.CodeInvalidTransition {
		t.Fatal("retry bypassed pending cancellation", op)
	}
	if current := upgradeRead(h); current.View.Status != "attention" || current.View.Revision != v.Revision {
		t.Fatal("rejected retry changed batch", current)
	}
	upgradeAck(h, "linux-2", protocol.RunnerNormal)
	rt := upgradeAck(h, b.InstanceID, protocol.RunnerReady)
	rt.Sequence++
	rt.UpdaterReceipt = &protocol.UpdaterReceipt{Binding: b, Phase: "cancelled", CurrentProcessInstanceID: rt.InstanceID, CurrentSHA256: rt.Build.BinarySHA256}
	if _, err := h.app.RuntimeHeartbeat(t.Context(), runner(b.InstanceID), protocol.HeartbeatPayload{Runtime: &rt}); err != nil {
		t.Fatal(err)
	}
	upgradeTick(h, ServerObservation{})
	if got := upgradeCtl(h, b.InstanceID); got.Mode != protocol.RunnerNormal {
		t.Fatalf("durable no-start receipt ignored: %+v", got)
	}
	r := upgradeRead(h)
	if op := h.exec(owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: b.UpgradeID}, map[string]string{"action": "cancel"}, rev(r.View.Revision)), ControlUpgrade{}); op.Status != protocol.OpSucceeded {
		t.Fatal("reconciled cancellation could not finish", op)
	}
}
