package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

// UpgradeRow keeps transition inputs and receipts alongside the public view.
// Restoration receipts use phase rolled_back or cancelled (replacement never started).
type UpgradeRow struct {
	View                protocol.UpgradeView     `json:"view"`
	Manifest            release.Manifest         `json:"manifest"`
	ManifestSHA256      string                   `json:"manifest_sha256"`
	ManifestSize        int64                    `json:"manifest_size"`
	ServerPreviousHash  string                   `json:"server_previous_hash"`
	ServerRollback      *protocol.UpdaterReceipt `json:"server_rollback,omitempty"`
	ServerHash          string                   `json:"server_hash"`
	ServerInstance      string                   `json:"server_instance"`
	ServerProcess       string                   `json:"server_process"`
	ServerGrantDeadline int64                    `json:"server_grant_deadline"`
	ServerHealthy       int64                    `json:"server_healthy"`
	ServerHealthAt      int64                    `json:"server_health_at"`
	Deadline            int64                    `json:"deadline"`
	DrainMillis         int64                    `json:"drain_ms"`
	RestartMillis       int64                    `json:"restart_ms"`
	GrantMillis         int64                    `json:"grant_ms"`
	Hosts               []UpgradeHost            `json:"hosts"`
}
type UpgradeHost struct {
	Configuration     *protocol.RunnerConfiguration `json:"configuration,omitempty"`
	Committed         *protocol.UpdaterReceipt      `json:"committed,omitempty"`
	HealthInstance    string                        `json:"health_instance,omitempty"`
	Rollback          *protocol.UpdaterReceipt      `json:"rollback,omitempty"`
	HostID            string                        `json:"host_id"`
	PreviousHash      string                        `json:"previous_hash"`
	TargetHash        string                        `json:"target_hash"`
	CandidateInstance string                        `json:"candidate_instance"`
	Healthy           int64                         `json:"healthy"`
	LastSequence      int64                         `json:"last_sequence"`
	LastHealthyAt     int64                         `json:"last_healthy_at"`
	GrantDeadline     int64                         `json:"grant_deadline"`
}

func GetUpgrade(ctx context.Context, q Querier, id string) (UpgradeRow, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT state_json FROM upgrades WHERE upgrade_id=?`, id).Scan(&raw)
	if err != nil {
		return UpgradeRow{}, notFound(err, "upgrade "+id)
	}
	var r UpgradeRow
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return r, fmt.Errorf("store: decode upgrade: %w", err)
	}
	return r, nil
}
func LatestUpgrade(ctx context.Context, q Querier) (UpgradeRow, error) {
	var id string
	err := q.QueryRowContext(ctx, `SELECT upgrade_id FROM upgrades ORDER BY created_at DESC,rowid DESC LIMIT 1`).Scan(&id)
	if err != nil {
		return UpgradeRow{}, notFound(err, "latest upgrade")
	}
	return GetUpgrade(ctx, q, id)
}
func (t *Tx) PutUpgrade(ctx context.Context, r UpgradeRow) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("store: encode upgrade: %w", err)
	}
	_, err = t.Exec(ctx, `INSERT INTO upgrades(upgrade_id,release_id,revision,status,phase,state_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(upgrade_id) DO UPDATE SET revision=excluded.revision,status=excluded.status,phase=excluded.phase,state_json=excluded.state_json,updated_at=excluded.updated_at`, r.View.UpgradeID, r.View.ReleaseID, r.View.Revision, r.View.Status, r.View.Phase, string(raw), r.View.CreatedAt.UnixMilli(), r.View.UpdatedAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("store: put upgrade: %w", err)
	}
	return nil
}

// ListCancelledUpgradeIDs supplies durable cancellation intents even when a
// newer batch has become latest before the local supervisor receives a POST.
func ListCancelledUpgradeIDs(ctx context.Context, q Querier) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT upgrade_id FROM upgrades WHERE status='cancelled' ORDER BY created_at,upgrade_id`)
	if err != nil {
		return nil, fmt.Errorf("store: list cancelled upgrades: %w", err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: scan cancelled upgrade: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
