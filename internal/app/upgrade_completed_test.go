package app

import (
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestUpgradeCompletedHostRestartCanCancel(t *testing.T) {
	for _, paused := range []bool{false, true} {
		t.Run(map[bool]string{false: "running", true: "attention"}[paused], func(t *testing.T) {
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

			if paused {
				h.clock.Advance(31 * time.Minute)
				upgradeTick(h, ServerObservation{})
			}
			deadline := upgradeRead(h).Deadline
			notices := h.count("message_notices")
			runtime := upgradeAck(h, "linux-1", protocol.RunnerNormal)
			runtime.InstanceID = "completed-successor"
			runtime.Generation++
			runtime.Sequence = 1
			runtime.PendingClaims = 1
			runtime.PendingOperations = 1
			runtime.Blockers = []string{"normal active work"}
			runtime.UpdaterReceipt.CurrentProcessInstanceID = runtime.InstanceID
			if op := h.versionedHello("linux-1", &runtime); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			for range 3 {
				h.clock.Advance(15 * time.Second)
				upgradeAck(h, "linux-1", protocol.RunnerNormal)
				upgradeAck(h, "linux-2", protocol.RunnerMaintenance)
				upgradeTick(h, ServerObservation{})
			}
			r := upgradeRead(h)
			if r.Hosts[0].HealthInstance != runtime.InstanceID || r.Hosts[0].Committed.CurrentProcessInstanceID != runtime.InstanceID || r.Hosts[0].CandidateInstance != b.ProcessInstanceID {
				t.Fatal("completed successor proof not reconciled", r.Hosts[0])
			}
			if r.Deadline != deadline || h.count("message_notices") != notices {
				t.Fatal("completed reconciliation renewed phase or notified again")
			}
			if !committedMatches(r, r.Hosts[0], runtime.UpdaterReceipt, runtime) {
				t.Fatal("invalid successor proof")
			}
			op = h.exec(owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: r.View.UpgradeID}, map[string]string{"action": "cancel"}, rev(r.View.Revision)), ControlUpgrade{})
			if op.Status != protocol.OpSucceeded {
				t.Fatalf("completed successor cannot cancel: %+v", op.Error)
			}
		})
	}
}
