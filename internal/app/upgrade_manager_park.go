package app

import (
	"context"
	"database/sql"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func captureUpgradeManagers(ctx context.Context, x *Exec, tx *store.Tx, batch *store.UpgradeRow) error {
	for _, host := range batch.Hosts {
		runs, err := store.ListOpenRunsOnHost(ctx, tx.Q(), host.HostID)
		if err != nil {
			return err
		}
		for _, run := range runs {
			if run.Role != protocol.RoleIssueManager {
				continue
			}
			lease, err := store.GetLeaseByRun(ctx, tx.Q(), run.RunID)
			if err != nil {
				return err
			}

			// A new upgrade window is not permission to repeat an uncertain
			// close on the same execution. Only a definite pre-execution
			// rejection permits a fresh physical request.
			var closeID string
			err = tx.Q().QueryRowContext(ctx, `SELECT request_id FROM manager_native_requests WHERE run_id=? AND method='close' AND (status IN ('pending','started','unknown') OR (COALESCE(json_extract(result_json,'$.error'),'')<>'' AND COALESCE(json_extract(result_json,'$.rejected'),0)=0)) ORDER BY created_at DESC,request_id DESC LIMIT 1`, run.RunID).Scan(&closeID)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if closeID == "" {
				closeID = x.IDs.New(protocol.PrefixRequest)
			}
			batch.View.ManagerParks = append(batch.View.ManagerParks, protocol.UpgradeManagerParkView{TaskID: run.TaskID, RunID: run.RunID, Phase: "waiting_idle"})
			batch.ManagerParks = append(batch.ManagerParks, store.UpgradeManagerPark{TaskID: run.TaskID, RunID: run.RunID, LeaseID: lease.LeaseID, CloseRequestID: closeID})
		}
	}
	return nil
}

// advanceManagerParks queues only an idle manager's existing native close
// protocol. Neither timeout nor missing receipts constitute physical proof.
func advanceManagerParks(ctx context.Context, x *Exec, tx *store.Tx, batch *store.UpgradeRow) (bool, error) {
	if !batch.ParkIdleManagers {
		return true, nil
	}
	ready := true
	batch.View.ManagerParks = nil
	for _, park := range batch.ManagerParks {
		batch.View.ManagerParks = append(batch.View.ManagerParks, protocol.UpgradeManagerParkView{TaskID: park.TaskID, RunID: park.RunID, Phase: "waiting_idle"})
	}
	for index, park := range batch.ManagerParks {
		view := &batch.View.ManagerParks[index]
		run, err := store.GetRun(ctx, tx.Q(), park.RunID)
		if err != nil {
			return false, err
		}
		if run.TaskID != park.TaskID || run.Role != protocol.RoleIssueManager {
			return false, protocol.Errorf(protocol.CodeInvalidTransition, "parked manager identity changed")
		}
		lease, err := store.GetLeaseByRun(ctx, tx.Q(), park.RunID)
		if err != nil {
			return false, err
		}
		if lease.LeaseID != park.LeaseID {
			return false, protocol.Errorf(protocol.CodeInvalidTransition, "parked manager lease changed")
		}

		// Physical exit does not resolve pending or unknown native business.
		// This guard is shared by terminal and live runs.
		var nativePending bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM manager_native_requests WHERE run_id=? AND status IN ('pending','started','unknown') AND method NOT IN ('models','find','usage'))`, run.RunID).Scan(&nativePending); err != nil {
			return false, err
		}
		if nativePending {
			ready = false
			view.Phase = "waiting_receipts"
			continue
		}
		manager, err := store.GetIssueManagerTask(ctx, tx.Q(), run.TaskID)
		if err != nil {
			return false, err
		}
		pending, err := views.ManagerNativeReceiptPending(ctx, tx.Q(), manager)
		if err != nil {
			return false, err
		}
		if pending {
			ready = false
			view.Phase, view.Reason = "waiting_receipts", "Original native receipt requires reconciliation"
			continue
		}
		if terminal := run.ObservedState == protocol.RunFinished || run.ObservedState == protocol.RunFailed; terminal {
			uncertain, err := managerBusinessReceiptsPending(ctx, tx.Q(), run.RunID)
			if err != nil {
				return false, err
			}
			if uncertain {
				ready = false
				view.Phase = "waiting_receipts"
				continue
			}
			view.Phase = "parked"
			continue
		}
		ready = false
		if lease.Status != store.LeaseActive || run.ObservedState == protocol.RunUnknown || run.ObservedState == protocol.RunInterrupted {
			continue
		}
		pending, err = managerBusinessReceiptsPending(ctx, tx.Q(), run.RunID)
		if err != nil {
			return false, err
		}
		if pending {
			view.Reason = "Original native turn or receipt requires reconciliation"
			continue
		}
		// Queued/started business operations must finish under their original IDs
		// before close is admitted. Receipt reconciliation itself remains allowed.
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hosted_executions e JOIN logical_turn_executions le ON le.request_id=e.execution_id JOIN logical_turns lt USING(logical_turn_id) WHERE json_extract(e.private_snapshot,'$.runner_run_id')=? AND lt.status NOT IN ('completed','stopped','awaiting_clarification'))`, run.RunID).Scan(&pending); err != nil {
			return false, err
		}
		if pending {
			view.Phase = "waiting_receipts"
			continue
		}
		var priorRun, priorMethod, priorStatus, priorError string
		err = tx.Q().QueryRowContext(ctx, `SELECT run_id,method,status,COALESCE(json_extract(result_json,'$.error'),'') FROM manager_native_requests WHERE request_id=?`, park.CloseRequestID).Scan(&priorRun, &priorMethod, &priorStatus, &priorError)
		if err == nil {
			if priorRun != park.RunID || priorMethod != "close" {
				return false, protocol.Errorf(protocol.CodeInvalidTransition, "park close request identity changed")
			}
			view.Phase = "closing"
			if priorStatus == "unknown" || priorError != "" {
				view.Phase = "waiting_receipts"
				view.Reason = "Original close outcome requires reconciliation"
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		view.Phase = "closing"
		if _, err := tx.Exec(ctx, `INSERT INTO manager_native_requests(request_id,run_id,method,payload_json,status,created_at) VALUES(?,?,'close','{}','pending',?)`, park.CloseRequestID, park.RunID, x.Now.UnixMilli()); err != nil {
			return false, err
		}
	}
	return ready, nil
}

// managerUpgradePark binds every attachment and teardown to the same original
// task/run/lease. Historical entries preserve only the exact close identity and
// its unresolved physical fence; receipt authority requires a live window or an unresolved
// physical close bound to this same historical run/lease.
func managerUpgradePark(ctx context.Context, q store.Querier, run string, unfinished bool) (store.UpgradeManagerPark, bool, error) {
	var p store.UpgradeManagerPark
	err := q.QueryRowContext(ctx, `SELECT json_extract(p.value,'$.task_id'),json_extract(p.value,'$.run_id'),json_extract(p.value,'$.lease_id'),json_extract(p.value,'$.close_request_id') FROM upgrades u,json_each(u.state_json,'$.manager_parks') p JOIN runs r ON r.run_id=json_extract(p.value,'$.run_id') JOIN leases l ON l.run_id=r.run_id WHERE json_extract(u.state_json,'$.park_idle_managers')=1 AND (?=0 OR u.status IN ('running','attention')) AND r.run_id=? AND r.task_id=json_extract(p.value,'$.task_id') AND r.role='issue_manager' AND l.lease_id=json_extract(p.value,'$.lease_id') ORDER BY u.created_at DESC,u.upgrade_id DESC LIMIT 1`, unfinished, run).Scan(&p.TaskID, &p.RunID, &p.LeaseID, &p.CloseRequestID)
	if errors.Is(err, sql.ErrNoRows) {
		return p, false, nil
	}
	return p, err == nil, err
}

// UpgradeParkReceiptRead admits only a captured runner's read-only original
// attachment while its window or unresolved physical close fences new business.
func (a *App) UpgradeParkReceiptRead(ctx context.Context, conversation, run string) (bool, error) {
	if run == "" {
		return false, nil
	}
	var allowed bool
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		p, found, err := managerUpgradePark(ctx, tx.Q(), run, true)
		if err != nil {
			return err
		}
		if !found {
			var task string
			err := tx.Q().QueryRowContext(ctx, `SELECT r.task_id FROM runs r JOIN leases l USING(run_id) JOIN plan_actions a USING(action_id) WHERE r.run_id=? AND r.role='issue_manager' AND r.observed_state IN ('starting','running','waiting') AND l.status='active' AND json_extract(a.trigger_json,'$.receipt_only')=1 AND EXISTS(SELECT 1 FROM upgrades u,json_each(u.state_json,'$.hosts') h WHERE u.status IN ('running','attention') AND json_extract(u.state_json,'$.park_idle_managers')=1 AND json_extract(h.value,'$.host_id')=r.host_id)`, run).Scan(&task)
			if err == nil {
				m, err := store.GetIssueManagerTask(ctx, tx.Q(), task)
				if err != nil {
					return err
				}
				pending, err := views.ManagerReceiptPending(ctx, tx.Q(), m)
				allowed = pending && m.ConversationID == conversation
				return err
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			prior, err := unresolvedManagerParkClose(ctx, tx.Q(), run)
			if err != nil || prior == nil {
				return err
			}
			p, found, err = managerUpgradePark(ctx, tx.Q(), run, false)
			if err != nil || !found {
				return err
			}
		}
		m, err := store.GetIssueManagerTask(ctx, tx.Q(), p.TaskID)
		if err != nil {
			return err
		}
		allowed = m.ConversationID == conversation
		return nil
	})
	return allowed, err
}

// unresolvedManagerParkClose is independent of window completion: a non-rejected
// close error remains physical uncertainty for this exact historical run/lease.
func unresolvedManagerParkClose(ctx context.Context, q store.Querier, run string) (*protocol.ManagerNativeRequest, error) {
	_, found, err := managerUpgradePark(ctx, q, run, false)
	if err != nil || !found {
		return nil, err
	}
	var prior protocol.ManagerNativeRequest
	var raw string
	err = q.QueryRowContext(ctx, `SELECT request_id,payload_json FROM manager_native_requests WHERE run_id=? AND method='close' AND (status IN ('pending','started','unknown') OR (COALESCE(json_extract(result_json,'$.error'),'')<>'' AND COALESCE(json_extract(result_json,'$.rejected'),0)=0)) ORDER BY created_at DESC,request_id DESC LIMIT 1`, run).Scan(&prior.ID, &raw)
	if err == nil {
		raw, err = store.ResolveJSON(ctx, q, raw)
		if err != nil {
			return nil, err
		}
		prior.Method = "close"
		prior.Payload = []byte(raw)
		return &prior, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return nil, nil
}

// parkTeardownClose never turns attachment cleanup into a second physical
// operation. Only advanceManagerParks may admit the reserved close after idle.
func parkTeardownClose(ctx context.Context, q store.Querier, run string) (*protocol.ManagerNativeRequest, error) {
	prior, err := unresolvedManagerParkClose(ctx, q, run)
	if err != nil || prior != nil {
		return prior, err
	}
	if _, active, err := managerUpgradePark(ctx, q, run, true); err != nil {
		return nil, err
	} else if active {
		return nil, protocol.Errorf(protocol.CodeInvalidTransition, "upgrade window owns physical close admission")
	}
	return nil, nil
}
