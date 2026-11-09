package app

import (
	"context"
	"database/sql"
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
	if x.serverDeliveryObservations {
		return Outcome{Result: result, NoEffect: true}, nil
	}
	if rt.Mode != protocol.RunnerNormal {
		return Outcome{Result: result, NoEffect: true}, nil
	}
	events, progressed, err := fanoutDeliveryFacts(ctx, x, tx, nil, 100, false)
	if err != nil {
		return Outcome{}, err
	}
	if _, err := stageDeliveryObservations(ctx, x, tx); err != nil {
		return Outcome{}, err
	}
	items, err := store.ListIssues(ctx, tx.Q(), "", "")
	if err != nil {
		return Outcome{}, err
	}
	now := x.Now.UnixMilli()
	var selected *store.DeliveryObservationRow
	var selectedDelivery protocol.IssueDelivery
	var selectedItem protocol.WorkItem
	for _, item := range items {
		for _, delivery := range item.Deliveries {
			if delivery.HostID != "" && delivery.HostID != x.Principal.Subject || !slices.Contains(rt.Capabilities, deliveryCapability(delivery.Provider)) {
				continue
			}
			var ready bool
			if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM observation_jobs WHERE issue_id=? AND delivery_id=? AND status='pending')`, item.WorkItemID, delivery.DeliveryID).Scan(&ready); err != nil {
				return Outcome{}, err
			}
			if !ready {
				continue
			}
			row, err := observationRow(ctx, tx.Q(), item.WorkItemID, delivery.DeliveryID)
			if err != nil {
				return Outcome{}, err
			}
			var serverUntil int64
			if err := tx.Q().QueryRowContext(ctx, `SELECT query_until FROM server_delivery_jobs WHERE provider=? AND repository=? AND number=?`, delivery.Provider, delivery.Repository, delivery.Number).Scan(&serverUntil); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return Outcome{}, err
			}
			if row.QueryUntil > now || row.NextCheckAt > now || serverUntil > now || row.ObservedRevision < row.FactRevision && !row.RefreshRequested {
				continue
			}
			var reading bool
			if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM server_delivery_references r JOIN delivery_observations o ON o.work_item_id=r.issue_id AND o.delivery_id=r.delivery_id WHERE r.provider=? AND r.repository=? AND r.number=? AND o.host_id<>'server' AND o.nonce<>'' AND o.query_until>? AND NOT EXISTS(SELECT 1 FROM retired_runner_delivery_queries retired WHERE retired.nonce=o.nonce))`, delivery.Provider, delivery.Repository, delivery.Number, now).Scan(&reading); err != nil {
				return Outcome{}, err
			}
			if reading {
				continue
			}
			if !row.RefreshRequested && (item.Status == "closed" || row.Observation.State == "merged" || row.Observation.State == "closed") {
				continue
			}
			if selected == nil || row.NextCheckAt < selected.NextCheckAt {
				selected = &row
				selectedDelivery = delivery
				selectedItem = item
			}
		}
	}
	if selected == nil {
		return Outcome{Result: result, Events: events, NoEffect: !progressed}, nil
	}
	plannedAt := selected.NextCheckAt
	baseline, err := deliveryBaseline(ctx, tx.Q())
	if err != nil {
		return Outcome{}, err
	}
	selected.QueryBaselineUpgradeID = baseline.View.UpgradeID
	selected.QueryBaselineUpgradeRevision = baseline.View.Revision
	if err := fenceServerDeliveryQuery(ctx, tx, selectedDelivery, now); err != nil {
		return Outcome{}, err
	}
	// Replacing this Issue's generation keeps its existing stale-report
	// disposition. Other references retain valid reports as shared history.
	if err := retireRunnerDeliveryQuery(ctx, x, tx, selected); err != nil {
		return Outcome{}, err
	}
	if err := retireSharedRunnerQueries(ctx, x, tx, selectedDelivery, selected.Nonce, now); err != nil {
		return Outcome{}, err
	}
	if err := tx.Q().QueryRowContext(ctx, `SELECT refresh_revision FROM server_delivery_jobs WHERE provider=? AND repository=? AND number=?`, selectedDelivery.Provider, selectedDelivery.Repository, selectedDelivery.Number).Scan(&selected.ServerRefreshRevision); err != nil {
		return Outcome{}, err
	}
	selected.Observation.Generation++
	selected.Nonce = x.IDs.New(protocol.PrefixRequest)
	selected.HostID = x.Principal.Subject
	selected.InstanceID = p.InstanceID
	selected.QueryUntil = now + (2 * time.Minute).Milliseconds()
	selected.Observation.RefreshPending = true
	// A timed-out query joins the queue at its last attempt, instead of always
	// preceding untouched references. A completed report sets the next interval.
	selected.NextCheckAt = now
	if err := tx.PutDeliveryObservationQuery(ctx, *selected); err != nil {
		return Outcome{}, err
	}
	eventID := x.IDs.New(protocol.Prefix("trigger"))
	if err := tx.InsertTrigger(ctx, protocol.TriggerEvent{ID: eventID, IssueID: selectedItem.WorkItemID, ScheduleID: "observation:" + selectedItem.WorkItemID + ":" + selectedDelivery.DeliveryID, Title: selectedItem.Title, Source: "observation", Status: "processing", RequestID: x.Env.RequestID, PlannedAt: plannedAt, CreatedAt: now, UpdatedAt: now, Steps: []protocol.TriggerStep{{Kind: "claimed", At: now, Detail: x.Principal.Subject}}}); err != nil {
		return Outcome{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE observation_jobs SET status='claimed',event_id=?,updated_at=? WHERE issue_id=? AND delivery_id=?`, eventID, now, selectedItem.WorkItemID, selectedDelivery.DeliveryID); err != nil {
		return Outcome{}, err
	}
	result.Assignments = append(result.Assignments, protocol.DeliveryAssignment{TargetLineage: slices.Contains(rt.Capabilities, protocol.CapabilityDeliveryTargetLineage), CandidateIdentity: slices.Contains(rt.Capabilities, protocol.CapabilityDeliveryCandidateIdentity), WorkItemID: selected.Observation.WorkItemID, Delivery: selectedDelivery, Generation: selected.Observation.Generation, Nonce: selected.Nonce})
	event := deliveryChanged(selectedItem, selected.Observation)
	event.Kind = "work_item.delivery_query_started"
	return Outcome{Result: result, Events: append(events, event)}, nil
}
func deliveryCapability(provider protocol.IssueDeliveryProvider) string {
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
	if _, err := settleObservationJobs(ctx, tx, item, item.UpdatedAt, false); err != nil {
		return err
	}
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
		if err := tx.PutDeliveryObservationQuery(ctx, row); err != nil {
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
	{
		saved, accepted, err := retainRetiredDeliveryReport(ctx, x, tx, p)
		if err != nil {
			return Outcome{}, err
		}
		if saved {
			return Outcome{Result: protocol.DeliveryReportResult{Accepted: accepted}}, nil
		}
	}
	if _, err := deliveryRuntime(ctx, x, tx.Q(), p.InstanceID); err != nil {
		return Outcome{}, err
	}
	item, err := store.GetIssue(ctx, tx.Q(), p.WorkItemID)
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
	if err := validateDeliveryReport(p); err != nil {
		return Outcome{}, err
	}
	rt, err := store.GetHostRuntime(ctx, tx.Q(), x.Principal.Subject)
	if err != nil {
		return Outcome{}, err
	}
	if !slices.Contains(rt.Runtime.Capabilities, deliveryCapability(delivery.Provider)) {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "observer capability is not available")
	}
	var triggerID string
	if err := tx.Q().QueryRowContext(ctx, `SELECT event_id FROM observation_jobs WHERE issue_id=? AND delivery_id=?`, p.WorkItemID, p.DeliveryID).Scan(&triggerID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Outcome{}, err
	}
	var handedOff bool
	if row.QueryUntil > 0 {
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM server_delivery_queries WHERE provider=? AND repository=? AND number=? AND started_at>=?)`, delivery.Provider, delivery.Repository, delivery.Number, row.QueryUntil).Scan(&handedOff); err != nil {
			return Outcome{}, err
		}
	}
	if handedOff {
		if err := retireRunnerDeliveryQuery(ctx, x, tx, &row); err != nil {
			return Outcome{}, err
		}
		if _, _, err := retainRetiredDeliveryReport(ctx, x, tx, p); err != nil {
			return Outcome{}, err
		}
		return Outcome{Result: protocol.DeliveryReportResult{}}, nil
	}
	if row.QueryUntil > 0 {
		var superseded bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM server_delivery_references r JOIN delivery_observations o ON o.work_item_id=r.issue_id AND o.delivery_id=r.delivery_id WHERE r.provider=? AND r.repository=? AND r.number=? AND o.nonce<>? AND o.host_id<>'server' AND o.nonce<>'' AND o.query_until>=? AND NOT EXISTS(SELECT 1 FROM retired_runner_delivery_queries retired WHERE retired.nonce=o.nonce))`, delivery.Provider, delivery.Repository, delivery.Number, row.Nonce, row.QueryUntil).Scan(&superseded); err != nil {
			return Outcome{}, err
		}
		if superseded {
			// Equal original deadlines do not prove which legacy read began
			// later. Keep that whole ambiguous group as original history.
			if err := retireSharedRunnerQueries(ctx, x, tx, delivery, "", row.QueryUntil); err != nil {
				return Outcome{}, err
			}
			_, accepted, err := retainRetiredDeliveryReport(ctx, x, tx, p)
			return Outcome{Result: protocol.DeliveryReportResult{Accepted: accepted}}, err
		}
		if err := retireSharedRunnerQueries(ctx, x, tx, delivery, row.Nonce, row.QueryUntil); err != nil {
			return Outcome{}, err
		}
	}
	previous := row.Observation
	manualRefresh := row.RefreshRequested
	row.Observation.AttemptedAt = x.Now.UnixMilli()
	row.Observation.Observer = "runner"
	row.Observation.BaselineUpgradeID = row.QueryBaselineUpgradeID
	row.Observation.BaselineUpgradeRevision = row.QueryBaselineUpgradeRevision
	row.Observation.RetryCount = 0
	row.Observation.RetryAt = 0
	row.Observation.ErrorMessage = ""
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
	fact, err := tx.PutDeliveryObservationFact(ctx, delivery, row.Observation)
	if err != nil {
		return Outcome{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE server_delivery_jobs SET observed_refresh_revision=MAX(observed_refresh_revision,?) WHERE provider=? AND repository=? AND number=?`, row.ServerRefreshRevision, delivery.Provider, delivery.Repository, delivery.Number); err != nil {
		return Outcome{}, err
	}
	changed := row.ObservedRevision < fact.ChangedRevision
	if err := tx.PutDeliveryObservationQuery(ctx, row); err != nil {
		return Outcome{}, err
	}
	// Clearing this original query may still leave a refresh requested after
	// it began. Its suppressed transition must remain available to all members.
	fact, err = store.GetDeliveryObservationFact(ctx, tx.Q(), delivery)
	if err != nil {
		return Outcome{}, err
	}
	row.Observation.RefreshPending = fact.Observation.RefreshPending
	if !row.Observation.RefreshPending {
		row.ObservedRevision = fact.FactRevision
		row.NotificationPending = false
		if err := tx.PutDeliveryObservationQuery(ctx, row); err != nil {
			return Outcome{}, err
		}
	} else if manualRefresh && changed {
		// Retain this explicit recipient even when its Issue is already
		// closed. This is notification responsibility, not another read.
		row.NotificationPending = true
		if err := tx.PutDeliveryObservationQuery(ctx, row); err != nil {
			return Outcome{}, err
		}
	}
	status, reason := "completed", "observation changed"
	if p.ErrorKind != "" {
		status, reason = "failed", p.ErrorKind
	} else if reflect.DeepEqual(previous.DeliverySnapshot, row.Observation.DeliverySnapshot) {
		status, reason = "not_dispatched", "observation unchanged"
	}
	if err := tx.FinishTrigger(ctx, triggerID, status, reason, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE observation_jobs SET status='finished',updated_at=? WHERE issue_id=? AND delivery_id=?`, x.Now.UnixMilli(), p.WorkItemID, p.DeliveryID); err != nil {
		return Outcome{}, err
	}
	events := []Event{}
	if !row.Observation.RefreshPending && (changed || manualRefresh) {
		event := deliveryChanged(item, row.Observation)
		event.Body["previous_state"] = fact.PreviousState
		event.Body["previous_error_kind"] = fact.PreviousErrorKind
		if !changed {
			event.Body["previous_state"] = previous.State
			event.Body["previous_error_kind"] = previous.ErrorKind
		}
		events = append(events, event)
	} else {
		// An unchanged read still advances the displayed check/attempt time.
		// Invalidate live views without creating a notice or agent turn.
		event := deliveryChanged(item, row.Observation)
		event.Kind = "work_item.delivery_checked"
		events = append(events, event)
	}
	sharedEvents, _, err := fanoutDeliveryFacts(ctx, x, tx, &delivery, 99, false)
	if err != nil {
		return Outcome{}, err
	}
	events = append(events, sharedEvents...)
	return Outcome{Result: protocol.DeliveryReportResult{Accepted: true}, Events: events}, nil
}

func validateDeliveryReport(p protocol.DeliveryReportPayload) error {
	if (p.Snapshot == nil) == (p.ErrorKind == "") {
		return protocol.Errorf(protocol.CodeInvalidRequest, "provide either provider facts or an observation error")
	}
	if p.Snapshot != nil && !oneOf(p.Snapshot.State, "open", "merged", "closed") {
		return protocol.Errorf(protocol.CodeInvalidRequest, "unknown delivery state")
	}
	if p.ErrorKind != "" && !slices.Contains([]string{"invalid_reference", "unsupported_provider", "cli_unavailable", "query_failed", "invalid_response", "not_found", "authentication_required", "unknown_state", "timeout"}, p.ErrorKind) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "unknown observation error category")
	}
	return nil
}

// The old Issue-local query identities stay valid reports. A later shared read
// only retires their authority to replace the current fact. An undated legacy
// query is not ordered by guessing its start time.
func retireSharedRunnerQueries(ctx context.Context, x *Exec, tx *store.Tx, ref protocol.IssueDelivery, except string, until int64) error {
	rows, err := tx.Q().QueryContext(ctx, `SELECT r.issue_id,r.delivery_id FROM server_delivery_references r JOIN delivery_observations o ON o.work_item_id=r.issue_id AND o.delivery_id=r.delivery_id WHERE r.provider=? AND r.repository=? AND r.number=? AND o.host_id<>'server' AND o.nonce<>'' AND o.nonce<>? AND o.query_until>0 AND o.query_until<=? AND NOT EXISTS(SELECT 1 FROM retired_runner_delivery_queries retired WHERE retired.nonce=o.nonce)`, ref.Provider, ref.Repository, ref.Number, except, until)
	if err != nil {
		return err
	}
	type member struct{ issue, delivery string }
	members := []member{}
	for rows.Next() {
		var m member
		if err = rows.Scan(&m.issue, &m.delivery); err != nil {
			break
		}
		members = append(members, m)
	}
	readErr := rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if readErr != nil {
		return readErr
	}
	for _, m := range members {
		row, err := store.GetDeliveryObservation(ctx, tx.Q(), m.issue, m.delivery)
		if err != nil {
			return err
		}
		if err := retireRunnerDeliveryQueryDisposition(ctx, x, tx, &row, true); err != nil {
			return err
		}
	}
	return nil
}

// deliveryNotificationCandidates keeps suppressed recipients outside the batch,
// unless their covered refresh or stale notice flag can make metadata progress.
func deliveryNotificationCandidates(ref *protocol.IssueDelivery, notificationOnly bool) (string, []any) {
	filter := ""
	args := []any{}
	if ref != nil {
		filter = " AND r.provider=? AND r.repository=? AND r.number=?"
		args = append(args, ref.Provider, ref.Repository, ref.Number)
	}
	if notificationOnly {
		filter += " AND COALESCE(json_extract(o.query_json,'$.notification_pending'),0)=1"
	}
	return `WITH fanout AS (
 SELECT r.issue_id,r.delivery_id,
 COALESCE(json_extract(o.query_json,'$.observed_revision'),0) AS observed,
 (o.refresh_requested=1 AND o.server_refresh_revision>0 AND o.server_refresh_revision<=j.observed_refresh_revision) AS covered,
 (COALESCE(json_extract(o.query_json,'$.notification_pending'),0)=1 AND COALESCE(json_extract(o.query_json,'$.observed_revision'),0)>=j.changed_revision) AS settled_notice,
 ` + store.DeliveryObservationPendingSQL + ` AS pending
 FROM server_delivery_references r JOIN server_delivery_jobs j ON j.provider=r.provider AND j.repository=r.repository AND j.number=r.number
 LEFT JOIN delivery_observations o ON o.work_item_id=r.issue_id AND o.delivery_id=r.delivery_id
 WHERE (r.active=1 OR o.refresh_requested=1 OR COALESCE(json_extract(o.query_json,'$.notification_pending'),0)=1)
 AND (COALESCE(json_extract(o.query_json,'$.observed_revision'),0)<j.fact_revision OR
 (COALESCE(json_extract(o.query_json,'$.notification_pending'),0)=1 AND COALESCE(json_extract(o.query_json,'$.observed_revision'),0)>=j.changed_revision))` + filter + `)`, args
}

const deliveryNotificationCanProgressSQL = `NOT pending OR covered OR settled_notice`

// A terminal shared fact stops provider polling, but every active or explicitly
// deferred reference still consumes its revision in bounded notification batches.
// notificationOnly preserves the server observer's existing cached-query audit.
func fanoutDeliveryFacts(ctx context.Context, x *Exec, tx *store.Tx, ref *protocol.IssueDelivery, limit int, notificationOnly bool) ([]Event, bool, error) {
	query, args := deliveryNotificationCandidates(ref, notificationOnly)
	args = append(args, limit)
	rows, err := tx.Q().QueryContext(ctx, query+`
 SELECT issue_id,delivery_id,COALESCE(covered,0) FROM fanout WHERE `+deliveryNotificationCanProgressSQL+`
 ORDER BY pending,observed,issue_id,delivery_id LIMIT ?`, args...)
	if err != nil {
		return nil, false, err
	}
	type member struct {
		issue, delivery string
		covered         bool
	}
	members := []member{}
	for rows.Next() {
		var m member
		if err = rows.Scan(&m.issue, &m.delivery, &m.covered); err != nil {
			break
		}
		members = append(members, m)
	}
	readErr := rows.Err()
	rows.Close()
	if err != nil {
		return nil, false, err
	}
	if readErr != nil {
		return nil, false, readErr
	}
	events := []Event{}
	progressed := false
	for _, m := range members {
		item, err := store.GetIssue(ctx, tx.Q(), m.issue)
		if err != nil {
			return nil, false, err
		}
		delivery, ok := findDelivery(item, m.delivery)
		if !ok {
			continue
		}
		row, err := store.GetDeliveryObservation(ctx, tx.Q(), m.issue, m.delivery)
		if err != nil {
			return nil, false, err
		}
		fact, err := store.GetDeliveryObservationFact(ctx, tx.Q(), delivery)
		if err != nil {
			return nil, false, err
		}
		dirty := false
		changed := row.ObservedRevision < fact.ChangedRevision
		if row.NotificationPending && !changed {
			row.NotificationPending = false
			dirty = true
		}
		if m.covered {
			// Selection proved that this actual result covers the member's
			// request. Settle its queued responsibility without claiming a read.
			if changed && fact.Observation.RefreshPending && item.Status == "closed" {
				row.NotificationPending = true
			}
			row.RefreshRequested = false
			dirty = true
			if _, err := tx.Exec(ctx, `UPDATE observation_jobs SET status='finished',updated_at=? WHERE issue_id=? AND delivery_id=? AND status='pending'`, x.Now.UnixMilli(), m.issue, m.delivery); err != nil {
				return nil, false, err
			}
		}
		if fact.Observation.RefreshPending || row.ObservedRevision >= fact.FactRevision {
			if dirty {
				if err := tx.PutDeliveryObservationQuery(ctx, row); err != nil {
					return nil, false, err
				}
				progressed = true
			}
			continue
		}
		row.ObservedRevision = fact.FactRevision
		row.NotificationPending = false
		if row.Nonce == "" && !row.RefreshRequested {
			interval := 5 * time.Minute
			if item.Status == "snoozed" {
				interval = time.Hour
			}
			row.NextCheckAt = max(row.NextCheckAt, fact.Observation.AttemptedAt+interval.Milliseconds())
		}
		if err := tx.PutDeliveryObservationQuery(ctx, row); err != nil {
			return nil, false, err
		}
		progressed = true
		event := deliveryChanged(item, row.Observation)
		event.Body["previous_state"] = fact.PreviousState
		event.Body["previous_error_kind"] = fact.PreviousErrorKind
		if !changed {
			event.Kind = "work_item.delivery_checked"
		}
		events = append(events, event)
	}
	return events, progressed, nil
}
func findDelivery(item protocol.WorkItem, id string) (protocol.IssueDelivery, bool) {
	for _, d := range item.Deliveries {
		if d.DeliveryID == id {
			return d, true
		}
	}
	return protocol.IssueDelivery{}, false
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
	item, err := store.GetIssue(ctx, tx.Q(), x.Env.Target.ID)
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
	if err := queueDeliveryObservationRefresh(ctx, x, tx, &row); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: row.Observation, Events: []Event{deliveryChanged(item, row.Observation)}}, nil
}

// Queue the member and its shared provider job together. A new refresh revision
// prevents an earlier read or shared cache from settling a later request.
func queueDeliveryObservationRefresh(ctx context.Context, x *Exec, tx *store.Tx, row *store.DeliveryObservationRow) error {
	if x.serverDeliveryObservations {
		if err := retireRunnerDeliveryQuery(ctx, x, tx, row); err != nil {
			return err
		}
	}
	// Capture ordering independently of the wall clock: a refresh queued
	// after an external read began must not consume that earlier result.
	if err := tx.Q().QueryRowContext(ctx, `UPDATE server_delivery_jobs SET next_at=?,attempts=0,refresh_revision=refresh_revision+1 WHERE (provider,repository,number) IN (SELECT provider,repository,number FROM server_delivery_references WHERE issue_id=? AND delivery_id=?) RETURNING refresh_revision`, x.Now.UnixMilli(), row.Observation.WorkItemID, row.Observation.DeliveryID).Scan(&row.ServerRefreshRevision); err != nil {
		return err
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
	return tx.PutDeliveryObservationQuery(ctx, *row)
}

// fenceServerDeliveryQuery retires only an expired original server read. A late
// report retains its query identity in history and cannot replace runner facts.
func fenceServerDeliveryQuery(ctx context.Context, tx *store.Tx, ref protocol.IssueDelivery, now int64) error {
	if _, err := tx.Exec(ctx, `UPDATE server_delivery_queries SET status='retired',finished_at=? WHERE status='reading' AND query_id=(SELECT nonce FROM server_delivery_jobs WHERE provider=? AND repository=? AND number=? AND query_until<=?)`, now, ref.Provider, ref.Repository, ref.Number, now); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE server_delivery_jobs SET nonce='',query_until=0 WHERE provider=? AND repository=? AND number=? AND query_until<=?`, ref.Provider, ref.Repository, ref.Number, now)
	return err
}
