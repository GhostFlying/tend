package app

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

const OpManagerMigration = "issues.manager_migration"

type ManagerMigration struct{ Backup *store.ManagerBackupReceipt }

func (ManagerMigration) Operation() string { return OpManagerMigration }
func (ManagerMigration) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}

func (c ManagerMigration) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.ManagerMigrationPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if p.Phase == "finalize" {
		expected, err := requireRevision(x.Env)
		if err != nil {
			return Outcome{}, err
		}
		issue, err := store.GetCanonicalIssue(ctx, tx.Q(), x.Env.Target.ID)
		if err != nil {
			return Outcome{}, err
		}
		if issue.Revision != expected || issue.AssignmentEpoch != p.ExpectedAssignmentEpoch {
			return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "cutover issue revision or assignment changed")
		}
		var remaining bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_controls i WHERE i.state IN ('active','paused','cancelling') AND i.assignee_kind='agent' AND NOT EXISTS(SELECT 1 FROM issue_manager_tasks m WHERE m.issue_id=i.issue_id AND m.state='assigned' AND m.assignment_epoch=i.assignment_epoch)) OR EXISTS(SELECT 1 FROM local_hosted_executions e JOIN main_turns t ON t.request_id=e.execution_id WHERE e.role='issue_manager' AND (t.status IN ('starting','started','unknown') OR EXISTS(SELECT 1 FROM native_turn_receipts_pending p WHERE p.request_id=e.execution_id)))`).Scan(&remaining); err != nil {
			return Outcome{}, err
		}
		if remaining {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "all existing manager responsibilities and receipts must migrate before finalizing")
		}
		if strings.TrimSpace(p.Reason) == "" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "cutover reason is required")
		}
		_, err = tx.Exec(ctx, `UPDATE manager_runner_cutover SET finalized=1,request_id=?,updated_at=? WHERE singleton=1`, x.Env.RequestID, x.Now.UnixMilli())
		return Outcome{Result: map[string]any{"phase": "finalized"}, Events: []Event{{ObjectType: "configuration", ObjectID: "issue-managers", Kind: "managers.runner_cutover", Body: map[string]any{"reason": p.Reason}}}}, err
	}
	if p.CategoryID == "" {
		p.CategoryID = protocol.DefaultIssueManagerCategory
	}
	if p.Phase == "status" {
		m, err := store.CurrentIssueManagerTask(ctx, tx.Q(), x.Env.Target.ID)
		if err != nil {
			return Outcome{}, err
		}
		blockers, err := hostedHandoffBlockers(ctx, tx.Q(), m.IssueID, m.ExpectedEpoch)
		return Outcome{NoEffect: true, Result: map[string]any{"manager": m, "blockers": blockers}}, err
	}
	current, err := store.GetCanonicalIssue(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	expected, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	if current.Revision != expected || current.AssignmentEpoch != p.ExpectedAssignmentEpoch {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "migration Issue revision or epoch changed")
	}
	if p.Phase == "cancel" {
		m, err := store.CurrentIssueManagerTask(ctx, tx.Q(), current.IssueID)
		if err != nil {
			return Outcome{}, err
		}
		if m.State != "pending" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "claimed responsibility requires a reconciled reverse category handoff")
		}
		blockers, err := hostedHandoffBlockers(ctx, tx.Q(), current.IssueID, current.AssignmentEpoch)
		if err != nil {
			return Outcome{}, err
		}
		if len(blockers) > 0 {
			return Outcome{}, canonicalEndpointError("old execution must settle before returning responsibility to owner", blockers)
		}
		if strings.TrimSpace(p.Reason) == "" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "cancellation reason is required")
		}
		next := current
		next.Assignee = protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: x.Principal.Subject}
		next.AssignmentEpoch++
		next.Revision++
		next.UpdatedAt = x.Now.UnixMilli()
		if _, err = tx.Exec(ctx, `UPDATE tasks SET lifecycle='cancelled',updated_at=? WHERE task_id=?`, x.Now.UnixMilli(), m.TaskID); err != nil {
			return Outcome{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE issue_manager_tasks SET state='cancelled',updated_at=? WHERE task_id=?`, x.Now.UnixMilli(), m.TaskID); err != nil {
			return Outcome{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE manager_handoffs SET stage='cancelled',updated_at=? WHERE handoff_id=?`, x.Now.UnixMilli(), m.HandoffID); err != nil {
			return Outcome{}, err
		}
		if err := redirectPendingIssueInputsToOwner(ctx, x, tx, current.IssueID); err != nil {
			return Outcome{}, err
		}
		return saveCanonicalOwnerDefinition(ctx, x, tx, next, current.Revision, canonicalOwnerIssueSource{}, p, "issue.manager_migration_cancelled")
	}
	eligible, err := managerMigrationExecutors(ctx, tx.Q(), p.CategoryID, x.Now.UnixMilli())
	if err != nil {
		return Outcome{}, err
	}
	blockers, err := hostedHandoffBlockers(ctx, tx.Q(), current.IssueID, current.AssignmentEpoch+1)
	if err != nil {
		return Outcome{}, err
	}
	if p.Phase == "preflight" {
		return Outcome{NoEffect: true, Result: map[string]any{"phase": "preflight", "category_id": p.CategoryID, "eligible_executors": eligible, "blockers": blockers, "backup_required": true}}, nil
	}
	if p.Phase != "switch" || strings.TrimSpace(p.Reason) == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "migration phase and reason are required")
	}
	if eligible == 0 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "no accepted protocol-2 manager executor is available for the category")
	}
	if c.Backup == nil || c.Backup.ID != x.Env.RequestID {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "migration requires the server's consistent backup receipt")
	}
	input := protocol.RequestIssueManagerPayload{CategoryID: p.CategoryID, ExpectedAssignmentEpoch: p.ExpectedAssignmentEpoch, Reason: p.Reason, Replace: current.Assignee.Kind == protocol.IssueAssigneeAgent}
	raw, err := json.Marshal(input)
	if err != nil {
		return Outcome{}, err
	}
	adapted := *x
	env := *x.Env
	env.Payload = raw
	adapted.Env = &env
	out, err := (RequestIssueManager{migration: true}).Apply(ctx, &adapted, tx)
	if err != nil {
		return Outcome{}, err
	}
	manager, ok := out.Result.(store.IssueManagerTask)
	if !ok {
		return Outcome{}, protocol.Errorf(protocol.CodeInternal, "migration produced no manager responsibility")
	}
	backup, err := json.Marshal(c.Backup)
	if err != nil {
		return Outcome{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO manager_migrations(request_id,issue_id,task_id,backup_json,created_at) VALUES(?,?,?,?,?)`, x.Env.RequestID, current.IssueID, manager.TaskID, string(backup), x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	out.Result = map[string]any{"manager": manager, "backup": c.Backup, "phase": "fencing"}
	return out, nil
}

func managerMigrationExecutors(ctx context.Context, q store.Querier, category string, now int64) (int, error) {
	hosts, err := store.ListActiveHosts(ctx, q)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, host := range hosts {
		runtime, err := store.GetHostRuntime(ctx, q, host.HostID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if runtime.Runtime.Protocol < 2 || !slices.Contains(runtime.Runtime.Capabilities, protocol.CapabilityIssueManager) {
			continue
		}
		control, err := store.GetHostControl(ctx, q, host.HostID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return 0, err
		}
		if control.Mode != protocol.RunnerNormal || runtime.Runtime.Mode != protocol.RunnerNormal || control.Revision != runtime.Runtime.AppliedControlRevision {
			continue
		}
		executors, err := views.HostExecutors(ctx, q, host.HostID, now)
		if err != nil {
			return 0, err
		}
		for _, e := range executors {
			if e.Pin.Purpose == "issue_manager" && slices.Contains(e.Categories, category) {
				count++
			}
		}
	}
	return count, nil
}

// MigrateManager separates immutable backup IO from the atomic ledger change.
// A retry uses the same backup and request, never a second handoff.
func (a *App) MigrateManager(ctx context.Context, p protocol.Principal, env *protocol.Envelope) (protocol.Operation, error) {
	if p.Kind != protocol.PrincipalOwner {
		return protocol.Operation{}, protocol.Errorf(protocol.CodeForbidden, "only owner may migrate a manager")
	}
	if err := p.Validate(); err != nil {
		return protocol.Operation{}, err
	}
	if err := env.Validate(); err != nil {
		return protocol.Operation{}, err
	}
	var payload protocol.ManagerMigrationPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return protocol.Operation{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid migration payload")
	}
	command := ManagerMigration{}
	if payload.Phase == "switch" {
		recorded := false
		err := a.store.Read(ctx, func(tx *store.ReadTx) error {
			_, err := store.GetRequest(ctx, tx.Q(), p.DedupDomain(), env.RequestID)
			if err == nil {
				recorded = true
				return nil
			}
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			return err
		})
		if err != nil {
			return protocol.Operation{}, err
		}
		if !recorded {
			// Validate current eligibility/revisions before producing a backup. Recheck
			// them in the write transaction after IO; intervening edits remain fenced.
			probe := *env
			// This preparatory request must never consume the switch's
			// identity with a different payload, including on rejection.
			probe.RequestID = a.ids.New(protocol.PrefixRequest)
			input := payload
			input.Phase = "preflight"
			probe.Payload, _ = json.Marshal(input)
			preflight, err := a.Execute(ctx, p, &probe, command)
			if err != nil {
				return preflight, err
			}
			if preflight.Status != protocol.OpSucceeded {
				// Re-evaluate the original command to record its deterministic
				// rejection under the original envelope and payload hash.
				return a.Execute(ctx, p, env, command)
			}
			receipt, err := a.store.BackupManagers(ctx, env.RequestID)
			if err != nil {
				return protocol.Operation{}, err
			}
			command.Backup = &receipt
		}
	}
	return a.Execute(ctx, p, env, command)
}
