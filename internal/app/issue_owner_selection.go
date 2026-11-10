package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func validateIssueOwnerSelection(ctx context.Context, q store.Querier, selection *protocol.IssueOwnerSelection) error {
	if selection == nil || !protocol.AgentInventoryID(selection.CategoryID) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "select an owner capability category before moving the Issue to todo")
	}
	category, err := store.GetAgentCategory(ctx, q, selection.CategoryID)
	if err != nil {
		return errNotFound(err, "owner capability category does not exist")
	}
	if category.State != "active" {
		return protocol.Errorf(protocol.CodeInvalidTransition, "owner capability category is inactive")
	}
	if selection.AgentID != "" {
		parts := strings.Split(selection.AgentID, "/")
		if len(parts) != 2 || !protocol.AgentInventoryID(parts[0]) || !protocol.AgentInventoryID(parts[1]) {
			return protocol.Errorf(protocol.CodeInvalidRequest, "agent_id requires an environment/local identity")
		}
	}
	return nil
}

func selectIssueOwner(ctx context.Context, x *Exec, tx *store.Tx, p protocol.RequestIssueManagerPayload, policy string) (Outcome, error) {
	return selectIssueOwnerDefinition(ctx, x, tx, p, policy, false)
}
func selectIssueOwnerDefinition(ctx context.Context, x *Exec, tx *store.Tx, p protocol.RequestIssueManagerPayload, policy string, migration bool) (Outcome, error) {
	if x.Env.Target.Type != "issue" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "owner selection requires an Issue target")
	}
	issue, err := store.GetCanonicalIssue(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	expected, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	if issue.Revision != expected || issue.AssignmentEpoch != p.ExpectedAssignmentEpoch {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "Issue or assignment changed")
	}
	if !migration {
		needed, err := store.IssueNeedsHostedMigration(ctx, tx.Q(), issue)
		if err != nil {
			return Outcome{}, err
		}
		if needed {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "existing hosted manager requires the backed-up manager migration operation")
		}
	}
	if issue.State != protocol.IssueActive && issue.State != protocol.IssuePaused {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "select ownership only for an active or paused Issue")
	}
	selection := &protocol.IssueOwnerSelection{CategoryID: p.CategoryID, AgentID: p.AgentID, OwnershipPolicy: policy}
	if err := validateIssueOwnerSelection(ctx, tx.Q(), selection); err != nil {
		return Outcome{}, err
	}
	if strings.TrimSpace(p.Reason) == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "owner selection reason is required")
	}
	manager, managerErr := store.CurrentIssueManagerTask(ctx, tx.Q(), issue.IssueID)
	if managerErr != nil && !errors.Is(managerErr, store.ErrNotFound) {
		return Outcome{}, managerErr
	}
	if managerErr == nil && !p.Replace {
		raw, err := store.GetSpec(ctx, tx.Q(), manager.TaskID, 1)
		if err != nil {
			return Outcome{}, err
		}
		var spec protocol.IssueManagerSpec
		if err := json.Unmarshal([]byte(raw), &spec); err != nil {
			return Outcome{}, err
		}
		if spec.CategoryID != selection.CategoryID || spec.AgentID != selection.AgentID || spec.OwnershipPolicy != selection.OwnershipPolicy {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "replace=true is required to change an existing responsibility")
		}
	}
	if reflect.DeepEqual(issue.OwnerSelection, selection) && !p.Replace {
		return Outcome{Result: issue}, nil
	}
	if managerErr == nil && p.Replace {
		if _, err := tx.Exec(ctx, `UPDATE issue_manager_tasks SET state='retiring',updated_at=? WHERE task_id=?`, x.Now.UnixMilli(), manager.TaskID); err != nil {
			return Outcome{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE plan_actions SET status='superseded' WHERE task_id=? AND status IN ('pending','dispatched') AND COALESCE(json_extract(trigger_json,'$.receipt_only'),0)=0`, manager.TaskID); err != nil {
			return Outcome{}, err
		}
	}
	if p.Replace {
		issue.AssignmentEpoch++
	}
	issue.OwnerSelection = selection
	issue.Revision++
	issue.UpdatedAt = x.Now.UnixMilli()
	source := canonicalOwnerIssueSource{SourceMessageID: p.SourceMessageID}
	if p.SourceMessageID != "" {
		var revision int64
		if err := tx.Q().QueryRowContext(ctx, `SELECT COALESCE(d.content_revision,1) FROM conversation_messages m LEFT JOIN message_dispositions d USING(message_id) WHERE m.message_id=?`, p.SourceMessageID).Scan(&revision); err != nil {
			return Outcome{}, errNotFound(err, "owner selection source does not exist")
		}
		source.ExpectedSourceRevision = &revision
	}
	if err := validateCanonicalSourceReference(ctx, tx.Q(), source); err != nil {
		return Outcome{}, err
	}
	if p.SourceMessageID != "" {
		if err := tx.BindMessage(ctx, p.SourceMessageID, "work_item", issue.IssueID, "owner_selection"); err != nil {
			return Outcome{}, err
		}
	}
	out, err := saveCanonicalOwnerDefinition(ctx, x, tx, issue, expected, source, p, "issue.owner_selected")
	if err != nil {
		return Outcome{}, err
	}
	if _, err := ensureCanonicalManagerConversation(ctx, tx, issue, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	return out, nil
}
