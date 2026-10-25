package app

import (
	"context"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func acknowledgeCompletionReviews(ctx context.Context, tx *store.Tx, w protocol.WorkItem, reviews []protocol.CompletionReviewAcknowledgement) error {
	for _, r := range reviews {
		if strings.TrimSpace(r.Reason) == "" {
			return protocol.Errorf(protocol.CodeInvalidRequest, "completion_reviews.reason must explain the stage assessment and remaining responsibility")
		}
		if err := tx.AcknowledgeCompletionReview(ctx, w.WorkItemID, r.EventID, w.Revision, r.Reason); err != nil {
			return err
		}
	}
	return nil
}

// Settled stages acknowledge their completed tasks only when every linked stage
// has been assessed. Explicit acknowledgements cover continuing review/service work.
func settleCompletionReviews(ctx context.Context, tx *store.Tx, w protocol.WorkItem) error {
	pending, err := store.ListPendingCompletionReviews(ctx, tx.Q(), w.WorkItemID)
	if err != nil {
		return err
	}
	for _, r := range pending {
		settled := true
		linked := false
		for _, s := range w.Stages {
			if !slices.Contains(s.TaskIDs, r.TaskID) {
				continue
			}
			linked = true
			if s.Status != "done" && s.Status != "cancelled" && s.Status != "retired" {
				settled = false
			}
		}
		if w.Status == "closed" || linked && settled {
			if err := tx.AcknowledgeCompletionReview(ctx, w.WorkItemID, r.EventID, w.Revision, "Linked stages settled with validated plan evidence"); err != nil {
				return err
			}
		}
	}
	return nil
}
