package app

import (
	"context"
	"strings"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// ReviewHostedIssueContext reviews only the admitted manager's Issue share.
// Its versions fence concurrent corrections; sources are evidence, not execution identity.
type ReviewHostedIssueContext struct{}

func (ReviewHostedIssueContext) Operation() string { return "issues.review_context" }
func (ReviewHostedIssueContext) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return protocol.Errorf(protocol.CodeForbidden, "context review requires the hosted coordination boundary")
}
func (ReviewHostedIssueContext) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if x.coordinationExecution == nil || x.coordinationExecution.IssueID == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "an admitted issue manager is required")
	}
	issueID := x.coordinationExecution.IssueID
	var p protocol.IssueContextReview
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	current, err := store.GetCanonicalIssue(ctx, tx.Q(), issueID)
	if err != nil {
		return Outcome{}, err
	}
	if err := domain.CheckIssueEpoch(current.IssueControl, p.ExpectedAssignmentEpoch); err != nil {
		return Outcome{}, err
	}
	if strings.TrimSpace(p.Summary) == "" || len(p.Summary) > 8000 || len(p.Sources) > 100 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "bounded explicit review summary and sources are required")
	}
	var revision int64
	if err := tx.Q().QueryRowContext(ctx, `SELECT revision FROM work_item_contexts WHERE work_item_id=?`, issueID).Scan(&revision); err != nil {
		return Outcome{}, err
	}
	if revision != p.ContextRevision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "issue source context changed")
	}
	if len(p.ConfirmedFactIDs)+len(p.RetiredFactIDs) > 100 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "at most 100 own fact reconciliations")
	}
	factsSeen := map[string]bool{}
	for _, group := range []struct {
		ids    []string
		retire bool
	}{{p.ConfirmedFactIDs, false}, {p.RetiredFactIDs, true}} {
		for _, id := range group.ids {
			if factsSeen[id] {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "distinct fact reconciliations are required")
			}
			factsSeen[id] = true
			var source string
			if err := tx.Q().QueryRowContext(ctx, `SELECT source_message_id FROM work_item_context_facts WHERE fact_id=? AND work_item_id=? AND scope='work_item' AND superseded=0`, id, issueID).Scan(&source); err != nil {
				return Outcome{}, errNotFound(err, "fact is outside this Issue")
			}
			if err := requireHostedSourceAdmitted(ctx, tx.Q(), source); err != nil {
				return Outcome{}, err
			}
			if !group.retire {
				var linked bool
				if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM message_work_items WHERE message_id=? AND work_item_id=? AND active=1)`, source, issueID).Scan(&linked); err != nil {
					return Outcome{}, err
				}
				if !linked {
					return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "retire or replace a fact whose source was removed")
				}
			}
			if _, err := tx.Exec(ctx, `UPDATE work_item_context_facts SET superseded=?,needs_review=0 WHERE fact_id=?`, group.retire, id); err != nil {
				return Outcome{}, err
			}
		}
	}
	if len(factsSeen) > 0 {
		if _, err := tx.Exec(ctx, `UPDATE work_item_contexts SET revision=revision+1 WHERE work_item_id=?`, issueID); err != nil {
			return Outcome{}, err
		}
		revision++
	}
	seen := map[string]bool{}
	for _, s := range p.Sources {
		if seen[s.MessageID] {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "duplicate reviewed source")
		}
		seen[s.MessageID] = true
		if err := requireHostedSourceAdmitted(ctx, tx.Q(), s.MessageID); err != nil {
			return Outcome{}, err
		}
		if err := hostedSourceInIssueScope(ctx, tx.Q(), issueID, s.MessageID); err != nil {
			return Outcome{}, err
		}
		source, err := store.MessageAssociations(ctx, tx.Q(), s.MessageID)
		if err != nil {
			return Outcome{}, err
		}
		if source.ContentRevision != s.ContentRevision || source.Revision != s.AssociationRevision {
			return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "reviewed source changed")
		}
		// Reading an inherited source does not clear an ancestor's obligation.
		if _, err := tx.Exec(ctx, `UPDATE message_context_obligations SET needs_review=0 WHERE work_item_id=? AND message_id=?`, issueID, s.MessageID); err != nil {
			return Outcome{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT OR IGNORE INTO logical_turn_messages(logical_turn_id,message_id) VALUES(?,?)`, x.coordinationLogicalID, s.MessageID); err != nil {
			return Outcome{}, err
		}
	}
	pending, err := hostedIssueContextPending(ctx, tx.Q(), issueID)
	if err != nil {
		return Outcome{}, err
	}
	if pending {
		if _, err := tx.Exec(ctx, `DELETE FROM logical_turn_context_reviews WHERE logical_turn_id=? AND work_item_id=?`, x.coordinationLogicalID, issueID); err != nil {
			return Outcome{}, err
		}
		// A bounded batch is durable progress. Remaining shares still prevent
		// turn commitment; do not manufacture a complete context review.
		return Outcome{Result: map[string]any{"work_item_id": issueID, "revision": revision, "complete": false}, Events: []Event{{ObjectType: "issue", ObjectID: issueID, Kind: "issue.context_review_progress", Body: map[string]any{"summary": p.Summary, "context_revision": revision, "sources": p.Sources}}}}, nil
	}
	if _, err := tx.Exec(ctx, `DELETE FROM logical_turn_contexts WHERE logical_turn_id=? AND work_item_id<>?`, x.coordinationLogicalID, issueID); err != nil {
		return Outcome{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO logical_turn_context_reviews(logical_turn_id,work_item_id,revision) VALUES(?,?,?) ON CONFLICT(logical_turn_id,work_item_id) DO UPDATE SET revision=excluded.revision`, x.coordinationLogicalID, issueID, revision); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: map[string]any{"work_item_id": issueID, "revision": revision, "complete": true}, Events: []Event{{ObjectType: "issue", ObjectID: issueID, Kind: "issue.context_reviewed", Body: map[string]any{"summary": p.Summary, "context_revision": revision, "sources": p.Sources}}}}, nil
}

// A planner acknowledges delegated responsibility, not the manager's technical
// context. Local source obligations and all worker contracts remain unchanged.
func reviewCanonicalDelegatedContext(ctx context.Context, tx *store.Tx, logical, issue string, revision int64) (bool, error) {
	result, err := tx.Exec(ctx, `INSERT INTO logical_turn_context_reviews(logical_turn_id,work_item_id,revision)
 SELECT ?,c.issue_id,w.revision FROM issue_controls c JOIN work_item_contexts w ON w.work_item_id=c.issue_id
 WHERE c.issue_id=? AND c.assignment_state='active' AND w.revision=?
 AND EXISTS(SELECT 1 FROM hosted_executions h JOIN logical_turn_executions e ON e.request_id=h.execution_id WHERE e.logical_turn_id=? AND h.role='main')
 ON CONFLICT(logical_turn_id,work_item_id) DO UPDATE SET revision=excluded.revision`, logical, issue, revision, logical)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func commitHostedIssueTurn(ctx context.Context, x *Exec, tx *store.Tx, v protocol.LogicalTurnState, p protocol.LogicalTurnCommit) (Outcome, error) {
	issue := x.coordinationExecution.IssueID
	if len(p.Contexts) != 1 || p.Contexts[0].WorkItemID != issue {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "review exactly this Issue context")
	}
	var reviewed bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM logical_turn_context_reviews r JOIN work_item_contexts c USING(work_item_id) WHERE r.logical_turn_id=? AND r.work_item_id=? AND r.revision=c.revision AND r.revision=?)`, v.ID, issue, p.Contexts[0].Revision).Scan(&reviewed); err != nil {
		return Outcome{}, err
	}
	pending, err := hostedIssueContextPending(ctx, tx.Q(), issue)
	if err != nil {
		return Outcome{}, err
	}
	reviewed = reviewed && !pending
	if !reviewed {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "review the current Issue source context before committing")
	}
	allowed := map[string]bool{}
	for _, m := range v.Messages {
		allowed[m.MessageID] = true
	}
	seen := map[string]bool{}
	for _, m := range p.Messages {
		if !allowed[m.MessageID] || seen[m.MessageID] {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "review distinct messages of this coordination turn")
		}
		seen[m.MessageID] = true
		current, err := store.MessageAssociations(ctx, tx.Q(), m.MessageID)
		if err != nil {
			return Outcome{}, err
		}
		if current.ContentRevision != m.ContentRevision || current.Revision != m.AssociationRevision {
			return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "coordination source changed")
		}
	}
	if len(seen) != len(allowed) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "review every message in this coordination turn")
	}
	return Outcome{Result: map[string]any{"logical_turn_id": v.ID}, Notify: true}, nil
}

// HostedIssueContextFact reuses the existing fact validation and replacement
// path, while narrowing writes to the admitted manager's own Issue.
type HostedIssueContextFact struct{}

func (HostedIssueContextFact) Operation() string { return "issues.context_fact" }
func (HostedIssueContextFact) Authorize(_ context.Context, _ *Exec, _ store.Querier) error {
	return protocol.Errorf(protocol.CodeForbidden, "use the hosted coordination boundary")
}
func (HostedIssueContextFact) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if x.coordinationExecution == nil || x.coordinationExecution.IssueID == "" || x.Env.Target.ID != x.coordinationExecution.IssueID {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "fact target is outside admitted Issue")
	}
	return (ContextCommand{Action: "fact"}).Apply(ctx, x, tx)
}
func attachContextReviewSource(ctx context.Context, x *Exec, tx *store.Tx, message string) error {
	if x.coordinationExecution == nil {
		return attachLegacyReviewSource(ctx, x, tx, message)
	}
	if err := hostedSourceInIssueScope(ctx, tx.Q(), x.coordinationExecution.IssueID, message); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT OR IGNORE INTO logical_turn_messages(logical_turn_id,message_id) VALUES(?,?)`, x.coordinationLogicalID, message)
	return err
}

// Queued successors keep their own durable obligations, without becoming the
// current admitted manager's context-review responsibility.
func hostedIssueContextPending(ctx context.Context, q store.Querier, issue string) (bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT message_id FROM message_context_obligations WHERE work_item_id=? AND needs_review=1 UNION SELECT source_message_id FROM work_item_context_facts WHERE work_item_id=? AND superseded=0 AND needs_review=1`, issue, issue)
	if err != nil {
		return false, err
	}
	var sources []string
	for rows.Next() {
		var message string
		if err := rows.Scan(&message); err != nil {
			rows.Close()
			return false, err
		}
		sources = append(sources, message)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	for _, message := range sources {
		admitted, err := store.OwnerInputAdmitted(ctx, q, message)
		if err != nil {
			return false, err
		}
		if admitted {
			return true, nil
		}
	}
	return false, nil
}
