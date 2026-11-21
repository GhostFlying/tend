package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/GhostFlying/tend/internal/blobstore"
	"github.com/GhostFlying/tend/internal/protocol"
)

// RollingPendingOperations inspects one snapshot of the predecessor's live
// journal. A nil slice means the pending set could not be classified.
func RollingPendingOperations(ctx context.Context, path, journal, upgrade string, revision, count int64) ([]protocol.RollingPendingOperation, error) {
	if count <= 0 || count > protocol.MaxRollingPendingProof {
		return nil, nil
	}
	db, err := sql.Open("sqlite", sqliteFileURI(path, url.Values{"mode": {"ro"}}))
	if err != nil {
		return nil, fmt.Errorf("journal: open predecessor read-only: %w", err)
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT q.operation, q.request_id, r.run_id, r.lease_id,
		CASE WHEN q.operation=? THEN q.payload_json ELSE '' END
		FROM requests q JOIN runs r USING(run_id)
		WHERE json_extract(r.assignment_json,'$.role')=?
		AND json_extract(q.target_json,'$.type')='run' AND json_extract(q.target_json,'$.id')=r.run_id
		AND ((q.operation=? AND r.stopped_confirmed=1)
			OR (?<>'' AND q.operation=? AND r.stopped_confirmed=0 AND r.phase=? AND r.held=0 AND r.fenced=0
				AND COALESCE(json_extract(r.assignment_json,'$.workspace.id'),'')=''
				AND NOT EXISTS (SELECT 1 FROM manager_native_operations n WHERE n.run_id=r.run_id AND n.status IN ('received','started'))))
		AND (SELECT COUNT(*) FROM requests)=?
		AND EXISTS(SELECT 1 FROM runner_control WHERE singleton=1
			AND json_extract(control_json,'$.mode')=?
			AND json_extract(control_json,'$.revision')=?
			AND json_extract(control_json,'$.upgrade_id')=?)
		AND NOT EXISTS(SELECT 1 FROM events)
		AND NOT EXISTS(SELECT 1 FROM workspace_releases)
		AND NOT EXISTS(SELECT 1 FROM run_message_deliveries WHERE settled=0)`,
		protocol.OpManagerNativeReply, protocol.RoleIssueManager, protocol.OpManagerNativeReply, journal, protocol.OpManagerNativeClaim, PhaseStarted,
		count, protocol.RunnerMaintenance, revision, upgrade)
	if err != nil {
		return nil, fmt.Errorf("journal: inspect predecessor pending operations: %w", err)
	}
	defer rows.Close()
	operations := make([]protocol.RollingPendingOperation, 0, count)
	for rows.Next() {
		var op protocol.RollingPendingOperation
		var payload string
		if err := rows.Scan(&op.Operation, &op.RequestID, &op.RunID, &op.LeaseID, &payload); err != nil {
			return nil, fmt.Errorf("journal: read predecessor operation identity: %w", err)
		}
		if op.Operation == protocol.OpManagerNativeReply {
			body, err := readSpoolBody(ctx, payload, func() (*blobstore.Store, error) { return blobstore.OpenStore(path + ".spool") })
			if err != nil {
				return nil, err
			}
			var p struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal([]byte(body), &p); err != nil {
				return nil, fmt.Errorf("journal: decode predecessor reply identity: %w", err)
			}
			op.CommandID = p.ID
		}
		operations = append(operations, op)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("journal: read predecessor pending operations: %w", err)
	}
	if int64(len(operations)) != count {
		return nil, nil
	}
	return operations, nil
}
