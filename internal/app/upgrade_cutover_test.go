package app

import (
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func staleCutoverFleet(t *testing.T) *harness {
	t.Helper()
	h, c := upgradeHarness(t)
	if op := startTestUpgrade(h, "cutover-recovery", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	upgradeTick(h, ServerObservation{MainDrained: true, BinarySHA256: strings.Repeat("a", 64)})
	for _, host := range []string{"linux-1", "linux-2"} {
		upgradeAck(h, host, protocol.RunnerMaintenance)
	}
	binding := protocol.UpgradeBinding{UpgradeID: "cutover-recovery", ReleaseID: c.Manifest.ReleaseID, ManifestSHA256: c.ManifestSHA256, ArtifactSHA256: c.ServerHash, InstanceID: c.ServerInstance, ProcessInstanceID: "new-server"}
	if grant, err := h.app.ServerUpgradeAuthorize(t.Context(), binding); err != nil || !grant.Allowed {
		t.Fatal(grant, err)
	}
	h.clock.Advance(RunnerOfflineAfter + 10*time.Second)
	if _, err := h.app.SweepRunnerPresence(t.Context()); err != nil {
		t.Fatal(err)
	}
	observation := ServerObservation{InstanceID: c.ServerInstance, ProcessInstanceID: binding.ProcessInstanceID, BinarySHA256: c.ServerHash, Healthy: true, MainDrained: true}
	upgradeTick(h, observation)
	h.clock.Advance(5 * time.Second)
	if v := upgradeTick(h, observation); v.Status != "running" || v.Phase != "runners" {
		t.Fatal(v)
	}
	return h
}

func TestUpgradeCutoverWaitsForStaleFleet(t *testing.T) {
	h := staleCutoverFleet(t)
	before := upgradeRead(h)
	for range 2 {
		if v := upgradeTick(h, ServerObservation{}); v.Status != "running" || v.Hosts[0].Status != "queued" || v.Hosts[1].Status != "queued" {
			t.Fatal("server cutover observation gap paused or drained fleet", v)
		}
		h.app = New(h.store, h.ids, h.clock.Now)
		h.clock.Advance(5 * time.Second)
		after := upgradeRead(h)
		if after.Deadline != before.Deadline || after.View.OverallDeadlineMillis != before.View.OverallDeadlineMillis {
			t.Fatal("restart extended original deadlines")
		}
	}
	upgradeAck(h, "linux-1", protocol.RunnerNormal)
	if v := upgradeTick(h, ServerObservation{}); v.Status != "running" || v.Hosts[0].Status != "queued" {
		t.Fatal("started before retained host actual ACK", v)
	}
	upgradeAck(h, "linux-2", protocol.RunnerNormal)
	if v := upgradeTick(h, ServerObservation{}); v.Status != "running" || v.Hosts[0].Phase != "draining" {
		t.Fatal("fresh normal ACK did not resume original batch", v)
	}
	if upgradeCtl(h, "linux-2").Mode != protocol.RunnerNormal {
		t.Fatal("retained host drained")
	}
}

func TestUpgradeCutoverWaitKeepsOriginalDeadlines(t *testing.T) {
	h := staleCutoverFleet(t)
	before := upgradeRead(h)
	h.clock.Advance(time.Duration(before.Deadline-h.clock.Now().UnixMilli()) * time.Millisecond)
	if v := upgradeTick(h, ServerObservation{}); v.Status != "attention" {
		t.Fatal("missing ACK did not expire at original stage deadline", v)
	}
	after := upgradeRead(h)
	if after.Deadline != before.Deadline || after.View.OverallDeadlineMillis != before.View.OverallDeadlineMillis {
		t.Fatal("wait renewed deadline")
	}
}

func TestUpgradeCutoverWaitRejectsChangedOrMissingCohort(t *testing.T) {
	for _, kind := range []string{"missing-runtime", "missing-host", "zero-slots", "other-batch", "instance", "generation", "control", "legacy-no-cohort"} {
		t.Run(kind, func(t *testing.T) {
			h := staleCutoverFleet(t)
			before := upgradeRead(h)
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				if kind == "legacy-no-cohort" {
					r := before
					r.CutoverRunners = nil
					return tx.PutUpgrade(t.Context(), r)
				}
				if kind == "missing-runtime" {
					_, err := tx.Exec(t.Context(), `DELETE FROM host_runtime WHERE host_id='linux-2'`)
					return err
				}
				if kind == "missing-host" {
					_, err := tx.Exec(t.Context(), `UPDATE hosts SET status='removed' WHERE host_id='linux-2'`)
					return err
				}
				if kind == "zero-slots" {
					_, err := tx.Exec(t.Context(), `UPDATE hosts SET slots=0 WHERE host_id='linux-2'`)
					return err
				}
				rt, err := store.GetHostRuntime(t.Context(), tx.Q(), "linux-2")
				if err != nil {
					return err
				}
				switch kind {
				case "other-batch":
					rt.Runtime.UpgradeID = "another-batch"
				case "instance":
					rt.Runtime.InstanceID = "replacement"
				case "generation":
					rt.Runtime.Generation++
				case "control":
					ctl, err := runnerControl(t.Context(), tx.Q(), "linux-2", h.clock.Now().UnixMilli())
					if err != nil {
						return err
					}
					ctl.Revision++
					return tx.PutHostControl(t.Context(), "linux-2", ctl, h.clock.Now().UnixMilli())
				}
				return tx.PutHostRuntime(t.Context(), "linux-2", rt)
			}); err != nil {
				t.Fatal(err)
			}
			if v := upgradeTick(h, ServerObservation{}); v.Status != "attention" || v.Hosts[0].Status != "queued" {
				t.Fatal("foreign/missing state acquired cutover wait or drain", v)
			}
		})
	}
}
