package app

import (
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestFault_UpgradeCoverageReservesRestartOutsideInstallation(t *testing.T) {
	h, c := upgradeHarness(t)
	if op := startTestUpgrade(h, "installation-budget", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	upgradeTick(h, ServerObservation{MainDrained: true, BinarySHA256: strings.Repeat("a", 64)})
	batch := upgradeRead(h)
	// Real coverage delivery/ACK takes time, before the server can stop.
	h.clock.Advance(30 * time.Second)
	for _, host := range []string{"linux-1", "linux-2"} {
		upgradeAck(h, host, protocol.RunnerMaintenance)
	}
	binding := protocol.UpgradeBinding{UpgradeID: batch.View.UpgradeID, InstanceID: c.ServerInstance, ProcessInstanceID: "candidate", ReleaseID: c.Manifest.ReleaseID, ManifestSHA256: c.ManifestSHA256, ArtifactSHA256: c.ServerHash}
	grant, err := h.app.ServerUpgradeAuthorize(t.Context(), binding)
	if err != nil || !grant.Allowed {
		t.Fatal(grant, err)
	}
	// A normal stop, 70-second backup and spaced health samples must fit;
	// the separate rollback reserve must still remain behind this cutoff.
	if grant.RemainingMillis < (135 * time.Second).Milliseconds() {
		t.Fatalf("installation consumed by coverage and restart reserve: %dms", grant.RemainingMillis)
	}
	original := upgradeRead(h)
	h.clock.Advance(70 * time.Second)
	for _, host := range []string{"linux-1", "linux-2"} {
		upgradeAck(h, host, protocol.RunnerMaintenance)
	}
	replayed, err := h.app.ServerUpgradeAuthorize(t.Context(), binding)
	if err != nil || replayed.ExpiresAt != grant.ExpiresAt || upgradeRead(h).View.OverallDeadlineMillis != original.View.OverallDeadlineMillis {
		t.Fatal("replay extended the original authorization", replayed, err)
	}
}
