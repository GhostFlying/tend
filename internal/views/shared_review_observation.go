package views

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// AcceptanceSharedReviews projects only the current acceptance's frozen tuple
// within its existing observation scope. Pending links can bootstrap a genuine
// whole-PR verdict without inventing a round or exposing unrelated Issue links.
func AcceptanceSharedReviews(ctx context.Context, q store.Querier, task string, spec int64, artifact string, allowed []string) ([]protocol.ObservedDeliveryReview, error) {
	out := []protocol.ObservedDeliveryReview{}
	seen := map[string]bool{}
	for _, id := range allowed {
		// Filter by the current task before loading bodies. A worker must not
		// miss its binding behind another task's first page of reviews.
		rows, err := q.QueryContext(ctx, `SELECT DISTINCT r.provider,r.repository,r.number FROM shared_delivery_review_links l JOIN shared_delivery_reviews r USING(review_id) WHERE l.issue_id=? AND l.task_id=? ORDER BY r.review_id`, id, task)
		if err != nil {
			return nil, err
		}
		refs := []protocol.IssueDelivery{}
		for rows.Next() {
			var ref protocol.IssueDelivery
			if err := rows.Scan(&ref.Provider, &ref.Repository, &ref.Number); err != nil {
				rows.Close()
				return nil, err
			}
			refs = append(refs, ref)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		for _, ref := range refs {
			r, err := store.SharedDeliveryReviewForTarget(ctx, q, ref)
			if err != nil {
				return nil, err
			}
			if err := store.SharedDeliveryReviewCurrent(ctx, q, &r); err != nil {
				return nil, err
			}
			if seen[r.ReviewID] {
				continue
			}
			owns := false
			for _, l := range r.Links {
				owns = owns || l.IssueID == id && l.TaskID == task
			}
			if len(r.Rounds) > 0 {
				last := r.Rounds[len(r.Rounds)-1]
				owns = owns && last.TaskID == task && last.SpecVersion == spec && (last.ArtifactID == "" || last.ArtifactID == artifact)
			}
			if !owns {
				continue
			}
			seen[r.ReviewID] = true
			out = append(out, *observedSharedReview(r))
		}
	}
	return out, nil
}
