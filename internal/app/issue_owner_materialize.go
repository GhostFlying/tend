package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
)

// materializeIssueOwner is called only for the candidate selected by runner.claim.
// Selection, carrier creation and lease allocation share the claim transaction.
func materializeIssueOwner(ctx context.Context, x *Exec, tx *store.Tx, c *scheduler.Candidate) ([]Event, error) {
	issue, err := store.GetCanonicalIssue(ctx, tx.Q(), c.Manager.IssueID)
	if err != nil {
		return nil, err
	}
	if issue.State != protocol.IssueActive || issue.AssignmentState != "active" || issue.BoardStatus() == protocol.IssueBacklog {
		return nil, protocol.Errorf(protocol.CodeInvalidTransition, "Issue is not admitted for execution")
	}
	if err := validateIssueOwnerSelection(ctx, tx.Q(), issue.OwnerSelection); err != nil {
		return nil, err
	}
	selection := issue.OwnerSelection
	if selection.CategoryID != c.Manager.CategoryID || selection.AgentID != c.Manager.AgentID || selection.OwnershipPolicy != c.Manager.OwnershipPolicy || issue.AssignmentEpoch != c.Manager.ExpectedAssignmentEpoch {
		return nil, protocol.Errorf(protocol.CodeStaleRevision, "Issue execution selection changed")
	}
	if _, err := store.CurrentIssueManagerTask(ctx, tx.Q(), issue.IssueID); !errors.Is(err, store.ErrNotFound) {
		if err != nil {
			return nil, err
		}
		return nil, protocol.Errorf(protocol.CodeInvalidTransition, "Issue already has an execution responsibility")
	}
	conversation, err := ensureCanonicalManagerConversation(ctx, tx, issue, x.Now.UnixMilli())
	if err != nil {
		return nil, err
	}
	id := x.IDs.New(protocol.PrefixTask)
	spec := protocol.IssueManagerSpec{AdmissionRevision: issue.Revision, IssueID: issue.IssueID, CategoryID: selection.CategoryID, AgentID: selection.AgentID, OwnershipPolicy: selection.OwnershipPolicy, ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Execute the explicitly admitted Issue under its current definition"}
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	now := x.Now.UnixMilli()
	if err := tx.InsertTask(ctx, store.TaskRow{Kind: protocol.TaskKindIssueManager, TaskID: id, Title: "Manage: " + issue.Plan.Title, Project: issue.Plan.Project, Lifecycle: protocol.TaskQueued, BlockReason: protocol.BlockNone, ControlMode: protocol.ControlManaged, ControlRevision: 1, SpecVersion: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		return nil, err
	}
	if err := tx.InsertSpec(ctx, id, 1, string(raw), "", now); err != nil {
		return nil, err
	}
	if err := tx.InsertTaskIssueOwner(ctx, id, issue.IssueID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO issue_manager_tasks(task_id,issue_id,category_id,expected_epoch,source_message_id,request_id,created_at,updated_at,handoff_id,conversation_id) VALUES(?,?,?,?,'',?,?,?,'',?)`, id, issue.IssueID, selection.CategoryID, issue.AssignmentEpoch, x.Env.RequestID, now, now, conversation); err != nil {
		return nil, err
	}
	var migrationRequest, backup string
	err = tx.Q().QueryRowContext(ctx, `SELECT request_id,backup_json FROM issue_owner_migrations WHERE issue_id=? AND assignment_epoch=?`, issue.IssueID, issue.AssignmentEpoch).Scan(&migrationRequest, &backup)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		handoff := x.IDs.New(protocol.Prefix("handoff"))
		if _, err := tx.Exec(ctx, `INSERT INTO manager_handoffs(handoff_id,issue_id,task_id,from_epoch,to_epoch,stage,reason,request_id,created_at,updated_at) VALUES(?,?,?,?,?,'fencing',?,?,?,?)`, handoff, issue.IssueID, id, issue.AssignmentEpoch-1, issue.AssignmentEpoch, "Materialize backed-up ownership migration", migrationRequest, now, now); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE issue_manager_tasks SET handoff_id=? WHERE task_id=?`, handoff, id); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO manager_migrations(request_id,issue_id,task_id,backup_json,created_at) VALUES(?,?,?,?,?)`, migrationRequest, issue.IssueID, id, backup, now); err != nil {
			return nil, err
		}
	}
	if err := insertManagerAction(ctx, x, tx, id, 1); err != nil {
		return nil, err
	}
	actions, err := store.FollowupIDs(ctx, tx.Q(), `SELECT action_id FROM plan_actions WHERE task_id=? AND kind='coordinate' AND generation=1`, id)
	if err != nil {
		return nil, err
	}
	if len(actions) != 1 {
		return nil, protocol.Errorf(protocol.CodeInternal, "new Issue owner has no unique initial action")
	}
	c.TaskID, c.ActionID = id, actions[0]
	c.Manager = &spec
	return []Event{{ObjectType: "task", ObjectID: id, Kind: "task.owner_materialized", Body: map[string]any{"issue_id": issue.IssueID, "issue_revision": issue.Revision}}}, nil
}
