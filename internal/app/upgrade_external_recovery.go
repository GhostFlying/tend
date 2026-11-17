package app

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
)

// External replacement terminates only the old installation intent. It does not
// resolve any Run, native request, artifact or unknown external business effect.
func recordExternalRecoveries(ctx context.Context, x *Exec, tx *store.Tx, batch *store.UpgradeRow, o ServerObservation) ([]Event, error) {
	type observed struct {
		instance, process, hash string
		receipt                 *protocol.UpdaterReceipt
		host                    bool
	}
	observations := []observed{{o.InstanceID, o.ProcessInstanceID, o.BinarySHA256, o.UpdaterReceipt, false}}
	hosts, err := store.FollowupIDs(ctx, tx.Q(), `SELECT host_id FROM host_runtime WHERE json_extract(runtime_json,'$.updater_receipt.external_recovery')=1 AND NOT EXISTS(SELECT 1 FROM external_recoveries e WHERE e.instance_id=host_id AND e.recovery_id=json_extract(runtime_json,'$.updater_receipt.binding.upgrade_id'))`)
	if err != nil {
		return nil, err
	}
	for _, host := range hosts {
		rt, err := store.GetHostRuntime(ctx, tx.Q(), host)
		if err != nil {
			return nil, err
		}
		if !rt.Online || x.Now.UnixMilli()-rt.LastHeartbeatAt >= RunnerOfflineAfter.Milliseconds() {
			continue
		}
		observations = append(observations, observed{host, rt.Runtime.InstanceID, rt.Runtime.Build.BinarySHA256, rt.Runtime.UpdaterReceipt, true})
	}
	var events []Event
	for _, o := range observations {
		r := o.receipt
		if r == nil || !r.ExternalRecovery || r.Phase != "committed" || !r.CommittedConfirmed || r.RollbackConfirmed || r.Binding.UpgradeID == "" || r.Binding.InstanceID != o.instance || r.CurrentProcessInstanceID != o.process || r.CurrentSHA256 != o.hash || r.Binding.ArtifactSHA256 != o.hash || !release.ValidSHA(o.hash) {
			continue
		}
		raw, err := json.Marshal(r)
		if err != nil {
			return nil, err
		}
		upgrade := r.SupersededUpgradeID
		ownsBatch := batch != nil && batch.View.UpgradeID == upgrade && ((!o.host && batch.ServerInstance == o.instance) || o.host && slices.ContainsFunc(batch.Hosts, func(h store.UpgradeHost) bool { return h.HostID == o.instance }))
		result, err := tx.Exec(ctx, `INSERT INTO external_recoveries(instance_id,recovery_id,receipt_json,upgrade_id,created_at) VALUES(?,?,?,?,?) ON CONFLICT(instance_id,recovery_id) DO NOTHING`, o.instance, r.Binding.UpgradeID, string(raw), upgrade, x.Now.UnixMilli())
		if err != nil {
			return nil, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if count == 0 {
			continue
		}
		if ownsBatch && (batch.View.Status == "running" || batch.View.Status == "attention") {
			batch.View.Status, batch.View.Phase = "cancelled", "external_recovery"
			batch.View.Reason = "An independently committed external replacement superseded this installation. Original uncertain work and unreplaced hosts still require their own reconciliation."
			batch.View.Revision++
			batch.View.UpdatedAt = x.Now
			batch.Deadline = 0
			if err := tx.PutUpgrade(ctx, *batch); err != nil {
				return nil, err
			}
		}
		if o.host {
			control, err := runnerControl(ctx, tx.Q(), o.instance, x.Now.UnixMilli())
			if err != nil {
				return nil, err
			}
			if upgrade != "" && control.UpgradeID == upgrade {
				control.Revision++
				control.Mode = protocol.RunnerNormal
				control.UpgradeID = ""
				control.ReleaseID = ""
				control.Upgrade = nil
				control.Authorization = nil
				if err := tx.PutHostControl(ctx, o.instance, control, x.Now.UnixMilli()); err != nil {
					return nil, err
				}
			}
		}
		events = append(events, Event{ObjectType: "host", ObjectID: o.instance, Kind: "host.external_recovery_committed", Body: map[string]any{"receipt": r, "superseded_upgrade_id": upgrade}})
	}
	return events, nil
}
