package app

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
)

func checkDrainingWindow(ctx context.Context, q store.Querier, window release.Range) error {
	var host, instance string
	var version int64
	err := q.QueryRowContext(ctx, `SELECT host_id,instance_id,json_extract(runtime_json,'$.protocol') FROM draining_runners
	 WHERE json_extract(runtime_json,'$.protocol') NOT BETWEEN ? AND ? ORDER BY host_id,instance_id LIMIT 1`, window.Min, window.Max).Scan(&host, &instance, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return protocol.Errorf(protocol.CodeInvalidTransition, "host %s draining runner %s uses protocol %d outside the target range [%d,%d]", host, instance, version, window.Min, window.Max)
}

func issueJournalCoverage(ctx context.Context, x *Exec, tx *store.Tx, batch *store.UpgradeRow, host, journal string) (*protocol.MaintenanceAuthorization, error) {
	now := x.Now.UnixMilli()
	runs, err := store.ListOpenRunsInJournal(ctx, tx.Q(), host, journal)
	if err != nil {
		return nil, err
	}
	coverage := min(batch.GrantMillis+batch.RestartMillis, (15 * time.Minute).Milliseconds())
	grant := &protocol.MaintenanceAuthorization{ID: x.IDs.New(protocol.PrefixLease), DeadlineMillis: capUpgradeDeadline(batch, now+coverage), RunIDs: []string{}}
	for _, run := range runs {
		lease, err := store.GetLeaseByRun(ctx, tx.Q(), run.RunID)
		if err != nil {
			return nil, err
		}
		if lease.Status != store.LeaseActive || run.ObservedState == protocol.RunUnknown {
			return nil, protocol.Errorf(protocol.CodeInvalidTransition, "run %s must reconcile before server restart", run.RunID)
		}
		grant.RunIDs = append(grant.RunIDs, run.RunID)
		if err := tx.SetLease(ctx, lease.LeaseID, store.LeaseActive, store.LeaseActive, max(lease.ExpiresAt, grant.DeadlineMillis), now); err != nil {
			return nil, err
		}
	}
	return grant, nil
}

func coverDrainingJournals(ctx context.Context, x *Exec, tx *store.Tx, batch *store.UpgradeRow, host string) error {
	ids, err := store.DrainingInstances(ctx, tx.Q(), host)
	if err != nil {
		return err
	}
	for _, id := range ids {
		drain, err := store.GetDrainingRunner(ctx, tx.Q(), host, id)
		if err != nil {
			return err
		}
		grant, err := issueJournalCoverage(ctx, x, tx, batch, host, drain.Runtime.JournalID)
		if err != nil {
			return err
		}
		if len(grant.RunIDs) == 0 {
			continue
		}
		drain.Control.Revision++
		drain.Control.UpgradeID = batch.View.UpgradeID
		drain.Control.Authorization = grant
		if err := tx.PutDrainingControl(ctx, host, id, drain.Control); err != nil {
			return err
		}
	}
	return nil
}

func confirmedRestartCutoff(control protocol.RunnerControl, rt store.HostRuntimeRow, now time.Time, restartMillis int64) (int64, error) {
	grant := control.Authorization
	if grant == nil || rt.Runtime.AuthorizationID != grant.ID || rt.Runtime.AppliedControlRevision != control.Revision || !rt.Online || now.UnixMilli()-rt.LastHeartbeatAt >= RunnerOfflineAfter.Milliseconds() || grant.DeadlineMillis-now.UnixMilli() < 2*restartMillis {
		return 0, protocol.Errorf(protocol.CodeInvalidTransition, "runner %s restart coverage is not confirmed or too short", rt.Runtime.InstanceID)
	}
	return min(grant.DeadlineMillis, rt.Runtime.AuthorizationDeadlineMillis) - restartMillis, nil
}

func drainingRestartCutoff(ctx context.Context, q store.Querier, batch store.UpgradeRow, host string, now time.Time, cutoff int64) (int64, error) {
	ids, err := store.DrainingInstances(ctx, q, host)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		drain, err := store.GetDrainingRunner(ctx, q, host, id)
		if err != nil {
			return 0, err
		}
		if drain.Control.UpgradeID != batch.View.UpgradeID {
			continue
		}
		drain.Online = true // Presence of a draining runner is checked against its own heartbeat timestamp.
		covered, err := confirmedRestartCutoff(drain.Control, drain.HostRuntimeRow, now, batch.RestartMillis)
		if err != nil {
			return 0, err
		}
		cutoff = min(cutoff, covered)
	}
	return cutoff, nil
}
