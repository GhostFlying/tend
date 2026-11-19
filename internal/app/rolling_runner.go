package app

import (
	"context"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type AuthorizeRollingRunner struct{}

func (AuthorizeRollingRunner) Operation() string { return "runner.rolling_authorize" }
func (AuthorizeRollingRunner) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalRunner)
}

func (AuthorizeRollingRunner) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var req protocol.RollingRunnerGrantRequest
	if err := decodePayload(x.Env, &req); err != nil {
		return Outcome{}, err
	}
	b := req.Binding
	batch, err := store.GetUpgrade(ctx, tx.Q(), b.UpgradeID)
	if err != nil {
		return Outcome{}, err
	}
	now := x.Now.UnixMilli()
	if !bindingMatches(batch, b) || b.InstanceID != x.Principal.Subject || b.InstanceID != x.Env.Target.ID || b.ProcessInstanceID == "" || x.Env.Target.Type != "host" ||
		batch.View.Status != "running" || batch.View.Phase != "runners" || !needsSelfUpdate(batch) || upgradeBatchExpired(batch, now) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "rolling runner launch is not authorized")
	}
	for i := range batch.Hosts {
		h := &batch.Hosts[i]
		if h.HostID != b.InstanceID {
			continue
		}
		if h.Configuration != nil || h.TargetHash != b.ArtifactSHA256 || req.Updater.InstanceID != h.HostID || !selfUpdateMatches(batch, h.TargetHash, &req.Updater) {
			return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "rolling launch differs from the prepared complete release")
		}
		if h.CandidateInstance != "" {
			if h.CandidateInstance != b.ProcessInstanceID || h.RollingFrom == "" || h.RollingProcess != req.Updater.SupervisedProcess {
				return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "runner installation already bound to a different process")
			}
			return Outcome{NoEffect: true, Result: upgradeGrant(b, h.GrantDeadline, x.Now)}, nil
		}
		proof := req.Updater.SupervisedProcess
		rt, err := store.GetHostRuntime(ctx, tx.Q(), h.HostID)
		if err != nil {
			return Outcome{}, err
		}
		control, err := store.GetHostControl(ctx, tx.Q(), h.HostID)
		if err != nil {
			return Outcome{}, err
		}
		if proof.PID <= 1 || proof.StartTicks == 0 || proof.LaunchID == "" || proof.LaunchID == b.ProcessInstanceID || proof.LaunchID != rt.Runtime.InstanceID ||
			int64(proof.PID) != rt.Runtime.Build.PID || proof.SHA256 != rt.Runtime.Build.BinarySHA256 || req.Updater.Receipt.RetainedProcess != proof {
			return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "predecessor no longer matches the supervisor receipt")
		}
		if !rt.Online || now-rt.LastHeartbeatAt >= RunnerOfflineAfter.Milliseconds() || control.UpgradeID != b.UpgradeID || control.Mode != protocol.RunnerMaintenance ||
			x.Env.ExpectedControlRevision == nil || *x.Env.ExpectedControlRevision != control.Revision || rt.Runtime.AppliedControlRevision != control.Revision ||
			rt.Runtime.UpgradeID != b.UpgradeID || rt.Runtime.Mode == protocol.RunnerNormal || rt.Runtime.PendingClaims != 0 || rt.Runtime.PendingOperations != 0 || batch.View.Hosts[i].Phase != "draining" {
			return Outcome{NoEffect: true, Result: protocol.UpgradeGrant{Binding: b}}, nil
		}
		// The original runner predates host file locks. Wait for initial workspace
		// preparation (report_started) before allowing another process to claim.
		if rt.Runtime.JournalID == "" {
			var preparing bool
			if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE host_id=? AND observed_state='starting')`, h.HostID).Scan(&preparing); err != nil {
				return Outcome{}, err
			}
			if preparing || slices.Contains(rt.Runtime.Blockers, "workspace_operation_active") {
				return Outcome{NoEffect: true, Result: protocol.UpgradeGrant{Binding: b}}, nil
			}
		}
		h.RollingFrom, h.RollingProcess, h.CandidateInstance = rt.Runtime.InstanceID, proof, b.ProcessInstanceID
		h.GrantDeadline = capUpgradeDeadline(&batch, now+batch.GrantMillis)
		control.Revision++
		control.Upgrade = runnerUpgradeTarget(batch, *h, false)
		control.Upgrade.Updater.Authorized = true
		control.Upgrade.Rolling = rt.Runtime.JournalID != ""
		if err := tx.PutHostControl(ctx, h.HostID, control, now); err != nil {
			return Outcome{}, err
		}
		batch.View.Hosts[i].Phase = "installing"
		batch.View.Revision++
		batch.View.UpdatedAt = x.Now
		if err := tx.PutUpgrade(ctx, batch); err != nil {
			return Outcome{}, err
		}
		return Outcome{Result: upgradeGrant(b, h.GrantDeadline, x.Now), Events: []Event{upgradeEvent(batch)}}, nil
	}
	return Outcome{}, protocol.Errorf(protocol.CodeNotFound, "host is not in this upgrade")
}

// RollingTarget returns authority to update the supervisor while retaining its
// child. The separate launch grant serializes against ordinary stop admission.
func (a *App) RollingTarget(ctx context.Context, p protocol.Principal, req protocol.RollingTargetRequest) (protocol.RollingTarget, error) {
	var result protocol.RollingTarget
	if p.Kind != protocol.PrincipalRunner {
		return result, protocol.Errorf(protocol.CodeForbidden, "runner host required")
	}
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		b, err := store.GetUpgrade(ctx, tx.Q(), req.UpgradeID)
		if err != nil {
			return err
		}
		if b.View.Status != "running" || b.View.Phase != "runners" || b.ManifestSHA256 != req.ManifestSHA256 || b.ReleaseSetSHA256 != req.ReleaseSetSHA256 || !needsSelfUpdate(b) || upgradeBatchExpired(b, a.now().UnixMilli()) {
			return protocol.Errorf(protocol.CodeInvalidTransition, "no matching rolling upgrade")
		}
		for i, h := range b.Hosts {
			if h.HostID != p.Subject || h.Configuration != nil || h.PreviousHash == h.TargetHash || b.View.Hosts[i].Phase != "draining" {
				continue
			}
			rt, err := store.GetHostRuntime(ctx, tx.Q(), h.HostID)
			if err != nil {
				return err
			}
			ctl, err := store.GetHostControl(ctx, tx.Q(), h.HostID)
			if err != nil {
				return err
			}
			if !rt.Online || a.now().UnixMilli()-rt.LastHeartbeatAt >= RunnerOfflineAfter.Milliseconds() || ctl.UpgradeID != req.UpgradeID || ctl.Mode != protocol.RunnerMaintenance || rt.Runtime.AppliedControlRevision != ctl.Revision || rt.Runtime.Mode == protocol.RunnerNormal || rt.Runtime.UpgradeID != req.UpgradeID || rt.Runtime.InstanceID != req.Predecessor.LaunchID || rt.Runtime.Build.PID != int64(req.Predecessor.PID) || rt.Runtime.Build.BinarySHA256 != req.Predecessor.SHA256 || req.Predecessor.StartTicks == 0 {
				break
			}
			result.Revision = ctl.Revision
			result.Updater = *runnerUpgradeTarget(b, h, false).Updater
			result.Updater.Authorized = true
			return nil
		}
		return protocol.Errorf(protocol.CodeInvalidTransition, "host is not ready for rolling launch")
	})
	return result, err
}
