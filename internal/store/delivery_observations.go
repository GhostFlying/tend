package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

// DeliveryObservationRow includes private query ownership and scheduling facts.
type DeliveryObservationRow struct {
	Observation               protocol.DeliveryObservation
	Nonce, HostID, InstanceID string
	QueryUntil, NextCheckAt   int64
	RefreshRequested          bool
}

func GetDeliveryObservation(ctx context.Context, q Querier, item, id string) (DeliveryObservationRow, error) {
	var row DeliveryObservationRow
	var raw string
	var generation int64
	err := q.QueryRowContext(ctx, `SELECT generation,nonce,host_id,instance_id,query_until,next_check_at,refresh_requested,observation_json FROM delivery_observations WHERE work_item_id=? AND delivery_id=?`, item, id).Scan(&generation, &row.Nonce, &row.HostID, &row.InstanceID, &row.QueryUntil, &row.NextCheckAt, &row.RefreshRequested, &raw)
	if err != nil {
		return row, notFound(err, "delivery observation")
	}
	if err := json.Unmarshal([]byte(raw), &row.Observation); err != nil {
		return row, fmt.Errorf("store: decode delivery observation: %w", err)
	}
	row.Observation.WorkItemID = item
	row.Observation.DeliveryID = id
	row.Observation.Generation = generation
	row.Observation.RefreshPending = row.RefreshRequested || row.Nonce != ""
	return row, nil
}
func (t *Tx) PutDeliveryObservation(ctx context.Context, row DeliveryObservationRow) error {
	o := row.Observation
	raw, err := json.Marshal(o)
	if err != nil {
		return err
	}
	_, err = t.Exec(ctx, `INSERT INTO delivery_observations(work_item_id,delivery_id,generation,nonce,host_id,instance_id,query_until,next_check_at,refresh_requested,observation_json) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(work_item_id,delivery_id) DO UPDATE SET generation=excluded.generation,nonce=excluded.nonce,host_id=excluded.host_id,instance_id=excluded.instance_id,query_until=excluded.query_until,next_check_at=excluded.next_check_at,refresh_requested=excluded.refresh_requested,observation_json=excluded.observation_json`, o.WorkItemID, o.DeliveryID, o.Generation, row.Nonce, row.HostID, row.InstanceID, row.QueryUntil, row.NextCheckAt, row.RefreshRequested, string(raw))
	if err != nil {
		return fmt.Errorf("store: put delivery observation: %w", err)
	}
	return nil
}

// ListDeliveryObservations never includes removed plan references or private
// query nonces. Plans cannot overwrite independently recorded provider facts.
func ListDeliveryObservations(ctx context.Context, q Querier, item string) ([]protocol.DeliveryObservation, error) {
	work, err := GetIssue(ctx, q, item)
	if err != nil {
		return nil, err
	}
	out := []protocol.DeliveryObservation{}
	for _, delivery := range work.Deliveries {
		row, err := GetDeliveryObservation(ctx, q, item, delivery.DeliveryID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		out = append(out, row.Observation)
	}
	return out, nil
}
