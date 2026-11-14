package app

import (
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
