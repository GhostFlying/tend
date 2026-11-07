package views

import (
	"context"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// DeploymentPreparationDeliveries reads only registered deliveries of the
// authorized Issues, including historical observation scopes. It never probes.
func DeploymentPreparationDeliveries(ctx context.Context, q store.Querier, issueIDs []string) ([]protocol.IssueDeliveryPage, error) {
	out := []protocol.IssueDeliveryPage{}
	for _, id := range issueIDs {
		var canonical bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_controls WHERE issue_id=?)`, id).Scan(&canonical); err != nil {
			return nil, err
		}
		if canonical {
			page, err := CanonicalIssueDeliveries(ctx, q, id)
			if err != nil {
				return nil, err
			}
			out = append(out, page)
			continue
		}
		// Old frozen observation scopes may name a historical work item. Do
		// not treat a broken canonical aggregate as that compatibility case.
		issue, err := store.GetIssue(ctx, q, id)
		if err != nil {
			return nil, err
		}
		page := protocol.IssueDeliveryPage{IssueID: id, Items: []protocol.IssueDeliveryView{}}
		for _, delivery := range issue.Deliveries {
			observation := protocol.DeliveryObservation{WorkItemID: id, DeliveryID: delivery.DeliveryID, DeliverySnapshot: protocol.DeliverySnapshot{State: "unknown"}}
			row, err := store.GetDeliveryObservation(ctx, q, id, delivery.DeliveryID)
			if err == nil {
				observation = row.Observation
			} else if !errors.Is(err, store.ErrNotFound) {
				return nil, err
			}
			page.Items = append(page.Items, protocol.IssueDeliveryView{Delivery: delivery, Observation: protocol.CanonicalDeliveryObservation(observation)})
		}
		out = append(out, page)
	}
	return out, nil
}
