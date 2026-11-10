package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// A completed whole-PR execution can discharge earlier accepted contributions.
// This records historical evidence; it does not bind or authorize execution.
func contributionDeliverySettlementEvidence(ctx context.Context, q store.Querier, issueID string, contribution protocol.IssueDeliveryDuty, p protocol.UpdateIssueDeliveryPayload) ([]protocol.IssueEvidence, error) {
	if contribution.Status != "preparing" || contribution.Candidate != nil || contribution.ExecutionTaskID != "" || p.ExecutionTaskID == contribution.TaskID {
		return nil, protocol.Errorf(protocol.CodeInvalidTransition, "only an unbound preparing contribution may reference an already completed delivery")
	}
	for _, taskID := range []string{contribution.TaskID, p.ExecutionTaskID} {
		owner, err := store.GetTaskIssueOwner(ctx, q, taskID)
		if err != nil {
			return nil, err
		}
		if owner != issueID {
			return nil, protocol.Errorf(protocol.CodeInvalidTransition, "contribution and completed execution must belong to this Issue")
		}
	}
	basis, err := completedTaskEvidence(ctx, q, contribution.TaskID, p.Reason)
	if err != nil {
		return nil, err
	}
	executionBasis, err := completedTaskEvidence(ctx, q, p.ExecutionTaskID, "Original completed delivery execution")
	if err != nil {
		return nil, err
	}
	var sourceTaskID, upgradeID, candidateJSON string
	if err := q.QueryRowContext(ctx, `SELECT source_task_id,upgrade_id,candidate_json FROM issue_delivery_executions WHERE task_id=? AND issue_id=?`, p.ExecutionTaskID, issueID).Scan(&sourceTaskID, &upgradeID, &candidateJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, protocol.Errorf(protocol.CodeInvalidTransition, "original delivery execution does not exist")
		}
		return nil, err
	}
	settled, err := store.GetIssueDeliveryDuty(ctx, q, issueID, sourceTaskID)
	if err != nil {
		return nil, err
	}
	var frozen deliveryExecutionSnapshot
	if err := json.Unmarshal([]byte(candidateJSON), &frozen); err != nil {
		return nil, err
	}
	if settled.Status != "complete" || settled.ExecutionTaskID != p.ExecutionTaskID || settled.Candidate == nil || !reflect.DeepEqual(*settled.Candidate, frozen.IssueDeliveryCandidate) || len(settled.Evidence) == 0 {
		return nil, protocol.Errorf(protocol.CodeInvalidTransition, "the original delivery execution must already have complete frozen settlement evidence")
	}
	var linked bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM shared_delivery_review_links contribution
 JOIN shared_delivery_review_links execution ON execution.review_id=contribution.review_id AND execution.issue_id=contribution.issue_id
 WHERE contribution.issue_id=? AND contribution.task_id=? AND execution.task_id=? AND execution.delivery_id=?)`, issueID, contribution.TaskID, p.ExecutionTaskID, settled.Candidate.DeliveryID).Scan(&linked); err != nil {
		return nil, err
	}
	if !linked {
		return nil, protocol.Errorf(protocol.CodeInvalidTransition, "an explicit shared PR contribution link to the completed execution is required")
	}
	if p.UpgradeID != "" && p.UpgradeID != upgradeID {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "upgrade_id must identify the original completed execution's upgrade")
	}
	// Keep the original provider/installation evidence even after later main,
	// review or maintenance changes. Never copy the execution's authorization.
	evidence := []protocol.IssueEvidence{basis, executionBasis}
	return append(evidence, settled.Evidence...), nil
}
