package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// RetainedRunReconciliation reads only the original ledger, never the provider.
func (a *App) RetainedRunReconciliation(ctx context.Context, run string) (protocol.RetainedRunReconciliation, error) {
	var s protocol.RetainedRunReconciliation
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		s, err = retainedRunReconciliation(ctx, tx.Q(), run)
		return err
	})
	return s, err
}

func retainedRunReconciliation(ctx context.Context, q store.Querier, id string) (protocol.RetainedRunReconciliation, error) {
	s := protocol.RetainedRunReconciliation{RunID: id, Protections: []protocol.RetainedRunProtection{}, OriginalRequestIDs: []string{}, Blockers: []string{}}
	r, err := store.GetRun(ctx, q, id)
	if err != nil {
		return s, errNotFound(err, "original Run does not exist")
	}
	t, err := store.GetTask(ctx, q, r.TaskID)
	if err != nil {
		return s, err
	}
	action, err := store.GetAction(ctx, q, r.ActionID)
	if err != nil {
		return s, err
	}
	lease, err := store.GetLeaseByRun(ctx, q, id)
	if err != nil {
		return s, err
	}
	s.Identity = protocol.RetainedRunIdentity{TaskID: r.TaskID, ActionID: r.ActionID, Generation: r.Generation, SpecVersion: action.TargetSpecVersion, HostID: r.HostID, LeaseID: lease.LeaseID}
	s.TaskRevision, s.Role, s.Observed, s.LeaseStatus = t.ControlRevision, r.Role, r.ObservedState, lease.Status
	pin, err := store.GetRunExecution(ctx, q, id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return s, err
	}
	if pin == nil {
		s.Blockers = append(s.Blockers, "named_execution_missing")
	} else {
		s.Identity.Execution = *pin
	}
	if r.Role == protocol.RoleIssueManager {
		s.Blockers = append(s.Blockers, "manager_requires_business_reconciliation")
	}
	if pin != nil && (pin.Worker != protocol.WorkerOrca || pin.Validate(r.HostID) != nil) {
		s.Blockers = append(s.Blockers, "strict_resume_worker_unsupported")
	}
	wanted, err := maintenanceRunWanted(ctx, q, r)
	if err != nil {
		return s, err
	}
	if !wanted || t.ControlMode != protocol.ControlManaged || !maintenanceRunOpen(r.ObservedState) || lease.Status == store.LeaseReleased || lease.HostID != r.HostID {
		s.Blockers = append(s.Blockers, "original_run_not_wanted")
	}
	if action.TaskID != r.TaskID || action.Generation != r.Generation || action.TargetSpecVersion != t.SpecVersion {
		s.Blockers = append(s.Blockers, "original_action_changed")
	}
	issueID, err := store.GetTaskIssueOwner(ctx, q, r.TaskID)
	if err != nil {
		return s, err
	}
	if issueID != "" {
		issue, e := store.GetCanonicalIssue(ctx, q, issueID)
		if e != nil && !errors.Is(e, store.ErrNotFound) {
			return s, e
		}
		if e == nil && (issue.State != protocol.IssueActive || issue.AssignmentState != "active") {
			s.Blockers = append(s.Blockers, "issue_not_active")
		}
	}
	rows, err := store.MaintenanceProtectedRuns(ctx, q, id)
	if err != nil {
		return s, err
	}
	for _, p := range rows {
		// The complete current set includes every capture, including older captures.
		m, err := store.GetCommittedMaintenance(ctx, q, p.MaintenanceID)
		if err != nil {
			return s, err
		}
		s.Protections = append(s.Protections, protocol.RetainedRunProtection{MaintenanceID: p.MaintenanceID, RunID: p.RunID, TaskID: p.TaskID, ActionID: p.ActionID, HostID: p.HostID, LeaseID: p.LeaseID, OriginalRequestsJSON: p.OriginalRequestsJSON, Linked: m.Linked})
		if p.TaskID != r.TaskID || p.ActionID != r.ActionID || p.HostID != r.HostID || p.LeaseID != lease.LeaseID {
			s.Blockers = append(s.Blockers, "protection_identity_changed")
		}
		if !m.Linked {
			s.Blockers = append(s.Blockers, "maintenance_not_linked")
		}
		var ids []string
		if err := json.Unmarshal([]byte(p.OriginalRequestsJSON), &ids); err != nil {
			return s, err
		}
		for _, request := range ids {
			if !slices.Contains(s.OriginalRequestIDs, request) {
				s.OriginalRequestIDs = append(s.OriginalRequestIDs, request)
			}
			var known, settled bool
			if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM main_turns WHERE request_id=?),EXISTS(SELECT 1 FROM main_turns t WHERE t.request_id=? AND t.status NOT IN ('starting','started','unknown') AND NOT EXISTS(SELECT 1 FROM native_turn_receipts_pending n WHERE n.request_id=t.request_id))`, request, request).Scan(&known, &settled); err != nil {
				return s, err
			}
			if !known || !settled {
				s.UnresolvedNativeRequests++
			}
		}
	}
	slices.SortFunc(s.Protections, func(a, b protocol.RetainedRunProtection) int {
		return strings.Compare(a.MaintenanceID, b.MaintenanceID)
	})
	slices.Sort(s.OriginalRequestIDs)
	raw, err := json.Marshal(s.Protections)
	if err != nil {
		return s, err
	}
	sum := sha256.Sum256(raw)
	s.ProtectionSetSHA256 = hex.EncodeToString(sum[:])
	if len(s.Protections) == 0 {
		s.Blockers = append(s.Blockers, "maintenance_protection_missing")
	}
	var nativeUnresolved int64
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM manager_native_requests WHERE run_id=? AND (status<>'completed' OR (COALESCE(json_extract(result_json,'$.error'),'')<>'' AND COALESCE(json_extract(result_json,'$.rejected'),0)=0))`, id).Scan(&nativeUnresolved); err != nil {
		return s, err
	}
	s.UnresolvedNativeRequests += nativeUnresolved
	if s.UnresolvedNativeRequests != 0 {
		s.Blockers = append(s.Blockers, "native_requests_unreconciled")
	}
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM manager_native_events WHERE run_id=? AND applied=0`, id).Scan(&s.UnappliedEvents); err != nil {
		return s, err
	}
	if s.UnappliedEvents != 0 {
		s.Blockers = append(s.Blockers, "native_events_unreconciled")
	}
	s.PendingBusinessReceipts, err = managerBusinessReceiptsPending(ctx, q, id)
	if err != nil {
		return s, err
	}
	if s.PendingBusinessReceipts {
		s.Blockers = append(s.Blockers, "business_receipts_unreconciled")
	}
	archive, err := store.ArchivePreparation(ctx, q)
	if err != nil {
		return s, err
	}
	if archive.Prepared {
		s.Blockers = append(s.Blockers, "archive_prepared")
	}
	control, err := store.GetHostControl(ctx, q, r.HostID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return s, err
	}
	if control.Mode != protocol.RunnerNormal {
		s.Blockers = append(s.Blockers, "host_not_normal")
	}
	rt, err := store.GetHostRuntime(ctx, q, r.HostID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return s, err
	}
	if err != nil || !runtimeSupportsStrictRetainedResume(&rt.Runtime) {
		s.Blockers = append(s.Blockers, "strict_resume_runner_unsupported")
	}
	if rt.Runtime.Mode != protocol.RunnerNormal {
		s.Blockers = append(s.Blockers, "runner_not_normal")
	}
	if pin != nil {
		available := false
		for _, executor := range rt.Runtime.AgentExecutors {
			if executor.Pin == *pin && executor.Available {
				available = true
				break
			}
		}
		if !available {
			s.Blockers = append(s.Blockers, "original_executor_unavailable")
		}
	}
	prior, err := store.LatestRetainedRunContinuation(ctx, q, id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return s, err
	}
	if err == nil {
		s.ContinuationRequestID, s.ContinuationTaskRevision = prior.RequestID, prior.TaskRevision
	}
	s.CanContinue = len(s.Blockers) == 0
	return s, nil
}

func runtimeSupportsStrictRetainedResume(rt *protocol.RunnerRuntime) bool {
	return runtimeSupportsRetainedReconciliation(rt) && rt.JournalVersion >= 16 && slices.Contains(rt.Capabilities, protocol.CapabilityStrictRetainedResume)
}

// ContinueRetainedRun records permission only; Hello owns lease restoration.
type ContinueRetainedRun struct{}

func (ContinueRetainedRun) Operation() string { return protocol.OpContinueRetainedRun }
func (ContinueRetainedRun) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (ContinueRetainedRun) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if x.Env.Target.Type != "run" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "original Run target required")
	}
	var p protocol.ContinueRetainedRunPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if p.TaskID == "" || p.ActionID == "" || p.HostID == "" || p.LeaseID == "" || p.Generation < 1 || p.SpecVersion < 1 || len(p.Protections) == 0 || len(p.ProtectionSetSHA256) != 64 || strings.TrimSpace(p.Reason) == "" || p.Execution.Validate(p.HostID) != nil {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "complete original Run identity, protection set and independent reason required")
	}
	expected, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	s, err := retainedRunReconciliation(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	if expected != s.TaskRevision || p.RetainedRunIdentity != s.Identity || p.ProtectionSetSHA256 != s.ProtectionSetSHA256 || !reflect.DeepEqual(p.Protections, s.Protections) {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "original Run identity, control or maintenance protection set changed")
	}
	if !s.CanContinue {
		return Outcome{}, protocol.Errorf(protocol.CodeUnknownOutcome, "original Run cannot continue until its exact reconciliation blockers are resolved")
	}
	if err := validateCanonicalSourceReference(ctx, tx.Q(), canonicalOwnerIssueSource(p.IssueSourceReference)); err != nil {
		return Outcome{}, err
	}
	// A hosted owner may repair only through its admitted coordination wrapper.
	if x.Principal.Kind != protocol.PrincipalOwner && x.coordinationExecution == nil {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "continuation requires the owner or admitted responsibility")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return Outcome{}, err
	}
	record := store.RetainedRunContinuation{RequestDomain: x.Principal.DedupDomain(), RequestID: x.Env.RequestID, RunID: s.RunID, TaskID: p.TaskID, LeaseID: p.LeaseID, TaskRevision: expected + 1, ProtectionSHA256: p.ProtectionSetSHA256, PayloadJSON: string(raw), CreatedAt: x.Now.UnixMilli()}
	if err := tx.InsertRetainedRunContinuation(ctx, record); err != nil {
		return Outcome{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE tasks SET control_revision=control_revision+1,updated_at=? WHERE task_id=? AND control_revision=?`, x.Now.UnixMilli(), p.TaskID, expected); err != nil {
		return Outcome{}, err
	}
	result := map[string]any{"request_id": x.Env.RequestID, "run_id": s.RunID, "task_id": p.TaskID, "lease_id": p.LeaseID, "task_revision": expected + 1, "protection_set_sha256": p.ProtectionSetSHA256, "status": "awaiting_original_journal"}
	return Outcome{Result: result, Events: []Event{{ObjectType: "run", ObjectID: s.RunID, Kind: "run.retained_continuation_authorized", Body: result}}}, nil
}

func retainedContinuation(ctx context.Context, q store.Querier, id string, activated bool) (*store.RetainedRunContinuation, error) {
	record, err := store.LatestRetainedRunContinuation(ctx, q, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if activated && record.ActivatedAt == 0 {
		return nil, nil
	}
	s, err := retainedRunReconciliation(ctx, q, id)
	if err != nil {
		return nil, err
	}
	var p protocol.ContinueRetainedRunPayload
	if json.Unmarshal([]byte(record.PayloadJSON), &p) != nil {
		return nil, protocol.Errorf(protocol.CodeInternal, "invalid continuation record")
	}
	if !s.CanContinue || record.TaskRevision != s.TaskRevision || record.LeaseID != s.Identity.LeaseID || record.ProtectionSHA256 != s.ProtectionSetSHA256 || p.RetainedRunIdentity != s.Identity || !reflect.DeepEqual(p.Protections, s.Protections) {
		return nil, nil
	}
	return &record, nil
}
