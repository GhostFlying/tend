package views

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func SharedDeliveryReviews(ctx context.Context, q store.Querier, issueID, deliveryID, after string, limit int) (protocol.SharedDeliveryReviewPage, error) {
	out := protocol.SharedDeliveryReviewPage{Items: []protocol.SharedDeliveryReview{}}
	if limit < 1 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT r.review_id,r.provider,r.repository,r.number FROM shared_delivery_review_links l JOIN shared_delivery_reviews r USING(review_id) WHERE l.issue_id=? AND (?='' OR l.delivery_id=?) AND r.review_id>? ORDER BY r.review_id LIMIT ?`, issueID, deliveryID, deliveryID, after, limit+1)
	if err != nil {
		return out, err
	}
	type target struct {
		id  string
		ref protocol.IssueDelivery
	}
	targets := []target{}
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.id, &t.ref.Provider, &t.ref.Repository, &t.ref.Number); err != nil {
			rows.Close()
			return out, err
		}
		targets = append(targets, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(targets) > limit {
		out.More = true
		targets = targets[:limit]
	}
	for _, t := range targets {
		r, err := store.SharedDeliveryReviewForTarget(ctx, q, t.ref)
		if err != nil {
			return out, err
		}
		if err := store.SharedDeliveryReviewCurrent(ctx, q, &r); err != nil {
			return out, err
		}
		out.Items = append(out.Items, r)
		out.Next = t.id
	}
	if !out.More {
		out.Next = ""
	}
	return out, nil
}
