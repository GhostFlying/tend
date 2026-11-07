package app

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"testing"
	"time"
)

func TestFault_UpgradeBatchDeadlinePersistsAcrossStageAndRetry(t *testing.T) {
	h, c := upgradeHarness(t)
	if op := startTestUpgrade(h, "bounded-batch", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	original := upgradeRead(h)
	deadline := original.View.CreatedAt.Add(20 * time.Minute).UnixMilli()
	if original.View.OverallDeadlineMillis != deadline || original.DrainMillis != (6*time.Minute).Milliseconds() {
		t.Fatal(original)
	}
	h.clock.Advance(19 * time.Minute)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		r, err := store.LatestUpgrade(t.Context(), tx.Q())
		if err != nil {
			return err
		}
		r.View.Status = "attention"
		r.View.Phase = "runners"
		r.Deadline = 0
		return tx.PutUpgrade(t.Context(), r)
	}); err != nil {
		t.Fatal(err)
	}
	r := upgradeRead(h)
	op := h.exec(owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: r.View.UpgradeID}, protocol.UpgradeControlPayload{Action: "retry"}, rev(r.View.Revision)), ControlUpgrade{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	r = upgradeRead(h)
	if r.Deadline != deadline || r.View.OverallDeadlineMillis != deadline {
		t.Fatal("retry renewed batch", r)
	}
	h.clock.Advance(time.Minute)
	if _, err := h.app.TickUpgrade(t.Context(), ServerObservation{}); err != nil {
		t.Fatal(err)
	}
	r = upgradeRead(h)
	if r.View.Status != "attention" || r.View.OverallDeadlineMillis != deadline {
		t.Fatal(r)
	}
	op = h.exec(owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: r.View.UpgradeID}, protocol.UpgradeControlPayload{Action: "retry"}, rev(r.View.Revision)), ControlUpgrade{})
	expectWorkCode(t, op, protocol.CodeInvalidTransition)
}

func TestFault_UpgradeRestartCoverageCannotOutliveBatch(t *testing.T) {
	h, c := upgradeHarness(t)
	c.DrainMillis = (30 * time.Minute).Milliseconds()
	if op := startTestUpgrade(h, "deadline-coverage", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	r := upgradeRead(h)
	if r.Deadline != r.View.OverallDeadlineMillis {
		t.Fatal("initial stage exceeds batch", r)
	}
	h.clock.Advance(19 * time.Minute)
	v := upgradeTick(h, ServerObservation{MainDrained: true})
	if v.Phase != "server_ready" {
		t.Fatal(v)
	}
	for _, host := range []string{"linux-1", "linux-2"} {
		ctl := upgradeCtl(h, host)
		if ctl.Authorization == nil || ctl.Authorization.DeadlineMillis != r.View.OverallDeadlineMillis {
			t.Fatal("restart coverage exceeds batch", ctl)
		}
		upgradeAck(h, host, protocol.RunnerMaintenance)
	}
	b := protocol.UpgradeBinding{UpgradeID: r.View.UpgradeID, ReleaseID: c.Manifest.ReleaseID, ManifestSHA256: c.ManifestSHA256, ArtifactSHA256: c.ServerHash, InstanceID: c.ServerInstance, ProcessInstanceID: "candidate"}
	if g, err := h.app.ServerUpgradeAuthorize(t.Context(), b); err == nil || g.Allowed {
		t.Fatal("insufficient restart budget authorized", g, err)
	}
}
