package app

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func captureMaintenanceRunProtection(ctx context.Context, x *Exec, tx *store.Tx, p protocol.CommittedMaintenancePlan) error {
	if p.ReceiptRecoveryStop == nil {
		return nil
	}
	captured := make(map[string]protocol.CommittedRetainedRun, len(p.ReceiptRecoveryStop.Runs))
	for _, item := range p.ReceiptRecoveryStop.Runs {
		if item.TaskID == "" || item.Run.RunID == "" || !maintenanceRunOpen(item.Run.Observed) {
			return protocol.Errorf(protocol.CodeInvalidRequest, "recovery stop requires the complete original open run set")
		}
		if _, exists := captured[item.Run.RunID]; exists {
			return protocol.Errorf(protocol.CodeInvalidRequest, "recovery stop repeats a run identity")
		}
		captured[item.Run.RunID] = item
	}
	open, err := store.MaintenanceOpenRuns(ctx, tx.Q())
	if err != nil {
		return err
	}
	for _, r := range open {
		if _, exists := captured[r.RunID]; !exists {
			return protocol.Errorf(protocol.CodeConflictPayload, "recovery stop omitted an unfinished run")
		}
	}
	for id, item := range captured {
		r, err := store.GetRun(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		action, err := store.GetAction(ctx, tx.Q(), r.ActionID)
		if err != nil {
			return err
		}
		if r.TaskID != item.TaskID || r.Role != item.Run.Role || r.HostID != item.Run.Host || r.Generation != item.Run.Generation || action.TargetSpecVersion != item.Run.SpecVersion {
			return protocol.Errorf(protocol.CodeConflictPayload, "recovery stop run identity changed")
		}
		// Completion committed before the crash remains authoritative. Neither a
		// captured inventory nor receipt uncertainty can revive a terminal run.
		if !maintenanceRunOpen(r.ObservedState) {
			continue
		}
		wanted, err := maintenanceRunWanted(ctx, tx.Q(), r)
		if err != nil || !wanted {
			if err != nil {
				return err
			}
			continue
		}
		requests, err := store.FollowupIDs(ctx, tx.Q(), `SELECT e.execution_id FROM hosted_executions e JOIN main_turns t ON t.request_id=e.execution_id WHERE json_extract(e.private_snapshot,'$.runner_run_id')=? AND (t.status IN ('starting','started','unknown') OR EXISTS(SELECT 1 FROM native_turn_receipts_pending n WHERE n.request_id=e.execution_id)) ORDER BY e.execution_id`, id)
		if err != nil {
			return err
		}
		if len(requests) == 0 && r.Role == protocol.RoleIssueManager {
			continue
		}
		lease, err := store.GetLeaseByRun(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		if lease.Status == store.LeaseReleased || lease.HostID != r.HostID {
			return protocol.Errorf(protocol.CodeConflictPayload, "recovery stop original lease is unavailable")
		}
		raw, err := json.Marshal(requests)
		if err != nil {
			return err
		}
		if err := tx.InsertMaintenanceRunProtection(ctx, store.MaintenanceRunProtection{MaintenanceID: p.MaintenanceID, RunID: id, TaskID: r.TaskID, ActionID: r.ActionID, HostID: r.HostID, LeaseID: lease.LeaseID, OriginalRequestsJSON: string(raw)}, x.Now.UnixMilli()); err != nil {
			return err
		}
	}
	return nil
}

func maintenanceRunOpen(observed protocol.RunObserved) bool {
	switch observed {
	case protocol.RunStarting, protocol.RunRunning, protocol.RunWaiting, protocol.RunInterrupted, protocol.RunUnknown:
		return true
	}
	return false
}

// Explicit task/Issue controls win over deployment protection. This deliberately
// does not use desiredFor, whose receipt-only exception can bypass task pause.
func maintenanceRunWanted(ctx context.Context, q store.Querier, r store.RunRow) (bool, error) {
	if r.DesiredState != protocol.DesiredRun || !maintenanceRunOpen(r.ObservedState) {
		return false, nil
	}
	var wanted bool
	if r.Role != protocol.RoleIssueManager {
		err := q.QueryRowContext(ctx, `SELECT t.control_mode='managed' AND t.lifecycle NOT IN ('completed','cancelled') AND a.status='dispatched' FROM tasks t JOIN plan_actions a ON a.action_id=? WHERE t.task_id=?`, r.ActionID, r.TaskID).Scan(&wanted)
		return wanted, err
	}
	err := q.QueryRowContext(ctx, `SELECT t.control_mode='managed' AND t.lifecycle NOT IN ('completed','cancelled') AND a.status='dispatched' AND m.state='assigned' AND i.state='active' AND i.assignment_state='active' AND i.assignment_epoch=m.assignment_epoch FROM tasks t JOIN plan_actions a ON a.action_id=? JOIN issue_manager_tasks m ON m.task_id=t.task_id JOIN issue_controls i ON i.issue_id=m.issue_id WHERE t.task_id=?`, r.ActionID, r.TaskID).Scan(&wanted)
	return wanted, err
}

func activeMaintenanceRunProtection(ctx context.Context, q store.Querier, r store.RunRow) (*store.MaintenanceRunProtection, error) {
	rows, err := store.MaintenanceProtectedRuns(ctx, q, r.RunID)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	wanted, err := maintenanceRunWanted(ctx, q, r)
	if err != nil || !wanted {
		return nil, err
	}
	lease, err := store.GetLeaseByRun(ctx, q, r.RunID)
	if err != nil {
		return nil, err
	}
	for _, p := range rows {
		if p.TaskID == r.TaskID && p.ActionID == r.ActionID && p.HostID == r.HostID && p.LeaseID == lease.LeaseID && lease.HostID == r.HostID && lease.Status != store.LeaseReleased {
			return &p, nil
		}
	}
	return nil, nil
}

// A physical stop is not proof that an unresolved business execution completed.
// Qualification is frozen at registration, so a delayed original stop cannot
// end the run merely because receipt rereading just completed or DB link won.
func preserveMaintenancePhysicalStop(ctx context.Context, x *Exec, tx *store.Tx, run string, lastSequence int64) (bool, error) {
	r, err := store.GetRun(ctx, tx.Q(), run)
	if err != nil {
		return false, err
	}
	p, err := activeMaintenanceRunProtection(ctx, tx.Q(), r)
	if err != nil || p == nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO maintenance_physical_stops(request_domain,request_id,maintenance_id,run_id,lease_id,operation,payload_json,last_sequence,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, x.Principal.DedupDomain(), x.Env.RequestID, p.MaintenanceID, run, p.LeaseID, x.Env.Operation, string(x.Env.Payload), lastSequence, x.Now.UnixMilli())
	return err == nil, err
}

func maintenanceNativeBusinessFenced(ctx context.Context, q store.Querier, r store.RunRow) (bool, error) {
	var startup bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM server_committed_maintenances p WHERE p.linked=0 AND json_type(p.state_json,'$.plan.receipt_recovery_stop')='object' AND NOT EXISTS(SELECT 1 FROM server_committed_maintenances c WHERE c.parent_id=p.maintenance_id))`).Scan(&startup); err != nil {
		return false, err
	}
	if startup {
		return true, nil
	}
	p, err := activeMaintenanceRunProtection(ctx, q, r)
	return p != nil, err
}

// CheckMaintenanceHello refuses an entire legacy reconciliation rather than
// issuing a Continue that could restart an unresolved original execution. Old
// runners have no retain verdict, so this host honestly remains unreconciled.
func checkMaintenanceHello(ctx context.Context, q store.Querier, host string) error {
	runs, err := store.ListOpenRunsOnHost(ctx, q, host)
	if err != nil {
		return err
	}
	for _, r := range runs {
		p, err := activeMaintenanceRunProtection(ctx, q, r)
		if err != nil {
			return err
		}
		if p != nil {
			return protocol.Errorf(protocol.CodeUnknownOutcome, "retained maintenance execution requires an independent business reconciliation before host continuation")
		}
	}
	return nil
}

// A worker release does not identify whether its failure came from maintenance
// transport loss or business execution. Keep the original unclassified fact;
// only an independent business action may resolve this accepted request.
func preserveMaintenanceGiveUp(ctx context.Context, x *Exec, tx *store.Tx, run string) (bool, error) {
	r, err := store.GetRun(ctx, tx.Q(), run)
	if err != nil {
		return false, err
	}
	p, err := activeMaintenanceRunProtection(ctx, tx.Q(), r)
	if err != nil || p == nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO maintenance_physical_stops(request_domain,request_id,maintenance_id,run_id,lease_id,operation,payload_json,last_sequence,created_at) VALUES(?,?,?,?,?,?,?,-1,?)`, x.Principal.DedupDomain(), x.Env.RequestID, p.MaintenanceID, run, p.LeaseID, x.Env.Operation, string(x.Env.Payload), x.Now.UnixMilli())
	return err == nil, err
}

// An independently accepted owner stop may use a prior physical stop/give-up.
// Its own request commits this decision; the original accepted/succeeded native
// request and its raw facts remain immutable.
func settleMaintenanceStopsOnOwnerControl(ctx context.Context, x *Exec, tx *store.Tx) error {
	if x.Principal.Kind != protocol.PrincipalOwner {
		return nil
	}
	var scope string
	switch x.Env.Operation {
	case "tasks.cancel", "control.set_mode":
		scope = `r.task_id=?`
	case "issues.cancel", "issues.finish_cancel", "issues.pause":
		scope = `(EXISTS(SELECT 1 FROM issue_manager_tasks m JOIN scope s ON s.id=m.issue_id WHERE m.task_id=r.task_id) OR EXISTS(SELECT 1 FROM task_work_item_owners o JOIN scope s ON s.id=o.work_item_id WHERE o.task_id=r.task_id) OR EXISTS(SELECT 1 FROM run_issue_responsibilities d JOIN scope s ON s.id=d.issue_id WHERE d.run_id=r.run_id))`
	default:
		return nil
	}
	query := `WITH RECURSIVE scope(id) AS(SELECT ? UNION ALL SELECT i.issue_id FROM issue_controls i JOIN scope p ON i.parent_issue_id=p.id)
 SELECT DISTINCT r.run_id FROM maintenance_protected_runs p JOIN maintenance_physical_stops s ON s.run_id=p.run_id AND s.lease_id=p.lease_id JOIN runs r ON r.run_id=p.run_id AND r.task_id=p.task_id AND r.action_id=p.action_id JOIN tasks t ON t.task_id=r.task_id JOIN plan_actions a ON a.action_id=r.action_id JOIN leases l ON l.run_id=r.run_id AND l.lease_id=p.lease_id
 WHERE ` + scope + ` AND r.observed_state IN ('starting','running','waiting','interrupted','unknown')
 AND (r.desired_state='stopped' OR t.lifecycle IN ('completed','cancelled') OR a.status IN ('cancelled','superseded') OR EXISTS(SELECT 1 FROM issue_manager_tasks m JOIN issue_controls i ON i.issue_id=m.issue_id WHERE m.task_id=r.task_id AND i.state IN ('cancelling','cancelled','closed')))
 ORDER BY r.run_id`
	args := []any{x.Env.Target.ID}
	if scope == `r.task_id=?` {
		args = append(args, x.Env.Target.ID)
	}
	ids, err := store.FollowupIDs(ctx, tx.Q(), query, args...)
	if err != nil {
		return err
	}
	for _, id := range ids {
		r, err := store.GetRun(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		if err := endRun(ctx, x, tx, r, protocol.RunFailed); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE plan_actions SET status='done',updated_at=? WHERE action_id=? AND status='dispatched'`, x.Now.UnixMilli(), r.ActionID); err != nil {
			return err
		}
		if r.Role == protocol.RoleIssueManager {
			if _, err := tx.Exec(ctx, `UPDATE tasks SET lifecycle='waiting',updated_at=? WHERE task_id=? AND lifecycle NOT IN ('completed','cancelled')`, x.Now.UnixMilli(), r.TaskID); err != nil {
				return err
			}
		}
		body, err := json.Marshal(map[string]any{"control_request_id": x.Env.RequestID, "physical_fact_retained": true})
		if err != nil {
			return err
		}
		if _, err := tx.InsertEvent(ctx, store.EventRow{EventID: x.IDs.New(protocol.PrefixEvent), Source: string(x.Principal.Kind), ObjectType: "run", ObjectID: id, Kind: "run.maintenance_stop_resolved", BodyJSON: string(body), RequestID: x.Env.RequestID, TraceID: x.TraceID, CreatedAt: x.Now.UnixMilli()}); err != nil {
			return err
		}
	}
	return nil
}

// ReceiptRecoveryStartupPending is a component startup gate bound to the exact
// locally verified maintenance registration, not a substitute model readiness.
func (a *App) ReceiptRecoveryStartupPending(ctx context.Context) (bool, error) {
	if a.committedMaintenanceID == "" {
		return false, nil
	}
	var pending bool
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		row, err := store.GetCommittedMaintenance(ctx, tx.Q(), a.committedMaintenanceID)
		if errors.Is(err, store.ErrNotFound) {
			return protocol.Errorf(protocol.CodeInvalidTransition, "maintenance startup registration is incomplete")
		}
		if err != nil {
			return err
		}
		pending = row.Plan.ReceiptRecoveryStop != nil && !row.Linked
		return nil
	})
	return pending, err
}
