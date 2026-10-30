package app

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func recordManagerCancellationIntent(ctx context.Context, x *Exec, tx *store.Tx, issue, event string) error {
	manager, err := store.CurrentIssueManagerTask(ctx, tx.Q(), issue)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil || manager.State != "assigned" {
		return err
	}
	message := x.IDs.New(protocol.PrefixMessage)
	if _, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: message, ConversationID: manager.ConversationID, Author: "system", Body: "The owner requested cancellation of Issue " + issue + ". Reconcile remaining obligations and finish cancellation. Do not start or expand work.", CreatedAt: x.Now.UnixMilli()}); err != nil {
		return err
	}
	return appendBackgroundSource(ctx, x, tx, "work_item:"+issue, issue, message, event, "issue.cancelling", nil)
}

type FinishManagerTurn struct{}

func (FinishManagerTurn) Operation() string { return "manager.finish_turn" }
func (FinishManagerTurn) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalSystem)
}
func (FinishManagerTurn) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	r, err := store.GetRun(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	if r.Role != protocol.RoleIssueManager {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "not a manager run")
	}
	if r.ObservedState == protocol.RunFinished {
		return Outcome{Result: map[string]any{"run_id": r.RunID}}, nil
	}
	var stopped, uncertain bool
	if err = tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM manager_native_requests WHERE run_id=? AND method='close' AND status='completed' AND COALESCE(json_extract(result_json,'$.error'),'')='')`, r.RunID).Scan(&stopped); err != nil {
		return Outcome{}, err
	}
	uncertain, err = managerBusinessReceiptsPending(ctx, tx.Q(), r.RunID)
	if err != nil {
		return Outcome{}, err
	}
	if !stopped || uncertain {
		return Outcome{}, protocol.Errorf(protocol.CodeUnknownOutcome, "manager native execution requires reconciliation before releasing its slot")
	}
	if err = endRun(ctx, x, tx, r, protocol.RunFinished); err != nil {
		return Outcome{}, err
	}
	if err = tx.SetActionStatus(ctx, r.ActionID, protocol.ActionDone, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE tasks SET lifecycle='waiting',updated_at=? WHERE task_id=? AND lifecycle NOT IN ('completed','cancelled')`, x.Now.UnixMilli(), r.TaskID)
	return Outcome{Result: map[string]any{"run_id": r.RunID}, Events: []Event{{ObjectType: "run", ObjectID: r.RunID, Kind: "run.manager_waiting", Body: map[string]any{"task_id": r.TaskID}}}}, err
}
func (a *App) FinishRunnerManager(ctx context.Context, run string) error {
	env := &protocol.Envelope{RequestID: a.ids.New(protocol.PrefixRequest), Operation: "manager.finish_turn", Target: protocol.Target{Type: "run", ID: run}, Payload: json.RawMessage(`{}`)}
	op, err := a.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalSystem}, env, FinishManagerTurn{})
	if err == nil && op.Error != nil {
		err = op.Error
	}
	return err
}

// refreshManagerTasks turns committed inbox work into another ordinary action.
// Waiting responsibility holds neither a lease nor a host slot.
func refreshManagerTasks(ctx context.Context, x *Exec, tx *store.Tx) error {
	ids, err := store.FollowupIDs(ctx, tx.Q(), `SELECT task_id FROM issue_manager_tasks WHERE state IN ('pending','assigned','retiring') ORDER BY task_id`)
	if err != nil {
		return err
	}
	for _, id := range ids {
		m, err := store.GetIssueManagerTask(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		i, err := store.GetCanonicalIssue(ctx, tx.Q(), m.IssueID)
		if err != nil {
			return err
		}
		// Issue control fences coordinator runs without touching delegated
		// workers. Restore only pauses this projection introduced itself.
		if i.State == protocol.IssuePaused {
			if _, err = tx.Exec(ctx, `UPDATE issue_manager_tasks SET issue_paused=1 WHERE task_id=? AND EXISTS(SELECT 1 FROM tasks WHERE task_id=? AND control_mode='managed')`, id, id); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE tasks SET control_mode='paused',control_revision=control_revision+1 WHERE task_id=? AND control_mode='managed'`, id); err != nil {
				return err
			}
		} else if i.State == protocol.IssueActive || i.State == protocol.IssueCancelling {
			if _, err = tx.Exec(ctx, `UPDATE tasks SET control_mode='managed',control_revision=control_revision+1 WHERE task_id=? AND control_mode='paused' AND EXISTS(SELECT 1 FROM issue_manager_tasks WHERE task_id=? AND issue_paused=1)`, id, id); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE issue_manager_tasks SET issue_paused=0 WHERE task_id=?`, id); err != nil {
				return err
			}
		}
		task, err := store.GetTask(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		authorized, err := store.IssueManagerAuthorizationValid(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		if !authorized && !task.Lifecycle.Terminal() {
			if _, err = tx.Exec(ctx, `UPDATE tasks SET lifecycle='cancelled',control_revision=control_revision+1,updated_at=? WHERE task_id=?`, x.Now.UnixMilli(), id); err != nil {
				return err
			}
			task.Lifecycle = protocol.TaskCancelled
		}
		epoch := m.AssignmentEpoch
		if m.State == "pending" {
			epoch = m.ExpectedEpoch
		}
		retiring := i.State == protocol.IssueClosed || i.State == protocol.IssueCancelled || (i.State == protocol.IssueCancelling && m.State == "pending") || i.AssignmentEpoch != epoch || m.State == "retiring" || task.Lifecycle == protocol.TaskCancelled
		if retiring {
			// A terminal operation must return its tool result and account for
			// the native turn before stopping. Other fences interrupt execution.
			gracefulClose := (i.State == protocol.IssueClosed || i.State == protocol.IssueCancelled) && i.AssignmentEpoch == epoch && m.State == "assigned"
			if _, err = tx.Exec(ctx, `UPDATE plan_actions SET status='superseded' WHERE task_id=? AND (status='pending' OR (status='dispatched' AND NOT ?)) AND COALESCE(json_extract(trigger_json,'$.receipt_only'),0)=0`, id, gracefulClose); err != nil {
				return err
			}
		}
		var busy bool
		if err = tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE task_id=? AND observed_state IN ('starting','running','waiting','interrupted','unknown'))`, id).Scan(&busy); err != nil {
			return err
		}
		if busy {
			continue
		}
		receipt, err := views.ManagerReceiptPending(ctx, tx.Q(), m)
		if err != nil {
			return err
		}
		if receipt {
			if retiring && m.State != "retiring" {
				if _, err = tx.Exec(ctx, `UPDATE issue_manager_tasks SET state='retiring' WHERE task_id=?`, id); err != nil {
					return err
				}
			}
			due, err := views.ManagerReceiptDue(ctx, tx.Q(), m, x.Now.UnixMilli())
			if err != nil {
				return err
			}
			if due {
				var queued bool
				if err = tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM plan_actions WHERE task_id=? AND kind='coordinate' AND status IN ('pending','dispatched') AND json_extract(trigger_json,'$.receipt_only')=1)`, id).Scan(&queued); err != nil {
					return err
				}
				if !queued {
					var generation int64
					if err = tx.Q().QueryRowContext(ctx, `SELECT COALESCE(MAX(generation),0)+1 FROM plan_actions WHERE task_id=?`, id).Scan(&generation); err != nil {
						return err
					}
					if err = insertManagerAction(ctx, x, tx, id, generation); err != nil {
						return err
					}
					if _, err = tx.Exec(ctx, `UPDATE plan_actions SET trigger_json='{"receipt_only":true}' WHERE task_id=? AND generation=? AND kind='coordinate'`, id, generation); err != nil {
						return err
					}
				}
			}
			continue
		}
		if retiring {
			state, life := "completed", "completed"
			if i.State == protocol.IssueCancelled || i.State == protocol.IssueCancelling || task.Lifecycle == protocol.TaskCancelled {
				state, life = "cancelled", "cancelled"
			}
			if _, err = tx.Exec(ctx, `UPDATE issue_manager_tasks SET state=?,updated_at=? WHERE task_id=?`, state, x.Now.UnixMilli(), id); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE tasks SET lifecycle=?,updated_at=? WHERE task_id=?`, life, x.Now.UnixMilli(), id); err != nil {
				return err
			}
			continue
		}
		if (i.State != protocol.IssueActive && i.State != protocol.IssueCancelling) || m.State == "pending" {
			continue
		}
		var pending, queued bool
		if err = tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM outbox WHERE destination=? AND status='pending' AND next_attempt_at<=?),EXISTS(SELECT 1 FROM plan_actions WHERE task_id=? AND kind='coordinate' AND status IN ('pending','dispatched'))`, "background_thread:"+m.ConversationID, x.Now.UnixMilli(), id).Scan(&pending, &queued); err != nil {
			return err
		}
		if !pending || queued {
			continue
		}
		var generation int64
		if err = tx.Q().QueryRowContext(ctx, `SELECT COALESCE(MAX(generation),0)+1 FROM plan_actions WHERE task_id=?`, id).Scan(&generation); err != nil {
			return err
		}
		if err = insertManagerAction(ctx, x, tx, id, generation); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE tasks SET lifecycle='queued',updated_at=? WHERE task_id=?`, x.Now.UnixMilli(), id); err != nil {
			return err
		}
	}
	return nil
}

func managerBusinessReceiptsPending(ctx context.Context, q store.Querier, run string) (bool, error) {
	var uncertain bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hosted_executions e JOIN main_turns t ON t.request_id=e.execution_id WHERE json_extract(e.private_snapshot,'$.runner_run_id')=? AND (t.status IN ('starting','started','unknown') OR EXISTS(SELECT 1 FROM native_turn_receipts_pending p WHERE p.request_id=e.execution_id)))`, run).Scan(&uncertain)
	return uncertain, err
}
