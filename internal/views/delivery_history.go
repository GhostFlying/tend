package views

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func deliveryExecutionHistoryIndex(ctx context.Context, q store.Querier, task string) ([]protocol.DeliveryExecutionReference, bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT run_id,generation,duty_revision,upgrade_id FROM issue_delivery_execution_history WHERE task_id=? ORDER BY generation DESC,duty_revision DESC LIMIT 21`, task)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	items := []protocol.DeliveryExecutionReference{}
	for rows.Next() {
		var item protocol.DeliveryExecutionReference
		if err := rows.Scan(&item.RunID, &item.Generation, &item.DutyRevision, &item.UpgradeID); err != nil {
			return nil, false, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(items) > 20
	if more {
		items = items[:20]
	}
	return items, more, nil
}

// HistoricalDeliveryExecutionAuthority is selected by its actual original Run.
// It describes an already settled result; Valid is always false for execution.
func HistoricalDeliveryExecutionAuthority(ctx context.Context, q store.Querier, run string) (*protocol.DeliveryExecutionAuthority, error) {
	v := &protocol.DeliveryExecutionAuthority{Historical: true, Source: "server.issue_delivery_execution_history", RunID: run, InvalidReason: "historical_result_only; no current execution authority"}
	var candidate, raw, artifact string
	if err := q.QueryRowContext(ctx, `SELECT task_id,issue_id,source_task_id,duty_revision,candidate_json,duty_json,artifact_id,upgrade_id FROM issue_delivery_execution_history WHERE run_id=? ORDER BY created_at DESC,duty_revision DESC LIMIT 1`, run).Scan(&v.TaskID, &v.IssueID, &v.SourceTaskID, &v.DutyRevision, &candidate, &raw, &artifact, &v.UpgradeID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	var frozen struct {
		protocol.IssueDeliveryCandidate
		AssignmentEpoch int64                        `json:"assignment_epoch"`
		Authorization   *protocol.IssueDeliveryGrant `json:"authorization,omitempty"`
	}
	if err := json.Unmarshal([]byte(candidate), &frozen); err != nil {
		return nil, err
	}
	var duty protocol.IssueDeliveryDuty
	if err := json.Unmarshal([]byte(raw), &duty); err != nil {
		return nil, err
	}
	v.Candidate, v.Grant, v.AssignmentEpoch, v.DutyStatus = frozen.IssueDeliveryCandidate, frozen.Authorization, frozen.AssignmentEpoch, duty.Status
	v.Settlement = &protocol.DeliverySettlementAuthority{Eligible: duty.Status == "complete", ArtifactID: artifact, AcceptanceID: duty.AcceptanceID, UpgradeID: v.UpgradeID, DutyRevision: v.DutyRevision, CurrentDutyRevision: duty.Revision}
	for _, e := range duty.Evidence {
		if e.Upgrade != nil {
			v.Settlement.Upgrade = e.Upgrade
			v.Settlement.TargetSHA = e.Upgrade.SourceCommit
		}
	}
	return v, nil
}
