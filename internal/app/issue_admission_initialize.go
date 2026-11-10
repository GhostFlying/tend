package app

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// InitializeIssueAdmission appends current definitions without changing retained
// requests, historical snapshots, native operations or explicit backlog intent.
func (a *App) InitializeIssueAdmission(ctx context.Context) error {
	var ids []string
	if err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		ids, err = store.FollowupIDs(ctx, tx.Q(), `SELECT issue_id FROM issue_controls WHERE admission_initialized=0 OR EXISTS(SELECT 1 FROM issue_manager_tasks m WHERE m.issue_id=issue_controls.issue_id AND m.issue_paused=1) OR (work_status='backlog' AND EXISTS(SELECT 1 FROM runs r JOIN task_work_item_owners o USING(task_id) WHERE o.work_item_id=issue_controls.issue_id AND r.observed_state IN ('starting','running','waiting','interrupted','unknown') AND NOT EXISTS(SELECT 1 FROM run_pause_handoffs p WHERE p.run_id=r.run_id))) ORDER BY issue_id`)
		return err
	}); err != nil {
		return err
	}
	for _, id := range ids {
		env := &protocol.Envelope{RequestID: a.ids.New(protocol.PrefixRequest), Operation: "system.initialize_issue_admission", Target: protocol.Target{Type: "issue", ID: id}, Payload: json.RawMessage(`{}`)}
		if _, err := a.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalSystem, Subject: "issue-admission-initializer"}, env, initializeIssueAdmission{}); err != nil {
			return err
		}
	}
	return nil
}

type initializeIssueAdmission struct{}

func (initializeIssueAdmission) Operation() string { return "system.initialize_issue_admission" }
func (initializeIssueAdmission) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalSystem)
}
func (initializeIssueAdmission) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	i, err := store.GetCanonicalIssue(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	previous := i.Revision
	if i.WorkStatus == "" {
		var work bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_work_item_owners WHERE work_item_id=?) OR EXISTS(SELECT 1 FROM hosted_executions WHERE issue_id=?)`, i.IssueID, i.IssueID).Scan(&work); err != nil {
			return Outcome{}, err
		}
		i.WorkStatus = protocol.IssueBacklog
		if work || i.Assignee.Kind == protocol.IssueAssigneeAgent {
			i.WorkStatus = protocol.IssueTodo
			i.TodoAt = i.CreatedAt
			i.TodoRevision = previous + 1
		}
	}
	if i.OwnerSelection == nil {
		m, e := store.LatestIssueManagerTask(ctx, tx.Q(), i.IssueID)
		if e != nil && !errors.Is(e, store.ErrNotFound) {
			return Outcome{}, e
		}
		if e == nil {
			raw, e := store.GetSpec(ctx, tx.Q(), m.TaskID, 1)
			if e != nil {
				return Outcome{}, e
			}
			var spec protocol.IssueManagerSpec
			if e := json.Unmarshal([]byte(raw), &spec); e != nil {
				return Outcome{}, e
			}
			i.OwnerSelection = &protocol.IssueOwnerSelection{CategoryID: spec.CategoryID, AgentID: spec.AgentID, OwnershipPolicy: spec.OwnershipPolicy}
		}
	}
	// Convert the explicitly marked old Issue-induced Task pause into the
	// Issue overlay. Independent paused/human controls have no such marker.
	if _, err := tx.Exec(ctx, `UPDATE tasks SET control_mode='managed',control_revision=control_revision+1 WHERE control_mode='paused' AND EXISTS(SELECT 1 FROM issue_manager_tasks m WHERE m.task_id=tasks.task_id AND m.issue_id=? AND m.issue_paused=1)`, i.IssueID); err != nil {
		return Outcome{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE issue_manager_tasks SET issue_paused=0 WHERE issue_id=? AND issue_paused=1`, i.IssueID); err != nil {
		return Outcome{}, err
	}
	i.Revision++
	i.UpdatedAt = x.Now.UnixMilli()
	if err := tx.SaveCanonicalIssue(ctx, i, previous, store.IssueRevisionProvenance{AdmissionDefinition: true, Actor: x.Principal, RequestDomain: x.RequestDomain(), RequestID: x.Env.RequestID, Reason: map[string]string{"reason": "Normalize current admission while preserving original execution facts"}}); err != nil {
		return Outcome{}, err
	}
	if i.BoardStatus() == protocol.IssueBacklog || i.State == protocol.IssuePaused {
		if err := recordIssuePause(ctx, x, tx, i); err != nil {
			return Outcome{}, err
		}
	}
	return Outcome{Result: i, Events: []Event{{ObjectType: "issue", ObjectID: i.IssueID, ObjectVersion: i.Revision, Kind: "issue.admission_initialized", Body: map[string]any{"work_status": i.WorkStatus}}}}, nil
}
