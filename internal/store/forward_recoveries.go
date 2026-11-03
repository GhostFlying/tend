package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

func GetForwardRecovery(ctx context.Context, q Querier, id string) (protocol.ForwardRecoveryLedger, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT state_json FROM server_forward_recoveries WHERE recovery_id=?`, id).Scan(&raw)
	if err != nil {
		return protocol.ForwardRecoveryLedger{}, notFound(err, "forward recovery")
	}
	var r protocol.ForwardRecoveryLedger
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return r, fmt.Errorf("store: decode recovery: %w", err)
	}
	return r, nil
}
func ForwardRecoveryFenced(ctx context.Context, q Querier) (bool, error) {
	var fenced bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM server_forward_recoveries WHERE linked=0)`).Scan(&fenced)
	return fenced, err
}
func (t *Tx) PutForwardRecovery(ctx context.Context, r protocol.ForwardRecoveryLedger, now int64) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = t.Exec(ctx, `INSERT INTO server_forward_recoveries(recovery_id,original_upgrade_id,request_id,state_json,linked,created_at,updated_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(recovery_id) DO UPDATE SET state_json=excluded.state_json,linked=excluded.linked,updated_at=excluded.updated_at`, r.Plan.RecoveryID, r.Plan.Original.UpgradeID, r.Plan.RequestID, string(raw), r.Linked, now, now)
	return err
}
