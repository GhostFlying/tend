package runner

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/reviewwatch"
)

// DeliveryObserver is independent of worker harnesses and execution slots.
type DeliveryObserver interface {
	Observe(context.Context, reviewwatch.Ref) (reviewwatch.Observation, error)
}

func (r *Runner) deliveryObservations(ctx context.Context) {
	for ctx.Err() == nil {
		if !r.maintenance() {
			if err := r.observeDelivery(ctx); err != nil && ctx.Err() == nil {
				var pe *protocol.Error
				if errors.As(err, &pe) && pe.Code == protocol.CodeInvalidRequest {
					// New runners can still execute tasks against an older server. It did
					// not advertise this optional operation; don't generate a retry storm.
					r.cfg.Log.InfoContext(ctx, "delivery observation is unsupported by the server")
					return
				}
				r.cfg.Log.WarnContext(ctx, "delivery observation deferred")
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
	}
}

func (r *Runner) observeDelivery(ctx context.Context) error {
	env, err := r.newEnvelope(protocol.OpDeliveriesPoll, protocol.Target{Type: "host", ID: r.cfg.HostID}, protocol.DeliveryPollPayload{InstanceID: r.instanceID})
	if err != nil {
		return err
	}
	// Queries have no external mutations and never enter the execution journal.
	// Lost responses can expire and be freshly observed after a process restart.
	op, err := r.hostOperation(ctx, env)
	if err != nil {
		return err
	}
	var result protocol.DeliveryPollResult
	if err := json.Unmarshal(op.Result, &result); err != nil {
		return err
	}
	for _, assignment := range result.Assignments {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		observed, observeErr := r.cfg.DeliveryObserver.Observe(ctx, reviewwatch.Ref{Provider: string(assignment.Delivery.Provider), Repository: assignment.Delivery.Repository, Number: assignment.Delivery.Number})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		report := protocol.DeliveryReportPayload{InstanceID: r.instanceID, WorkItemID: assignment.WorkItemID, DeliveryID: assignment.Delivery.DeliveryID, Generation: assignment.Generation, Nonce: assignment.Nonce}
		if observeErr == nil {
			report.Snapshot = &protocol.DeliverySnapshot{State: observed.State, HeadSHA: observed.HeadSHA, MergeSHA: observed.MergeSHA, URL: observed.URL, ProviderUpdatedAt: observed.UpdatedAt, MergedAt: observed.MergedAt, ClosedAt: observed.ClosedAt, MergedBy: deliveryActor(observed.MergedBy), ClosedBy: deliveryActor(observed.ClosedBy)}
		} else {
			report.ErrorKind = "query_failed"
			var failure *reviewwatch.Error
			if errors.As(observeErr, &failure) {
				report.ErrorKind = failure.Kind
			} else if errors.Is(observeErr, context.DeadlineExceeded) {
				report.ErrorKind = "timeout"
			}
		}
		reportEnv, err := r.newEnvelope(protocol.OpDeliveriesReport, protocol.Target{Type: "host", ID: r.cfg.HostID}, report)
		if err != nil {
			return err
		}
		// Reuse the same identity within this process when the report reply is
		// lost. Restart can re-observe; generation/nonce makes old reports stale.
		if err := r.retry(ctx, func() error { _, err := r.hostOperation(ctx, reportEnv); return err }); err != nil {
			return err
		}
	}
	return nil
}
func deliveryActor(a *reviewwatch.Actor) *protocol.DeliveryActor {
	if a == nil {
		return nil
	}
	return &protocol.DeliveryActor{ID: a.ID, Login: a.Login}
}
