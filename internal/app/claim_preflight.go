package app

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
)

// preflightClaim avoids acquiring the sole writer for idle polls. It grants no
// execution rights: every nonempty result goes through the ordinary transaction,
// with fresh runtime, control, lease, concurrency and candidate validation.
func (a *App) preflightClaim(ctx context.Context, p protocol.Principal, env *protocol.Envelope) (bool, error) {
	var payload protocol.ClaimPayload
	if json.Unmarshal(env.Payload, &payload) != nil || payload.WorkspaceProtocol < 0 || payload.WorkspaceProtocol > 1 {
		return false, nil // Preserve normal validation of the actual claim.
	}
	noWork := false
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		q := tx.Q()
		if !protocol.ValidID(protocol.PrefixRun, payload.RunID) {
			return nil
		}
		if _, err := store.GetRun(ctx, q, payload.RunID); err == nil {
			return nil
		}
		x := &Exec{Env: env, Principal: p, requestDomain: p.DedupDomain()}
		if err := checkClaimRuntime(ctx, x, q); err != nil {
			return nil // The authoritative command retains its rejection semantics.
		}
		fenced, err := a.recoveryStartupFenced(ctx, q)
		if err != nil || fenced {
			return err
		}
		host, err := store.GetHost(ctx, q, p.Subject)
		if err != nil {
			return err
		}
		if host.WorkspaceProtocol != payload.WorkspaceProtocol {
			return nil // A changed report must invalidate baselines in the writer.
		}
		_, busy, err := store.HostSlotCounts(ctx, q, p.Subject)
		if err != nil {
			return err
		}
		if busy >= host.Slots {
			noWork = true
			return nil
		}
		ids, err := store.ClaimActionIDsForHost(ctx, q, p.Subject)
		if err != nil || len(ids) > 0 {
			return err
		}
		var potential bool
		// Pending manager maintenance and stopped handoffs may create a new
		// action inside Claim. Keep those conservative paths in the writer.
		err = q.QueryRowContext(ctx, `SELECT
		 EXISTS(SELECT 1 FROM issue_manager_tasks m WHERE m.state IN ('pending','assigned','retiring')
		 AND NOT EXISTS(SELECT 1 FROM runs r WHERE r.task_id=m.task_id AND r.observed_state IN ('starting','running','waiting','interrupted','unknown')))
		 OR EXISTS(SELECT 1 FROM plan_actions a JOIN issue_manager_tasks m USING(task_id)
		 WHERE a.kind='coordinate' AND a.status='pending' AND json_extract(a.trigger_json,'$.receipt_only')=1)
		 OR EXISTS(SELECT 1 FROM run_pause_handoffs WHERE stopped=1 AND continuation_action_id IS NULL AND successor_run_id IS NULL)
		 OR EXISTS(SELECT 1 FROM run_external_waits WHERE status='waiting' AND next_check_at<=?)`, a.now().UnixMilli()).Scan(&potential)
		if err != nil || potential {
			return err
		}
		potential, err = a.initialOwnerClaimPossible(ctx, q, p.Subject, payload.Runtime)
		noWork = err == nil && !potential
		return err
	})
	return noWork, err
}

// initialOwnerClaimPossible checks only whether a named executor could match an
// admitted definition. It never skips maintenance for a materialized manager,
// allocates resources, or carries this observation into the claim transaction.
func (a *App) initialOwnerClaimPossible(ctx context.Context, q store.Querier, host string, runtime *protocol.RunnerRuntime) (bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT i.owner_selection_json,i.assignee_kind,
 CASE WHEN i.assignee_kind='agent' THEN i.assignee_ref ELSE '' END,
 CASE WHEN i.assignee_kind='agent' THEN i.issue_id ELSE '' END,
 CASE WHEN i.assignee_kind='agent' THEN i.assignment_epoch ELSE 0 END
 FROM issue_controls i WHERE i.state='active' AND i.assignment_state='active' AND i.work_status IN ('todo','in_progress','in_review')
 AND i.owner_selection_json<>'' AND NOT EXISTS(SELECT 1 FROM issue_manager_tasks m WHERE m.issue_id=i.issue_id AND m.state IN ('pending','assigned'))`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return false, rows.Err()
	}
	if !runtimeSupportsAgentExecution(runtime) || !slices.Contains(runtime.Capabilities, protocol.CapabilityIssueManager) {
		return true, nil
	}
	observed, err := store.GetHostRuntime(ctx, q, host)
	if errors.Is(err, store.ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !slices.Equal(runtime.AgentExecutors, observed.Runtime.AgentExecutors) || !slices.Equal(runtime.Capabilities, observed.Runtime.Capabilities) {
		return true, nil
	}
	if _, err := store.GetAgentInventory(ctx, q, host); errors.Is(err, store.ErrNotFound) {
		return true, nil
	} else if err != nil {
		return false, err
	}
	executors, err := hostExecutors(ctx, q, host, a.now().UnixMilli())
	if err != nil {
		return false, err
	}
	h := scheduler.Host{ID: host, Executors: executors, IssueOwners: slices.Contains(runtime.Capabilities, protocol.CapabilityIssueOwner)}
	for {
		var body string
		var issue protocol.IssueControl
		if err := rows.Scan(&body, &issue.Assignee.Kind, &issue.Assignee.Ref, &issue.IssueID, &issue.AssignmentEpoch); err != nil {
			return false, err
		}
		var selection *protocol.IssueOwnerSelection
		if json.Unmarshal([]byte(body), &selection) != nil || selection == nil {
			return true, nil
		}
		agentID, err := store.IssueOwnerAgentID(ctx, q, issue, selection.AgentID)
		if err != nil {
			return false, err
		}
		c := scheduler.Candidate{Manager: &protocol.IssueManagerSpec{CategoryID: selection.CategoryID, AgentID: agentID, OwnershipPolicy: selection.OwnershipPolicy}}
		if pin, _ := scheduler.SelectExecution(h, c); pin != nil {
			return true, nil
		}
		if !rows.Next() {
			return false, rows.Err()
		}
	}
}
