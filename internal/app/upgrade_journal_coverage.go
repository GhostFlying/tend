package app

import (
	"context"
	"database/sql"
	"errors"
	"slices"
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

type restartJournal struct {
	runtime     store.HostRuntimeRow
	control     protocol.RunnerControl
	primary     bool
	live        bool
	renewals    []store.LeaseRow
	fixed       bool
	delegated   bool
	localCutoff int64
}

// restartJournals resolves all open Runs, including ones outside the primary
// and draining journals. Journal absence must not hide an orphan from restart.
func restartJournals(ctx context.Context, q store.Querier, batch store.UpgradeRow, host store.UpgradeHost, now int64) ([]restartJournal, error) {
	var journals []restartJournal
	if !host.PrimaryOffline {
		rt, err := store.GetHostRuntime(ctx, q, host.HostID)
		if err != nil {
			return nil, err
		}
		ctl, err := runnerControl(ctx, q, host.HostID, now)
		if err != nil {
			return nil, err
		}
		journals = append(journals, restartJournal{runtime: rt, control: ctl, primary: true})
	}
	ids, err := store.DrainingInstances(ctx, q, host.HostID)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		drain, err := store.GetDrainingRunner(ctx, q, host.HostID, id)
		if err != nil {
			return nil, err
		}
		drain.Online = true // Only a fresh heartbeat can confirm this registered predecessor.
		journals = append(journals, restartJournal{runtime: drain.HostRuntimeRow, control: drain.Control})
	}
	byID := make(map[string]*restartJournal, len(journals))
	for i := range journals {
		j := &journals[i]
		rt := j.runtime.Runtime
		if !batch.Manifest.Protocol.Contains(rt.Protocol) {
			return nil, protocol.Errorf(protocol.CodeInvalidTransition, "runner %s uses protocol %d outside the restart window", rt.InstanceID, rt.Protocol)
		}
		if byID[rt.JournalID] != nil {
			return nil, protocol.Errorf(protocol.CodeConflictPayload, "host %s has multiple owners for runner journal %q", host.HostID, rt.JournalID)
		}
		byID[rt.JournalID] = j
	}

	runs, err := store.ListOpenRunsOnHost(ctx, q, host.HostID)
	if err != nil {
		return nil, err
	}
	for _, run := range runs {
		if err := classifyRestartRun(ctx, q, batch, byID, run, now); err != nil {
			return nil, err
		}
	}
	return journals, nil
}

func classifyRestartRun(ctx context.Context, q store.Querier, batch store.UpgradeRow, journals map[string]*restartJournal, run store.RunRow, now int64) error {
	if run.ObservedState == protocol.RunUnknown {
		stopped, err := store.WindowRunStopped(ctx, q, run.HostID, run.RunID)
		if err != nil || stopped {
			return err
		}
	}
	if !batch.Manifest.Protocol.Contains(run.Protocol) {
		return protocol.Errorf(protocol.CodeInvalidTransition, "unfinished run %s requires a retired protocol", run.RunID)
	}
	journal, err := store.RunJournal(ctx, q, run.RunID)
	if err != nil {
		return err
	}
	j := journals[journal]
	lease, err := store.GetLeaseByRun(ctx, q, run.RunID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if err != nil || lease.HostID != run.HostID {
		return protocol.Errorf(protocol.CodeInvalidTransition, "run %s has no matching original lease for server restart", run.RunID)
	}
	action, err := store.GetAction(ctx, q, run.ActionID)
	if err != nil {
		return err
	}
	if action.TaskID != run.TaskID || action.Generation != run.Generation {
		return protocol.Errorf(protocol.CodeConflictPayload, "run %s has conflicting task or action identity", run.RunID)
	}

	var fixed *protocol.MaintenanceAuthorization
	for _, owner := range journals {
		grant := owner.control.Authorization
		if grant == nil || !slices.Contains(grant.RunIDs, run.RunID) {
			continue
		}
		if owner == j {
			fixed = grant
		} else if grant.DeadlineMillis > now {
			return protocol.Errorf(protocol.CodeConflictPayload, "run %s is named in another journal's maintenance grant", run.RunID)
		}
	}
	var localDeadline int64
	if j != nil && protocol.RunnerOwnsExecution(run.Protocol) && j.runtime.Runtime.MaintenanceCoverage != nil {
		localDeadline = j.runtime.Runtime.MaintenanceCoverage.RunDeadlines[run.RunID]
	}
	if run.ObservedState == protocol.RunUnknown && lease.Status == store.LeaseLapsed {
		if fixed != nil && fixed.DeadlineMillis > now || localDeadline > now {
			return protocol.Errorf(protocol.CodeConflictPayload, "run %s has a lapsed lease and an unexpired maintenance grant", run.RunID)
		}
		if journal != "" {
			if j != nil && j.runtime.Runtime.Protocol >= run.Protocol {
				return nil // Identity stays attached to its original journal. No resource is released.
			}
			if j == nil {
				retired, err := store.JournalRetirementConfirmed(ctx, q, run.HostID, journal)
				if err != nil || retired {
					return err
				}
			}
		}
		protected, err := activeMaintenanceRunProtection(ctx, q, run)
		if err != nil || protected != nil {
			return err
		}
		return protocol.Errorf(protocol.CodeInvalidTransition, "run %s lacks proof of journal isolation for server restart", run.RunID)
	}
	if j == nil || j.runtime.Runtime.Protocol < run.Protocol {
		return protocol.Errorf(protocol.CodeInvalidTransition, "run %s has no compatible journal owner for server restart", run.RunID)
	}
	if run.ObservedState == protocol.RunUnknown || !lease.Current(now) {
		return protocol.Errorf(protocol.CodeInvalidTransition, "run %s must reconcile before server restart", run.RunID)
	}
	j.live = true
	if protocol.RunnerOwnsExecution(run.Protocol) {
		j.delegated = true
		if localDeadline != 0 && (j.localCutoff == 0 || localDeadline < j.localCutoff) {
			j.localCutoff = localDeadline
		}
		if lease.MaintenanceDeadline != 0 {
			if j.control.Mode != protocol.RunnerMaintenance || fixed == nil || fixed.DeadlineMillis != lease.MaintenanceDeadline {
				return protocol.Errorf(protocol.CodeConflictPayload, "run %s has conflicting fixed execution authority", run.RunID)
			}
			j.fixed = true
		}
	} else {
		j.renewals = append(j.renewals, lease)
	}
	return nil
}

// issueJournalCoverage adds a maintenance grant only for renewal-bound Runs.
// A prior fixed grant stays intact, including its original deadline and scope.
func issueJournalCoverage(ctx context.Context, x *Exec, tx *store.Tx, batch store.UpgradeRow, j restartJournal) (*protocol.MaintenanceAuthorization, error) {
	prior := j.control.Authorization
	if prior != nil && prior.DeadlineMillis > x.Now.UnixMilli() {
		if err := checkRestartGrant(j); err != nil {
			return nil, err
		}
		return prior, nil
	}
	if len(j.renewals) == 0 {
		return nil, nil
	}
	// An aggregate-only ACK cannot reveal the old delegated Run deadlines
	// after a new grant replaces its reported identity.
	if rt := j.runtime.Runtime; j.delegated && rt.MaintenanceCoverage == nil && rt.AuthorizationID != "" &&
		!slices.Contains(rt.Capabilities, protocol.CapabilityRunnerMaintenanceCoverage) {
		return nil, protocol.Errorf(protocol.CodeInvalidTransition, "runner %s must reconcile retained coverage before receiving a new restart grant", rt.InstanceID)
	}
	coverage := min(batch.GrantMillis+batch.RestartMillis, (15 * time.Minute).Milliseconds())
	now := x.Now.UnixMilli()
	grant := &protocol.MaintenanceAuthorization{ID: x.IDs.New(protocol.PrefixLease), DeadlineMillis: capUpgradeDeadline(&batch, now+coverage)}
	for _, lease := range j.renewals {
		grant.RunIDs = append(grant.RunIDs, lease.RunID)
		if err := tx.SetLease(ctx, lease.LeaseID, store.LeaseActive, store.LeaseActive, max(lease.ExpiresAt, grant.DeadlineMillis), now); err != nil {
			return nil, err
		}
	}
	return grant, nil
}

func checkRestartGrant(j restartJournal) error {
	grant := j.control.Authorization
	for _, l := range j.renewals {
		if grant == nil || !slices.Contains(grant.RunIDs, l.RunID) || l.ExpiresAt < grant.DeadlineMillis {
			return protocol.Errorf(protocol.CodeInvalidTransition, "run %s lacks persisted restart coverage", l.RunID)
		}
	}
	return nil
}

func prepareRestartJournals(ctx context.Context, x *Exec, tx *store.Tx, batch *store.UpgradeRow, host *store.UpgradeHost) error {
	now := x.Now.UnixMilli()
	journals, err := restartJournals(ctx, tx.Q(), *batch, *host, now)
	if err != nil {
		return err
	}
	for _, j := range journals {
		ctl := j.control
		if j.primary && (ctl.UpgradeID != "" && ctl.UpgradeID != batch.View.UpgradeID || ctl.Authorization != nil && ctl.Authorization.DeadlineMillis > now) {
			return protocol.Errorf(protocol.CodeConflictPayload, "host %s has another restart authority", host.HostID)
		}
		if j.primary && ctl.Mode != protocol.RunnerMaintenance {
			// Entering maintenance enables the runner's fixed Run watchdog.
			// Leave a runner freshness window to restore normal control if
			// the replacement was not authorized while that bound still fit.
			reserve := max(batch.RestartMillis, RunnerOfflineAfter.Milliseconds())
			deadline, err := restartLocalDeadline(j, now, reserve)
			if err != nil {
				return err
			}
			if deadline != 0 {
				batch.Deadline = min(batch.Deadline, deadline-reserve)
			}
		}
		grant, err := issueJournalCoverage(ctx, x, tx, *batch, j)
		if err != nil {
			return err
		}
		if j.primary {
			if batch.View.ServerOnly && ctl.UpgradeID != batch.View.UpgradeID {
				host.MaintenanceHandoff = ctl.Mode == protocol.RunnerMaintenance
			}
			ctl.Revision++
			ctl.Mode = protocol.RunnerMaintenance
			ctl.UpgradeID = batch.View.UpgradeID
			if ctl.CancelledUpgradeID != batch.View.UpgradeID {
				ctl.CancelledUpgradeID = ""
			}
			ctl.ReleaseID = batch.View.ReleaseID
			ctl.Authorization = grant
			if err := tx.PutHostControl(ctx, host.HostID, ctl, now); err != nil {
				return err
			}
		} else if grant != nil && grant != ctl.Authorization {
			ctl.Revision++
			ctl.UpgradeID = batch.View.UpgradeID
			ctl.Authorization = grant
			if err := tx.PutDrainingControl(ctx, host.HostID, j.runtime.Runtime.InstanceID, ctl); err != nil {
				return err
			}
		}
	}
	return nil
}

// A lapsed Run can reattach from its original journal. Close that renewal
// path while a server replacement may already use the frozen coverage set.
func uncoveredRestartReattachment(ctx context.Context, q store.Querier, lease store.LeaseRow, ctl *protocol.RunnerControl, now int64) (bool, error) {
	if lease.Status != store.LeaseLapsed || protocol.RunnerOwnsExecution(lease.Protocol) ||
		ctl != nil && ctl.Authorization != nil && ctl.Authorization.DeadlineMillis > now && lease.ExpiresAt >= ctl.Authorization.DeadlineMillis && slices.Contains(ctl.Authorization.RunIDs, lease.RunID) {
		return false, nil
	}
	batch, err := store.LatestUpgrade(ctx, q)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil || batch.View.Status != "running" || batch.View.Phase != "server_ready" {
		return false, err
	}
	return slices.ContainsFunc(batch.Hosts, func(h store.UpgradeHost) bool {
		return h.HostID == lease.HostID
	}), nil
}

func restartLocalDeadline(j restartJournal, now, restartMillis int64) (int64, error) {
	if !j.delegated {
		return 0, nil
	}
	rt := j.runtime.Runtime
	scope := rt.MaintenanceCoverage
	if slices.Contains(rt.Capabilities, protocol.CapabilityRunnerMaintenanceCoverage) &&
		(scope == nil || scope.ObservedAtMillis == 0 || now-scope.ObservedAtMillis >= RunnerOfflineAfter.Milliseconds()) {
		return 0, protocol.Errorf(protocol.CodeInvalidTransition, "runner %s has no fresh acknowledgement of its local Run coverage", rt.InstanceID)
	}
	deadline := j.localCutoff
	if scope == nil && rt.AuthorizationID != "" {
		// Legacy runners report aggregate coverage. A live fixed execution
		// can still be subject to that bound even after the server cleared
		// the old control; do not assume the scope was empty.
		deadline = rt.AuthorizationDeadlineMillis
		if deadline == 0 {
			return 0, protocol.Errorf(protocol.CodeInvalidTransition, "runner %s has unresolved retained restart coverage", rt.InstanceID)
		}
	}
	if deadline != 0 && deadline-now <= restartMillis {
		return 0, protocol.Errorf(protocol.CodeInvalidTransition, "runner %s local Run coverage cannot fit the restart budget", rt.InstanceID)
	}
	return deadline, nil
}

func journalRestartCutoff(j restartJournal, batch store.UpgradeRow, now, cutoff int64) (int64, error) {
	if !j.primary && !j.live {
		return cutoff, nil
	}
	rt, ctl := j.runtime, j.control
	// A fresh ACK of the maintenance revision still fences all new claims,
	// even when the journal does not need a per-Run restart grant.
	if ctl.Mode != protocol.RunnerMaintenance || j.primary && ctl.UpgradeID != batch.View.UpgradeID ||
		rt.Runtime.AppliedControlRevision != ctl.Revision || rt.Runtime.UpgradeID != ctl.UpgradeID ||
		rt.Runtime.Mode != protocol.RunnerMaintenance && rt.Runtime.Mode != protocol.RunnerReady ||
		!rt.Online || now-rt.LastHeartbeatAt >= RunnerOfflineAfter.Milliseconds() {
		return 0, protocol.Errorf(protocol.CodeInvalidTransition, "runner %s restart fence is not confirmed", rt.Runtime.InstanceID)
	}
	localDeadline, err := restartLocalDeadline(j, now, batch.RestartMillis)
	if err != nil {
		return 0, err
	}
	if localDeadline != 0 {
		cutoff = min(cutoff, localDeadline-batch.RestartMillis)
	}
	if len(j.renewals) == 0 && !j.fixed {
		return cutoff, nil
	}
	if err := checkRestartGrant(j); err != nil {
		return 0, err
	}
	grant := ctl.Authorization
	if grant == nil || rt.Runtime.AuthorizationID != grant.ID || grant.DeadlineMillis-now < 2*batch.RestartMillis {
		return 0, protocol.Errorf(protocol.CodeInvalidTransition, "runner %s restart coverage is not confirmed or too short", rt.Runtime.InstanceID)
	}
	return min(cutoff, min(grant.DeadlineMillis, rt.Runtime.AuthorizationDeadlineMillis)-batch.RestartMillis), nil
}
