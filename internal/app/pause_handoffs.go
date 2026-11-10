package app

import (
	"context"
	"errors"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

// recordIssuePause snapshots the entire responsibility tree in the Issue write.
// Task controls remain independent; a quick resume cannot erase a stop in flight.
func recordIssuePause(ctx context.Context, x *Exec, tx *store.Tx, issue protocol.Issue) error {
	if _, err := tx.Exec(ctx, `INSERT INTO issue_pause_operations(request_id,issue_id,issue_revision,created_at) VALUES(?,?,?,?)`, x.Env.RequestID, issue.IssueID, issue.Revision, x.Now.UnixMilli()); err != nil {
		return err
	}
	scopeSQL := `WITH RECURSIVE scope(id) AS (SELECT ? UNION ALL SELECT i.issue_id FROM issue_controls i JOIN scope s ON i.parent_issue_id=s.id) `
	if _, err := tx.Exec(ctx, scopeSQL+`INSERT INTO issue_pause_tasks(issue_id,issue_revision,task_id,control_revision) SELECT ?,?,t.task_id,t.control_revision FROM tasks t WHERE t.lifecycle NOT IN ('completed','cancelled') AND (EXISTS(SELECT 1 FROM task_work_item_owners o JOIN scope s ON s.id=o.work_item_id WHERE o.task_id=t.task_id) OR EXISTS(SELECT 1 FROM run_issue_responsibilities rr JOIN scope s ON s.id=rr.issue_id JOIN runs prior ON prior.run_id=rr.run_id WHERE prior.task_id=t.task_id) OR EXISTS(SELECT 1 FROM task_execution_contracts c JOIN scope s ON s.id=c.review_issue_id WHERE c.task_id=t.task_id))`, issue.IssueID, issue.IssueID, issue.Revision); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, scopeSQL+`INSERT INTO run_pause_handoffs(run_id,request_id,issue_id,issue_revision,control_revision) SELECT r.run_id,?,?,?,p.control_revision FROM issue_pause_tasks p JOIN runs r USING(task_id) WHERE p.issue_id=? AND p.issue_revision=? AND r.observed_state IN ('starting','running','waiting','interrupted','unknown') AND EXISTS(SELECT 1 FROM scope s WHERE s.id=COALESCE((SELECT rr.issue_id FROM run_issue_responsibilities rr WHERE rr.run_id=r.run_id),CASE WHEN r.role='accept' THEN (SELECT NULLIF(c.review_issue_id,'') FROM task_execution_contracts c WHERE c.task_id=r.task_id) END,(SELECT o.work_item_id FROM task_work_item_owners o WHERE o.task_id=r.task_id))) ON CONFLICT(run_id) DO NOTHING`, issue.IssueID, x.Env.RequestID, issue.IssueID, issue.Revision, issue.IssueID, issue.Revision)

	return err
}

type YieldPause struct{}

func (YieldPause) Operation() string { return protocol.OpRunsYieldPause }
func (YieldPause) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	if x.Principal.Kind != protocol.PrincipalRun || x.Principal.Subject != x.Env.Target.ID {
		return protocol.Errorf(protocol.CodeForbidden, "only the source execution may confirm physical stop")
	}
	return nil
}
func (YieldPause) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.YieldPausePayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	rc, err := loadRun(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	expected, err := store.NegotiatedPauseHandoff(ctx, tx.Q(), rc.run)
	if err != nil {
		return Outcome{}, err
	}
	if expected == nil || *expected != p.PauseHandoff || x.Principal.Role != rc.run.Role {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "run has no matching negotiated pause handoff")
	}
	if err := endRun(ctx, x, tx, rc.run, protocol.RunFinished); err != nil {
		return Outcome{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE run_pause_handoffs SET stopped=1,thread_id=? WHERE run_id=?`, p.ThreadID, rc.run.RunID); err != nil {
		return Outcome{}, err
	}
	if rc.run.Role == protocol.RoleIssueManager {
		if err := settleStoppedManagerRequests(ctx, tx, rc.run.RunID); err != nil {
			return Outcome{}, err
		}
	}
	scope, e := store.ResolveTaskIssueScope(ctx, tx.Q(), rc.run.TaskID)
	if e != nil {
		return Outcome{}, e
	}
	if scope.Canonical {
		issue, e := store.GetCanonicalIssue(ctx, tx.Q(), scope.PrimaryID)
		if e != nil {
			return Outcome{}, e
		}
		if issue.State == protocol.IssueCancelling && rc.action.Status == protocol.ActionDispatched {
			if e := tx.SetActionStatus(ctx, rc.action.ActionID, protocol.ActionDone, x.Now.UnixMilli()); e != nil {
				return Outcome{}, e
			}
		}
	}

	// The same physical stop also satisfies an outstanding question handoff;
	// that existing continuation owns the successor, never a second action.
	h, err := store.GetQuestionHandoff(ctx, tx.Q(), rc.run.RunID)
	if err != nil {
		return Outcome{}, err
	}
	if h != nil {
		if _, err := tx.Exec(ctx, `UPDATE run_question_handoffs SET stopped=1,thread_id=? WHERE run_id=?`, p.ThreadID, rc.run.RunID); err != nil {
			return Outcome{}, err
		}
		if err := resumeAnsweredQuestion(ctx, x, tx, h.DecisionID); err != nil {
			return Outcome{}, err
		}
	}
	if err := refreshPauseContinuations(ctx, x, tx); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: map[string]any{"run_id": rc.run.RunID, "observed": protocol.RunFinished, "pause_request_id": p.RequestID}, Events: []Event{{ObjectType: "run", ObjectID: rc.run.RunID, Kind: "run.pause_yielded", Body: map[string]any{"pause": p.PauseHandoff}}}}, nil
}

func refreshPauseContinuations(ctx context.Context, x *Exec, tx *store.Tx) error {
	// Retained backlog is not ready work. Avoid loading its frozen Task and
	// responsibility graph on every unrelated native event or empty claim.
	// Cancellation of the primary responsibility must still settle its action,
	// including when the original pause was requested by a held review Issue.
	ids, err := store.FollowupIDs(ctx, tx.Q(), `SELECT h.run_id FROM run_pause_handoffs h JOIN runs r USING(run_id) JOIN plan_actions a ON a.action_id=r.action_id JOIN issue_controls paused ON paused.issue_id=h.issue_id JOIN task_work_item_owners o ON o.task_id=r.task_id JOIN issue_controls primary_issue ON primary_issue.issue_id=o.work_item_id WHERE h.stopped=1 AND h.continuation_action_id IS NULL AND h.successor_run_id IS NULL AND a.status='dispatched' AND ((paused.work_status<>'backlog' AND paused.state<>'paused') OR primary_issue.state='cancelling') AND NOT EXISTS(SELECT 1 FROM run_question_handoffs q WHERE q.run_id=h.run_id) ORDER BY h.run_id`)
	if err != nil {
		return err
	}
	for _, id := range ids {
		r, err := store.GetRun(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		a, err := store.GetAction(ctx, tx.Q(), r.ActionID)
		if err != nil {
			return err
		}
		t, err := store.GetTask(ctx, tx.Q(), r.TaskID)
		if err != nil {
			return err
		}
		if t.Lifecycle.Terminal() || t.ControlMode != protocol.ControlManaged || r.DesiredState == protocol.DesiredStopped || a.Status != protocol.ActionDispatched || a.TargetSpecVersion != t.SpecVersion || (a.Kind == protocol.ActionImplement && a.Generation != t.ImplementGeneration) || (a.Kind == protocol.ActionAccept && a.TargetArtifactID != t.CurrentArtifactID) {
			continue
		}
		scope, err := store.ResolveTaskIssueScope(ctx, tx.Q(), r.TaskID)
		if err != nil {
			return err
		}
		if !scope.Canonical {
			continue
		}
		issue, err := store.GetCanonicalIssue(ctx, tx.Q(), scope.PrimaryID)
		if err != nil {
			return err
		}
		if issue.State == protocol.IssueCancelling {
			// Physical stop was already confirmed before cancellation. Retire
			// its old action so normal cancellation settlement can be claimed;
			// no business pause continuation is published.
			if err := tx.SetActionStatus(ctx, a.ActionID, protocol.ActionDone, x.Now.UnixMilli()); err != nil {
				return err
			}
			continue
		}

		c := scheduler.Candidate{DependenciesDone: true}
		if err := views.ApplyIssueAdmission(ctx, tx.Q(), &c, scope.PrimaryID, r.Role == protocol.RoleIssueManager); err != nil {
			return err
		}
		if r.Role == protocol.RoleAccept {
			contract, err := store.GetTaskOwnerContract(ctx, tx.Q(), r.TaskID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
			if err == nil && contract.ReviewIssueID != "" {
				if err := views.ApplyIssueAdmission(ctx, tx.Q(), &c, contract.ReviewIssueID, false); err != nil {
					return err
				}
			}
		}
		if !c.DependenciesDone || c.AdmissionMessage != "" {
			continue
		}
		current, err := store.RunIssueResponsibilityCurrent(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		if !current {
			continue
		}
		if r.Role == protocol.RoleIssueManager {
			m, err := store.GetIssueManagerTask(ctx, tx.Q(), r.TaskID)
			if err != nil {
				return err
			}
			if m.State != "assigned" {
				continue
			}
			pending, err := views.ManagerReceiptPending(ctx, tx.Q(), m)
			if err != nil {
				return err
			}
			var nativeUnknown bool
			if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM manager_native_requests n JOIN runs r USING(run_id) WHERE r.task_id=? AND n.status IN ('started','unknown') AND n.method NOT IN ('models','find','usage'))`, r.TaskID).Scan(&nativeUnknown); err != nil {
				return err
			}
			pending = pending || nativeUnknown
			if pending {
				continue
			}
		} else {
			current, err := store.TaskOwnerContractCurrent(ctx, tx.Q(), r.TaskID)
			if err != nil {
				return err
			}
			if !current {
				continue
			}
		}
		original := a.ActionID
		a.ActionID = x.IDs.New(protocol.PrefixAction)
		a.EffectKey = "pause_continuation|" + id
		a.Status = protocol.ActionPending
		a.CreatedAt = x.Now.UnixMilli()
		a.CreatedRequestID = x.Env.RequestID
		if err := tx.InsertAction(ctx, a); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE run_pause_handoffs SET continuation_action_id=? WHERE run_id=?`, a.ActionID, id); err != nil {
			return err
		}
		if err := tx.SetActionStatus(ctx, original, protocol.ActionDone, x.Now.UnixMilli()); err != nil {
			return err
		}
	}
	return nil
}

// PauseHandoffReplayCredential grants only an identical committed stop receipt.
func (a *App) PauseHandoffReplayCredential(ctx context.Context, run string, env *protocol.Envelope) (leaseID, hostID string, role protocol.RunRole, err error) {
	if env.Operation != protocol.OpRunsYieldPause || env.Target.Type != "run" || env.Target.ID != run {
		return "", "", "", store.ErrNotFound
	}
	hash, err := protocol.PayloadHash(env)
	if err != nil {
		return "", "", "", store.ErrNotFound
	}
	err = a.store.Read(ctx, func(tx *store.ReadTx) error {
		r, err := store.GetRun(ctx, tx.Q(), run)
		if err != nil {
			return err
		}
		record, err := store.GetRequest(ctx, tx.Q(), (protocol.Principal{Kind: protocol.PrincipalRun, Subject: run, Role: r.Role}).DedupDomain(), env.RequestID)
		if err != nil {
			return err
		}
		if record.Status != protocol.OpSucceeded || record.Operation != env.Operation || record.PayloadHash != hash {
			return store.ErrNotFound
		}
		var stopped bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT stopped FROM run_pause_handoffs WHERE run_id=?`, run).Scan(&stopped); err != nil {
			return err
		}
		if !stopped {
			return store.ErrNotFound
		}
		lease, err := store.GetLeaseByRun(ctx, tx.Q(), run)
		if err != nil {
			return err
		}
		leaseID, hostID, role = lease.LeaseID, r.HostID, r.Role
		return nil
	})
	return
}

func (a *App) nativePausePredecessor(ctx context.Context, run string) (string, error) {
	var parent string
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		ids, err := store.FollowupIDs(ctx, tx.Q(), `SELECT h.run_id FROM run_pause_handoffs h JOIN runs old ON old.run_id=h.run_id JOIN runs next ON next.run_id=h.successor_run_id WHERE h.successor_run_id=? AND h.stopped=1 AND old.observed_state='finished' AND next.role=old.role AND next.role='implement' AND next.host_id=old.host_id AND next.generation=old.generation AND next.task_id=old.task_id`, run)
		if err != nil || len(ids) == 0 {
			return err
		}
		previous, err := store.GetRunExecution(ctx, tx.Q(), ids[0])
		if err != nil {
			return err
		}
		next, err := store.GetRunExecution(ctx, tx.Q(), run)
		if err != nil {
			return err
		}
		if previous == nil || next == nil || *previous != *next {
			return protocol.Errorf(protocol.CodeForbidden, "pause successor execution identity changed")
		}
		parent = ids[0]
		return nil
	})
	return parent, err
}
