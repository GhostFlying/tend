package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func canonicalOwnerEndpointFacts(ctx context.Context, q store.Querier, current protocol.Issue) (domain.IssueLifecycleFacts, error) {
	var facts domain.IssueLifecycleFacts
	rows, err := q.QueryContext(ctx, `SELECT c.issue_id,w.revision FROM issue_controls c JOIN work_items w ON w.work_item_id=c.issue_id WHERE c.parent_issue_id=? AND c.state NOT IN ('closed','cancelled') ORDER BY c.issue_id`, current.IssueID)
	if err != nil {
		return facts, err
	}
	for rows.Next() {
		var b protocol.IssueBlocker
		b.Code = "child_responsibility"
		if err = rows.Scan(&b.ObjectID, &b.Revision); err != nil {
			rows.Close()
			return facts, err
		}
		facts.UnsettledChildren = append(facts.UnsettledChildren, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return facts, err
	}
	// Source changes remain visible context; they do not create additional
	// semantic-review responsibilities that prevent an actual endpoint.
	duties, err := store.ListIssueDeliveryDuties(ctx, q, current.IssueID)
	if err != nil {
		return facts, err
	}
	for _, d := range duties {
		if d.Status != "complete" && d.Status != "report" && d.Status != "ended" {
			facts.OpenAttention = append(facts.OpenAttention, protocol.IssueBlocker{Code: "delivery_responsibility", ObjectID: d.TaskID, Revision: d.Revision})
		}
	}
	descendantDuties, err := store.FollowupIDs(ctx, q, `WITH RECURSIVE descendants(id) AS (
 SELECT issue_id FROM issue_controls WHERE parent_issue_id=? UNION ALL SELECT c.issue_id FROM issue_controls c JOIN descendants d ON c.parent_issue_id=d.id)
 SELECT DISTINCT d.issue_id FROM issue_delivery_duties d JOIN descendants s ON s.id=d.issue_id WHERE d.status NOT IN ('complete','report','ended') ORDER BY d.issue_id`, current.IssueID)
	if err != nil {
		return facts, err
	}
	for _, id := range descendantDuties {
		facts.OpenAttention = append(facts.OpenAttention, protocol.IssueBlocker{Code: "child_delivery_responsibility", ObjectID: id})
	}
	attentionIDs, err := store.FollowupIDs(ctx, q, `SELECT attention_id FROM attentions WHERE issue_id=? AND status='open' AND kind IN ('decision','action') AND json_extract(body_json,'$.blocks_close')=1 ORDER BY attention_id`, current.IssueID)
	if err != nil {
		return facts, err
	}
	for _, id := range attentionIDs {
		facts.OpenAttention = append(facts.OpenAttention, protocol.IssueBlocker{Code: "attention", ObjectID: id})
	}
	for _, f := range current.Plan.FollowOns {
		if (f.Kind == protocol.WorkItemFollowOnCommitment || f.Kind == protocol.WorkItemFollowOnProblem) && f.Status == protocol.WorkItemFollowOnOpen {
			facts.OpenCommitments = append(facts.OpenCommitments, protocol.IssueBlocker{Code: "follow_on", ObjectID: f.FollowOnID, Revision: current.Revision})
		}
	}
	if err := appendCanonicalTaskFacts(ctx, q, current.IssueID, &facts); err != nil {
		return facts, err
	}
	// Source interpretation is recorded during ordinary work. It does not add
	// endpoint gates; active executions and unconfirmed receipts remain facts.
	return facts, nil
}

func applyCanonicalSourceDispositions(ctx context.Context, tx *store.Tx, issue string, p canonicalOwnerEndpoint) error {
	if len(p.SourceDispositions) == 0 {
		if p.ExpectedContextRevision != nil {
			return protocol.Errorf(protocol.CodeInvalidRequest, "context revision requires explicit source dispositions")
		}
		return nil
	}
	var currentContext int64
	if err := tx.Q().QueryRowContext(ctx, `SELECT revision FROM work_item_contexts WHERE work_item_id=?`, issue).Scan(&currentContext); err != nil {
		return err
	}
	if p.ExpectedContextRevision == nil || *p.ExpectedContextRevision != currentContext {
		return protocol.Errorf(protocol.CodeStaleRevision, "issue source context changed")
	}
	seen := map[string]bool{}
	for _, d := range p.SourceDispositions {
		if seen[d.MessageID] || strings.TrimSpace(d.Reason) == "" {
			return protocol.Errorf(protocol.CodeInvalidRequest, "distinct source dispositions and reasons are required")
		}
		seen[d.MessageID] = true
		current, err := store.MessageAssociations(ctx, tx.Q(), d.MessageID)
		if err != nil {
			return err
		}
		if current.ContentRevision != d.ContentRevision || current.Revision != d.AssociationRevision {
			return protocol.Errorf(protocol.CodeStaleRevision, "disposed source changed")
		}
		result, err := tx.Exec(ctx, `UPDATE message_context_obligations SET needs_review=0 WHERE work_item_id=? AND message_id=? AND needs_review=1`, issue, d.MessageID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return protocol.Errorf(protocol.CodeInvalidTransition, "source has no pending obligation for this issue")
		}
	}
	// No message_dispositions.facts_revision, global facts, main-turn status,
	// native receipt or other Issue obligation is changed by this owner endpoint.
	return nil
}

func recordCanonicalEndpointContextReviews(ctx context.Context, tx *store.Tx, issue string) error {
	_, err := tx.Exec(ctx, `INSERT INTO logical_turn_context_reviews(logical_turn_id,work_item_id,revision)
 SELECT lc.logical_turn_id,c.work_item_id,c.revision FROM logical_turn_contexts lc
 JOIN logical_turns l USING(logical_turn_id) JOIN work_item_contexts c ON c.work_item_id=lc.work_item_id
 WHERE lc.work_item_id=? AND l.status<>'completed'
 AND NOT EXISTS(SELECT 1 FROM message_context_obligations o WHERE o.work_item_id=c.work_item_id AND o.needs_review=1)
 ON CONFLICT(logical_turn_id,work_item_id) DO UPDATE SET revision=excluded.revision`, issue)
	return err
}

func checkCanonicalCompletionPrerequisites(ctx context.Context, q store.Querier, issue protocol.Issue) error {
	for _, id := range issue.DependsOn {
		prerequisite, err := store.GetCanonicalIssue(ctx, q, id)
		if err != nil {
			return err
		}
		achieved := prerequisite.State == protocol.IssueClosed && prerequisite.Closure != nil && prerequisite.Closure.Kind == protocol.WorkItemClosureCompleted
		// This immutable aggregate was validated on its actual app close path. Do not
		// rescan actor/request/event history as protection against local owner edits.
		if !achieved {
			return canonicalEndpointError("completion prerequisite is not achieved", []protocol.IssueBlocker{{Code: "issue_dependency", ObjectID: id, Revision: prerequisite.Revision}})
		}
		if err = domain.ValidateIssueEvidence(prerequisite.Closure.Evidence); err != nil {
			return fmt.Errorf("app: persisted prerequisite endpoint: %w", err)
		}
	}
	return nil
}

// reviewCanonicalTerminalContext records the exact share explicitly reviewed by
// CommitLogicalTurn, including a share registered after owner source settlement.
// It never initializes execution or clears a pending source obligation.
func reviewCanonicalTerminalContext(ctx context.Context, tx *store.Tx, logical, issue string, revision int64) (bool, error) {
	reviewed, err := canonicalTerminalContextReviewed(ctx, tx.Q(), logical, issue, revision)
	if err != nil || reviewed {
		return reviewed, err
	}
	result, err := tx.Exec(ctx, `INSERT INTO logical_turn_context_reviews(logical_turn_id,work_item_id,revision)
 SELECT ?,c.issue_id,w.revision FROM issue_controls c
 JOIN work_item_contexts w ON w.work_item_id=c.issue_id
 WHERE c.issue_id=? AND c.state IN ('closed','cancelled') AND w.revision=?
 AND NOT EXISTS(SELECT 1 FROM message_context_obligations o WHERE o.work_item_id=c.issue_id AND o.needs_review=1)
 ON CONFLICT(logical_turn_id,work_item_id) DO UPDATE SET revision=excluded.revision`, logical, issue, revision)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

// canonicalTerminalContextReviewed is the bounded CommitLogicalTurn integration
// seam. It never certifies an active context or an execution admission baseline.
func canonicalTerminalContextReviewed(ctx context.Context, q store.Querier, logical, issue string, revision int64) (bool, error) {
	var reviewed bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_controls c
 JOIN logical_turn_context_reviews r ON r.work_item_id=c.issue_id
 WHERE c.issue_id=? AND c.state IN ('closed','cancelled') AND r.logical_turn_id=? AND r.revision=?
 AND NOT EXISTS(SELECT 1 FROM message_context_obligations o WHERE o.work_item_id=c.issue_id AND o.needs_review=1))`, issue, logical, revision).Scan(&reviewed)
	return reviewed, err
}
