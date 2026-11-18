package views

import (
	"context"
	"errors"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// issueDeliveryReviewProgress projects verified evidence without recording a
// review result, changing its applicability or giving it settlement authority.
func issueDeliveryReviewProgress(ctx context.Context, q store.Querier, issue string, delivery protocol.IssueDelivery) (*protocol.IssueDeliveryReviewProgress, error) {
	review, err := store.SharedDeliveryReviewForTarget(ctx, q, delivery)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(review.Rounds) == 0 || !slices.ContainsFunc(review.Links, func(link protocol.SharedDeliveryReviewLink) bool {
		return link.IssueID == issue && link.DeliveryID == delivery.DeliveryID
	}) {
		return nil, nil
	}

	round := review.Rounds[len(review.Rounds)-1]
	result := round.Result
	if len(round.Corrections) > 0 {
		result = &round.Corrections[len(round.Corrections)-1].Result
	}
	recorded := result != nil
	if !recorded && (review.Status == "reviewing" || review.Status == "waiting") {
		input := protocol.SharedReviewInputRound(round)
		if input.ReviewerRole == protocol.RoleAccept {
			result, _, _, err = store.ReadSharedAcceptanceResult(ctx, q, review, &input)
		} else {
			result, _, _, err = store.ReadSharedReviewResult(ctx, q, review, input)
		}
		if err != nil {
			if errors.Is(err, store.ErrNotFound) ||
				errors.Is(err, store.ErrAcceptanceReviewEvidence) ||
				protocol.CodeOf(err) != protocol.CodeInternal {
				return nil, nil
			}
			return nil, err
		}
	}
	if result == nil {
		return nil, nil
	}
	if recorded {
		if err := store.SharedDeliveryReviewCurrent(ctx, q, &review); err != nil {
			return nil, err
		}
	}
	return &protocol.IssueDeliveryReviewProgress{
		Round: round.Number, HeadSHA: result.HeadSHA, BaseSHA: result.BaseSHA,
		Verdict: result.Verdict, Recorded: recorded, Valid: review.Valid && recorded,
	}, nil
}
