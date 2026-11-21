package views

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func SharedDeliveryReviews(ctx context.Context, q store.Querier, issueID, deliveryID, after string, limit int, scopes ...store.MaterialScope) (protocol.SharedDeliveryReviewPage, error) {
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
		var scope store.MaterialScope
		if len(scopes) > 0 {
			scope = scopes[0]
		} else {
			ids, err := MaterialIssueIDs(ctx, q, issueID)
			if err != nil {
				return out, err
			}
			scope, err = MaterialScope(ctx, q, ids)
			if err != nil {
				return out, err
			}
		}
		evidence, err := SharedReviewExistingEvidence(ctx, q, r, scope)
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, r)
		out.ExistingEvidence = append(out.ExistingEvidence, evidence)
		out.Next = t.id
	}
	if !out.More {
		out.Next = ""
	}
	return out, nil
}

// SharedDeliveryReviewReceipt reads a committed review revision by its original
// request within the Issue's delivery. It never infers failure from absence.
func SharedDeliveryReviewReceipt(ctx context.Context, q store.Querier, issueID, deliveryID, requestID string) (protocol.SharedDeliveryReviewPage, error) {
	out := protocol.SharedDeliveryReviewPage{Items: []protocol.SharedDeliveryReview{}}
	if deliveryID == "" || !protocol.ValidID(protocol.PrefixRequest, requestID) {
		return out, protocol.Errorf(protocol.CodeInvalidRequest, "delivery_id and the original request_id are required")
	}
	issue, err := store.GetCanonicalIssue(ctx, q, issueID)
	if err != nil {
		return out, err
	}
	for _, delivery := range issue.Plan.Deliveries {
		if delivery.DeliveryID != deliveryID {
			continue
		}
		out.Receipt = &protocol.SharedReviewRequestReceipt{RequestID: requestID, Status: "unknown"}
		var raw string
		err := q.QueryRowContext(ctx, `SELECT h.body_json FROM shared_delivery_review_history h JOIN shared_delivery_reviews r USING(review_id)
 WHERE r.provider=? AND r.repository=? AND r.number=? AND h.request_id=?
 AND EXISTS(SELECT 1 FROM json_each(h.body_json,'$.links') l WHERE json_extract(l.value,'$.issue_id')=? AND json_extract(l.value,'$.delivery_id')=?)
 ORDER BY h.revision DESC LIMIT 1`, delivery.Provider, delivery.Repository, delivery.Number, requestID, issueID, deliveryID).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		var review protocol.SharedDeliveryReview
		if err := json.Unmarshal([]byte(raw), &review); err != nil {
			return out, err
		}
		review.Valid = false
		review.InvalidReason = "historical receipt; read the current review for applicability"
		out.Receipt.Status, out.Receipt.Review = "committed", &review
		return out, nil
	}
	return out, protocol.Errorf(protocol.CodeNotFound, "delivery is outside this Issue")
}
