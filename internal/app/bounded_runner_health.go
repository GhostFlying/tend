package app

import (
	"context"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// boundedRunnerUpgradeHealth is a separate explicit force contract. Healthy
// means this exact successor can retain its recovery inventory; it does not
// report business drain, RunnerReady, or resolution of original unknown work.
func (a *App) boundedRunnerUpgradeHealth(ctx context.Context, binding protocol.UpgradeBinding) (*protocol.UpgradeHealth, error) {
	var result *protocol.UpgradeHealth
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		records, err := boundedRunnerProtectionRecords(ctx, tx.Q(), binding.InstanceID)
		if err != nil || len(records) == 0 {
			return err
		}
		known := false
		for _, record := range records {
			known = known || record.Grant.Plan.Target.UpgradeID == binding.UpgradeID
		}
		if !known {
			return nil
		}
		grant := records[len(records)-1].Grant
		if grant.Plan.Target != binding {
			return protocol.Errorf(protocol.CodeConflictPayload, "bounded runner successor authority changed")
		}
		health := protocol.UpgradeHealth{Binding: binding}
		result = &health
		runtime, err := store.GetHostRuntime(ctx, tx.Q(), binding.InstanceID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		control, err := runnerControl(ctx, tx.Q(), binding.InstanceID, a.now().UnixMilli())
		if err != nil {
			return err
		}
		rt := runtime.Runtime
		health.ServerRevision, health.ControlRevision = control.Revision, control.Revision
		health.BoundedRecoveryAuthorizationID = rt.BoundedRecoveryAuthorizationID
		health.JournalVersion = rt.JournalVersion
		health.HeartbeatSequence = uint64(max(0, rt.Sequence))
		health.ServerConfirmed = runtime.Online && a.now().UnixMilli()-runtime.LastHeartbeatAt < RunnerOfflineAfter.Milliseconds() && protocol.RunnerProtocolSupported(rt.Protocol) && boundedRunnerRuntimeMatches(grant, rt)
		health.NewWorkAccepted = control.BoundedRecoveryRelease != nil && control.BoundedRecoveryRelease.RecoveryAuthorizationID == grant.RecoveryAuthorizationID
		health.AdmissionFenced = health.ServerConfirmed && !health.NewWorkAccepted && rt.Mode == protocol.RunnerMaintenance
		health.Healthy = health.AdmissionFenced
		return nil
	})
	return result, err
}
