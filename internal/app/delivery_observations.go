package app

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// PollDeliveries reserves a read-only query, not a worker or execution slot.
type PollDeliveries struct{}

func (PollDeliveries) Operation() string { return protocol.OpDeliveriesPoll }
func (PollDeliveries) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	_, err := requireActiveHost(ctx, x, q)
	return err
}
func (PollDeliveries) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.DeliveryPollPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	rt, err := deliveryRuntime(ctx, x, tx.Q(), p.InstanceID)
	if err != nil {
		return Outcome{}, err
	}
	result := protocol.DeliveryPollResult{Assignments: []protocol.DeliveryAssignment{}}
	if rt.Mode != protocol.RunnerNormal {
		return Outcome{Result: result, NoEffect: true}, nil
	}
	items, err := store.ListWorkItems(ctx, tx.Q(), "", "")
	if err != nil {
		return Outcome{}, err
	}
	now := x.Now.UnixMilli()
	var selected *store.DeliveryObservationRow
	var selectedDelivery protocol.WorkItemDelivery
	for _, item := range items {
		for _, delivery := range item.Deliveries {
			if delivery.HostID != "" && delivery.HostID != x.Principal.Subject || !slices.Contains(rt.Capabilities, deliveryCapability(delivery.Provider)) {
				continue
			}
			row, err := observationRow(ctx, tx.Q(), item.WorkItemID, delivery.DeliveryID)
			if err != nil {
				return Outcome{}, err
			}
			if row.QueryUntil > now || row.NextCheckAt > now {
				continue
			}
			if !row.RefreshRequested && (item.Status == "closed" || row.Observation.State == "merged" || row.Observation.State == "closed") {
				continue
			}
			if selected == nil || row.NextCheckAt < selected.NextCheckAt {
				selected = &row
				selectedDelivery = delivery
			}
		}
	}
	if selected == nil {
		return Outcome{Result: result, NoEffect: true}, nil
	}
	selected.Observation.Generation++
	selected.Nonce = x.IDs.New(protocol.PrefixRequest)
	selected.HostID = x.Principal.Subject
	selected.InstanceID = p.InstanceID
	selected.QueryUntil = now + (2 * time.Minute).Milliseconds()
	// A timed-out query joins the queue at its last attempt, instead of always
	// preceding untouched references. A completed report sets the next interval.
	selected.NextCheckAt = now
	if err := tx.PutDeliveryObservation(ctx, *selected); err != nil {
		return Outcome{}, err
	}
	result.Assignments = append(result.Assignments, protocol.DeliveryAssignment{WorkItemID: selected.Observation.WorkItemID, Delivery: selectedDelivery, Generation: selected.Observation.Generation, Nonce: selected.Nonce})
	return Outcome{Result: result}, nil
}
func deliveryCapability(provider protocol.WorkItemDeliveryProvider) string {
	switch provider {
	case "github":
		return protocol.CapabilityDeliveryGitHub
	case "codebase":
		return protocol.CapabilityDeliveryCodebase
	}
	return ""
}
func deliveryRuntime(ctx context.Context, x *Exec, q store.Querier, instance string) (protocol.RunnerRuntime, error) {
	row, err := store.GetHostRuntime(ctx, q, x.Principal.Subject)
	if err != nil {
		return protocol.RunnerRuntime{}, err
	}
	if instance == "" || row.Runtime.InstanceID != instance || !row.Online {
		return protocol.RunnerRuntime{}, protocol.Errorf(protocol.CodeInvalidTransition, "delivery observer instance is not current")
	}
	return row.Runtime, nil
}
func observationRow(ctx context.Context, q store.Querier, item, id string) (store.DeliveryObservationRow, error) {
	row, err := store.GetDeliveryObservation(ctx, q, item, id)
	if errors.Is(err, store.ErrNotFound) {
		row.Observation.WorkItemID = item
		row.Observation.DeliveryID = id
		err = nil
	}
	return row, err
}

// cancelDeliveryQueries fences observations issued before responsibility closed.
// The last provider facts remain intact; a later explicit refresh gets a new
// query identity even when the work item stays closed.
func cancelDeliveryQueries(ctx context.Context, tx *store.Tx, item protocol.WorkItem) error {
	for _, delivery := range item.Deliveries {
		row, err := store.GetDeliveryObservation(ctx, tx.Q(), item.WorkItemID, delivery.DeliveryID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if row.Nonce == "" && !row.RefreshRequested {
			continue
		}
		row.Observation.Generation++
		row.Nonce, row.HostID, row.InstanceID = "", "", ""
		row.QueryUntil = 0
		row.RefreshRequested = false
		row.Observation.RefreshPending = false
		if err := tx.PutDeliveryObservation(ctx, row); err != nil {
			return err
		}
	}
	return nil
}

// ReportDelivery applies only the latest server-issued query generation and
// nonce, even when an older host's read finished later. Failures retain facts.
type ReportDelivery struct{}

func (ReportDelivery) Operation() string { return protocol.OpDeliveriesReport }
func (ReportDelivery) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	_, err := requireActiveHost(ctx, x, q)
	return err
}
func (ReportDelivery) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.DeliveryReportPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if _, err := deliveryRuntime(ctx, x, tx.Q(), p.InstanceID); err != nil {
		return Outcome{}, err
	}
	item, err := store.GetWorkItem(ctx, tx.Q(), p.WorkItemID)
	if err != nil {
		return Outcome{}, err
	}
	delivery, ok := findDelivery(item, p.DeliveryID)
	if !ok {
		return Outcome{Result: protocol.DeliveryReportResult{}}, nil
	}
	row, err := observationRow(ctx, tx.Q(), p.WorkItemID, p.DeliveryID)
	if err != nil {
		return Outcome{}, err
	}
	if row.Nonce == "" || row.Nonce != p.Nonce || row.Observation.Generation != p.Generation || row.HostID != x.Principal.Subject || row.InstanceID != p.InstanceID {
		return Outcome{Result: protocol.DeliveryReportResult{}}, nil
	}
	if (p.Snapshot == nil) == (p.ErrorKind == "") {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "provide either provider facts or an observation error")
	}
	if p.Snapshot != nil && p.Snapshot.State != "open" && p.Snapshot.State != "merged" && p.Snapshot.State != "closed" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "unknown delivery state")
	}
	if p.ErrorKind != "" && !slices.Contains([]string{"invalid_reference", "unsupported_provider", "cli_unavailable", "query_failed", "invalid_response", "not_found", "authentication_required", "unknown_state", "timeout"}, p.ErrorKind) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "unknown observation error category")
	}
	rt, err := store.GetHostRuntime(ctx, tx.Q(), x.Principal.Subject)
	if err != nil {
		return Outcome{}, err
	}
	if !slices.Contains(rt.Runtime.Capabilities, deliveryCapability(delivery.Provider)) {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "observer capability is not available")
	}
	previous := row.Observation
	manualRefresh := row.RefreshRequested
	row.Observation.AttemptedAt = x.Now.UnixMilli()
	row.Observation.ErrorKind = p.ErrorKind
	if p.Snapshot != nil {
		row.Observation.DeliverySnapshot = *p.Snapshot
		row.Observation.CheckedAt = x.Now.UnixMilli()
	}
	row.Nonce = ""
	row.QueryUntil = 0
	row.RefreshRequested = false
	row.Observation.RefreshPending = false
	interval := 5 * time.Minute
	if item.Status == "snoozed" {
		interval = time.Hour
	}
	row.NextCheckAt = x.Now.Add(interval).UnixMilli()
	if err := tx.PutDeliveryObservation(ctx, row); err != nil {
		return Outcome{}, err
	}
	events := []Event{}
	if !reflect.DeepEqual(previous.DeliverySnapshot, row.Observation.DeliverySnapshot) || previous.ErrorKind != p.ErrorKind || manualRefresh {
		event := deliveryChanged(item, row.Observation)
		event.Body["previous_state"] = previous.State
		event.Body["previous_error_kind"] = previous.ErrorKind
		events = append(events, event)
	} else {
		// An unchanged read still advances the displayed check/attempt time.
		// Invalidate live views without creating a notice or agent turn.
		event := deliveryChanged(item, row.Observation)
		event.Kind = "work_item.delivery_checked"
		events = append(events, event)
	}
	return Outcome{Result: protocol.DeliveryReportResult{Accepted: true}, Events: events}, nil
}
func findDelivery(item protocol.WorkItem, id string) (protocol.WorkItemDelivery, bool) {
	for _, d := range item.Deliveries {
		if d.DeliveryID == id {
			return d, true
		}
	}
	return protocol.WorkItemDelivery{}, false
}
func deliveryChanged(item protocol.WorkItem, o protocol.DeliveryObservation) Event {
	return Event{ObjectType: "work_item", ObjectID: item.WorkItemID, ObjectVersion: item.Revision, Kind: "work_item.delivery_changed", Body: map[string]any{"delivery_id": o.DeliveryID, "state": o.State, "previous_state": "", "error_kind": o.ErrorKind, "checked_at": o.CheckedAt, "generation": o.Generation, "refresh_pending": o.RefreshPending}}
}

// RefreshDelivery invalidates an older query and requests one fresh read. It
// neither changes the plan revision nor asserts a provider outcome.
type RefreshDelivery struct{}

func (RefreshDelivery) Operation() string { return protocol.OpRefreshDelivery }
func (RefreshDelivery) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner, protocol.PrincipalMainAgent)
}
func (RefreshDelivery) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.RefreshDeliveryPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	item, err := store.GetWorkItem(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	rev, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	if rev != item.Revision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "work item revision changed")
	}
	if _, ok := findDelivery(item, p.DeliveryID); !ok {
		return Outcome{}, protocol.Errorf(protocol.CodeNotFound, "delivery reference not found")
	}
	row, err := observationRow(ctx, tx.Q(), item.WorkItemID, p.DeliveryID)
	if err != nil {
		return Outcome{}, err
	}
	row.Observation.Generation++
	row.Nonce = ""
	row.QueryUntil = 0
	// Make a future check due now while retaining an already queued request's
	// age. Repeated refreshes neither jump ahead of nor postpone older work.
	if row.NextCheckAt > x.Now.UnixMilli() {
		row.NextCheckAt = x.Now.UnixMilli()
	}
	row.RefreshRequested = true
	row.Observation.RefreshPending = true
	if err := tx.PutDeliveryObservation(ctx, row); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: row.Observation, Events: []Event{deliveryChanged(item, row.Observation)}}, nil
}
