package app

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func prepareLiveDeliveryDuty(ctx context.Context, tx *store.Tx, issue protocol.Issue, p protocol.UpdateIssueDeliveryPayload) (protocol.IssueDeliveryDuty, error) {
	var d protocol.IssueDeliveryDuty
	if p.Action != "prepare" || p.Candidate == nil {
		return d, protocol.Errorf(protocol.CodeInvalidTransition, "present the live Task's real delivery candidate before binding execution")
	}
	if _, err := currentLiveDeliveryTask(ctx, tx.Q(), issue.IssueID, p.TaskID, p.ExpectedTaskControlRevision); err != nil {
		return d, err
	}
	if err := normalizeDeliveryBaseline(ctx, tx.Q(), p.Candidate); err != nil {
		return d, err
	}
	if err := validateDeliveryCandidate(ctx, tx.Q(), issue, *p.Candidate); err != nil {
		return d, err
	}
	var event string
	if err := tx.Q().QueryRowContext(ctx, `SELECT event_id FROM events WHERE object_type='task' AND object_id=? AND kind='task.created' ORDER BY event_seq LIMIT 1`, p.TaskID).Scan(&event); err != nil {
		return d, err
	}
	d = protocol.IssueDeliveryDuty{TaskID: p.TaskID, EventID: event, Revision: 1, Status: "preparing", AssignmentEpoch: issue.AssignmentEpoch, Candidate: p.Candidate, Reason: p.Reason}
	raw, err := json.Marshal(d)
	if err != nil {
		return d, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO issue_delivery_duties(issue_id,task_id,event_id,revision,status,body_json) VALUES(?,?,?,?,?,?)`, issue.IssueID, d.TaskID, event, d.Revision, d.Status, string(raw))
	return d, err
}

func currentLiveDeliveryTask(ctx context.Context, q store.Querier, issue, task string, expected *int64) (store.TaskRow, error) {
	r, err := store.GetTask(ctx, q, task)
	if err != nil {
		return r, errNotFound(err, "delivery Task does not exist")
	}
	primary, err := store.GetTaskIssueOwner(ctx, q, task)
	if err != nil {
		return r, err
	}
	if primary != issue || r.Kind != protocol.TaskKindExecution {
		return r, protocol.Errorf(protocol.CodeForbidden, "delivery must retain this Issue's execution Task")
	}
	if expected == nil || r.ControlRevision != *expected {
		return r, protocol.Errorf(protocol.CodeStaleRevision, "delivery requires the current Task control revision")
	}
	if r.Lifecycle.Terminal() {
		return r, protocol.Errorf(protocol.CodeInvalidTransition, "delivery binding cannot revive a terminal Task")
	}
	return r, nil
}

func bindLiveDeliveryTask(ctx context.Context, tx *store.Tx, issue protocol.Issue, d protocol.IssueDeliveryDuty, p protocol.UpdateIssueDeliveryPayload) error {
	if p.ExecutionTaskID == "" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "execution_task_id is required for bind")
	}
	if _, err := currentLiveDeliveryTask(ctx, tx.Q(), issue.IssueID, p.ExecutionTaskID, p.ExpectedTaskControlRevision); err != nil {
		return err
	}
	if d.ExecutionTaskID != "" {
		return protocol.Errorf(protocol.CodeInvalidTransition, "delivery already has its original execution; reconcile it instead of binding another")
	}
	if _, err := deliveryExecutionAuthorized(ctx, tx.Q(), issue, protocol.IssueDeliveryAuthorization{TaskID: d.TaskID, Revision: d.Revision}); err != nil {
		return err
	}
	if d.Candidate.Deploy {
		available, err := deliveryTargetAvailable(ctx, tx.Q(), p.ExecutionTaskID)
		if err != nil {
			return err
		}
		if !available {
			return protocol.Errorf(protocol.CodeUnknownOutcome, "another delivery still owns the controlled upgrade target")
		}
	}
	var exists bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_delivery_executions WHERE task_id=?)`, p.ExecutionTaskID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return protocol.Errorf(protocol.CodeInvalidTransition, "the Task already retains another immutable delivery execution")
	}
	raw, err := json.Marshal(deliveryExecutionSnapshot{IssueDeliveryCandidate: *d.Candidate, AssignmentEpoch: issue.AssignmentEpoch, Authorization: d.Authorization})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO issue_delivery_executions(task_id,issue_id,source_task_id,duty_revision,candidate_json) VALUES(?,?,?,?,?)`, p.ExecutionTaskID, issue.IssueID, d.TaskID, d.Revision+1, string(raw))
	return err
}

func loadOrPrepareLiveDeliveryDuty(ctx context.Context, tx *store.Tx, issue protocol.Issue, p protocol.UpdateIssueDeliveryPayload) (protocol.IssueDeliveryDuty, error) {
	d, err := store.GetIssueDeliveryDuty(ctx, tx.Q(), issue.IssueID, p.TaskID)
	if errors.Is(err, store.ErrNotFound) {
		return prepareLiveDeliveryDuty(ctx, tx, issue, p)
	}
	return d, err
}
