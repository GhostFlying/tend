package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// AuthorizeServerUpdater serializes ordinary installer admission against owner
// cancellation. It grants no new request identity or extension of either cutoff.
type AuthorizeServerUpdater struct{}

func (AuthorizeServerUpdater) Operation() string { return "upgrades.authorize_supervisor" }
func (AuthorizeServerUpdater) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalSystem)
}
func (AuthorizeServerUpdater) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	r, err := store.GetUpgrade(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	if x.Env.ExpectedControlRevision == nil || *x.Env.ExpectedControlRevision != r.View.Revision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "upgrade changed before supervisor admission")
	}
	if !needsSelfUpdate(r) || r.View.Status != "running" || r.View.Phase != "server_updater" || upgradeBatchExpired(r, x.Now.UnixMilli()) || r.Deadline > 0 && x.Now.UnixMilli() >= r.Deadline {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "supervisor handoff is not dispatchable")
	}
	if r.ServerUpdaterAuthorized {
		return Outcome{Result: true, NoEffect: true}, nil
	}
	r.ServerUpdaterAuthorized = true
	r.View.Revision++
	r.View.UpdatedAt = x.Now
	if err := tx.PutUpgrade(ctx, r); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: true, Events: []Event{upgradeEvent(r)}}, nil
}

func needsSelfUpdate(r store.UpgradeRow) bool { return r.ReleaseSet != nil && r.ReleaseSet.Format >= 2 }

func selfUpdateMatches(r store.UpgradeRow, serviceHash string, o *protocol.UpdaterSelfObservation) bool {
	if !needsSelfUpdate(r) {
		return true
	}
	if o == nil || o.Receipt == nil || o.InstanceID == "" || o.Process.PID <= 1 || o.Process.StartTicks == 0 || o.SupervisedProcess.PID <= 1 {
		return false
	}
	proof := o.Receipt
	if proof.RequestID != r.StartRequestID || proof.UpgradeID != r.View.UpgradeID || proof.ReleaseSetSHA256 != r.ReleaseSetSHA256 || proof.InstanceID != o.InstanceID || proof.CompletedAtMillis <= 0 || proof.RetainedProcess.PID <= 1 || proof.Installed.PID <= 1 || proof.Installed.StartTicks == 0 {
		return false
	}
	os, arch := "", ""
	for _, a := range r.Manifest.Artifacts {
		if a.SHA256 == serviceHash {
			os, arch = a.OS, a.Arch
			break
		}
	}
	for _, a := range r.ReleaseSet.Artifacts {
		if a.Role == "updater" && a.OS == os && a.Arch == arch {
			return a.SHA256 == o.Process.SHA256 && a.SHA256 == proof.Installed.SHA256
		}
	}
	return false
}

func runnerSelfMatches(r store.UpgradeRow, h store.UpgradeHost, rt protocol.RunnerRuntime) bool {
	if !needsSelfUpdate(r) {
		return true
	}
	return rt.Updater != nil && rt.Updater.InstanceID == h.HostID && selfUpdateMatches(r, h.TargetHash, rt.Updater) && rt.Updater.SupervisedProcess.SHA256 == rt.Build.BinarySHA256 && int64(rt.Updater.SupervisedProcess.PID) == rt.Build.PID && rt.Updater.SupervisedProcess.LaunchID == rt.InstanceID
}

func hostNeedsSelfUpdate(r store.UpgradeRow, h store.UpgradeHost) bool {
	return needsSelfUpdate(r) && (!r.SupervisorThenRunners || h.Configuration != nil)
}

func selfUpdateRejected(r store.UpgradeRow, instance string, o *protocol.UpdaterSelfObservation) bool {
	if !needsSelfUpdate(r) || o == nil || o.Rejection == nil || o.InstanceID != instance {
		return false
	}
	p := o.Rejection
	return p.RequestID == r.StartRequestID && p.UpgradeID == r.View.UpgradeID && p.ReleaseSetSHA256 == r.ReleaseSetSHA256 && p.InstanceID == instance && p.RejectedAtMillis > 0
}
