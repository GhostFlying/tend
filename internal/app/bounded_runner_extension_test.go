package app

import (
	"reflect"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestBoundedRunnerFreshWindowKeepsOriginalLaunchAndProtectionSet(t *testing.T) {
	h, as, release := boundedRunnerAdmissionFixture(t)
	original, err := h.app.RunnerBoundedMaintenance(t.Context(), release.HostID, release.RecoveryAuthorizationID)
	if err != nil {
		t.Fatal(err)
	}
	p := original.Plan
	p.ExpectedControlRevision = original.ControlRevision
	p.Authorization.RequestID = h.ids.New(protocol.PrefixRequest)
	p.Authorization.ExpectedGeneration++
	p.Authorization.ExpectedPhase = "health"
	p.Authorization.ExpiresAtMillis += time.Minute.Milliseconds()
	p.Authorization.PlanSHA256, err = protocol.RunnerBoundedMaintenancePlanSHA256(p)
	if err != nil || p.Authorization.PlanSHA256 != original.Plan.Authorization.PlanSHA256 {
		t.Fatal("fresh host CAS changed the immutable source plan", err)
	}
	// A late unknown observation remains a new fact. Extending the budget must
	// preserve the original snapshot rather than silently capture it again.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.UpdateRunObserved(t.Context(), as.RunID, protocol.RunUnknown, h.clock.Now().UnixMilli()); err != nil {
			return err
		}
		rt, err := store.GetHostRuntime(t.Context(), tx.Q(), p.HostID)
		if err != nil {
			return err
		}
		rt.Online = true
		rt.Runtime.Mode = protocol.RunnerMaintenance
		return tx.PutHostRuntime(t.Context(), p.HostID, rt)
	}); err != nil {
		t.Fatal(err)
	}
	extended, err := h.app.AuthorizeRunnerBoundedMaintenance(t.Context(), owner, p)
	if err != nil {
		t.Fatal("exact successor could not obtain a fresh external window", err)
	}
	if extended.RecoveryAuthorizationID != original.RecoveryAuthorizationID || extended.Plan.Authorization.RequestID == original.Plan.Authorization.RequestID || !reflect.DeepEqual(extended.Runs, original.Runs) || extended.ProtectionSetSHA256 != original.ProtectionSetSHA256 || extended.ControlRevision != original.ControlRevision+1 {
		t.Fatal("fresh window changed launch lineage or original protection snapshot", extended)
	}
	health, err := h.app.RunnerUpgradeHealth(t.Context(), runner(p.HostID), p.Target)
	if err != nil || !health.Healthy || health.BoundedRecoveryAuthorizationID != original.RecoveryAuthorizationID {
		t.Fatal("fresh window invalidated the existing successor", health, err)
	}
	old, err := h.app.RunnerBoundedMaintenance(t.Context(), p.HostID, original.Plan.Authorization.RequestID)
	if err != nil || !reflect.DeepEqual(old, original) {
		t.Fatal("fresh window overwrote the original authority", old, err)
	}
}
