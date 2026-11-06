package app

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestBoundedRunnerHealthConfirmsRecoveryWithoutPretendingBusinessReady(t *testing.T) {
	h, as, release := boundedRunnerAdmissionFixture(t)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		r, err := store.GetHostRuntime(t.Context(), tx.Q(), release.HostID)
		if err != nil {
			return err
		}
		r.Online = true
		r.LastHeartbeatAt = h.clock.Now().UnixMilli()
		r.Runtime.Mode = protocol.RunnerMaintenance
		return tx.PutHostRuntime(t.Context(), release.HostID, r)
	}); err != nil {
		t.Fatal(err)
	}
	beforeRun, beforeLease := h.run(as.RunID), h.lease(as.RunID)
	health, err := h.app.RunnerUpgradeHealth(t.Context(), runner(release.HostID), release.Successor)
	if err != nil || !health.ServerConfirmed || !health.Healthy || !health.AdmissionFenced || health.NewWorkAccepted || health.RollbackSafe || health.BoundedRecoveryAuthorizationID != release.RecoveryAuthorizationID || health.JournalVersion != 17 {
		t.Fatal("exact recovery successor could not be verified independently of drain", health, err)
	}
	if h.run(as.RunID) != beforeRun || h.lease(as.RunID) != beforeLease {
		t.Fatal("recovery health changed original work")
	}
	if op := h.exec(owner, admissionReleaseEnvelope(h, release), ReleaseBoundedRunnerAdmission{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	health, err = h.app.RunnerUpgradeHealth(t.Context(), runner(release.HostID), release.Successor)
	if err != nil || !health.NewWorkAccepted || health.AdmissionFenced || health.Healthy || health.RollbackSafe {
		t.Fatal("issued admission release was hidden by an older heartbeat", health, err)
	}
}
