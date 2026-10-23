package app

import (
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestUpgradeLateServerHealthReconcilesWithoutRenewingAuthority(t *testing.T) {
	for _, action := range []string{"retry", "cancel"} {
		t.Run(action, func(t *testing.T) {
			h, c := upgradeHarness(t)
			if op := startTestUpgrade(h, "late-server", c); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			upgradeTick(h, ServerObservation{MainDrained: true, BinarySHA256: strings.Repeat("a", 64)})
			for _, host := range []string{"linux-1", "linux-2"} {
				upgradeAck(h, host, protocol.RunnerMaintenance)
			}
			b := protocol.UpgradeBinding{UpgradeID: "late-server", ReleaseID: c.Manifest.ReleaseID, ManifestSHA256: c.ManifestSHA256, ArtifactSHA256: c.ServerHash, InstanceID: c.ServerInstance, ProcessInstanceID: "late-candidate"}
			if g, err := h.app.ServerUpgradeAuthorize(t.Context(), b); err != nil || !g.Allowed {
				t.Fatal(g, err)
			}
			before := upgradeRead(h)
			h.clock.Advance(4 * time.Minute)
			if v := upgradeTick(h, ServerObservation{}); v.Status != "attention" {
				t.Fatal(v)
			}
			o := ServerObservation{InstanceID: b.InstanceID, ProcessInstanceID: b.ProcessInstanceID, BinarySHA256: b.ArtifactSHA256, Healthy: true, MainDrained: true}
			for _, invalid := range []string{"instance", "process", "hash", "unhealthy", "busy"} {
				bad := o
				switch invalid {
				case "instance":
					bad.InstanceID = "foreign"
				case "process":
					bad.ProcessInstanceID = "foreign"
				case "hash":
					bad.BinarySHA256 = strings.Repeat("e", 64)
				case "unhealthy":
					bad.Healthy = false
				case "busy":
					bad.MainDrained = false
				}
				upgradeTick(h, bad)
				if upgradeRead(h).ServerHealthy != 0 {
					t.Fatal("unconfirmed candidate counted", invalid)
				}
			}
			upgradeTick(h, o)
			upgradeTick(h, o)
			if upgradeRead(h).ServerHealthy != 1 {
				t.Fatal("duplicate health counted")
			}
			h.clock.Advance(5 * time.Second)
			v := upgradeTick(h, o)
			after := upgradeRead(h)
			if v.Status != "attention" || v.Phase != "runners" || after.ServerHealthy != 2 || after.ServerGrantDeadline != before.ServerGrantDeadline || after.Deadline != before.Deadline {
				t.Fatal("late health did not reconcile with fixed authority", after)
			}
			if paused, err := h.app.UpgradePausesMain(t.Context()); err != nil || paused {
				t.Fatal("healthy server left chat paused", paused, err)
			}
			for _, host := range []string{"linux-1", "linux-2"} {
				if ctl := upgradeCtl(h, host); ctl.Mode != protocol.RunnerNormal || ctl.Authorization != nil || ctl.Upgrade != nil {
					t.Fatal("server recovery started another installation", ctl)
				}
				upgradeAck(h, host, protocol.RunnerNormal)
			}
			if g, err := h.app.ServerUpgradeAuthorize(t.Context(), b); err == nil || g.Allowed {
				t.Fatal("expired authority renewed", g, err)
			}
			if op := h.exec(owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: b.UpgradeID}, map[string]string{"action": action}, rev(v.Revision)), ControlUpgrade{}); op.Status != protocol.OpSucceeded {
				t.Fatal("reconciled batch cannot be controlled", op)
			}
		})
	}
}

func TestUpgradeLateRunnerHealthRestoresOnlyPinnedCandidate(t *testing.T) {
	h, c := upgradeHarness(t)
	upgradeToRunners(h, c)
	upgradeTick(h, ServerObservation{})
	upgradeAck(h, "linux-1", protocol.RunnerReady)
	upgradeTick(h, ServerObservation{})
	upgradeAck(h, "linux-1", protocol.RunnerReady)
	ctl := upgradeCtl(h, "linux-1")
	b := protocol.UpgradeBinding{UpgradeID: "upgrade-one", ReleaseID: c.Manifest.ReleaseID, ManifestSHA256: c.ManifestSHA256, ArtifactSHA256: strings.Repeat("b", 64), InstanceID: "linux-1", ProcessInstanceID: "late-runner"}
	if op := h.exec(runner("linux-1"), h.env("runner.upgrade_authorize", protocol.Target{Type: "host", ID: "linux-1"}, b, rev(ctl.Revision)), AuthorizeRunnerUpgrade{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	before := upgradeRead(h)
	h.clock.Advance(31 * time.Minute)
	if v := upgradeTick(h, ServerObservation{}); v.Status != "attention" {
		t.Fatal(v)
	}
	other := upgradeCtl(h, "linux-2")
	rt := testRuntime(b.ProcessInstanceID)
	rt.Build.BinarySHA256 = b.ArtifactSHA256
	rt.JournalVersion = c.Manifest.Storage.RunnerJournal.Max
	rt.Mode = protocol.RunnerReady
	rt.AppliedControlRevision = ctl.Revision
	rt.UpgradeID = b.UpgradeID
	if op := h.versionedHello("linux-1", rt); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	upgradeTick(h, ServerObservation{})
	upgradeTick(h, ServerObservation{})
	if upgradeRead(h).Hosts[0].Healthy != 1 {
		t.Fatal("duplicate heartbeat counted")
	}
	h.clock.Advance(15 * time.Second)
	upgradeAck(h, "linux-1", protocol.RunnerReady)
	upgradeTick(h, ServerObservation{})
	upgradeCommitAck(h, b)
	upgradeTick(h, ServerObservation{})
	if got := upgradeCtl(h, "linux-1"); got.Mode != protocol.RunnerNormal {
		t.Fatal("late healthy candidate remained in maintenance", got)
	}
	upgradeAck(h, "linux-1", protocol.RunnerNormal)
	v := upgradeTick(h, ServerObservation{})
	after := upgradeRead(h)
	if v.Status != "attention" || v.Hosts[0].Status != "completed" || v.Hosts[1].Status != "queued" || after.Deadline != before.Deadline || after.Hosts[0].GrantDeadline != before.Hosts[0].GrantDeadline {
		t.Fatal("recovery advanced unrelated work or renewed deadline", after)
	}
	if got := upgradeCtl(h, "linux-2"); got.Revision != other.Revision || got.Mode != other.Mode {
		t.Fatal("reconciliation started the next host", got)
	}
	upgradeAck(h, "linux-2", protocol.RunnerNormal)
	if op := h.exec(owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: b.UpgradeID}, map[string]string{"action": "retry"}, rev(v.Revision)), ControlUpgrade{}); op.Status != protocol.OpSucceeded {
		t.Fatal("completed candidate expired grant blocks retry", op)
	}
}
