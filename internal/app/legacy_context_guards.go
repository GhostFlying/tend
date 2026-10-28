package app

import (
	"context"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Reject the actual canonical impact of a legacy fact mutation;
// a legacy target does not make a global/project fact local to that target.
func rejectCanonicalLegacyFactScope(ctx context.Context, q store.Querier, scope, item, project string) error {
	var canonical bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_controls c JOIN work_items w ON w.work_item_id=c.issue_id WHERE ?='global' OR c.issue_id=? OR (?='project' AND w.project=?))`, scope, item, scope, project).Scan(&canonical)
	if err != nil {
		return fmt.Errorf("app: inspect shared fact scope: %w", err)
	}
	if canonical {
		return protocol.Errorf(protocol.CodeInvalidTransition, "legacy context mutation affects canonical issue responsibility")
	}
	return nil
}

// attachLegacyReviewSource fences the actual context enrichment shared by legacy
// planning commits, classification and sourced context writes. Formal decision
// and choice answers keep their existing answer-specific attachment path.
func attachLegacyReviewSource(ctx context.Context, x *Exec, tx *store.Tx, message string) error {
	if x.Principal.Kind != protocol.PrincipalOwner {
		if err := rejectCanonicalLegacySourceAttachment(ctx, x, tx.Q(), message); err != nil {
			return err
		}
	}
	return attachReviewSource(ctx, x, tx, message)
}

// Even an existing-fact no-op attaches a source to the current logical turn.
// 0046 may then add canonical contexts via active associations or pending source
// obligations. Match attachReviewSource exactly: a completed or missing logical
// turn, or an already attached message, inserts nothing and fires no 0046 trigger.
// Check this real effect rather than rejecting every global read.
func rejectCanonicalLegacySourceAttachment(ctx context.Context, x *Exec, q store.Querier, message string) error {
	request, err := x.reviewExecution(ctx, q)
	if err != nil {
		return err
	}
	var canonical bool
	err = q.QueryRowContext(ctx, `SELECT EXISTS(
      SELECT 1 FROM logical_turn_executions e JOIN logical_turns l ON l.logical_turn_id=e.logical_turn_id
      WHERE e.request_id=? AND l.status<>'completed'
      AND NOT EXISTS(SELECT 1 FROM logical_turn_messages lm WHERE lm.logical_turn_id=e.logical_turn_id AND lm.message_id=?)
      AND EXISTS(SELECT 1 FROM issue_controls c
        WHERE EXISTS(SELECT 1 FROM message_work_items a WHERE a.work_item_id=c.issue_id AND a.message_id=? AND a.active=1)
        OR EXISTS(SELECT 1 FROM message_context_obligations o WHERE o.work_item_id=c.issue_id AND o.message_id=? AND o.needs_review=1)))`, request, message, message, message).Scan(&canonical)
	if err != nil {
		return fmt.Errorf("app: inspect review source scope: %w", err)
	}
	if canonical {
		return protocol.Errorf(protocol.CodeInvalidTransition, "legacy source review attaches canonical responsibility")
	}
	return nil
}
