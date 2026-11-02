package app

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

// prepareIssueReopen settles only retired coordination projections. It never
// changes an implementation task or assumes an uncertain native turn stopped.
func prepareIssueReopen(ctx context.Context, x *Exec, tx *store.Tx, issue protocol.Issue, source canonicalOwnerIssueSource) (*protocol.RequestIssueManagerPayload, error) {
	if x.coordinationExecution != nil {
		if x.coordinationExecution.Selection.Purpose != agentconfig.Main || source.SourceMessageID == "" {
			return nil, protocol.Errorf(protocol.CodeForbidden, "reopen requires an admitted owner source through main")
		}
		if err := validateAttentionOwnerRelay(ctx, tx.Q(), x, issue.IssueID, source.SourceMessageID); err != nil {
			return nil, err
		}
	}
	if err := refreshIssueManagerTasks(ctx, x, tx, issue.IssueID); err != nil {
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
		return nil, nil
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
	raw, err := store.GetSpec(ctx, tx.Q(), latest.TaskID, 1)
	if err != nil {
		return nil, err
	}
	var spec protocol.IssueManagerSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		return nil, err
	}
	return &protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, CategoryID: latest.CategoryID, AgentID: spec.AgentID, Replace: true, SourceMessageID: source.SourceMessageID, Reason: "Restore responsibility after explicit owner reopen"}, nil
}

func restoreReopenedIssueManager(ctx context.Context, x *Exec, tx *store.Tx, issue protocol.Issue, p *protocol.RequestIssueManagerPayload) (Outcome, error) {
	if p == nil {
		return Outcome{Result: issue}, nil
	}
	inner := *x
	env := *x.Env
	env.Operation = protocol.OpIssueRequestManager
	env.ExpectedControlRevision = &issue.Revision
	env.Payload, _ = json.Marshal(p)
	inner.Env = &env
	out, err := (RequestIssueManager{}).Apply(ctx, &inner, tx)
	if err != nil {
		return Outcome{}, err
	}
	restored, err := store.GetCanonicalIssue(ctx, tx.Q(), issue.IssueID)
	if err != nil {
		return Outcome{}, err
	}
	// The stable conversation retains queued messages; a new coordinate action
	// claims the successor before admission can route them to the new epoch.
	out.Result = restored
	return out, nil
}
