package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/GhostFlying/tend/internal/protocol"
)

// DeliveryObservationRow combines current PR facts with this Issue's private
// query identity. Only the explicit query fields are persisted per Issue.
type DeliveryObservationRow struct {
	Observation                   protocol.DeliveryObservation
	Nonce, HostID, InstanceID     string
	QueryUntil, NextCheckAt       int64
	RefreshRequested              bool
	ServerRefreshRevision         int64
	QueryBaselineUpgradeID        string
	QueryBaselineUpgradeRevision  int64
	ObservedRevision              int64
	FactRevision, ChangedRevision int64
}

// DeliveryObservationFactRow is the single current provider result for a PR.
// Revisions order observations independently of wall-clock resolution.
type DeliveryObservationFactRow struct {
	Observation                   protocol.DeliveryObservation
	FactRevision, ChangedRevision int64
	PreviousState                 string
	PreviousErrorKind             string
}

// The transition belongs to ChangedRevision and survives later checked-only
// observations so every bounded notification batch sees the same predecessor.
type deliveryObservationFactJSON struct {
	protocol.DeliveryObservation
	PreviousState     string `json:"previous_state,omitempty"`
	PreviousErrorKind string `json:"previous_error_kind,omitempty"`
}

type deliveryObservationQuery struct {
	BaselineUpgradeID       string `json:"baseline_upgrade_id,omitempty"`
	BaselineUpgradeRevision int64  `json:"baseline_upgrade_revision,omitempty"`
	ObservedRevision        int64  `json:"observed_revision,omitempty"`
}

func deliveryObservationReference(ctx context.Context, q Querier, issue, id string) (protocol.IssueDelivery, error) {
	ref := protocol.IssueDelivery{DeliveryID: id}
	if err := q.QueryRowContext(ctx, `SELECT provider,repository,number FROM server_delivery_references WHERE issue_id=? AND delivery_id=?`, issue, id).Scan(&ref.Provider, &ref.Repository, &ref.Number); err != nil {
		return ref, notFound(err, "delivery reference")
	}
	return ref, nil
}

// GetDeliveryObservation projects one PR fact through its current Issue
// reference. A reference needs no local query row to see the current result.
func GetDeliveryObservation(ctx context.Context, q Querier, issue, id string) (DeliveryObservationRow, error) {
	var row DeliveryObservationRow
	ref, err := deliveryObservationReference(ctx, q, issue, id)
	if err != nil {
		return row, err
	}
	var raw string
	var generation int64
	err = q.QueryRowContext(ctx, `SELECT generation,nonce,host_id,instance_id,query_until,next_check_at,refresh_requested,server_refresh_revision,query_json FROM delivery_observations WHERE work_item_id=? AND delivery_id=?`, issue, id).Scan(&generation, &row.Nonce, &row.HostID, &row.InstanceID, &row.QueryUntil, &row.NextCheckAt, &row.RefreshRequested, &row.ServerRefreshRevision, &raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return row, fmt.Errorf("store: read delivery query: %w", err)
	}
	if err == nil {
		var query deliveryObservationQuery
		if err := json.Unmarshal([]byte(raw), &query); err != nil {
			return row, fmt.Errorf("store: decode delivery query: %w", err)
		}
		row.QueryBaselineUpgradeID, row.QueryBaselineUpgradeRevision, row.ObservedRevision = query.BaselineUpgradeID, query.BaselineUpgradeRevision, query.ObservedRevision
	}
	fact, err := GetDeliveryObservationFact(ctx, q, ref)
	if err != nil {
		return row, err
	}
	row.Observation = fact.Observation
	row.FactRevision, row.ChangedRevision = fact.FactRevision, fact.ChangedRevision
	row.Observation.WorkItemID, row.Observation.DeliveryID, row.Observation.Generation = issue, id, generation
	return row, nil
}

// GetDeliveryObservationFact never falls back to a historical Issue snapshot.
func GetDeliveryObservationFact(ctx context.Context, q Querier, ref protocol.IssueDelivery) (DeliveryObservationFactRow, error) {
	var fact DeliveryObservationFactRow
	var raw, nonce string
	var refreshPending, runnerPending bool
	err := q.QueryRowContext(ctx, `SELECT j.observation_json,j.fact_revision,j.changed_revision,j.nonce,
 EXISTS(SELECT 1 FROM server_delivery_references r JOIN delivery_observations o ON o.work_item_id=r.issue_id AND o.delivery_id=r.delivery_id
 WHERE r.provider=j.provider AND r.repository=j.repository AND r.number=j.number AND o.refresh_requested=1
 AND (o.server_refresh_revision=0 OR o.server_refresh_revision>j.observed_refresh_revision)),
 EXISTS(SELECT 1 FROM server_delivery_references r JOIN delivery_observations o ON o.work_item_id=r.issue_id AND o.delivery_id=r.delivery_id
 WHERE r.provider=j.provider AND r.repository=j.repository AND r.number=j.number AND o.host_id<>'server' AND o.nonce<>''
 AND NOT EXISTS(SELECT 1 FROM retired_runner_delivery_queries retired WHERE retired.nonce=o.nonce)
 AND NOT(o.query_until>0 AND EXISTS(SELECT 1 FROM server_delivery_queries handed
 WHERE handed.provider=j.provider AND handed.repository=j.repository AND handed.number=j.number AND handed.started_at>=o.query_until)))
 FROM server_delivery_jobs j WHERE j.provider=? AND j.repository=? AND j.number=?`, ref.Provider, ref.Repository, ref.Number).Scan(&raw, &fact.FactRevision, &fact.ChangedRevision, &nonce, &refreshPending, &runnerPending)
	if errors.Is(err, sql.ErrNoRows) {
		fact.Observation.State = "unknown"
		return fact, nil
	}
	if err != nil {
		return fact, fmt.Errorf("store: read delivery fact: %w", err)
	}
	var stored deliveryObservationFactJSON
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return fact, fmt.Errorf("store: decode delivery fact: %w", err)
	}
	fact.Observation = stored.DeliveryObservation
	fact.PreviousState, fact.PreviousErrorKind = stored.PreviousState, stored.PreviousErrorKind
	fact.Observation.WorkItemID, fact.Observation.DeliveryID, fact.Observation.Generation = "", "", 0
	fact.Observation.RefreshPending = nonce != "" || refreshPending || runnerPending
	if fact.Observation.State == "" {
		fact.Observation.State = "unknown"
	}
	return fact, nil
}

// PutDeliveryObservationQuery cannot update provider facts or another Issue's
// notification cursor. Its baseline belongs to the original query, not the
// current fact projected into Observation.
func (t *Tx) PutDeliveryObservationQuery(ctx context.Context, row DeliveryObservationRow) error {
	query := deliveryObservationQuery{BaselineUpgradeID: row.QueryBaselineUpgradeID, BaselineUpgradeRevision: row.QueryBaselineUpgradeRevision, ObservedRevision: row.ObservedRevision}
	raw, err := json.Marshal(query)
	if err != nil {
		return err
	}
	o := row.Observation
	_, err = t.Exec(ctx, `INSERT INTO delivery_observations(work_item_id,delivery_id,generation,nonce,host_id,instance_id,query_until,next_check_at,refresh_requested,server_refresh_revision,query_json) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(work_item_id,delivery_id) DO UPDATE SET generation=excluded.generation,nonce=excluded.nonce,host_id=excluded.host_id,instance_id=excluded.instance_id,query_until=excluded.query_until,next_check_at=excluded.next_check_at,refresh_requested=excluded.refresh_requested,server_refresh_revision=excluded.server_refresh_revision,query_json=excluded.query_json`, o.WorkItemID, o.DeliveryID, o.Generation, row.Nonce, row.HostID, row.InstanceID, row.QueryUntil, row.NextCheckAt, row.RefreshRequested, row.ServerRefreshRevision, string(raw))
	if err != nil {
		return fmt.Errorf("store: put delivery query: %w", err)
	}
	return nil
}

// PutDeliveryObservationFact records one admitted provider result. Cached
// notification fanout must only advance query cursors, not call this method.
func (t *Tx) PutDeliveryObservationFact(ctx context.Context, ref protocol.IssueDelivery, observation protocol.DeliveryObservation) (DeliveryObservationFactRow, error) {
	prior, err := GetDeliveryObservationFact(ctx, t.Q(), ref)
	if err != nil {
		return DeliveryObservationFactRow{}, err
	}
	observation.WorkItemID, observation.DeliveryID, observation.Generation = "", "", 0
	observation.RefreshPending = false
	if observation.State == "" {
		observation.State = "unknown"
	}
	fact := DeliveryObservationFactRow{Observation: observation, FactRevision: prior.FactRevision + 1, ChangedRevision: prior.ChangedRevision, PreviousState: prior.PreviousState, PreviousErrorKind: prior.PreviousErrorKind}
	if !reflect.DeepEqual(prior.Observation.DeliverySnapshot, observation.DeliverySnapshot) || prior.Observation.ErrorKind != observation.ErrorKind || prior.Observation.RetryAt > 0 && observation.RetryAt == 0 {
		fact.ChangedRevision = fact.FactRevision
		fact.PreviousState, fact.PreviousErrorKind = prior.Observation.State, prior.Observation.ErrorKind
	}
	raw, err := json.Marshal(deliveryObservationFactJSON{DeliveryObservation: observation, PreviousState: fact.PreviousState, PreviousErrorKind: fact.PreviousErrorKind})
	if err != nil {
		return fact, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fact, err
	}
	for _, field := range []string{"work_item_id", "delivery_id", "generation", "refresh_pending"} {
		delete(fields, field)
	}
	raw, err = json.Marshal(fields)
	if err != nil {
		return fact, err
	}
	_, err = t.Exec(ctx, `INSERT INTO server_delivery_jobs(provider,repository,number,observation_json,fact_revision,changed_revision) VALUES(?,?,?,?,?,?) ON CONFLICT(provider,repository,number) DO UPDATE SET observation_json=excluded.observation_json,fact_revision=excluded.fact_revision,changed_revision=excluded.changed_revision`, ref.Provider, ref.Repository, ref.Number, string(raw), fact.FactRevision, fact.ChangedRevision)
	if err != nil {
		return fact, fmt.Errorf("store: put delivery fact: %w", err)
	}
	return fact, nil
}

// ListDeliveryObservations projects only currently registered references.
func ListDeliveryObservations(ctx context.Context, q Querier, item string) ([]protocol.DeliveryObservation, error) {
	work, err := GetIssue(ctx, q, item)
	if err != nil {
		return nil, err
	}
	out := []protocol.DeliveryObservation{}
	for _, delivery := range work.Deliveries {
		row, err := GetDeliveryObservation(ctx, q, item, delivery.DeliveryID)
		if err != nil {
			return nil, err
		}
		out = append(out, row.Observation)
	}
	return out, nil
}
