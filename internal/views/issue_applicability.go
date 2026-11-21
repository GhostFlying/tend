package views

import (
	"context"
	"database/sql"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Applicability is derived from the same ledger snapshot as the original data.
// It never resolves an Attention or revises a completed business object.
func issueProgressProjection(ctx context.Context, q store.Querier, issue protocol.Issue, currentDelivery bool) (*protocol.IssueProgress, error) {
	p, err := store.IssueProgress(ctx, q, issue.IssueID, issue.Revision)
	if err != nil || p == nil {
		return p, err
	}
	recorded, err := store.GetCanonicalIssueRevision(ctx, q, issue.IssueID, p.IssueRevision)
	if err != nil {
		return nil, err
	}
	switch {
	case p.IssueRevision < issue.Revision && (issue.State == protocol.IssueClosed || issue.State == protocol.IssueCancelled):
		p.HistoricalReason = "issue_ended"
	case recorded.AssignmentEpoch != issue.AssignmentEpoch || recorded.Assignee != issue.Assignee:
		p.HistoricalReason = "responsibility_changed"
	default:
		var ended bool
		err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_control_revisions WHERE issue_id=? AND revision>=? AND revision<? AND json_extract(snapshot_json,'$.control.state') IN ('closed','cancelled'))`, issue.IssueID, p.IssueRevision, issue.Revision).Scan(&ended)
		if err != nil {
			return nil, err
		}
		if ended {
			p.HistoricalReason = "issue_reopened"
		}
	}
	if currentDelivery && p.HistoricalReason == "" && p.Delivery != nil {
		p.HistoricalReason, err = progressDeliveryApplicability(ctx, q, issue, *p.Delivery)
		if err != nil {
			return nil, err
		}
	}
	p.Historical = p.HistoricalReason != ""
	return p, nil
}

func progressDeliveryApplicability(ctx context.Context, q store.Querier, issue protocol.Issue, candidate protocol.IssueDeliveryProgress) (string, error) {
	for _, d := range issue.Plan.Deliveries {
		if d.DeliveryID != candidate.DeliveryID {
			continue
		}
		row, err := store.GetDeliveryObservation(ctx, q, issue.IssueID, d.DeliveryID)
		if errors.Is(err, store.ErrNotFound) {
			break
		}
		if err != nil {
			return "", err
		}
		if candidate.Matches(d, row.Observation) {
			if c := row.Observation.CurrentConflict(); candidate.ConflictAction != "" &&
				(c == nil || c.Revision != candidate.ConflictRevision) {
				return "delivery_conflict_changed", nil
			}
			return "", nil
		}
		break
	}
	return "delivery_candidate_changed", nil
}

func attentionApplicability(ctx context.Context, q store.Querier, v *protocol.AttentionView) error {
	// A pending action, decision or unknown outcome needs its original resolution,
	// even when its originating Issue has ended or responsibility moved.
	if v.Kind != "information" || v.BlocksClose || v.IssueTransition != nil {
		return nil
	}
	issue := v.IssueID
	var epoch int64
	if v.Responsibility != nil {
		issue = v.Responsibility.IssueID
		epoch = v.Responsibility.AssignmentEpoch
	}
	if issue == "" {
		return nil
	}
	var state protocol.IssueState
	var currentEpoch int64
	err := q.QueryRowContext(ctx, `SELECT state,assignment_epoch FROM issue_controls WHERE issue_id=?`, issue).Scan(&state, &currentEpoch)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	} // Historical noncanonical origin.
	if err != nil {
		return err
	}
	switch {
	case state == protocol.IssueClosed || state == protocol.IssueCancelled:
		v.HistoricalReason = "issue_ended"
	case epoch > 0 && epoch != currentEpoch:
		v.HistoricalReason = "responsibility_changed"
	default:
		// Information has no frozen Issue revision. Only strictly later terminal
		// history establishes expiry after a reopen; equal timestamps stay unknown.
		var ended bool
		err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_control_revisions WHERE issue_id=? AND created_at>? AND json_extract(snapshot_json,'$.control.state') IN ('closed','cancelled'))`, issue, v.CreatedAt).Scan(&ended)
		if err != nil {
			return err
		}
		if ended {
			v.HistoricalReason = "issue_reopened"
		}
	}
	v.Historical = v.HistoricalReason != ""
	return nil
}
