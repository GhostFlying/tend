package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

func GetCommittedMaintenance(ctx context.Context, q Querier, id string) (protocol.CommittedMaintenanceLedger, error) {
	var raw string
	if err := q.QueryRowContext(ctx, `SELECT state_json FROM server_committed_maintenances WHERE maintenance_id=?`, id).Scan(&raw); err != nil {
		return protocol.CommittedMaintenanceLedger{}, notFound(err, "committed maintenance")
	}
	var row protocol.CommittedMaintenanceLedger
	if err := json.Unmarshal([]byte(raw), &row); err != nil {
		return row, fmt.Errorf("store: decode committed maintenance: %w", err)
	}
	return row, nil
}

func CommittedMaintenanceFenced(ctx context.Context, q Querier) (bool, error) {
	return CommittedMaintenanceFencedExcept(ctx, q, "")
}

// Only an independently registered child can take responsibility for an
// unlinked parent. Its parent stays unlinked and its historical receipt intact.
func CommittedMaintenanceFencedExcept(ctx context.Context, q Querier, parentID string) (bool, error) {
	var fenced bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM server_committed_maintenances p WHERE p.linked=0 AND p.maintenance_id<>? AND NOT EXISTS(SELECT 1 FROM server_committed_maintenances c WHERE c.parent_id=p.maintenance_id))`, parentID).Scan(&fenced)
	return fenced, err
}

func CommittedMaintenanceHasSuccessor(ctx context.Context, q Querier, id string) (bool, error) {
	var exists bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM server_committed_maintenances WHERE parent_id=?)`, id).Scan(&exists)
	return exists, err
}

func (t *Tx) PutCommittedMaintenance(ctx context.Context, row protocol.CommittedMaintenanceLedger, now int64) error {
	raw, err := json.Marshal(row)
	if err != nil {
		return err
	}
	_, err = t.Exec(ctx, `INSERT INTO server_committed_maintenances(maintenance_id,request_id,parent_id,state_json,linked,created_at,updated_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(maintenance_id) DO UPDATE SET state_json=excluded.state_json,linked=excluded.linked,updated_at=excluded.updated_at`, row.Plan.MaintenanceID, row.Plan.RequestID, row.Plan.ParentMaintenanceID, string(raw), row.Linked, now, now)
	return err
}
