package app

import (
	"context"
	"errors"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

// prepareIssueReopen settles only retired coordination projections. It never
// changes an implementation task or assumes an uncertain native turn stopped.
func prepareIssueReopen(ctx context.Context, x *Exec, tx *store.Tx, issue protocol.Issue, source canonicalOwnerIssueSource) (*protocol.IssueOwnerSelection, error) {
	if x.coordinationExecution != nil {
		if x.coordinationExecution.Selection.Purpose != agentconfig.Main || source.SourceMessageID == "" {
			return nil, protocol.Errorf(protocol.CodeForbidden, "reopen requires an admitted owner source through main")
		}
		if err := validateAttentionOwnerRelay(ctx, tx.Q(), x, issue.IssueID, source.SourceMessageID); err != nil {
			return nil, err
		}
	}
	if source.SourceMessageID != "" && issue.Assignee.Kind == protocol.IssueAssigneeAgent {
		if err := tx.BindMessage(ctx, source.SourceMessageID, "work_item", issue.IssueID, "owner_selection"); err != nil {
			return nil, err
		}
	}
	if err := refreshIssueManagerTasks(ctx, x, tx, issue.IssueID, nil); err != nil {
		return nil, err
	}
	var unsettled bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_manager_tasks WHERE issue_id=? AND state IN ('pending','assigned','retiring'))`, issue.IssueID).Scan(&unsettled); err != nil {
		return nil, err
	}
	if unsettled {
		return nil, protocol.Errorf(protocol.CodeUnknownOutcome, "previous Issue coordination requires reconciliation before reopen")
	}
	latest, err := store.LatestIssueManagerTask(ctx, tx.Q(), issue.IssueID)
	if errors.Is(err, store.ErrNotFound) {
		return issue.OwnerSelection, nil
	}
	if err != nil {
		return nil, err
	}
	if latest.State != "completed" && latest.State != "cancelled" {
		return nil, protocol.Errorf(protocol.CodeUnknownOutcome, "previous Issue coordinator must settle before reopening; reconcile its original run and receipts")
	}
	pending, err := views.ManagerReceiptPending(ctx, tx.Q(), latest)
	if err != nil {
		return nil, err
	}
	if pending {
		return nil, protocol.Errorf(protocol.CodeUnknownOutcome, "previous Issue coordinator receipts require reconciliation")
	}
	if issue.OwnerSelection != nil || issue.Assignee.Kind != protocol.IssueAssigneeAgent {
		return issue.OwnerSelection, nil
	}
	if latest.Pin == nil || latest.Pin.Identity.ExecutionEnvironmentID+"/"+latest.Pin.Identity.LocalID != issue.Assignee.Ref {
		return nil, nil
	}
	ownerExecution, err := store.IssueUsesOwnerExecution(ctx, tx.Q(), issue.IssueID)
	if err != nil {
		return nil, err
	}
	policy := ""
	if ownerExecution {
		policy = protocol.IssueOwnerPolicy
	}
	return &protocol.IssueOwnerSelection{CategoryID: latest.CategoryID, AgentID: issue.Assignee.Ref, OwnershipPolicy: policy}, nil
}
