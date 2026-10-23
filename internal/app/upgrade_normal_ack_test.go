package app

import (
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// serverNormalAckGap leaves the actual server-first flow immediately after the
// server has issued normal controls, before the next runner heartbeat arrives.
func serverNormalAckGap(t *testing.T) *harness {
	t.Helper()
	h, c := upgradeHarness(t)
	if op := startTestUpgrade(h, "normal-ack-gap", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	upgradeTick(h, ServerObservation{MainDrained: true, BinarySHA256: strings.Repeat("a", 64)})
	for _, host := range []string{"linux-1", "linux-2"} {
		upgradeAck(h, host, protocol.RunnerMaintenance)
	}
	b := protocol.UpgradeBinding{UpgradeID: "normal-ack-gap", ReleaseID: c.Manifest.ReleaseID, ManifestSHA256: c.ManifestSHA256, ArtifactSHA256: c.ServerHash, InstanceID: c.ServerInstance, ProcessInstanceID: "new-server"}
	if g, err := h.app.ServerUpgradeAuthorize(t.Context(), b); err != nil || !g.Allowed {
		t.Fatalf("grant %+v %v", g, err)
	}
	o := ServerObservation{InstanceID: c.ServerInstance, ProcessInstanceID: b.ProcessInstanceID, BinarySHA256: c.ServerHash, Healthy: true, MainDrained: true}
	upgradeTick(h, o)
	h.clock.Advance(5 * time.Second)
	if v := upgradeTick(h, o); v.Status != "running" || v.Phase != "runners" {
		t.Fatal(v)
	}
	return h
}

func TestUpgradeServerRecoveryWaitsForNormalAcknowledgments(t *testing.T) {
	h := serverNormalAckGap(t)
	deadline := upgradeRead(h).Deadline
	for range 3 {
		h.clock.Advance(5 * time.Second)
		if v := upgradeTick(h, ServerObservation{}); v.Status != "running" || v.Hosts[0].Status != "queued" || v.Hosts[1].Status != "queued" {
			t.Fatal("heartbeat gap paused or started maintenance", v)
		}
		if r := upgradeRead(h); r.Deadline != deadline {
			t.Fatal("ack wait extended deadline")
		}
	}
	upgradeAck(h, "linux-1", protocol.RunnerNormal)
	if v := upgradeTick(h, ServerObservation{}); v.Status != "running" || v.Hosts[0].Status != "queued" {
		t.Fatal("retained host not yet normal", v)
	}
	upgradeAck(h, "linux-2", protocol.RunnerNormal)
	if v := upgradeTick(h, ServerObservation{}); v.Status != "running" || v.Hosts[0].Phase != "draining" {
		t.Fatal("acknowledged fleet did not continue", v)
	}
	if upgradeCtl(h, "linux-2").Mode != protocol.RunnerNormal {
		t.Fatal("retained host entered maintenance")
	}
}

func TestUpgradeRestoredHostMetadataWaitsForNormalAcknowledgment(t *testing.T) {
	h, c := upgradeHarness(t)
	upgradeToRunners(h, c)
	// This is the durable cut after a host has acknowledged normal and the
	// coordinator clears its upgrade ID with one further control revision.
	r := upgradeRead(h)
	r.View.Hosts[0].Status = "completed"
	r.View.Hosts[0].Phase = "restored"
	ctl := upgradeCtl(h, "linux-1")
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		rt, err := store.GetHostRuntime(t.Context(), tx.Q(), "linux-1")
		if err != nil {
			return err
		}
		rt.Runtime.Mode = protocol.RunnerNormal
		rt.Runtime.UpgradeID = r.View.UpgradeID
		rt.Runtime.AppliedControlRevision = ctl.Revision
		if err := tx.PutHostRuntime(t.Context(), "linux-1", rt); err != nil {
			return err
		}
		ctl.Revision++
		ctl.UpgradeID = ""
		ctl.ReleaseID = ""
		if err := tx.PutHostControl(t.Context(), "linux-1", ctl, h.clock.Now().UnixMilli()); err != nil {
			return err
		}
		return tx.PutUpgrade(t.Context(), r)
	}); err != nil {
		t.Fatal(err)
	}
	deadline := r.Deadline
	h.clock.Advance(5 * time.Second)
	if v := upgradeTick(h, ServerObservation{}); v.Status != "running" || v.Hosts[1].Status != "queued" {
		t.Fatal("restored metadata gap paused or advanced", v)
	}
	if upgradeRead(h).Deadline != deadline {
		t.Fatal("restored acknowledgment wait extended deadline")
	}
	upgradeAck(h, "linux-1", protocol.RunnerNormal)
	if v := upgradeTick(h, ServerObservation{}); v.Status != "running" || v.Hosts[1].Phase != "draining" {
		t.Fatal("metadata acknowledgment did not unblock next host", v)
	}
}

func TestUpgradeNormalAckWaitNeverTreatsUnavailableHostAsPending(t *testing.T) {
	for _, kind := range []string{"offline", "stale-heartbeat", "zero-slots", "other-batch", "current-revision-maintenance", "control-maintenance"} {
		t.Run(kind, func(t *testing.T) {
			h := serverNormalAckGap(t)
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				rt, err := store.GetHostRuntime(t.Context(), tx.Q(), "linux-2")
				if err != nil {
					return err
				}
				ctl, err := runnerControl(t.Context(), tx.Q(), "linux-2", h.clock.Now().UnixMilli())
				if err != nil {
					return err
				}
				switch kind {
				case "offline":
					rt.Online = false
				case "stale-heartbeat":
					rt.LastHeartbeatAt = h.clock.Now().Add(-RunnerOfflineAfter).UnixMilli()
				case "zero-slots":
					if _, err := tx.Exec(t.Context(), `UPDATE hosts SET slots=0 WHERE host_id='linux-2'`); err != nil {
						return err
					}
				case "other-batch":
					rt.Runtime.UpgradeID = "unrelated-upgrade"
				case "current-revision-maintenance":
					rt.Runtime.Mode = protocol.RunnerMaintenance
					rt.Runtime.AppliedControlRevision = ctl.Revision
				case "control-maintenance":
					ctl.Mode = protocol.RunnerMaintenance
				}
				if err := tx.PutHostControl(t.Context(), "linux-2", ctl, h.clock.Now().UnixMilli()); err != nil {
					return err
				}
				return tx.PutHostRuntime(t.Context(), "linux-2", rt)
			}); err != nil {
				t.Fatal(err)
			}
			if v := upgradeTick(h, ServerObservation{}); v.Status != "attention" {
				t.Fatal("unsafe host treated as pending normal", kind, v)
			}
		})
	}
}
