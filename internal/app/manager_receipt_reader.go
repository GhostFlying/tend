package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
)

type managerStopProof struct {
	request, hash string
	sequence      int64
}

func supportsManagerReceiptReader(runtime *protocol.RunnerRuntime) bool {
	return runtime != nil && runtime.Protocol >= protocol.CurrentRunnerProtocol && slices.Contains(runtime.Capabilities, protocol.CapabilityManagerReceiptReader)
}

// Only the newly negotiated reader mode requires this capability. Historical
// receipt-only actions retain their original compatibility contract.
func isProvedManagerReceiptReader(ctx context.Context, q store.Querier, action string) (bool, error) {
	var reader bool
	err := q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(trigger_json,'$.receipt_reader_request_id'),'')<>'' FROM plan_actions WHERE action_id=?`, action).Scan(&reader)
	return reader, err
}

// Keep the original negotiated responsibility across read failures, but do not
// attach it to unrelated receipts after its exact original pending set clears.
func managerReceiptReaderTrigger(ctx context.Context, q store.Querier, task string) (string, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT a.trigger_json FROM plan_actions a WHERE a.task_id=? AND COALESCE(json_extract(a.trigger_json,'$.receipt_reader_request_id'),'')<>'' AND EXISTS(SELECT 1 FROM json_each(a.trigger_json,'$.receipt_reader_pending_request_ids') source JOIN native_turn_receipts_pending pending ON pending.request_id=source.value) ORDER BY a.generation DESC LIMIT 1`, task).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return `{"receipt_only":true}`, nil
	}
	return raw, err
}

func filterManagerReceiptReaders(ctx context.Context, q store.Querier, candidates []scheduler.Candidate, runtime *protocol.RunnerRuntime) ([]scheduler.Candidate, error) {
	if supportsManagerReceiptReader(runtime) {
		return candidates, nil
	}
	out := candidates[:0]
	for _, candidate := range candidates {
		if candidate.Manager != nil && candidate.Manager.ReceiptOnly {
			reader, err := isProvedManagerReceiptReader(ctx, q, candidate.ActionID)
			if err != nil {
				return nil, err
			}
			if reader {
				continue
			}
		}
		out = append(out, candidate)
	}
	return out, nil
}

func checkManagerReceiptReaderRecovery(ctx context.Context, q store.Querier, host string, runtime *protocol.RunnerRuntime) error {
	if supportsManagerReceiptReader(runtime) {
		return nil
	}
	var reader bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs r JOIN plan_actions a USING(action_id) WHERE r.host_id=? AND r.observed_state IN ('starting','running','waiting','interrupted','unknown') AND COALESCE(json_extract(a.trigger_json,'$.receipt_reader_request_id'),'')<>'')`, host).Scan(&reader)
	if err != nil {
		return err
	}
	if reader {
		return protocol.Errorf(protocol.CodeInvalidRequest, "runner requires manager-receipt-reader-v1 to recover the original read-only transport")
	}
	return nil
}

// Read an independently admitted original stopped receipt, never caller-supplied
// process assertions. Maintenance retention preserves these physical facts.
func managerPhysicalStopProof(ctx context.Context, q store.Querier, run, lease string) (*managerStopProof, error) {
	var id, raw string
	var sequence int64
	err := q.QueryRowContext(ctx, `SELECT p.request_id,p.payload_json,p.last_sequence FROM maintenance_physical_stops p JOIN request_records r ON r.domain=p.request_domain AND r.request_id=p.request_id AND r.operation=p.operation WHERE p.run_id=? AND p.lease_id=? AND p.request_domain=? AND p.operation=? AND r.status='succeeded' AND r.error_code IS NULL ORDER BY p.created_at DESC,p.rowid DESC LIMIT 1`, run, lease, "run:"+run, protocol.OpManagerNativeStopped).Scan(&id, &raw, &sequence)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var stopped protocol.ManagerStoppedPayload
	if err := json.Unmarshal([]byte(raw), &stopped); err != nil || stopped.LastSequence != sequence || sequence < 0 {
		return nil, protocol.Errorf(protocol.CodeUnknownOutcome, "original stopped receipt is invalid")
	}
	hash := sha256.Sum256([]byte(raw))
	return &managerStopProof{request: id, hash: hex.EncodeToString(hash[:]), sequence: sequence}, nil
}

func admitManagerReceiptReader(ctx context.Context, x *Exec, tx *store.Tx, s protocol.ManagerRunReconciliation, p protocol.ReconcileManagerRunPayload, m store.IssueManagerTask) (Outcome, error) {
	prepared, err := archivePrepared(ctx, tx.Q())
	if err != nil {
		return Outcome{}, err
	}
	parked, err := store.ManagerUpgradeParked(ctx, tx.Q())
	if err != nil {
		return Outcome{}, err
	}
	if prepared || parked {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "maintenance preparation blocks receipt-reader admission")
	}
	runtime, err := store.GetHostRuntime(ctx, tx.Q(), s.HostID)
	if err != nil {
		return Outcome{}, err
	}
	if !supportsManagerReceiptReader(&runtime.Runtime) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "original host has not reported manager-receipt-reader-v1")
	}
	if p.StoppedRequestID == "" || len(p.StoppedPayloadSHA256) != sha256.Size*2 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "original stopped request and payload SHA256 required")
	}
	if s.StoppedRequestID != p.StoppedRequestID || s.StoppedPayloadSHA256 != p.StoppedPayloadSHA256 {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "original stopped receipt changed")
	}
	if s.Observed != protocol.RunUnknown || !s.PendingBusinessReceipts || !reflect.DeepEqual(s.Blockers, []string{"business_receipts_unreconciled"}) || s.StoppedLastSequence != s.CloseLastSequence {
		return Outcome{}, protocol.Errorf(protocol.CodeUnknownOutcome, "receipt reader requires an otherwise reconciled original physical stop")
	}
	r, err := store.GetRun(ctx, tx.Q(), s.RunID)
	if err != nil {
		return Outcome{}, err
	}
	l, err := store.GetLeaseByRun(ctx, tx.Q(), s.RunID)
	if err != nil {
		return Outcome{}, err
	}
	if s.Execution == nil || m.Pin == nil || !reflect.DeepEqual(s.Execution, m.Pin) || r.HostID != m.HostID || l.HostID != r.HostID || l.LeaseID != s.LeaseID {
		return Outcome{}, protocol.Errorf(protocol.CodeUnknownOutcome, "original receipt reader execution identity changed")
	}
	var competing bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE task_id=? AND run_id<>? AND observed_state IN ('starting','running','waiting','interrupted','unknown')) OR EXISTS(SELECT 1 FROM plan_actions WHERE task_id=? AND status='pending')`, s.TaskID, s.RunID, s.TaskID).Scan(&competing); err != nil {
		return Outcome{}, err
	}
	if competing {
		return Outcome{}, protocol.Errorf(protocol.CodeUnknownOutcome, "another manager transport or action requires reconciliation")
	}
	requests, err := store.FollowupIDs(ctx, tx.Q(), `SELECT h.execution_id FROM hosted_executions h JOIN main_turns t ON t.request_id=h.execution_id JOIN native_turn_receipts_pending p ON p.request_id=t.request_id WHERE h.issue_id=? AND h.assignment_epoch=? AND json_extract(h.private_snapshot,'$.runner_run_id')=? ORDER BY h.execution_id`, m.IssueID, m.AssignmentEpoch, s.RunID)
	if err != nil {
		return Outcome{}, err
	}
	if len(requests) == 0 {
		return Outcome{}, protocol.Errorf(protocol.CodeUnknownOutcome, "original business receipt identity is missing")
	}
	// This is the existing physical-stop failure transition, not a successful
	// business result. Original native/logical receipts and the pin stay intact.
	if err := endRun(ctx, x, tx, r, protocol.RunFailed); err != nil {
		return Outcome{}, err
	}
	if err := tx.SetActionStatus(ctx, r.ActionID, protocol.ActionDone, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE tasks SET lifecycle='waiting',control_revision=control_revision+1,updated_at=? WHERE task_id=? AND control_revision=?`, x.Now.UnixMilli(), s.TaskID, s.TaskRevision); err != nil {
		return Outcome{}, err
	}
	var generation int64
	if err := tx.Q().QueryRowContext(ctx, `SELECT COALESCE(MAX(generation),0)+1 FROM plan_actions WHERE task_id=? AND kind='coordinate'`, s.TaskID).Scan(&generation); err != nil {
		return Outcome{}, err
	}
	if err := insertManagerAction(ctx, x, tx, s.TaskID, generation); err != nil {
		return Outcome{}, err
	}
	trigger, err := json.Marshal(map[string]any{"receipt_only": true, "receipt_reader_request_id": x.Env.RequestID, "receipt_reader_original_run_id": s.RunID, "receipt_reader_pending_request_ids": requests})
	if err != nil {
		return Outcome{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE plan_actions SET trigger_json=? WHERE task_id=? AND kind='coordinate' AND generation=?`, string(trigger), s.TaskID, generation); err != nil {
		return Outcome{}, err
	}
	var actionID string
	if err := tx.Q().QueryRowContext(ctx, `SELECT action_id FROM plan_actions WHERE task_id=? AND kind='coordinate' AND generation=?`, s.TaskID, generation).Scan(&actionID); err != nil {
		return Outcome{}, err
	}
	result := map[string]any{"task_id": s.TaskID, "run_id": s.RunID, "lease_id": s.LeaseID, "task_revision": s.TaskRevision + 1, "mode": "read_receipts", "observed": protocol.RunFailed, "responsibility": "receipt_reading", "reader_action_id": actionID, "reader_generation": generation, "pending_request_ids": requests, "close_request_id": s.CloseRequestID, "close_result_sha256": s.CloseResultSHA256, "stopped_request_id": s.StoppedRequestID, "stopped_payload_sha256": s.StoppedPayloadSHA256}
	return Outcome{Result: result, Events: []Event{{ObjectType: "task", ObjectID: s.TaskID, ObjectVersion: s.TaskRevision + 1, Kind: "task.manager_receipt_reader_requested", Body: result}}}, nil
}
