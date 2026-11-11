package app

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
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
		busy, err := store.CountOpenRuns(ctx, q, p.Subject)
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
		 OR EXISTS(SELECT 1 FROM issue_controls i WHERE i.state='active' AND i.assignment_state='active'
		 AND i.work_status IN ('todo','in_progress','in_review') AND i.owner_selection_json<>''
		 AND NOT EXISTS(SELECT 1 FROM issue_manager_tasks m WHERE m.issue_id=i.issue_id AND m.state IN ('pending','assigned')))
		 OR EXISTS(SELECT 1 FROM run_pause_handoffs WHERE stopped=1 AND continuation_action_id IS NULL AND successor_run_id IS NULL)
		 OR EXISTS(SELECT 1 FROM run_external_waits WHERE status='waiting' AND next_check_at<=?)`, a.now().UnixMilli()).Scan(&potential)
		noWork = !potential
		return err
	})
	return noWork, err
}
