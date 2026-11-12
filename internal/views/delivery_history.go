package views

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func deliveryExecutionHistoryIndex(ctx context.Context, q store.Querier, task string) ([]protocol.DeliveryExecutionReference, bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT COALESCE(run_id,''),generation,duty_revision,upgrade_id FROM issue_delivery_execution_history WHERE task_id=? ORDER BY generation DESC,duty_revision DESC LIMIT 21`, task)
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
		item.ExecutionRunID = item.RunID
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

// HistoricalDeliveryExecutionAuthority selects an immutable binding by its
// original Run and duty revision. A Run alone is compatible only when unique.
// It describes retained facts; Valid is always false for execution.
func HistoricalDeliveryExecutionAuthority(ctx context.Context, q store.Querier, run string, dutyRevision int64) (*protocol.DeliveryExecutionAuthority, error) {
	return historicalDeliveryExecutionAuthority(ctx, q, "run_id", run, dutyRevision)
}

// HistoricalTaskDeliveryExecutionAuthority also selects bindings that predate a Run.
func HistoricalTaskDeliveryExecutionAuthority(ctx context.Context, q store.Querier, task string, dutyRevision int64) (*protocol.DeliveryExecutionAuthority, error) {
	if dutyRevision <= 0 {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "archived Task delivery requires its duty_revision")
	}
	return historicalDeliveryExecutionAuthority(ctx, q, "task_id", task, dutyRevision)
}

func historicalDeliveryExecutionAuthority(ctx context.Context, q store.Querier, selector, id string, dutyRevision int64) (*protocol.DeliveryExecutionAuthority, error) {
	if dutyRevision < 0 {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "archived duty_revision cannot be negative")
	}
	v := &protocol.DeliveryExecutionAuthority{Historical: true, Source: "server.issue_delivery_execution_history", InvalidReason: "historical_result_only; no current execution authority"}
	var candidate, raw, artifact string
	rows, err := q.QueryContext(ctx, `SELECT task_id,issue_id,source_task_id,duty_revision,candidate_json,duty_json,COALESCE(artifact_id,''),upgrade_id,COALESCE(run_id,'') FROM issue_delivery_execution_history WHERE `+selector+`=? AND (?=0 OR duty_revision=?) ORDER BY duty_revision DESC LIMIT 2`, id, dutyRevision, dutyRevision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, store.ErrNotFound
	}
	if err := rows.Scan(&v.TaskID, &v.IssueID, &v.SourceTaskID, &v.DutyRevision, &candidate, &raw, &artifact, &v.UpgradeID, &v.RunID); err != nil {
		return nil, err
	}
	if rows.Next() {
		if selector == "run_id" {
			failure := protocol.Errorf(protocol.CodeInvalidRequest, "multiple archived deliveries share this Run; select duty_revision from the Task's history index")
			failure.Details, _ = json.Marshal(map[string]any{"execution_run_id": id, "selector": "duty_revision"})
			return nil, failure
		}
		failure := protocol.Errorf(protocol.CodeInvalidRequest, "multiple archived deliveries share this selector; select the original Run and duty_revision from the Task's history index")
		failure.Details, _ = json.Marshal(map[string]any{"id": id, "selector": "execution_run_id and duty_revision"})
		return nil, failure
	}
	if err := rows.Err(); err != nil {
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
