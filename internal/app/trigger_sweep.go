package app

import (
	"context"
	"database/sql"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// SweepTriggers owns all due admission. Runner polling may invoke the same
// transaction helper to preserve the existing immediate-observation contract.
type SweepTriggers struct{}

func (SweepTriggers) Operation() string { return "triggers.sweep" }
func (SweepTriggers) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalSystem)
}
func (SweepTriggers) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if prepared, err := archivePrepared(ctx, tx.Q()); err != nil {
		return Outcome{}, err
	} else if prepared {
		return Outcome{NoEffect: true}, nil
	}
	out, err := (SweepFollowups{}).Apply(ctx, x, tx)
	if err != nil {
		return out, err
	}
	n, err := stageDeliveryObservations(ctx, x, tx)
	if err != nil {
		return Outcome{}, err
	}
	routed, err := routePendingTriggers(ctx, x, tx)
	if err != nil {
		return Outcome{}, err
	}
	web, err := dispatchWebhookReceipts(ctx, x, tx)
	if err != nil {
		return Outcome{}, err
	}
	if n+web+routed > 0 {
		out.NoEffect = false
		out.Notify = true
		out.Events = append(out.Events, Event{ObjectType: "triggers", Kind: "triggers.swept", Body: map[string]any{"observations": n, "webhooks": web}})
	}
	return out, nil
}

func stageDeliveryObservations(ctx context.Context, x *Exec, tx *store.Tx) (int, error) {
	items, err := store.ListIssues(ctx, tx.Q(), "", "")
	if err != nil {
		return 0, err
	}
	n := 0
	now := x.Now.UnixMilli()
	for _, item := range items {
		settled, err := settleObservationJobs(ctx, tx, item, now, true)
		if err != nil {
			return 0, err
		}
		n += settled
		for _, delivery := range item.Deliveries {
			row, err := observationRow(ctx, tx.Q(), item.WorkItemID, delivery.DeliveryID)
			if err != nil {
				return 0, err
			}
			due := row.QueryUntil <= now && row.NextCheckAt <= now && (row.RefreshRequested || (item.Status != "closed" && row.Observation.State != "closed" && row.Observation.State != "merged"))
			if !due {
				continue
			}
			var status, event string
			err = tx.Q().QueryRowContext(ctx, `SELECT status,event_id FROM observation_jobs WHERE issue_id=? AND delivery_id=?`, item.WorkItemID, delivery.DeliveryID).Scan(&status, &event)
			if err != nil && !errors.Is(err, store.ErrNotFound) && !errors.Is(err, sql.ErrNoRows) {
				return 0, err
			}
			if status == "pending" {
				continue
			}
			if status == "claimed" && event != "" {
				if err = tx.FinishTrigger(ctx, event, "failed", "observation lease expired; a new read may be claimed", now); err != nil {
					return 0, err
				}
			}
			// Until a new claim replaces the nonce, the runner protocol still
			// accepts the previous read's late report. Keep its event identity
			// so that accepted facts correct the same diagnostic record.
			_, err = tx.Exec(ctx, `INSERT INTO observation_jobs(issue_id,delivery_id,status,event_id,due_at,updated_at) VALUES(?,?,'pending','',?,?) ON CONFLICT(issue_id,delivery_id) DO UPDATE SET status='pending',event_id=CASE WHEN observation_jobs.status='claimed' THEN observation_jobs.event_id ELSE '' END,due_at=excluded.due_at,updated_at=excluded.updated_at`, item.WorkItemID, delivery.DeliveryID, row.NextCheckAt, now)
			if err != nil {
				return 0, err
			}
			n++
		}
	}
	return n, nil
}
