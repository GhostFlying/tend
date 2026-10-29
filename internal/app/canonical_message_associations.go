package app

import (
	"context"
	"errors"
	"slices"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// SetIssueMessageAssociations records classification, never technical context
// review or execution authority. Owner corrections and CAS share the old path.
type SetIssueMessageAssociations struct{}

func (SetIssueMessageAssociations) Operation() string { return "conversation.set_issue_associations" }
func (SetIssueMessageAssociations) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (SetIssueMessageAssociations) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if x.Principal.Kind != protocol.PrincipalOwner && (x.coordinationExecution == nil || x.coordinationExecution.Selection.Purpose != agentconfig.Main) {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "classification requires the owner or admitted named planner")
	}
	var p protocol.SetIssueMessageAssociationsPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if x.coordinationExecution != nil {
		if err := requireHostedSourceAdmitted(ctx, tx.Q(), x.Env.Target.ID); err != nil {
			return Outcome{}, err
		}
	}
	ownReply, err := ownCoordinationNarration(ctx, x, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	if !ownReply {
		return applyMessageAssociations(ctx, x, tx, protocol.SetMessageAssociationsPayload{WorkItemIDs: p.IssueIDs, Disposition: p.Disposition, ContentRevision: p.ContentRevision}, true)
	}
	// Association triggers predate the planner/manager split. Retain this
	// exact narration's preexisting duties, but do not create a technical
	// source-review obligation from display-only bookkeeping.
	previous := map[string]bool{}
	rows, err := tx.Q().QueryContext(ctx, `SELECT work_item_id,needs_review FROM message_context_obligations WHERE message_id=?`, x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	for rows.Next() {
		var id string
		var pending bool
		if err := rows.Scan(&id, &pending); err != nil {
			rows.Close()
			return Outcome{}, err
		}
		previous[id] = pending
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Outcome{}, err
	}
	out, err := applyMessageAssociations(ctx, x, tx, protocol.SetMessageAssociationsPayload{WorkItemIDs: p.IssueIDs, Disposition: p.Disposition, ContentRevision: p.ContentRevision}, true)
	if err != nil {
		return Outcome{}, err
	}
	created, err := store.FollowupIDs(ctx, tx.Q(), `SELECT o.work_item_id FROM message_context_obligations o JOIN issue_controls c ON c.issue_id=o.work_item_id WHERE o.message_id=? AND o.needs_review=1`, x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	for _, id := range created {
		pending, existed := previous[id]
		if pending {
			continue
		}
		if existed {
			_, err = tx.Exec(ctx, `UPDATE message_context_obligations SET needs_review=0 WHERE message_id=? AND work_item_id=?`, x.Env.Target.ID, id)
		} else {
			_, err = tx.Exec(ctx, `DELETE FROM message_context_obligations WHERE message_id=? AND work_item_id=?`, x.Env.Target.ID, id)
		}
		if err != nil {
			return Outcome{}, err
		}
	}
	return out, nil
}

// Both owner association APIs publish the same canonical correction fact.
// This is a notification, not admission of queued material as an instruction.
func notifyCanonicalAssociationChange(ctx context.Context, x *Exec, tx *store.Tx, message string, affected []string) (bool, error) {
	ownReply, err := ownCoordinationNarration(ctx, x, tx.Q(), message)
	if err != nil || ownReply {
		return false, err
	}
	notified := false
	for _, id := range slices.Sorted(slices.Values(affected)) {
		if _, err := store.GetCanonicalIssue(ctx, tx.Q(), id); errors.Is(err, store.ErrNotFound) {
			continue
		} else if err != nil {
			return false, err
		}
		if err := appendBackgroundSource(ctx, x, tx, "work_item:"+id, id, message, "", "conversation.associations_changed", nil); err != nil {
			return false, err
		}
		notified = true
	}
	return notified, nil
}
func attachAssociationReviewSource(ctx context.Context, x *Exec, tx *store.Tx, message string, canonical bool) error {
	if canonical {
		return attachReviewSource(ctx, x, tx, message)
	}
	return attachLegacyReviewSource(ctx, x, tx, message)
}

// Repairs retain the original logical identity. A later owner selection does
// not enter this exception and still creates its original review duty/wake.
func ownCoordinationNarration(ctx context.Context, x *Exec, q store.Querier, message string) (bool, error) {
	if x.coordinationExecution == nil || x.coordinationExecution.Selection.Purpose != agentconfig.Main {
		return false, nil
	}
	var own bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(
          SELECT 1 FROM conversation_agent_messages a
          JOIN main_turns t ON t.thread_id=a.thread_id AND t.turn_id=a.turn_id
          JOIN logical_turn_executions e ON e.request_id=t.request_id
          WHERE a.message_id=? AND e.logical_turn_id=?)`, message, x.coordinationLogicalID).Scan(&own)
	return own, err
}
