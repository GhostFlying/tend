package app

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
)

const windowMaintenance = "window_maintenance"

// A draining journal cannot acquire new Runs. Capture the exact process from
// its committed handoff and its still-open leases in the admitting transaction.
func captureWindowStops(ctx context.Context, q store.Querier, batch *store.UpgradeRow, host string) error {
	instances, err := store.DrainingInstances(ctx, q, host)
	if err != nil {
		return err
	}
	for _, id := range instances {
		drain, err := store.GetDrainingRunner(ctx, q, host, id)
		if err != nil {
			return err
		}
		rt := drain.Runtime
		if batch.Manifest.Protocol.Contains(rt.Protocol) {
			continue
		}
		source, err := store.GetUpgrade(ctx, q, drain.UpgradeID)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(source.Hosts, func(h store.UpgradeHost) bool { return h.HostID == host && h.RollingFrom == id && h.Committed != nil })
		if i < 0 {
			return protocol.Errorf(protocol.CodeInvalidTransition, "host %s runner %s has no committed supervisor handoff", host, id)
		}
		process := source.Hosts[i].RollingProcess
		if process.LaunchID != id || process.PID <= 1 || process.StartTicks == 0 || int64(process.PID) != rt.Build.PID || !release.ValidSHA(process.SHA256) || process.SHA256 != rt.Build.BinarySHA256 {
			return protocol.Errorf(protocol.CodeConflictPayload, "host %s runner %s differs from its supervisor receipt", host, id)
		}
		stop := protocol.UpgradeWindowStop{HostID: host, Process: process, SourceUpgradeID: drain.UpgradeID, JournalID: rt.JournalID, Generation: rt.Generation, Protocol: rt.Protocol, State: "pending_stop", Runs: []protocol.LeaseRef{}}
		var granted bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runner_retirements WHERE host_id=? AND instance_id=? AND confirmed_at IS NULL)`, host, id).Scan(&granted); err != nil {
			return err
		}
		if granted {
			_, pending, err := windowForRetirement(ctx, q, host, protocol.RunnerRetirement{UpgradeID: drain.UpgradeID, Process: process})
			if err != nil {
				return err
			}
			if pending != nil {
				return protocol.Errorf(protocol.CodeUnknownOutcome, "host %s runner %s has an unresolved stop from a prior maintenance window", host, id)
			}
			stop.State = "stop_authorized"
		}
		if err := appendWindowStop(ctx, q, batch, stop); err != nil {
			return err
		}
	}
	return nil
}

func appendWindowStop(ctx context.Context, q store.Querier, batch *store.UpgradeRow, stop protocol.UpgradeWindowStop) error {
	host := stop.HostID
	runs, err := store.ListOpenRunsInJournal(ctx, q, host, stop.JournalID)
	if err != nil {
		return err
	}
	for _, run := range runs {
		if run.ObservedState == protocol.RunUnknown {
			released, err := store.WindowRunStopped(ctx, q, host, run.RunID)
			if err != nil {
				return err
			}
			if released {
				continue
			}
		}
		lease, err := store.GetLeaseByRun(ctx, q, run.RunID)
		if err != nil {
			return err
		}
		stop.Runs = append(stop.Runs, protocol.LeaseRef{RunID: run.RunID, LeaseID: lease.LeaseID})
	}
	batch.View.WindowStops = append(batch.View.WindowStops, stop)
	return nil
}

func capturePrimaryStop(ctx context.Context, tx *store.Tx, batch *store.UpgradeRow, host string, rt protocol.RunnerRuntime, ctl protocol.RunnerControl) error {
	process := protocol.RecoveryIdentity{PID: int(rt.Build.PID), LaunchID: rt.InstanceID, SHA256: rt.Build.BinarySHA256}
	if process.PID <= 1 || process.LaunchID == "" || !release.ValidSHA(process.SHA256) {
		return protocol.Errorf(protocol.CodeInvalidTransition, "host %s lacks a supervised process identity", host)
	}
	if o := rt.Updater; o != nil && o.SupervisedProcess.PID == process.PID && o.SupervisedProcess.LaunchID == process.LaunchID && o.SupervisedProcess.SHA256 == process.SHA256 {
		process.StartTicks = o.SupervisedProcess.StartTicks
	}
	stop := protocol.UpgradeWindowStop{Primary: true, HostID: host, Process: process, SourceUpgradeID: batch.View.UpgradeID, JournalID: rt.JournalID, Generation: rt.Generation, Protocol: rt.Protocol, State: "pending_stop", Runs: []protocol.LeaseRef{}}
	if err := appendWindowStop(ctx, tx.Q(), batch, stop); err != nil {
		return err
	}
	ctl.Mode = protocol.RunnerMaintenance
	ctl.Revision++
	ctl.UpgradeID = batch.View.UpgradeID
	return tx.PutHostControl(ctx, host, ctl, batch.View.CreatedAt.UnixMilli())
}

func windowOwnsRun(stops []protocol.UpgradeWindowStop, host, run string) bool {
	for _, stop := range stops {
		if stop.HostID == host && slices.ContainsFunc(stop.Runs, func(ref protocol.LeaseRef) bool { return ref.RunID == run }) {
			return true
		}
	}
	return false
}

// Only the batch that pinned this exact handoff may change its retirement.
func windowForRetirement(ctx context.Context, q store.Querier, host string, input protocol.RunnerRetirement) (store.UpgradeRow, *protocol.UpgradeWindowStop, error) {
	var id string
	err := q.QueryRowContext(ctx, `SELECT u.upgrade_id FROM upgrades u JOIN json_each(u.state_json,'$.view.window_stops') s
		WHERE json_extract(s.value,'$.host_id')=? AND json_extract(s.value,'$.source_upgrade_id')=?
		AND json_extract(s.value,'$.process.launch_id')=? AND (
			json_extract(s.value,'$.state')='stop_authorized' OR (json_extract(s.value,'$.state')='not_stopped' AND u.upgrade_id=?) OR
			(u.phase='window_maintenance' AND u.status IN ('running','attention') AND json_extract(s.value,'$.state') IN ('draining','pending_stop')))
		ORDER BY u.created_at,u.rowid LIMIT 1`, host, input.UpgradeID, input.Process.LaunchID, input.WindowID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return store.UpgradeRow{}, nil, nil
	}
	if err != nil {
		return store.UpgradeRow{}, nil, err
	}
	batch, err := store.GetUpgrade(ctx, q, id)
	if err != nil {
		return batch, nil, err
	}
	for i := range batch.View.WindowStops {
		stop := &batch.View.WindowStops[i]
		expected := stop.Process
		if stop.Primary && expected.StartTicks == 0 {
			expected.StartTicks = input.Process.StartTicks
		}
		if stop.HostID == host && stop.SourceUpgradeID == input.UpgradeID && expected == input.Process {
			return batch, stop, nil
		}
	}
	return batch, nil, nil
}

// A confirmed exit proves loss of execution, never the result of a business
// action. Preserve its original lease and all evidence for late reconciliation.
func confirmWindowStop(ctx context.Context, x *Exec, tx *store.Tx, stop *protocol.UpgradeWindowStop, auth string) ([]Event, error) {
	var events []Event
	for _, ref := range stop.Runs {
		run, err := store.GetRun(ctx, tx.Q(), ref.RunID)
		if err != nil {
			return nil, err
		}
		if run.ObservedState != protocol.RunUnknown && !domain.RunTransitionAllowed(run.ObservedState, protocol.RunUnknown, false) {
			continue
		}
		lease, err := store.GetLeaseByRun(ctx, tx.Q(), ref.RunID)
		if err != nil {
			return nil, err
		}
		if lease.HostID != stop.HostID || lease.LeaseID != ref.LeaseID {
			return nil, protocol.Errorf(protocol.CodeConflictPayload, "original run lease changed")
		}
		if lease.Status == store.LeaseActive {
			if err := tx.SetLease(ctx, lease.LeaseID, store.LeaseActive, store.LeaseLapsed, lease.ExpiresAt, x.Now.UnixMilli()); err != nil {
				return nil, err
			}
		}
		if lease.Status == store.LeaseReleased {
			continue
		}
		if run.ObservedState != protocol.RunUnknown {
			if err := tx.UpdateRunObserved(ctx, run.RunID, protocol.RunUnknown, x.Now.UnixMilli()); err != nil {
				return nil, err
			}
			events = append(events, Event{ObjectType: "run", ObjectID: run.RunID, Kind: "run.unknown", Body: map[string]any{"from": run.ObservedState, "reason": "runner_window_stop", "lease_id": ref.LeaseID, "instance_id": stop.Process.LaunchID}})
			wake, err := recordManagerLeaseProblem(ctx, tx, x.IDs, run, lease.LeaseID, x.Now)
			if err != nil {
				return nil, err
			}
			x.FollowupQueued = x.FollowupQueued || wake
		}
		if _, err := tx.Exec(ctx, `INSERT INTO runner_window_released_leases(run_id,lease_id,authorization_id) VALUES(?,?,?)`, run.RunID, ref.LeaseID, auth); err != nil {
			return nil, err
		}
		stop.UnknownRuns = append(stop.UnknownRuns, run.RunID)
	}
	stop.State = "stopped"
	return events, nil
}

func persistWindowChange(ctx context.Context, x *Exec, tx *store.Tx, batch *store.UpgradeRow) (Event, error) {
	batch.View.Revision++
	batch.View.UpdatedAt = x.Now
	return upgradeEvent(*batch), tx.PutUpgrade(ctx, *batch)
}

func cancelPrimaryFence(ctx context.Context, x *Exec, tx *store.Tx, r store.UpgradeRow, h store.UpgradeHost) error {
	for _, stop := range r.View.WindowStops {
		if !stop.Primary || stop.HostID != h.HostID || stop.State == "stopped" {
			continue
		}
		ctl, err := runnerControl(ctx, tx.Q(), h.HostID, x.Now.UnixMilli())
		if err != nil {
			return err
		}
		if ctl.UpgradeID != r.View.UpgradeID {
			return nil
		}
		ctl.Mode = protocol.RunnerNormal
		if h.MaintenanceHandoff {
			ctl.Mode = protocol.RunnerMaintenance
		}
		ctl.Revision++
		ctl.UpgradeID = ""
		return tx.PutHostControl(ctx, h.HostID, ctl, x.Now.UnixMilli())
	}
	return nil
}

// The fixed supervisor can discover its own accepting process without a live
// runner bridge. Discovery alone never grants permission to send a signal.
func (a *App) PrimaryWindowTarget(ctx context.Context, p protocol.Principal, process protocol.RecoveryIdentity) (string, error) {
	if p.Kind != protocol.PrincipalRunner || process.StartTicks == 0 || process.LaunchID == "" || process.PID <= 1 || !release.ValidSHA(process.SHA256) {
		return "", protocol.Errorf(protocol.CodeInvalidRequest, "supervised runner identity required")
	}
	var id string
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		err := tx.Q().QueryRowContext(ctx, `SELECT u.upgrade_id FROM upgrades u JOIN json_each(u.state_json,'$.view.window_stops') s
          WHERE u.phase='window_maintenance' AND u.status IN ('running','attention') AND json_extract(s.value,'$.primary')=1
          AND json_extract(s.value,'$.host_id')=? AND json_extract(s.value,'$.process.launch_id')=?
          AND json_extract(s.value,'$.process.pid')=? AND json_extract(s.value,'$.process.sha256')=?
          AND json_extract(s.value,'$.process.start_ticks') IN (0,?) AND json_extract(s.value,'$.state') IN ('draining','pending_stop','stop_authorized') LIMIT 1`, p.Subject, process.LaunchID, process.PID, process.SHA256, process.StartTicks).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	})
	return id, err
}

func checkWindowRunOwnership(ctx context.Context, q store.Querier, r store.UpgradeRow, host string) error {
	runs, err := store.ListOpenRunsOnHost(ctx, q, host)
	if err != nil {
		return err
	}
	for _, run := range runs {
		if !r.Manifest.Protocol.Contains(run.Protocol) && !windowOwnsRun(r.View.WindowStops, host, run.RunID) {
			if run.ObservedState == protocol.RunUnknown {
				proved, err := store.WindowRunStopped(ctx, q, host, run.RunID)
				if err != nil {
					return err
				}
				if proved {
					continue
				}
			}
			return protocol.Errorf(protocol.CodeInvalidTransition, "unfinished run %s requires a retired protocol", run.RunID)
		}
	}
	return nil
}

// A supervisor may have persisted a stop intent before dispatch. Preserve the
// exact runner heartbeat sequence so it can reconcile that original grant.
func primaryStopSequence(ctx context.Context, q store.Querier, stop protocol.UpgradeWindowStop) (int64, error) {
	row, err := store.GetHostRuntime(ctx, q, stop.HostID)
	if err != nil {
		return 0, err
	}
	rt := row.Runtime
	if rt.InstanceID != stop.Process.LaunchID || rt.Build.PID != int64(stop.Process.PID) || rt.Build.BinarySHA256 != stop.Process.SHA256 || rt.Generation != stop.Generation || rt.JournalID != stop.JournalID || rt.Protocol != stop.Protocol {
		return 0, protocol.Errorf(protocol.CodeConflictPayload, "accepting process changed during emergency maintenance")
	}
	return rt.Sequence, nil
}
