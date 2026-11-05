package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

const managerRetirementKey = "cancellation_retirement_request_id"

// ManagerRetirement reads the original server ledger. Process and journal facts
// still have to arrive from the original authenticated runner.
func (a *App) ManagerRetirement(ctx context.Context, task, run, after string, limit int) (protocol.ManagerRetirementStatus, error) {
	var out protocol.ManagerRetirementStatus
	if limit < 1 || limit > 100 {
		return out, protocol.Errorf(protocol.CodeInvalidRequest, "limit must be between 1 and 100")
	}
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		out, err = managerRetirementStatus(ctx, tx.Q(), task, run)
		if err != nil {
			return err
		}
		return readManagerRetirementRequests(ctx, tx.Q(), &out, after, limit)
	})
	return out, err
}

func managerRetirementStatus(ctx context.Context, q store.Querier, task, run string) (protocol.ManagerRetirementStatus, error) {
	s := protocol.ManagerRetirementStatus{TaskID: task, RunID: run, Blockers: []string{}, NativeRequests: []protocol.ManagerNativeRequestStatus{}}
	r, err := store.GetRun(ctx, q, run)
	if err != nil {
		return s, errNotFound(err, "manager run does not exist")
	}
	if r.TaskID != task || r.Role != protocol.RoleIssueManager {
		return s, protocol.Errorf(protocol.CodeForbidden, "run is not this task's manager")
	}
	t, err := store.GetTask(ctx, q, task)
	if err != nil {
		return s, err
	}
	m, err := store.GetIssueManagerTask(ctx, q, task)
	if err != nil {
		return s, err
	}
	i, err := store.GetCanonicalIssue(ctx, q, m.IssueID)
	if err != nil {
		return s, err
	}
	l, err := store.GetLeaseByRun(ctx, q, run)
	if err != nil {
		return s, err
	}
	action, err := store.GetAction(ctx, q, r.ActionID)
	if err != nil {
		return s, err
	}
	s.IssueID, s.IssueRevision, s.AssignmentEpoch = i.IssueID, i.Revision, i.AssignmentEpoch
	s.TaskRevision, s.ActionID, s.HostID, s.LeaseID = t.ControlRevision, r.ActionID, r.HostID, l.LeaseID
	s.LeaseStatus = string(l.Status)
	s.Observed, s.Desired = r.ObservedState, desiredFor(r, action, t)
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0),COALESCE(SUM(CASE WHEN applied=0 THEN 1 ELSE 0 END),0) FROM manager_native_events WHERE run_id=?`, run).Scan(&s.EventLastSequence, &s.UnappliedEvents); err != nil {
		return s, err
	}
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM manager_native_requests WHERE run_id=? AND (status IN ('pending','started','unknown') OR (status='completed' AND COALESCE(json_extract(result_json,'$.error'),'')<>'' AND COALESCE(json_extract(result_json,'$.rejected'),0)=0 AND method NOT IN ('models','resume','find','usage','interrupt','close','tool_result','release')))`, run).Scan(&s.UnresolvedNativeRequests); err != nil {
		return s, err
	}
	s.PendingBusinessReceipts, err = managerBusinessReceiptsPending(ctx, q, run)
	if err != nil {
		return s, err
	}
	s.Execution, err = store.GetRunExecution(ctx, q, run)
	if err != nil {
		return s, err
	}
	if s.Execution != nil {
		raw, err := json.Marshal(s.Execution)
		if err != nil {
			return s, err
		}
		sum := sha256.Sum256(raw)
		s.ExecutionSHA256 = hex.EncodeToString(sum[:])
	}
	var trigger struct {
		RequestID string `json:"cancellation_retirement_request_id"`
	}
	if err := json.Unmarshal([]byte(action.TriggerJSON), &trigger); err != nil {
		return s, err
	}
	s.RetirementRequestID = trigger.RequestID
	if i.State != protocol.IssueCancelling {
		s.Blockers = append(s.Blockers, "issue_not_cancelling")
	}
	if i.AssignmentState != "active" || i.AssignmentEpoch != m.AssignmentEpoch || (m.State != "assigned" && m.State != "retiring") {
		s.Blockers = append(s.Blockers, "responsibility_changed")
	}
	if t.Kind != protocol.TaskKindIssueManager || t.ControlMode != protocol.ControlManaged {
		s.Blockers = append(s.Blockers, "manager_not_managed")
	}
	if s.Execution == nil || m.Pin == nil || !reflect.DeepEqual(s.Execution, m.Pin) || m.HostID != r.HostID || l.HostID != r.HostID {
		s.Blockers = append(s.Blockers, "original_execution_changed")
	}
	if !maintenanceRunOpen(r.ObservedState) || l.Status == store.LeaseReleased {
		s.Blockers = append(s.Blockers, "original_run_ended")
	}
	if action.TaskID != task || action.Kind != protocol.ActionCoordinate || action.Status != protocol.ActionDispatched || action.TargetSpecVersion != t.SpecVersion {
		s.Blockers = append(s.Blockers, "original_action_changed")
	}
	var competing bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE task_id=? AND run_id<>? AND observed_state IN ('starting','running','waiting','interrupted','unknown'))`, task, run).Scan(&competing); err != nil {
		return s, err
	}
	if competing {
		s.Blockers = append(s.Blockers, "other_manager_run_open")
	}
	if s.RetirementRequestID != "" {
		s.Blockers = append(s.Blockers, "retirement_already_requested")
	}
	s.CanRetire = len(s.Blockers) == 0
	return s, nil
}

func readManagerRetirementRequests(ctx context.Context, q store.Querier, s *protocol.ManagerRetirementStatus, after string, limit int) error {
	rows, err := q.QueryContext(ctx, `SELECT request_id,method,status,result_json IS NOT NULL,
 CASE WHEN LENGTH(CAST(result_json AS BLOB))<=65536 THEN result_json ELSE NULL END,
 COALESCE(LENGTH(CAST(result_json AS BLOB))>65536,0),
 COALESCE(json_extract(result_json,'$.error'),'')<>'',COALESCE(json_extract(result_json,'$.rejected'),0)
 FROM manager_native_requests WHERE run_id=? AND request_id>? ORDER BY request_id LIMIT ?`, s.RunID, after, limit+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if len(s.NativeRequests) == limit {
			s.More = true
			s.NextAfter = s.NativeRequests[len(s.NativeRequests)-1].RequestID
			break
		}
		var item protocol.ManagerNativeRequestStatus
		var raw sql.NullString
		if err := rows.Scan(&item.RequestID, &item.Method, &item.Status, &item.ResultPresent, &raw, &item.ResultHashOmitted, &item.Error, &item.Rejected); err != nil {
			return err
		}
		if raw.Valid {
			sum := sha256.Sum256([]byte(raw.String))
			item.ResultSHA256 = hex.EncodeToString(sum[:])
		}
		s.NativeRequests = append(s.NativeRequests, item)
	}
	return rows.Err()
}

// RetireManagerRun stops coordination without asserting that native execution
// stopped. Only the original runner's receipts may release physical capacity.
type RetireManagerRun struct{}

func (RetireManagerRun) Operation() string { return protocol.OpRetireManagerRun }
func (RetireManagerRun) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (RetireManagerRun) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if x.Env.Target.Type != "task" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "original manager task target required")
	}
	var p protocol.RetireManagerRunPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	decoded, err := hex.DecodeString(p.ExecutionSHA256)
	if p.IssueID == "" || p.RunID == "" || p.HostID == "" || p.LeaseID == "" || p.ExpectedIssueRevision < 1 || p.ExpectedAssignmentEpoch < 1 || strings.TrimSpace(p.Reason) == "" || err != nil || len(decoded) != sha256.Size {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "exact original Issue, Run, host, lease, execution SHA256 and reason required")
	}
	expected, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	s, err := managerRetirementStatus(ctx, tx.Q(), x.Env.Target.ID, p.RunID)
	if err != nil {
		return Outcome{}, err
	}
	if expected != s.TaskRevision || p.IssueID != s.IssueID || p.ExpectedIssueRevision != s.IssueRevision || p.ExpectedAssignmentEpoch != s.AssignmentEpoch || p.HostID != s.HostID || p.LeaseID != s.LeaseID || p.ExecutionSHA256 != s.ExecutionSHA256 {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "original manager control or execution identity changed")
	}
	if !s.CanRetire {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "original manager cannot retire under this cancellation")
	}
	if err := validateCanonicalSourceReference(ctx, tx.Q(), canonicalOwnerIssueSource(p.IssueSourceReference)); err != nil {
		return Outcome{}, err
	}
	action, err := store.GetAction(ctx, tx.Q(), s.ActionID)
	if err != nil {
		return Outcome{}, err
	}
	var trigger map[string]json.RawMessage
	if err := json.Unmarshal([]byte(action.TriggerJSON), &trigger); err != nil {
		return Outcome{}, err
	}
	if trigger == nil {
		trigger = map[string]json.RawMessage{}
	}
	trigger[managerRetirementKey], err = json.Marshal(x.Env.RequestID)
	if err != nil {
		return Outcome{}, err
	}
	raw, err := json.Marshal(trigger)
	if err != nil {
		return Outcome{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE plan_actions SET trigger_json=? WHERE action_id=?`, string(raw), s.ActionID); err != nil {
		return Outcome{}, err
	}
	// Include receipt-only actions: their ordinary dispatch exception must not
	// override this explicit owner's stop of the original transport.
	if _, err := tx.Exec(ctx, `UPDATE plan_actions SET status='cancelled' WHERE task_id=? AND status IN ('pending','dispatched')`, s.TaskID); err != nil {
		return Outcome{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE runs SET desired_state='stopped',updated_at=? WHERE run_id=?`, x.Now.UnixMilli(), s.RunID); err != nil {
		return Outcome{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE issue_manager_tasks SET state='retiring',updated_at=? WHERE task_id=?`, x.Now.UnixMilli(), s.TaskID); err != nil {
		return Outcome{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE tasks SET lifecycle='cancelled',control_revision=control_revision+1,updated_at=? WHERE task_id=? AND control_revision=?`, x.Now.UnixMilli(), s.TaskID, expected); err != nil {
		return Outcome{}, err
	}
	result, err := managerRetirementStatus(ctx, tx.Q(), s.TaskID, s.RunID)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: result, Events: []Event{{ObjectType: "task", ObjectID: s.TaskID, ObjectVersion: expected + 1, Kind: "task.manager_retirement_requested", Body: map[string]any{"issue_id": s.IssueID, "run_id": s.RunID, "lease_id": s.LeaseID, "request_id": x.Env.RequestID, "reason": p.Reason}}}}, nil
}

func managerCancellationRetired(ctx context.Context, q store.Querier, task string) (bool, error) {
	var retired bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM plan_actions WHERE task_id=? AND COALESCE(json_extract(trigger_json,'$.cancellation_retirement_request_id'),'')<>'')`, task).Scan(&retired)
	return retired, err
}

// A retiring external coordinator cannot use the ordinary manager's graceful
// self-close exception. Its physical capacity and original uncertain operations
// must settle before the owner can finish cancellation.
func appendRetiringManagerFacts(ctx context.Context, q store.Querier, root string, f *domain.IssueLifecycleFacts) error {
	rows, err := q.QueryContext(ctx, `WITH RECURSIVE scope(id) AS (
 SELECT issue_id FROM issue_controls WHERE issue_id=?
 UNION ALL SELECT c.issue_id FROM issue_controls c JOIN scope s ON c.parent_issue_id=s.id)
 SELECT t.task_id,t.control_revision,
 EXISTS(SELECT 1 FROM runs r LEFT JOIN leases l USING(run_id) WHERE r.task_id=t.task_id AND (r.observed_state IN ('starting','running','waiting','interrupted','unknown') OR l.status<>'released')),
 EXISTS(SELECT 1 FROM runs r JOIN manager_native_requests n USING(run_id) WHERE r.task_id=t.task_id AND
 (n.status IN ('pending','started','unknown') OR (n.status='completed' AND COALESCE(json_extract(n.result_json,'$.error'),'')<>'' AND COALESCE(json_extract(n.result_json,'$.rejected'),0)=0 AND n.method NOT IN ('models','resume','find','usage','interrupt','close','tool_result','release')))),
 EXISTS(SELECT 1 FROM runs r JOIN manager_native_events e USING(run_id) WHERE r.task_id=t.task_id AND e.applied=0),
 EXISTS(SELECT 1 FROM runs r JOIN hosted_executions e ON json_extract(e.private_snapshot,'$.runner_run_id')=r.run_id JOIN main_turns mt ON mt.request_id=e.execution_id WHERE r.task_id=t.task_id AND (mt.status IN ('starting','started','unknown') OR EXISTS(SELECT 1 FROM native_turn_receipts_pending p WHERE p.request_id=mt.request_id)))
 FROM task_work_item_owners o JOIN scope s ON s.id=o.work_item_id JOIN tasks t USING(task_id)
 WHERE t.kind='issue_manager' AND EXISTS(SELECT 1 FROM plan_actions a WHERE a.task_id=t.task_id AND COALESCE(json_extract(a.trigger_json,'$.cancellation_retirement_request_id'),'')<>'') ORDER BY t.task_id`, root)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var task string
		var revision int64
		var execution, uncertain, events, business bool
		if err := rows.Scan(&task, &revision, &execution, &uncertain, &events, &business); err != nil {
			return err
		}
		if execution {
			f.Executions = append(f.Executions, protocol.IssueBlocker{Code: "manager_retirement_execution", ObjectID: task, Revision: revision})
		}
		if uncertain {
			f.UnknownOperations = append(f.UnknownOperations, protocol.IssueBlocker{Code: "manager_native_unknown", ObjectID: task, Revision: revision})
		}
		if events {
			f.UnknownOperations = append(f.UnknownOperations, protocol.IssueBlocker{Code: "manager_native_events", ObjectID: task, Revision: revision})
		}
		if business {
			f.UnknownOperations = append(f.UnknownOperations, protocol.IssueBlocker{Code: "manager_business_unknown", ObjectID: task, Revision: revision})
		}
	}
	return rows.Err()
}
