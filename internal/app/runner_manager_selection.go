package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func runnerManagerSelection(ctx context.Context, q store.Querier, m store.IssueManagerTask) (hostedconfig.Selection, bool, error) {
	if m.Pin == nil || (m.State != "assigned" && m.State != "retiring") {
		return hostedconfig.Selection{}, false, protocol.Errorf(protocol.CodeInvalidTransition, "manager task awaits a runner claim")
	}
	var run string
	err := q.QueryRowContext(ctx, `SELECT r.run_id FROM runs r JOIN leases l USING(run_id) WHERE r.task_id=? AND r.observed_state IN ('starting','running','waiting','interrupted','unknown') AND l.status='active' ORDER BY r.created_at DESC LIMIT 1`, m.TaskID).Scan(&run)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return hostedconfig.Selection{}, false, err
	}
	closing, err := managerRunClosing(ctx, q, run)
	if err != nil {
		return hostedconfig.Selection{}, false, err
	}
	p := m.Pin
	owner, err := store.IssueUsesOwnerExecution(ctx, q, m.IssueID)
	if err != nil {
		return hostedconfig.Selection{}, false, err
	}
	policy := ""
	if owner {
		policy = protocol.IssueOwnerPolicy
	}
	return hostedconfig.Selection{OwnershipPolicy: policy, ReceiptOnly: closing, RunnerManaged: true, RunnerRunID: run, IssueID: m.IssueID, AssignmentEpoch: m.AssignmentEpoch, ConfigurationHash: p.PublicSHA256, Identity: p.Identity, Purpose: agentconfig.IssueManager, Definition: agentconfig.AgentDefinition{LocalID: p.Identity.LocalID, Revision: p.AgentRevision, RuntimeID: p.Runtime.RuntimeID, Categories: []string{m.CategoryID}, Purposes: []agentconfig.Purpose{agentconfig.IssueManager}, Model: p.Profile.Model, Effort: p.Profile.Effort, Enabled: true}, Runtime: agentconfig.RuntimeBinding{ID: p.Runtime.RuntimeID, Revision: p.Runtime.Revision, Kind: agentconfig.AppServerWorker, DeclaredPurposes: []agentconfig.Purpose{agentconfig.IssueManager}, Enabled: true}}, true, nil
}

// AgentConversationClaimed prevents the hosted pool from starting an Issue
// executor before its category task has been claimed by a runner.
func (a *App) AgentConversationClaimed(ctx context.Context, conversation string) (bool, error) {
	ready := true
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		c, err := store.AgentConversation(ctx, tx.Q(), conversation)
		if err != nil {
			return err
		}
		if c.Kind == "consultation" {
			return nil
		}
		// During a backed-up migration, the old host may need to read its
		// original native history before any successor can claim. This is a
		// receipt-only attachment, never another business coordinator.
		var hostedReceipt bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM local_hosted_executions e JOIN main_turns t ON t.request_id=e.execution_id JOIN issue_controls i ON i.issue_id=e.issue_id WHERE e.issue_id=? AND e.assignment_epoch<i.assignment_epoch AND (t.status IN ('starting','started','unknown') OR EXISTS(SELECT 1 FROM native_turn_receipts_pending p WHERE p.request_id=t.request_id)) AND EXISTS(SELECT 1 FROM manager_migrations mm JOIN issue_manager_tasks m ON m.task_id=mm.task_id WHERE m.issue_id=i.issue_id AND m.expected_epoch>e.assignment_epoch))`, c.WorkItemID).Scan(&hostedReceipt); err != nil {
			return err
		}
		if hostedReceipt {
			return nil
		}
		if selected, named, err := runnerReceiptSelection(ctx, tx.Q(), c.WorkItemID); err != nil {
			return err
		} else if named {
			ready = selected.RunnerRunID != ""
			return nil
		}
		m, err := store.CurrentIssueManagerTask(ctx, tx.Q(), c.WorkItemID)
		if errors.Is(err, store.ErrNotFound) {
			var migrated bool
			if err = tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_manager_tasks WHERE issue_id=?)`, c.WorkItemID).Scan(&migrated); err != nil {
				return err
			}
			ready = !migrated
			return nil
		}
		if err != nil {
			return err
		}
		selected, named, err := runnerManagerSelection(ctx, tx.Q(), m)
		ready = named && selected.RunnerRunID != ""
		if protocol.CodeOf(err) == protocol.CodeInvalidTransition {
			ready = false
			return nil
		}
		return err
	})
	return ready, err
}

func runnerReceiptSelection(ctx context.Context, q store.Querier, issue string) (hostedconfig.Selection, bool, error) {
	var task string
	err := q.QueryRowContext(ctx, `SELECT r.task_id FROM issue_manager_tasks m JOIN runs r USING(task_id) JOIN leases l USING(run_id) JOIN plan_actions a ON a.action_id=r.action_id WHERE m.issue_id=? AND l.status='active' AND r.observed_state IN ('starting','running','waiting','interrupted','unknown') AND json_extract(a.trigger_json,'$.receipt_only')=1 ORDER BY r.created_at LIMIT 1`, issue).Scan(&task)
	if errors.Is(err, sql.ErrNoRows) {
		return hostedconfig.Selection{}, false, nil
	}
	if err != nil {
		return hostedconfig.Selection{}, false, err
	}
	m, err := store.GetIssueManagerTask(ctx, q, task)
	if err != nil {
		return hostedconfig.Selection{}, false, err
	}
	selected, named, err := runnerManagerSelection(ctx, q, m)
	selected.ReceiptOnly = true
	return selected, named, err
}

// Rebind only the transport run, retaining the frozen native responsibility and
// binding identity. Old epochs receive a read-only reconciliation lease.
func rebindRunnerRuntime(ctx context.Context, q store.Querier, selected *hostedconfig.Selection) error {
	var run string
	var trigger string
	err := q.QueryRowContext(ctx, `SELECT r.run_id,a.trigger_json FROM issue_manager_tasks m JOIN runs r USING(task_id) JOIN leases l USING(run_id) JOIN plan_actions a ON a.action_id=r.action_id WHERE m.issue_id=? AND m.assignment_epoch=? AND l.status='active' AND r.observed_state IN ('starting','running','waiting','interrupted','unknown') ORDER BY r.created_at DESC LIMIT 1`, selected.IssueID, selected.AssignmentEpoch).Scan(&run, &trigger)
	if errors.Is(err, sql.ErrNoRows) {
		selected.RunnerRunID = ""
		return nil
	}
	if err != nil {
		return err
	}
	selected.RunnerRunID = run
	var receipt struct {
		ReceiptOnly bool `json:"receipt_only"`
	}
	if err = json.Unmarshal([]byte(trigger), &receipt); err != nil {
		return err
	}
	closing, err := managerRunClosing(ctx, q, run)
	if err != nil {
		return err
	}
	selected.ReceiptOnly = receipt.ReceiptOnly || closing
	return nil
}
