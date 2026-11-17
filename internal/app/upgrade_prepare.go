package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func runnerUpgradeTarget(batch store.UpgradeRow, host store.UpgradeHost, prepare bool) *protocol.RunnerUpgradeTarget {
	base := "/runner/v1/releases/" + batch.View.UpgradeID
	target := &protocol.RunnerUpgradeTarget{
		PrepareOnly: prepare, Configuration: host.Configuration,
		ManifestPath: base + "/manifest", ManifestSHA256: batch.ManifestSHA256,
		ManifestSize: batch.ManifestSize, ArtifactBasePath: base + "/artifacts/",
	}
	if batch.ReleaseSet != nil {
		target.ReleaseSet = &protocol.ReleaseSetTarget{Path: base + "/set", SHA256: batch.ReleaseSetSHA256, Size: batch.ReleaseSetSize}
		if needsSelfUpdate(batch) {
			target.Updater = &protocol.UpdaterSelfTarget{RequestID: batch.StartRequestID, ExpiresAtMillis: batch.View.OverallDeadlineMillis}
		}
	}
	return target
}

// Preparation uses the normal authenticated distribution channel while runners
// retain their admission mode. No stop or installer authority is granted here.
func prepareUpgradeHosts(ctx context.Context, x *Exec, tx *store.Tx, batch *store.UpgradeRow) error {
	batch.Deadline = batch.View.OverallDeadlineMillis
	for i, host := range batch.Hosts {
		if batch.View.Hosts[i].Status == "skipped" {
			continue
		}
		control, err := runnerControl(ctx, tx.Q(), host.HostID, x.Now.UnixMilli())
		if err != nil {
			return err
		}
		control.Revision++
		control.UpgradeID, control.ReleaseID = batch.View.UpgradeID, batch.View.ReleaseID
		control.Upgrade = runnerUpgradeTarget(*batch, host, true)
		if err := tx.PutHostControl(ctx, host.HostID, control, x.Now.UnixMilli()); err != nil {
			return err
		}
		batch.View.Hosts[i].Phase = "preparing"
	}
	return nil
}

func advanceUpgradePreparation(ctx context.Context, x *Exec, tx *store.Tx, batch *store.UpgradeRow) (bool, error) {
	if batch.View.Phase != "preparing" {
		return true, nil
	}
	ready := true
	now := x.Now.UnixMilli()
	for i, host := range batch.Hosts {
		view := &batch.View.Hosts[i]
		if view.Status == "skipped" {
			continue
		}
		control, err := runnerControl(ctx, tx.Q(), host.HostID, now)
		if err != nil {
			return false, err
		}
		runtime, err := store.GetHostRuntime(ctx, tx.Q(), host.HostID)
		if err != nil {
			return false, err
		}
		observed := runtime.Runtime
		if !runtime.Online || now-runtime.LastHeartbeatAt >= RunnerOfflineAfter.Milliseconds() || observed.AppliedControlRevision != control.Revision || observed.UpgradeID != batch.View.UpgradeID || (control.Mode == protocol.RunnerNormal && observed.Mode != protocol.RunnerNormal || control.Mode == protocol.RunnerMaintenance && observed.Mode != protocol.RunnerMaintenance && observed.Mode != protocol.RunnerReady) {
			ready = false
			continue
		}
		view.DownloadedBytes, view.Reason = observed.DownloadedBytes, observed.UpgradeError
		if observed.UpgradePhase != "prepared" || observed.UpgradeError != "" {
			ready = false
			continue
		}
		view.Phase = "prepared"
	}
	if !ready {
		return false, nil
	}
	batch.View.Phase, batch.InstallPhase = batch.InstallPhase, ""
	batch.Deadline = capUpgradeDeadline(batch, now+batch.DrainMillis)
	for i, host := range batch.Hosts {
		if host.Configuration == nil {
			continue
		}
		control, err := runnerControl(ctx, tx.Q(), host.HostID, now)
		if err != nil {
			return false, err
		}
		control.Revision++
		control.Mode, control.Upgrade = protocol.RunnerMaintenance, nil
		if err := tx.PutHostControl(ctx, host.HostID, control, now); err != nil {
			return false, err
		}
		batch.View.Hosts[i].Status, batch.View.Hosts[i].Phase = "running", "draining"
	}
	if batch.ParkIdleManagers {
		if err := captureUpgradeManagers(ctx, x, tx, batch); err != nil {
			return false, err
		}
	}
	return true, nil
}
