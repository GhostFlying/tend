package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// The existing report evidence carries a typed reference without changing the
// runner wire shape. Bind it atomically with the artifact before its accept
// action can race the controlled updater's first maintenance tick.
func bindDeliveryUpgradeArtifact(ctx context.Context, tx *store.Tx, task store.TaskRow, artifact protocol.ArtifactPayload) error {
	var raw, bound string
	err := tx.Q().QueryRowContext(ctx, `SELECT candidate_json,upgrade_id FROM issue_delivery_executions WHERE task_id=?`, task.TaskID).Scan(&raw, &bound)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var candidate protocol.IssueDeliveryCandidate
	if err := json.Unmarshal([]byte(raw), &candidate); err != nil {
		return err
	}
	if !candidate.Deploy {
		return nil
	}
	if artifact.Report == nil || candidate.Deployment == nil {
		return protocol.Errorf(protocol.CodeInvalidRequest, "delivery requires its deployment report and approved target")
	}
	id := ""
	references := 0
	for _, evidence := range artifact.Report.Evidence {
		if strings.HasPrefix(evidence, "upgrade:") {
			references++
			if references > 1 {
				return protocol.Errorf(protocol.CodeInvalidRequest, "report must identify exactly one controlled upgrade batch")
			}
			id = strings.TrimPrefix(evidence, "upgrade:")
		}
	}
	if id == "" || bound != "" && bound != id {
		return protocol.Errorf(protocol.CodeInvalidRequest, "report requires its original upgrade:<id> evidence reference")
	}
	if _, err := deliveryUpgradeBinding(ctx, tx.Q(), *candidate.Deployment, id, task.CreatedAt); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE issue_delivery_executions SET upgrade_id=? WHERE task_id=?`, id, task.TaskID)
	return err
}

// A deployed delivery's reviewer must not retain a run on a target while the
// installer is draining it. Terminal failures remain independently reviewable;
// only verified successful installations can later settle the delivery duty.
func deliveryAcceptanceReady(ctx context.Context, q store.Querier, task string) (bool, error) {
	var raw, id string
	err := q.QueryRowContext(ctx, `SELECT candidate_json,upgrade_id FROM issue_delivery_executions WHERE task_id=?`, task).Scan(&raw, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	var candidate protocol.IssueDeliveryCandidate
	if err := json.Unmarshal([]byte(raw), &candidate); err != nil {
		return false, err
	}
	if !candidate.Deploy {
		return true, nil
	}
	if id == "" {
		return false, nil
	}
	batch, err := store.GetUpgrade(ctx, q, id)
	if err != nil {
		return false, err
	}
	return batch.View.Status != "running" && batch.View.Status != "attention", nil
}
