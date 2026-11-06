package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
)

const boundedRunnerAuthorizedEvent = "host.bounded_maintenance_authorized"

type boundedRunnerProtectionRecord struct {
	Grant       protocol.BoundedRunnerMaintenanceGrant `json:"grant"`
	Protections []protocol.RetainedRunProtection       `json:"protections"`
}

// AuthorizeBoundedRunnerMaintenance accepts only an independent external
// authority. It does not stop a process, claim drain, or settle business work.
type AuthorizeBoundedRunnerMaintenance struct{}

func (AuthorizeBoundedRunnerMaintenance) Operation() string {
	return protocol.OpAuthorizeBoundedRunnerMaintenance
}

func (AuthorizeBoundedRunnerMaintenance) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner, protocol.PrincipalSystem)
}

func (AuthorizeBoundedRunnerMaintenance) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.RunnerBoundedMaintenancePlan
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if err := validateBoundedRunnerPlan(x, p); err != nil {
		return Outcome{}, err
	}
	host, err := store.GetHost(ctx, tx.Q(), p.HostID)
	if err != nil {
		return Outcome{}, errNotFound(err, "runner host does not exist")
	}
	if host.Status != store.HostActive || !slices.Contains(host.Nodes, p.NodeSelector) {
		return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "runner node is not authorized for this configured host")
	}
	control, err := runnerControl(ctx, tx.Q(), p.HostID, x.Now.UnixMilli())
	if err != nil {
		return Outcome{}, err
	}
	if control.Revision != p.ExpectedControlRevision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "runner host control revision changed")
	}
	runtime, err := store.GetHostRuntime(ctx, tx.Q(), p.HostID)
	if err != nil {
		return Outcome{}, errNotFound(err, "runner baseline observation does not exist")
	}
	records, err := boundedRunnerProtectionRecords(ctx, tx.Q(), p.HostID)
	if err != nil {
		return Outcome{}, err
	}
	var previous *boundedRunnerProtectionRecord
	if len(records) > 0 && records[len(records)-1].Grant.Plan.Authorization.PlanSHA256 == p.Authorization.PlanSHA256 {
		previous = &records[len(records)-1]
	}
	originalSource := runtime.Runtime.InstanceID == p.RunnerInstanceID && runtime.Runtime.Generation == p.RunnerGeneration && runtime.Runtime.Build.PID == int64(p.Source.PID) && runtime.Runtime.Build.BinarySHA256 == p.Source.SHA256
	knownSuccessor := previous != nil && boundedRunnerRuntimeMatches(previous.Grant, runtime.Runtime)
	if !originalSource && !knownSuccessor {
		return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "runner source differs from its last accepted observation")
	}
	// An offline process, a missing ACK and retained unknown work are accepted
	// by this explicit authority. The installer independently verifies actual
	// kernel process and journal ownership before stopping or starting anything.
	control.Revision++
	control.Mode = protocol.RunnerMaintenance
	if err := tx.PutHostControl(ctx, p.HostID, control, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	if previous != nil {
		// A fresh external window retains the original launch lineage and its
		// exact snapshot. It cannot recapture or add newly observed work.
		record := *previous
		record.Grant.Plan = p
		record.Grant.ControlRevision = control.Revision
		return boundedRunnerAuthorizationOutcome(record), nil
	}
	runs, err := store.ListOpenRunsOnHost(ctx, tx.Q(), p.HostID)
	if err != nil {
		return Outcome{}, err
	}
	record := boundedRunnerProtectionRecord{Grant: protocol.BoundedRunnerMaintenanceGrant{RecoveryAuthorizationID: p.Authorization.RequestID, Plan: p, ControlRevision: control.Revision, Runs: []protocol.CommittedRetainedRun{}}, Protections: []protocol.RetainedRunProtection{}}
	for _, r := range runs {
		action, err := store.GetAction(ctx, tx.Q(), r.ActionID)
		if err != nil {
			return Outcome{}, err
		}
		lease, err := store.GetLeaseByRun(ctx, tx.Q(), r.RunID)
		if err != nil {
			return Outcome{}, err
		}
		requests, err := store.FollowupIDs(ctx, tx.Q(), `SELECT execution_id FROM hosted_executions WHERE json_extract(private_snapshot,'$.runner_run_id')=? UNION SELECT request_id FROM manager_native_requests WHERE run_id=? ORDER BY 1`, r.RunID, r.RunID)
		if err != nil {
			return Outcome{}, err
		}
		if requests == nil {
			requests = []string{}
		}
		raw, err := json.Marshal(requests)
		if err != nil {
			return Outcome{}, err
		}
		record.Grant.Runs = append(record.Grant.Runs, protocol.CommittedRetainedRun{TaskID: r.TaskID, Run: protocol.RunView{RunID: r.RunID, Role: r.Role, Generation: r.Generation, SpecVersion: action.TargetSpecVersion, Host: r.HostID, Observed: r.ObservedState}})
		record.Protections = append(record.Protections, protocol.RetainedRunProtection{MaintenanceID: "runner-force:" + p.Authorization.RequestID, RunID: r.RunID, TaskID: r.TaskID, ActionID: r.ActionID, HostID: r.HostID, LeaseID: lease.LeaseID, OriginalRequestsJSON: string(raw)})
	}
	slices.SortFunc(record.Protections, func(a, b protocol.RetainedRunProtection) int { return strings.Compare(a.RunID, b.RunID) })
	slices.SortFunc(record.Grant.Runs, func(a, b protocol.CommittedRetainedRun) int { return strings.Compare(a.Run.RunID, b.Run.RunID) })
	raw, err := json.Marshal(record.Protections)
	if err != nil {
		return Outcome{}, err
	}
	sum := sha256.Sum256(raw)
	record.Grant.ProtectionSetSHA256 = hex.EncodeToString(sum[:])
	return boundedRunnerAuthorizationOutcome(record), nil
}

func boundedRunnerAuthorizationOutcome(record boundedRunnerProtectionRecord) Outcome {
	return Outcome{Result: record.Grant, Events: []Event{{ObjectType: "host", ObjectID: record.Grant.Plan.HostID, ObjectVersion: record.Grant.ControlRevision, Kind: boundedRunnerAuthorizedEvent, Body: map[string]any{"grant": record.Grant, "protections": record.Protections, "physical_exit_confirmed": false, "business_outcomes_settled": false}}}}
}

func validateBoundedRunnerPlan(x *Exec, p protocol.RunnerBoundedMaintenancePlan) error {
	a := p.Authorization
	if strings.TrimSpace(p.ReleaseDir) == "" || strings.ContainsRune(p.ReleaseDir, 0) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "runner source plan requires a local release bundle path")
	}
	if x.Env.Target.Type != "host" || x.Env.Target.ID != p.HostID || p.HostID == "" || p.NodeSelector == "" || p.RunnerInstanceID == "" || p.RunnerGeneration < 1 || p.Source.PID <= 1 || p.Source.StartTicks == 0 || p.Source.LaunchID == "" || !release.ValidSHA(p.Source.SHA256) || p.Journal.Inode == 0 || p.Journal.Incarnation == "" || p.Journal.Version <= 0 || a.RequestID != x.Env.RequestID || a.Role != "runner" || a.Source != p.Source || a.Target != p.Target || a.DataIdentity == nil || *a.DataIdentity != p.Journal || a.ExpectedPhase == "" || a.ExpiresAtMillis <= x.Now.UnixMilli() || strings.TrimSpace(a.Reason) == "" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "complete independent runner process, journal, target and force authority required")
	}
	if x.Env.ExpectedControlRevision == nil || *x.Env.ExpectedControlRevision != p.ExpectedControlRevision {
		return protocol.Errorf(protocol.CodeStaleRevision, "runner host control revision must match the source plan")
	}
	b := p.Target
	if b.InstanceID != p.HostID || b.ProcessInstanceID == "" || b.ProcessInstanceID == p.RunnerInstanceID || b.ProcessInstanceID == p.Source.LaunchID || !release.ValidID(b.UpgradeID) || !release.ValidID(b.ReleaseID) || !release.ValidSHA(b.ManifestSHA256) || !release.ValidSHA(b.ArtifactSHA256) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "exact independent runner successor binding required")
	}
	digest, err := protocol.RunnerBoundedMaintenancePlanSHA256(p)
	if err != nil {
		return err
	}
	if a.PlanSHA256 != digest {
		return protocol.Errorf(protocol.CodeConflictPayload, "runner source plan digest changed")
	}
	t := a.Timeouts
	if t.OverallMillis <= 0 || t.OverallMillis > 30*60*1000 {
		return protocol.Errorf(protocol.CodeInvalidRequest, "runner force requires finite positive phase and overall budgets")
	}
	var total int64
	for _, millis := range []int64{t.HandoffMillis, t.FenceMillis, t.DrainMillis, t.StopMillis, t.KillMillis, t.BackupMillis, t.StartHealthMillis, t.CompleteMillis} {
		if millis <= 0 || millis > t.OverallMillis {
			return protocol.Errorf(protocol.CodeInvalidRequest, "runner force requires finite positive phase and overall budgets")
		}
		total += millis
	}
	if total > t.OverallMillis {
		return protocol.Errorf(protocol.CodeInvalidRequest, "runner phase budgets exceed the overall deadline")
	}
	return nil
}

// AuthorizeRunnerBoundedMaintenance preserves the caller's original request
// and CAS. A replay returns its frozen grant, never a newly extended window.
func (a *App) AuthorizeRunnerBoundedMaintenance(ctx context.Context, principal protocol.Principal, p protocol.RunnerBoundedMaintenancePlan) (protocol.BoundedRunnerMaintenanceGrant, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return protocol.BoundedRunnerMaintenanceGrant{}, err
	}
	env := &protocol.Envelope{RequestID: p.Authorization.RequestID, Operation: protocol.OpAuthorizeBoundedRunnerMaintenance, Target: protocol.Target{Type: "host", ID: p.HostID}, ExpectedControlRevision: &p.ExpectedControlRevision, Payload: raw, Reason: p.Authorization.Reason}
	op, err := a.Execute(ctx, principal, env, AuthorizeBoundedRunnerMaintenance{})
	if err != nil {
		return protocol.BoundedRunnerMaintenanceGrant{}, err
	}
	if op.Error != nil {
		return protocol.BoundedRunnerMaintenanceGrant{}, op.Error
	}
	var grant protocol.BoundedRunnerMaintenanceGrant
	err = json.Unmarshal(op.Result, &grant)
	return grant, err
}

func boundedRunnerProtectionRecords(ctx context.Context, q store.Querier, host string) ([]boundedRunnerProtectionRecord, error) {
	rows, err := q.QueryContext(ctx, `SELECT body_json FROM events WHERE source='app' AND object_type='host' AND object_id=? AND kind=? ORDER BY event_seq`, host, boundedRunnerAuthorizedEvent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []boundedRunnerProtectionRecord
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var record boundedRunnerProtectionRecord
		if err := json.Unmarshal([]byte(raw), &record); err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

// A committed same-version supervisor restart keeps the installation binding
// while its receipt identifies the new process. Ordinary Hello still verifies
// the process generation before this observation can authorize recovery.
func boundedRunnerRuntimeMatches(grant protocol.BoundedRunnerMaintenanceGrant, runtime protocol.RunnerRuntime) bool {
	p := grant.Plan
	if grant.RecoveryAuthorizationID == "" || runtime.InstanceID == "" || runtime.BoundedRecoveryAuthorizationID != grant.RecoveryAuthorizationID || runtime.Build.BinarySHA256 != p.Target.ArtifactSHA256 || runtime.Generation <= p.RunnerGeneration || runtime.JournalVersion < 17 || !slices.Contains(runtime.Capabilities, protocol.CapabilityBoundedRunnerRecovery) {
		return false
	}
	if runtime.InstanceID == p.Target.ProcessInstanceID {
		return true
	}
	receipt := runtime.UpdaterReceipt
	return receipt != nil && receipt.CommittedConfirmed && !receipt.RollbackConfirmed && receipt.Phase == "committed" && receipt.Binding == p.Target && receipt.CurrentProcessInstanceID == runtime.InstanceID && receipt.CurrentSHA256 == runtime.Build.BinarySHA256
}

func activeBoundedRunnerRunProtection(ctx context.Context, q store.Querier, r store.RunRow) (*store.MaintenanceRunProtection, error) {
	records, err := boundedRunnerProtectionRecords(ctx, q, r.HostID)
	if err != nil || len(records) == 0 {
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
	for _, record := range records {
		for _, p := range record.Protections {
			if p.RunID == r.RunID && p.TaskID == r.TaskID && p.ActionID == r.ActionID && p.HostID == r.HostID && p.LeaseID == lease.LeaseID && lease.Status != store.LeaseReleased {
				return &store.MaintenanceRunProtection{MaintenanceID: p.MaintenanceID, RunID: p.RunID, TaskID: p.TaskID, ActionID: p.ActionID, HostID: p.HostID, LeaseID: p.LeaseID, OriginalRequestsJSON: p.OriginalRequestsJSON}, nil
			}
		}
	}
	return nil, nil
}

func boundedRunnerReconciliationProtections(ctx context.Context, q store.Querier, r store.RunRow, runtime protocol.RunnerRuntime) ([]protocol.RetainedRunProtection, string, bool, error) {
	records, err := boundedRunnerProtectionRecords(ctx, q, r.HostID)
	if err != nil {
		return nil, "", false, err
	}
	var protections []protocol.RetainedRunProtection
	seen := map[string]bool{}
	var latest *protocol.BoundedRunnerMaintenanceGrant
	for _, record := range records {
		for _, p := range record.Protections {
			if p.RunID == r.RunID {
				if !seen[p.MaintenanceID] {
					protections = append(protections, p)
					seen[p.MaintenanceID] = true
				}
				grant := record.Grant
				latest = &grant
			}
		}
	}
	if latest == nil {
		return nil, "", false, nil
	}
	matched := boundedRunnerRuntimeMatches(*latest, runtime)
	// A runner capture is not a server committed-maintenance ledger. Linked
	// here denotes the exact successor's accepted startup observation only.
	for i := range protections {
		protections[i].Linked = matched
	}
	return protections, latest.RecoveryAuthorizationID, matched, nil
}

func attachBoundedRunnerReconciliation(ctx context.Context, q store.Querier, r store.RunRow, s *protocol.RetainedRunReconciliation) (bool, error) {
	runtime, err := store.GetHostRuntime(ctx, q, r.HostID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	protections, authority, matched, err := boundedRunnerReconciliationProtections(ctx, q, r, runtime.Runtime)
	if err != nil {
		return false, err
	}
	s.RecoveryAuthorizationID = authority
	if authority != "" && !matched {
		s.Blockers = append(s.Blockers, "bounded_recovery_runtime_mismatch")
	}
	for _, p := range protections {
		s.Protections = append(s.Protections, p)
		if p.TaskID != s.Identity.TaskID || p.ActionID != s.Identity.ActionID || p.HostID != s.Identity.HostID || p.LeaseID != s.Identity.LeaseID {
			s.Blockers = append(s.Blockers, "protection_identity_changed")
		}
		var requests []string
		if err := json.Unmarshal([]byte(p.OriginalRequestsJSON), &requests); err != nil {
			return false, err
		}
		for _, id := range requests {
			if slices.Contains(s.OriginalRequestIDs, id) {
				continue
			}
			s.OriginalRequestIDs = append(s.OriginalRequestIDs, id)
			var settled, manager bool
			if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM main_turns t WHERE t.request_id=? AND t.status NOT IN ('starting','started','unknown') AND NOT EXISTS(SELECT 1 FROM native_turn_receipts_pending n WHERE n.request_id=t.request_id)),EXISTS(SELECT 1 FROM manager_native_requests n WHERE n.request_id=?)`, id, id).Scan(&settled, &manager); err != nil {
				return false, err
			}
			// The main reconciliation query counts manager-native requests as
			// one complete set, including those created after this capture.
			if !settled && !manager {
				s.UnresolvedNativeRequests++
			}
		}
	}
	return matched, nil
}

func retainedContinuationRecoveryAuthorization(record *store.RetainedRunContinuation) string {
	if record == nil {
		return ""
	}
	var p protocol.ContinueRetainedRunPayload
	if json.Unmarshal([]byte(record.PayloadJSON), &p) != nil {
		return ""
	}
	return p.RecoveryAuthorizationID
}

// RunnerBoundedMaintenance reads the immutable owner authorization, including
// an expired one, for reconciliation. It does not mint an installation grant.
func (a *App) RunnerBoundedMaintenance(ctx context.Context, host, request string) (protocol.BoundedRunnerMaintenanceGrant, error) {
	var grant protocol.BoundedRunnerMaintenanceGrant
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		records, err := boundedRunnerProtectionRecords(ctx, tx.Q(), host)
		if err != nil {
			return err
		}
		for _, record := range slices.Backward(records) {
			if request == "" || record.Grant.Plan.Authorization.RequestID == request {
				grant = record.Grant
				return nil
			}
		}
		return protocol.Errorf(protocol.CodeNotFound, "bounded runner authority does not exist")
	})
	return grant, err
}
