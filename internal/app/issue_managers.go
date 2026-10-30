package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// RequestIssueManager publishes category-selected work without choosing a runner.
type RequestIssueManager struct{ migration bool }

func (RequestIssueManager) Operation() string { return protocol.OpIssueRequestManager }
func (RequestIssueManager) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner, protocol.PrincipalMainAgent)
}
func (c RequestIssueManager) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.RequestIssueManagerPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if x.Env.Target.Type != "issue" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "manager publication requires an issue")
	}
	expected, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	issue, err := store.GetCanonicalIssue(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	if issue.Revision != expected || issue.AssignmentEpoch != p.ExpectedAssignmentEpoch {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "issue or assignment changed")
	}
	if issue.State != protocol.IssueActive || issue.AssignmentState != "active" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "issue is not available for manager publication")
	}
	if p.CategoryID == "" {
		p.CategoryID = protocol.DefaultIssueManagerCategory
	}
	if !protocol.AgentInventoryID(p.CategoryID) || strings.TrimSpace(p.Reason) == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "category and reason are required")
	}
	category, err := store.GetAgentCategory(ctx, tx.Q(), p.CategoryID)
	if err != nil {
		return Outcome{}, err
	}
	if category.State != "active" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "manager category is inactive")
	}
	if x.Principal.Kind == protocol.PrincipalMainAgent {
		if x.coordinationExecution == nil || p.SourceMessageID == "" {
			return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "manager publication needs an admitted owner source")
		}
	}
	var sourceContent, sourceAssociation int64
	if p.SourceMessageID != "" {
		var author string
		var withdrawn bool
		if err = tx.Q().QueryRowContext(ctx, `SELECT m.author,COALESCE(d.content_revision,1),COALESCE(a.revision,0),COALESCE(c.withdrawn_at,0)<>0 FROM conversation_messages m LEFT JOIN message_dispositions d USING(message_id) LEFT JOIN message_association_revisions a USING(message_id) LEFT JOIN conversation_message_controls c USING(message_id) WHERE m.message_id=? AND m.kind='message'`, p.SourceMessageID).Scan(&author, &sourceContent, &sourceAssociation, &withdrawn); err != nil {
			return Outcome{}, errNotFound(err, "manager publication source does not exist")
		}
		if withdrawn || (x.Principal.Kind == protocol.PrincipalMainAgent && author != "owner") {
			return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "manager publication requires a current owner authorization")
		}
		if x.Principal.Kind == protocol.PrincipalMainAgent {
			if err = requireHostedSourceAdmitted(ctx, tx.Q(), p.SourceMessageID); err != nil {
				return Outcome{}, err
			}
		}
	}
	var retiring *store.IssueManagerTask
	if existing, err := store.CurrentIssueManagerTask(ctx, tx.Q(), issue.IssueID); err == nil {
		if !p.Replace {
			if existing.CategoryID != p.CategoryID {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "change manager category through an explicit replacement")
			}
			return Outcome{Result: existing}, nil
		}
		retiring = &existing
	} else if !errors.Is(err, store.ErrNotFound) {
		return Outcome{}, err
	}
	if issue.Assignee.Kind == protocol.IssueAssigneeAgent && !p.Replace {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "replace=true is required to fence the current manager")
	}
	if issue.Assignee.Kind == protocol.IssueAssigneeAgent && retiring == nil && !c.migration {
		var runnerOwned bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_manager_tasks WHERE issue_id=? AND assignment_epoch=? AND host_id||'/'||json_extract(pin_json,'$.identity.local_id')=?)`, issue.IssueID, issue.AssignmentEpoch, issue.Assignee.Ref).Scan(&runnerOwned); err != nil {
			return Outcome{}, err
		}
		if !runnerOwned {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "existing hosted manager must use the backed-up manager migration operation")
		}
	}
	if p.SourceMessageID != "" {
		// Admit the original delegated material into the Issue before freezing
		// its association revision. Adding another explicit Issue share later
		// does not withdraw this share's authorization.
		if err := tx.BindMessage(ctx, p.SourceMessageID, "work_item", issue.IssueID, "manager_authorization"); err != nil {
			return Outcome{}, err
		}
		if err := tx.Q().QueryRowContext(ctx, `SELECT COALESCE((SELECT revision FROM message_association_revisions WHERE message_id=?),0)`, p.SourceMessageID).Scan(&sourceAssociation); err != nil {
			return Outcome{}, err
		}
	}
	handoff := ""
	var events []Event
	oldEpoch := issue.AssignmentEpoch
	if p.Replace {
		handoff = x.IDs.New(protocol.Prefix("handoff"))
		if retiring != nil {
			if _, err = tx.Exec(ctx, `UPDATE issue_manager_tasks SET state='retiring',updated_at=? WHERE task_id=?`, x.Now.UnixMilli(), retiring.TaskID); err != nil {
				return Outcome{}, err
			}
			if _, err = tx.Exec(ctx, `UPDATE plan_actions SET status='superseded' WHERE task_id=? AND status IN ('pending','dispatched')`, retiring.TaskID); err != nil {
				return Outcome{}, err
			}
		}
		next := issue
		next.AssignmentEpoch++
		next.Revision++
		next.UpdatedAt = x.Now.UnixMilli()
		fenced, err := saveCanonicalOwnerDefinition(ctx, x, tx, next, issue.Revision, canonicalOwnerIssueSource{}, p, "issue.manager_fenced")
		if err != nil {
			return Outcome{}, err
		}
		events = fenced.Events
		issue = next
	}
	now := x.Now.UnixMilli()
	id := x.IDs.New(protocol.PrefixTask)
	spec := protocol.IssueManagerSpec{SourceContentRevision: sourceContent, SourceAssociationRevision: sourceAssociation, IssueID: issue.IssueID, CategoryID: p.CategoryID, ExpectedAssignmentEpoch: issue.AssignmentEpoch, SourceMessageID: p.SourceMessageID, Reason: p.Reason}
	raw, err := json.Marshal(spec)
	if err != nil {
		return Outcome{}, err
	}
	if err = tx.InsertTask(ctx, store.TaskRow{Kind: protocol.TaskKindIssueManager, TaskID: id, Title: "Manage: " + issue.Plan.Title, Project: issue.Plan.Project, Lifecycle: protocol.TaskQueued, BlockReason: protocol.BlockNone, ControlMode: protocol.ControlManaged, ControlRevision: 1, SpecVersion: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		return Outcome{}, err
	}
	if err = tx.InsertSpec(ctx, id, 1, string(raw), p.SourceMessageID, now); err != nil {
		return Outcome{}, err
	}
	if err = tx.InsertTaskIssueOwner(ctx, id, issue.IssueID); err != nil {
		return Outcome{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO issue_manager_tasks(task_id,issue_id,category_id,expected_epoch,source_message_id,request_id,created_at,updated_at,handoff_id) VALUES(?,?,?,?,?,?,?,?,?)`, id, issue.IssueID, p.CategoryID, issue.AssignmentEpoch, p.SourceMessageID, x.Env.RequestID, now, now, handoff)
	if err != nil {
		return Outcome{}, err
	}
	if handoff != "" {
		if _, err = tx.Exec(ctx, `INSERT INTO manager_handoffs(handoff_id,issue_id,task_id,from_epoch,to_epoch,stage,reason,request_id,created_at,updated_at) VALUES(?,?,?,?,?,'fencing',?,?,?,?)`, handoff, issue.IssueID, id, oldEpoch, issue.AssignmentEpoch, p.Reason, x.Env.RequestID, now, now); err != nil {
			return Outcome{}, err
		}
	}
	if err = insertManagerAction(ctx, x, tx, id, 1); err != nil {
		return Outcome{}, err
	}
	manager, err := store.GetIssueManagerTask(ctx, tx.Q(), id)
	return Outcome{Result: manager, Events: append(events, Event{ObjectType: "task", ObjectID: id, Kind: "task.manager_requested", Body: map[string]any{"issue_id": issue.IssueID, "category_id": p.CategoryID}})}, err
}

func insertManagerAction(ctx context.Context, x *Exec, tx *store.Tx, task string, generation int64) error {
	return tx.InsertAction(ctx, store.ActionRow{ActionID: x.IDs.New(protocol.PrefixAction), TaskID: task, Kind: protocol.ActionCoordinate, EffectKey: fmt.Sprintf("coordinate|%s|%d", task, generation), TriggerJSON: `{}`, TargetSpecVersion: 1, AuthorizationJSON: `{}`, Generation: generation, Status: protocol.ActionPending, CreatedRequestID: x.Env.RequestID, CreatedAt: x.Now.UnixMilli()})
}
