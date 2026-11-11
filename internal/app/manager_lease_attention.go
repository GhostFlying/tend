package app

import (
	"context"
	"fmt"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// recordManagerLeaseProblem reports a proved transport failure to the manager's
// supervisor without settling or replaying any original business operation.
func (a *App) recordManagerLeaseProblem(ctx context.Context, tx *store.Tx, run store.RunRow, leaseID string, now time.Time) (bool, error) {
	if run.Role != protocol.RoleIssueManager {
		return false, nil
	}
	m, err := store.GetIssueManagerTask(ctx, tx.Q(), run.TaskID)
	if err != nil {
		return false, err
	}
	i, err := store.GetCanonicalIssue(ctx, tx.Q(), m.IssueID)
	if err != nil {
		return false, err
	}
	var exists bool
	err = tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_bindings WHERE object_type='lease' AND object_id=? AND kind='manager_lease_expired')`, leaseID).Scan(&exists)
	if err != nil || exists {
		return false, err
	}
	x := &Exec{Env: &protocol.Envelope{RequestID: a.ids.New(protocol.PrefixRequest)}, Principal: protocol.Principal{Kind: protocol.PrincipalSystem}, Now: now, IDs: a.ids}
	title := fmt.Sprintf("Manager lease expired; reconcile the original execution: run %s, lease %s. The run remains unknown; preserve pending requests and frozen execution, and do not restart or replay business operations.", run.RunID, leaseID)
	source := a.ids.New(protocol.PrefixMessage)
	if _, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: source, ConversationID: m.ConversationID, Author: "system", Body: title, CreatedAt: now.UnixMilli()}); err != nil {
		return false, err
	}
	for _, binding := range []struct{ kind, id string }{{"run", run.RunID}, {"lease", leaseID}, {"task", run.TaskID}} {
		if err := tx.BindMessage(ctx, source, binding.kind, binding.id, "manager_lease_expired"); err != nil {
			return false, err
		}
	}
	r := &protocol.AttentionResponsibility{OriginIssueID: m.IssueID, History: []protocol.AttentionResponsibilityHop{}}
	if err := responsibilityRecipient(ctx, tx.Q(), r, m.IssueID); err != nil {
		return false, err
	}
	r.OriginAssignmentEpoch = r.AssignmentEpoch
	// A replacement owner first receives the retained transport problem. Only
	// the still-current manager's own failure skips to its supervisor.
	if i.Assignee.Kind == protocol.IssueAssigneeAgent && i.AssignmentEpoch == m.AssignmentEpoch {
		r.History = append(r.History, responsibilityHop(r, "Manager lease expired; supervisor reconciliation is required", x.Env.RequestID))
		if err := responsibilityRecipient(ctx, tx.Q(), r, i.ParentIssueID); err != nil {
			return false, err
		}
	}
	attention := protocol.Attention{AttentionID: a.ids.New(protocol.Prefix("attn")), IssueID: m.IssueID, TaskID: run.TaskID, Kind: "action", Title: title, Status: "open", SourceMessageID: source, RaisedBy: x.Principal, Responsibility: r, CreatedAt: now.UnixMilli()}
	if err := saveAttention(ctx, x, tx, &attention, 0); err != nil {
		return false, err
	}
	if err := tx.SaveAttentionResponsibility(ctx, attention.AttentionID, r, 0, x.Env.RequestID); err != nil {
		return false, err
	}
	if err := recordResponsibilityWake(ctx, x, tx, attention, ""); err != nil {
		return false, err
	}
	return true, nil
}
