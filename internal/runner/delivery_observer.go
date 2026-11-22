package runner

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
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
		observed, observeErr := r.cfg.DeliveryObserver.Observe(ctx, reviewwatch.Ref{Provider: string(assignment.Delivery.Provider), Repository: assignment.Delivery.Repository, Number: assignment.Delivery.Number, IncludeChecks: assignment.PRChecks, PreservePartial: assignment.PRWaitEvidence, CheckAttempts: assignment.CheckAttempts})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		report := protocol.DeliveryReportPayload{InstanceID: r.instanceID, WorkItemID: assignment.WorkItemID, DeliveryID: assignment.Delivery.DeliveryID, Generation: assignment.Generation, Nonce: assignment.Nonce}
		if observeErr == nil {
			report.Snapshot = &protocol.DeliverySnapshot{Draft: observed.Draft, State: observed.State, HeadSHA: observed.HeadSHA, MergeSHA: observed.MergeSHA, URL: observed.URL, ProviderUpdatedAt: observed.UpdatedAt, MergedAt: observed.MergedAt, ClosedAt: observed.ClosedAt, MergedBy: deliveryActor(observed.MergedBy), ClosedBy: deliveryActor(observed.ClosedBy)}
			if assignment.PRWaitEvidence {
				report.Snapshot.TargetRef = observed.TargetRef
				report.Snapshot.CI = deliveryReportChecks(observed.CI, assignment.RequiredChecks)
				report.Snapshot.LineageErrorKind = observed.LineageErrorKind
			}
			if assignment.TargetLineage {
				report.Snapshot.TargetSHA = observed.TargetSHA
				report.Snapshot.TargetContainsMerge = observed.TargetContainsMerge
			}
			if assignment.CandidateIdentity {
				report.Snapshot.BaseSHA = observed.BaseSHA
				report.Snapshot.Mergeability = observed.Mergeability
			}
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

// Old servers strictly decode reports. Keep the provider data intact for the
// new contract, and omit the new field unless the assignment requested it.
func deliveryReportChecks(ci *protocol.DeliveryCI, requiredChecks bool) *protocol.DeliveryCI {
	if ci == nil || requiredChecks {
		return ci
	}
	legacy := *ci
	legacy.Checks = slices.Clone(ci.Checks)
	legacy.AttemptChecks = slices.Clone(ci.AttemptChecks)
	for i := range legacy.Checks {
		legacy.Checks[i].Required = nil
	}
	for i := range legacy.AttemptChecks {
		legacy.AttemptChecks[i].Required = nil
	}
	return &legacy
}

func deliveryActor(a *reviewwatch.Actor) *protocol.DeliveryActor {
	if a == nil {
		return nil
	}
	return &protocol.DeliveryActor{ID: a.ID, Login: a.Login}
}
