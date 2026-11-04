package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// ManagerRunReconciliation reads only retained authoritative ledger facts.
func (a *App) ManagerRunReconciliation(ctx context.Context, task, run string) (protocol.ManagerRunReconciliation, error) {
	var out protocol.ManagerRunReconciliation
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		out, err = managerRunReconciliation(ctx, tx.Q(), task, run)
		return err
	})
	return out, err
}

func managerRunReconciliation(ctx context.Context, q store.Querier, task, run string) (protocol.ManagerRunReconciliation, error) {
	out := protocol.ManagerRunReconciliation{Blockers: []string{}}
	r, err := store.GetRun(ctx, q, run)
	if err != nil {
		return out, errNotFound(err, "manager run does not exist")
	}
	if r.TaskID != task || r.Role != protocol.RoleIssueManager {
		return out, protocol.Errorf(protocol.CodeForbidden, "run is not this task's manager")
	}
	t, err := store.GetTask(ctx, q, task)
	if err != nil {
		return out, err
	}
	m, err := store.GetIssueManagerTask(ctx, q, task)
	if err != nil {
		return out, err
	}
	i, err := store.GetCanonicalIssue(ctx, q, m.IssueID)
	if err != nil {
		return out, err
	}
	l, err := store.GetLeaseByRun(ctx, q, run)
	if err != nil {
		return out, err
	}
	out.TaskID, out.RunID, out.ActionID, out.HostID, out.LeaseID = task, run, r.ActionID, r.HostID, l.LeaseID
	out.TaskRevision, out.AssignmentEpoch, out.Observed = t.ControlRevision, i.AssignmentEpoch, r.ObservedState
	action, err := store.GetAction(ctx, q, r.ActionID)
	if err != nil {
		return out, err
	}
	if action.TaskID != task || action.Kind != protocol.ActionCoordinate || action.Status != protocol.ActionDispatched || action.TargetSpecVersion != t.SpecVersion {
		out.Blockers = append(out.Blockers, "original_action_replaced")
	}
	out.Execution, err = store.GetRunExecution(ctx, q, run)
	if err != nil {
		return out, err
	}
	if !maintenanceRunOpen(r.ObservedState) || l.Status == store.LeaseReleased || t.Lifecycle.Terminal() {
		out.Blockers = append(out.Blockers, "original_run_ended")
	}
	if m.State != "assigned" || i.AssignmentState != "active" || i.AssignmentEpoch != m.AssignmentEpoch || i.State != protocol.IssueActive {
		out.Blockers = append(out.Blockers, "responsibility_changed")
	}
	var raw string
	var closeRow int64
	err = q.QueryRowContext(ctx, `SELECT request_id,result_json,rowid FROM manager_native_requests WHERE run_id=? AND method='close' AND status='completed' AND COALESCE(json_extract(result_json,'$.error'),'')='' AND COALESCE(json_extract(result_json,'$.rejected'),0)=0 ORDER BY rowid DESC LIMIT 1`, run).Scan(&out.CloseRequestID, &raw, &closeRow)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		out.Blockers = append(out.Blockers, "successful_native_close_missing")
	} else {
		var reply protocol.ManagerNativeReply
		if err := json.Unmarshal([]byte(raw), &reply); err != nil || reply.ID != out.CloseRequestID || reply.Error != "" || reply.Rejected || reply.LastSequence < 0 {
			out.Blockers = append(out.Blockers, "native_close_evidence_invalid")
		} else {
			out.CloseLastSequence = reply.LastSequence
			sum := sha256.Sum256([]byte(raw))
			out.CloseResultSHA256 = hex.EncodeToString(sum[:])
		}
	}
	if closeRow != 0 {
		// A prior close cannot prove a later resume or native action stopped.
		// Only observational reads and definite pre-execution rejection retain
		// that proof. Tied timestamps still have an unambiguous append order.
		var laterActivity bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM manager_native_requests WHERE run_id=? AND rowid>? AND method NOT IN ('models','find','usage') AND NOT(status='completed' AND COALESCE(json_extract(result_json,'$.rejected'),0)=1))`, run, closeRow).Scan(&laterActivity); err != nil {
			return out, err
		}
		if laterActivity {
			out.Blockers = append(out.Blockers, "native_activity_after_close")
		}
	}
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0),COALESCE(SUM(CASE WHEN applied=0 THEN 1 ELSE 0 END),0) FROM manager_native_events WHERE run_id=?`, run).Scan(&out.EventLastSequence, &out.UnappliedEvents); err != nil {
		return out, err
	}
	if out.CloseLastSequence != out.EventLastSequence || out.UnappliedEvents != 0 {
		out.Blockers = append(out.Blockers, "native_events_unreconciled")
	}
	rows, err := q.QueryContext(ctx, `SELECT method,status,COUNT(*) FROM manager_native_requests WHERE run_id=? AND (status IN ('pending','started','unknown') OR (status='completed' AND COALESCE(json_extract(result_json,'$.error'),'')<>'' AND COALESCE(json_extract(result_json,'$.rejected'),0)=0)) GROUP BY method,status`, run)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var method, status string
		var count int64
		if err := rows.Scan(&method, &status, &count); err != nil {
			rows.Close()
			return out, err
		}
		// A transport/error reply is not evidence that native business never
		// ran. Receipt-only read failures themselves start no new business.
		if status != "completed" || !managerNativeReceiptMethod(method) {
			out.UnresolvedNativeRequests += count
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if out.UnresolvedNativeRequests != 0 {
		out.Blockers = append(out.Blockers, "native_requests_unreconciled")
	}
	out.PendingBusinessReceipts, err = managerBusinessReceiptsPending(ctx, q, run)
	if err != nil {
		return out, err
	}
	if out.PendingBusinessReceipts {
		out.Blockers = append(out.Blockers, "business_receipts_unreconciled")
	}
	stop, err := managerPhysicalStopProof(ctx, q, run, l.LeaseID)
	if err != nil {
		return out, err
	}
	if stop != nil {
		out.StoppedRequestID, out.StoppedPayloadSHA256, out.StoppedLastSequence = stop.request, stop.hash, stop.sequence
	}
	out.CanSettleWaiting = len(out.Blockers) == 0
	return out, nil
}

type ReconcileManagerRun struct{}

func (ReconcileManagerRun) Operation() string { return protocol.OpReconcileManagerRun }

func (ReconcileManagerRun) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}

func (ReconcileManagerRun) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if x.Env.Target.Type != "task" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "original manager task target required")
	}
	var p protocol.ReconcileManagerRunPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if p.Mode != "" && p.Mode != "read_receipts" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "unknown manager reconciliation mode")
	}
	if p.RunID == "" || p.LeaseID == "" || p.CloseRequestID == "" || len(p.CloseResultSHA256) != sha256.Size*2 || strings.TrimSpace(p.Reason) == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "exact original run, lease, close receipt SHA256 and reason required")
	}
	expected, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	s, err := managerRunReconciliation(ctx, tx.Q(), x.Env.Target.ID, p.RunID)
	if err != nil {
		return Outcome{}, err
	}
	if s.TaskRevision != expected || s.AssignmentEpoch != p.ExpectedAssignmentEpoch || s.LeaseID != p.LeaseID || s.CloseRequestID != p.CloseRequestID || s.CloseResultSHA256 != p.CloseResultSHA256 {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "original manager identity or reconciliation evidence changed")
	}
	m, err := store.GetIssueManagerTask(ctx, tx.Q(), s.TaskID)
	if err != nil {
		return Outcome{}, err
	}
	if err := validateCanonicalSourceReference(ctx, tx.Q(), canonicalOwnerIssueSource(p.IssueSourceReference)); err != nil {
		return Outcome{}, err
	}
	if x.Principal.Kind != protocol.PrincipalOwner {
		if x.Principal.Kind != protocol.PrincipalMainAgent || x.coordinationExecution == nil || x.coordinationExecution.Selection.Purpose != agentconfig.Main || p.SourceMessageID == "" {
			return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "only the owner or an admitted main owner relay can reconcile a manager")
		}
		if err := validateAttentionOwnerRelay(ctx, tx.Q(), x, m.IssueID, p.SourceMessageID); err != nil {
			return Outcome{}, err
		}
	}
	if s.Observed == protocol.RunFinished || s.Observed == protocol.RunFailed {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "original manager run already ended")
	}
	if p.Mode == "read_receipts" {
		return admitManagerReceiptReader(ctx, x, tx, s, p, m)
	}
	if !s.CanSettleWaiting {
		return Outcome{}, protocol.Errorf(protocol.CodeUnknownOutcome, "original native and business receipts require reconciliation before settling this manager")
	}
	// Reuse only the existing proved-close waiting transition. The original
	// physical receipt and immutable execution pin are never rewritten.
	copyExec := *x
	copyEnvelope := *x.Env
	copyEnvelope.Target = protocol.Target{Type: "run", ID: s.RunID}
	copyExec.Env = &copyEnvelope
	out, err := (FinishManagerTurn{}).Apply(ctx, &copyExec, tx)
	if err != nil {
		return Outcome{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE tasks SET control_revision=control_revision+1 WHERE task_id=? AND control_revision=?`, s.TaskID, expected); err != nil {
		return Outcome{}, err
	}
	result := map[string]any{"task_id": s.TaskID, "run_id": s.RunID, "lease_id": s.LeaseID, "close_request_id": s.CloseRequestID, "close_result_sha256": s.CloseResultSHA256, "task_revision": expected + 1, "observed": protocol.RunFinished, "responsibility": "waiting", "reason": p.Reason, "source_message_id": p.SourceMessageID, "source_revision": p.ExpectedSourceRevision}
	out.Result = result
	out.Events = append(out.Events, Event{ObjectType: "task", ObjectID: s.TaskID, ObjectVersion: expected + 1, Kind: "task.manager_reconciled", Body: result})
	return out, nil
}
