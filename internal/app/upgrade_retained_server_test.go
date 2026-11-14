package app

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Process and heartbeat observations are external substitutes. All batch,
// control, health-count and evidence transitions use the normal App commands.
func TestMaintenanceRetainedServerRecordsUsableInstallationEvidence(t *testing.T) {
	h, c := selfUpgradeFixture(t)
	selfUpgradeHosts(t, h, true)
	c.ServerUpdaterSupported = true
	c.Manifest.Artifacts[1].SHA256 = strings.Repeat("a", 64)
	c.ReleaseSet.Artifacts[1].SHA256 = c.Manifest.Artifacts[1].SHA256
	c.Manifest.Storage.RunnerJournal.Max = 9
	if op := startTestUpgrade(h, "retained-maintenance", c); op.Error != nil {
		t.Fatal(op)
	}
	initial := upgradeRead(h)
	o := ServerObservation{InstanceID: c.ServerInstance, MainDrained: true, Healthy: true, BinarySHA256: c.ServerHash, ProcessInstanceID: "retained-server", Commit: c.ReleaseSet.Commit}
	o.Updater = selfUpgradeObservation(initial, c.ServerInstance, o.ProcessInstanceID, c.ServerHash)
	upgradeTick(h, o) // The current supervisor's exact receipt permits draining.
	v := upgradeTick(h, o)
	if v.Phase != "server_checking" || upgradeRead(h).ServerHealthy != 1 {
		t.Fatalf("independent maintenance skipped actual server proof: %+v", v)
	}
	upgradeTick(h, o)
	if upgradeRead(h).ServerHealthy != 1 {
		t.Fatal("one health sample was counted twice")
	}
	h.clock.Advance(5 * time.Second)
	if v := upgradeTick(h, o); v.Phase != "runners" {
		t.Fatal(v)
	}
	for _, host := range []string{"linux-1", "linux-2"} {
		upgradeAck(h, host, protocol.RunnerNormal)
	}
	// Both retained runners still need the existing updater receipt and normal
	// ACK transitions. No completed batch or health count is inserted by fixture.
	for _, host := range []string{"linux-1", "linux-2"} {
		upgradeTick(h, o)
		upgradeAck(h, host, protocol.RunnerReady)
		upgradeTick(h, o)
		selfRuntimeProof(t, h, host, protocol.RunnerReady)
		upgradeTick(h, o)
		upgradeAck(h, host, protocol.RunnerNormal)
		upgradeTick(h, o)
		upgradeAck(h, host, protocol.RunnerNormal)
	}
	v = upgradeTick(h, o)
	if v.Status != "completed" || upgradeRead(h).DeliveryTaskID != "" {
		t.Fatal("normal independent maintenance did not complete", v)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		proof, err := store.DeliveryUpgradeEvidence(t.Context(), tx.Q(), protocol.IssueDeliveryDeployment{ServerInstance: c.ServerInstance, HostIDs: []string{"linux-1", "linux-2"}}, v.UpgradeID, initial.View.CreatedAt.UnixMilli(), h.clock.Now().UnixMilli())
		if err == nil && (proof.ServerProcessInstanceID != o.ProcessInstanceID || proof.SourceCommit != c.ReleaseSet.Commit || len(proof.Hosts) != 2) {
			t.Fatal("original responsibility cannot read the exact installation", proof)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if final := upgradeRead(h); final.View.OverallDeadlineMillis != initial.View.OverallDeadlineMillis || final.ServerHealthy != 2 || h.count("upgrades") != 1 {
		t.Fatal("proof changed authority or repeated the installation", final)
	}
}

func retainedMaintenanceFixture(t *testing.T) (*harness, ServerObservation) {
	t.Helper()
	h, c := selfUpgradeFixture(t)
	selfUpgradeHosts(t, h, true)
	c.ServerUpdaterSupported = true
	c.Manifest.Artifacts[1].SHA256 = strings.Repeat("a", 64)
	c.ReleaseSet.Artifacts[1].SHA256 = c.Manifest.Artifacts[1].SHA256
	c.Manifest.Storage.RunnerJournal.Max = 9
	if op := startTestUpgrade(h, "retained-maintenance", c); op.Error != nil {
		t.Fatal(op)
	}
	o := ServerObservation{InstanceID: c.ServerInstance, MainDrained: true, Healthy: true, BinarySHA256: c.ServerHash, ProcessInstanceID: "retained-server", Commit: c.ReleaseSet.Commit}
	o.Updater = selfUpgradeObservation(upgradeRead(h), c.ServerInstance, o.ProcessInstanceID, c.ServerHash)
	if v := upgradeTick(h, o); v.Phase != "server_draining" {
		t.Fatal(v)
	}
	return h, o
}

func TestMaintenanceRetainedServerPreservesNormalControls(t *testing.T) {
	h, o := retainedMaintenanceFixture(t)
	before := map[string]protocol.RunnerControl{}
	for _, host := range []string{"linux-1", "linux-2"} {
		before[host] = upgradeCtl(h, host)
	}
	upgradeTick(h, o)
	h.clock.Advance(5 * time.Second)
	if v := upgradeTick(h, o); v.Phase != "runners" {
		t.Fatal(v)
	}
	for host, ctl := range before {
		if !reflect.DeepEqual(upgradeCtl(h, host), ctl) {
			t.Fatal("retained server changed an already-normal control", host, ctl, upgradeCtl(h, host))
		}
	}
	// A five-second controller tick can precede the next fifteen-second host
	// heartbeat. The unchanged control ACK must still admit the first drain.
	h.clock.Advance(5 * time.Second)
	v := upgradeTick(h, o)
	if v.Status != "running" || v.Hosts[0].Phase != "draining" || v.Hosts[1].Status != "queued" {
		t.Fatal("healthy fleet paused before its next heartbeat", v)
	}
}

func TestMaintenanceRetainedServerReconcilesExpiredObservation(t *testing.T) {
	for _, elapsed := range []time.Duration{7 * time.Minute, 21 * time.Minute} {
		t.Run(elapsed.String(), func(t *testing.T) {
			h, o := retainedMaintenanceFixture(t)
			proof := o.Updater
			o.Updater = nil
			upgradeTick(h, o)
			pinned := upgradeRead(h)
			if pinned.ServerProcess != o.ProcessInstanceID || pinned.ServerHealthy != 0 || pinned.View.Phase != "server_checking" {
				t.Fatal("unavailable observation lost the original verification path", pinned)
			}
			h.clock.Advance(elapsed)
			upgradeAck(h, "linux-1", protocol.RunnerNormal)
			upgradeAck(h, "linux-2", protocol.RunnerNormal)
			if v := upgradeTick(h, o); v.Status != "attention" {
				t.Fatal(v)
			}
			op, err := h.app.Execute(t.Context(), owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: pinned.View.UpgradeID}, protocol.UpgradeControlPayload{Action: "cancel"}, rev(upgradeRead(h).View.Revision)), ControlUpgrade{})
			if err == nil || op.Status == protocol.OpSucceeded {
				t.Fatal("unverified retained process was treated as reconciled", op, err)
			}
			o.Updater = proof
			o.ProcessInstanceID = "unrelated-process"
			upgradeTick(h, o)
			if upgradeRead(h).ServerHealthy != 0 {
				t.Fatal("a different process supplied the original health proof")
			}
			o.ProcessInstanceID = pinned.ServerProcess
			upgradeTick(h, o)
			upgradeTick(h, o)
			if upgradeRead(h).ServerHealthy != 1 {
				t.Fatal("repeated observation counted as two health samples")
			}
			h.clock.Advance(5 * time.Second)
			v := upgradeTick(h, o)
			after := upgradeRead(h)
			if v.Status != "attention" || v.Phase != "runners" || after.ServerHealthy != 2 || after.Deadline != pinned.Deadline || v.OverallDeadlineMillis != pinned.View.OverallDeadlineMillis {
				t.Fatal("read reconciliation resumed or renewed the expired operation", after)
			}
			for _, host := range []string{"linux-1", "linux-2"} {
				ctl := upgradeCtl(h, host)
				if ctl.Mode != protocol.RunnerNormal || ctl.Authorization != nil || ctl.Upgrade != nil || ctl.UpgradeID != "" {
					t.Fatal("expired reconciliation dispatched another host", ctl)
				}
			}
			if paused, err := h.app.UpgradePausesMain(t.Context()); err != nil || paused {
				t.Fatal("known retained server still fenced unrelated work", paused, err)
			}
			op = h.exec(owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: v.UpgradeID}, protocol.UpgradeControlPayload{Action: "cancel"}, rev(v.Revision)), ControlUpgrade{})
			if op.Status != protocol.OpSucceeded || upgradeRead(h).View.Status != "cancelled" || h.count("upgrades") != 1 {
				t.Fatal("original expired batch could not cancel normally", op)
			}
		})
	}
}
