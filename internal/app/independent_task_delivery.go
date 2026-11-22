package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// startNextDelivery keeps only one mutable duty for the Task. Before replacing
// it, freeze the original execution and settlement using the existing history.
func startNextDelivery(ctx context.Context, x *Exec, tx *store.Tx, issue protocol.Issue, p protocol.UpdateIssueDeliveryPayload, current protocol.IssueDeliveryDuty) (protocol.IssueDeliveryDuty, error) {
	if issue.State != protocol.IssueActive || issue.BoardStatus() == protocol.IssueBacklog {
		return current, protocol.Errorf(protocol.CodeInvalidTransition, "start_next requires an active Issue")
	}
	task, err := currentLiveDeliveryTask(ctx, tx.Q(), issue.IssueID, p.TaskID, p.ExpectedTaskControlRevision)
	if err != nil {
		return current, err
	}
	contract, err := store.TaskOwnerContractCurrent(ctx, tx.Q(), task.TaskID)
	if err != nil {
		return current, err
	}
	if !contract || task.ControlMode != protocol.ControlManaged {
		return current, protocol.Errorf(protocol.CodeForbidden, "start_next requires the current managed Task contract")
	}
	if err := validateNextDeliveryRun(ctx, x, tx.Q(), task, p.RunID); err != nil {
		return current, err
	}
	previous := p.PreviousExecution
	switch {
	case current.Status == "complete" && current.ExecutionTaskID == task.TaskID && current.Candidate != nil:
		original, _, err := store.SettledSelfDeliveryExecution(ctx, tx.Q(), issue.IssueID, current)
		if err != nil {
			return current, err
		}
		if original == nil || *original != *previous {
			return current, protocol.Errorf(protocol.CodeStaleRevision, "select the exact completed predecessor from delivery_authorization")
		}
		if err := archiveTaskDeliveryForContinuation(ctx, x, tx, task); err != nil {
			return current, err
		}
	case current.Status == "" || current.Status == "preparing" && current.Candidate == nil && current.ExecutionTaskID == "" && current.InstanceID == "":
		original, _, err := latestArchivedSelfDelivery(ctx, tx.Q(), issue.IssueID, task.TaskID)
		if err != nil {
			return current, err
		}
		if original != *previous {
			return current, protocol.Errorf(protocol.CodeStaleRevision, "select the latest completed predecessor from delivery_authorization")
		}
	default:
		return current, protocol.Errorf(protocol.CodeInvalidTransition, "settle the current self-bound delivery before opening an independent one")
	}
	if err := selfDeliveryEffectsReconciled(ctx, tx.Q(), task.TaskID, ""); err != nil {
		return current, err
	}

	var overlapping bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM issue_delivery_execution_history
  WHERE issue_id=?1 AND (json_extract(candidate_json,'$.delivery_id')=?2 OR lower(json_extract(candidate_json,'$.url'))=lower(?4))
  AND json_extract(duty_json,'$.status')='complete')
 OR EXISTS(SELECT 1 FROM issue_delivery_duties
  WHERE issue_id=?1 AND task_id<>?3 AND (json_extract(body_json,'$.candidate.delivery_id')=?2 OR lower(json_extract(body_json,'$.candidate.url'))=lower(?4)))`,
		issue.IssueID, p.Candidate.DeliveryID, task.TaskID, p.Candidate.URL).Scan(&overlapping); err != nil {
		return current, err
	}
	if overlapping {
		return current, protocol.Errorf(protocol.CodeInvalidTransition, "start_next requires an independent registered delivery")
	}

	var lastRevision int64
	if err := tx.Q().QueryRowContext(ctx, `SELECT COALESCE(MAX(MAX(duty_revision, COALESCE(json_extract(duty_json,'$.revision'),0))),0)
 FROM issue_delivery_execution_history WHERE issue_id=? AND task_id=?`, issue.IssueID, task.TaskID).Scan(&lastRevision); err != nil {
		return current, err
	}
	event := nextDeliveryEventID(x)
	return protocol.IssueDeliveryDuty{
		TaskID: task.TaskID, EventID: event, InstanceID: event, PreviousExecution: previous,
		Revision: max(lastRevision, current.Revision) + 1, Status: "preparing", AssignmentEpoch: issue.AssignmentEpoch,
	}, nil
}

type nextDeliveryReceipt struct {
	InputHash string                     `json:"input_hash"`
	Duty      protocol.IssueDeliveryDuty `json:"duty"`
}

func nextDeliveryEventID(x *Exec) string {
	return protocol.NativeObjectID(protocol.PrefixEvent, x.RequestDomain(), "delivery.start_next/"+x.Env.RequestID)
}

func readNextDeliveryReceipt(ctx context.Context, q store.Querier, x *Exec) (*nextDeliveryReceipt, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT body_json FROM events WHERE event_id=? AND kind='issue.delivery_started'`, nextDeliveryEventID(x)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var receipt nextDeliveryReceipt
	if err := json.Unmarshal([]byte(raw), &receipt); err != nil {
		return nil, err
	}
	hash, err := protocol.PayloadHash(x.Env)
	if err != nil {
		return nil, err
	}
	if receipt.InputHash != hash {
		return nil, protocol.Errorf(protocol.CodeConflictPayload, "this start_next request already belongs to another delivery input")
	}
	return &receipt, nil
}

func persistDeliveryDuty(ctx context.Context, x *Exec, tx *store.Tx, issue string, action string, d protocol.IssueDeliveryDuty) error {
	if action != "start_next" {
		return tx.PutIssueDeliveryDuty(ctx, issue, d)
	}
	hash, err := protocol.PayloadHash(x.Env)
	if err != nil {
		return err
	}
	receipt, err := json.Marshal(nextDeliveryReceipt{InputHash: hash, Duty: d})
	if err != nil {
		return err
	}
	actor, err := json.Marshal(x.ActualPrincipal())
	if err != nil {
		return err
	}
	if _, err := tx.InsertEvent(ctx, store.EventRow{EventID: d.EventID, Source: "app", ObjectType: "issue", ObjectID: issue,
		Kind: "issue.delivery_started", BodyJSON: string(receipt), ActorJSON: string(actor), RequestID: x.Env.RequestID,
		TraceID: x.TraceID, CreatedAt: x.Now.UnixMilli()}); err != nil {
		return err
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO issue_delivery_duties(issue_id,task_id,event_id,revision,status,body_json) VALUES(?,?,?,?,?,?)
 ON CONFLICT(issue_id,task_id) DO UPDATE SET event_id=excluded.event_id,revision=excluded.revision,status=excluded.status,body_json=excluded.body_json,artifact_id=NULL,acceptance_id=NULL`,
		issue, d.TaskID, d.EventID, d.Revision, d.Status, string(raw))
	return err
}

func validateNextDeliveryRun(ctx context.Context, x *Exec, q store.Querier, task store.TaskRow, runID string) error {
	if task.Kind != protocol.TaskKindIssueManager {
		if runID != "" {
			return protocol.Errorf(protocol.CodeInvalidRequest, "run_id selects the active Issue owner Run, not a worker Run")
		}
		return nil
	}
	if x.coordinationExecution != nil {
		actual := x.coordinationExecution.Selection.RunnerRunID
		if runID != "" && runID != actual {
			return protocol.Errorf(protocol.CodeForbidden, "start_next requires the actual executing Issue owner Run")
		}
		runID = actual
	}
	run, err := store.GetRun(ctx, q, runID)
	if err != nil {
		return errNotFound(err, "start_next requires the active Issue owner Run")
	}
	if run.TaskID != task.TaskID {
		return protocol.Errorf(protocol.CodeForbidden, "start_next Run belongs to another Task")
	}
	allowed, err := managerNativeBusinessAllowed(ctx, q, run)
	if err != nil {
		return err
	}
	lease, err := store.GetLeaseByRun(ctx, q, runID)
	if err != nil {
		return err
	}
	if !allowed || !lease.Current(x.Now.UnixMilli()) {
		return protocol.Errorf(protocol.CodeForbidden, "start_next requires a current active Issue owner Run and lease")
	}
	return nil
}

func latestArchivedSelfDelivery(ctx context.Context, q store.Querier, issue, task string) (protocol.DeliveryExecutionSelector, protocol.IssueDeliveryDuty, error) {
	var ref protocol.DeliveryExecutionSelector
	var raw string
	err := q.QueryRowContext(ctx, `SELECT COALESCE(run_id,''),duty_revision,duty_json FROM issue_delivery_execution_history
 WHERE task_id=? AND source_task_id=? AND issue_id=? ORDER BY generation DESC,duty_revision DESC LIMIT 1`,
		task, task, issue).Scan(&ref.ExecutionRunID, &ref.DutyRevision, &raw)
	var d protocol.IssueDeliveryDuty
	if errors.Is(err, sql.ErrNoRows) {
		return ref, d, nil
	}
	if err != nil {
		return ref, d, err
	}
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return ref, d, err
	}
	if d.Status != "complete" || d.Candidate == nil || ref.ExecutionRunID == "" {
		ref = protocol.DeliveryExecutionSelector{}
	}
	return ref, d, nil
}

// Older clients keep their existing unsettled and merge-to-deployment paths.
// A blank slot created by continuation does not silently open another PR.
func validateDeliveryPreparationInstance(ctx context.Context, q store.Querier, issue string, d protocol.IssueDeliveryDuty, p protocol.UpdateIssueDeliveryPayload) error {
	if p.Action == "start_next" || p.Candidate == nil {
		return nil
	}
	if d.InstanceID != "" && d.Candidate != nil && d.Candidate.DeliveryID != p.Candidate.DeliveryID {
		return protocol.Errorf(protocol.CodeInvalidTransition, "the new delivery is bound to its original registered provider identity")
	}
	if d.Candidate != nil {
		return nil
	}
	ref, previous, err := latestArchivedSelfDelivery(ctx, q, issue, p.TaskID)
	if err != nil || ref.ExecutionRunID == "" {
		return err
	}
	if !previous.Candidate.Deploy && p.Candidate.Deploy && p.Candidate.ProviderState == "merged" &&
		p.Candidate.DeliveryID == previous.Candidate.DeliveryID && p.Candidate.HeadSHA == previous.Candidate.HeadSHA {
		return nil
	}
	return protocol.Errorf(protocol.CodeInvalidTransition, "start_next explicitly opens an independent delivery after the original settlement")
}

func requireDeliveryInstance(d protocol.IssueDeliveryDuty, instance string) error {
	if d.InstanceID != instance {
		return protocol.Errorf(protocol.CodeStaleRevision, "supply the current delivery_instance_id from the original start_next receipt or Issue read")
	}
	return nil
}
