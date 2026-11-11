package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// ManagerNativeCommand binds the transport to an authenticated leased run.
// Native payloads never authorize a business operation on their own.
type ManagerNativeCommand struct{ OperationName string }

func (c ManagerNativeCommand) Operation() string { return c.OperationName }
func (c ManagerNativeCommand) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	if x.Principal.Kind == protocol.PrincipalSystem {
		return nil
	}
	if x.Principal.Kind != protocol.PrincipalRun || x.Principal.Role != protocol.RoleIssueManager || x.Principal.Subject != x.Env.Target.ID {
		return protocol.Errorf(protocol.CodeForbidden, "manager transport requires its own run credential")
	}
	r, err := store.GetRun(ctx, q, x.Principal.Subject)
	if err != nil {
		return err
	}
	if r.Role != protocol.RoleIssueManager {
		return protocol.Errorf(protocol.CodeForbidden, "run is not a manager")
	}
	l, err := store.GetLeaseByRun(ctx, q, r.RunID)
	if err != nil {
		return err
	}
	// Receipt-only writes remain admissible after expiry; they cannot queue
	// new work or acquire responsibility under a stale epoch.
	if c.OperationName == protocol.OpManagerNativeClaim && (l.Status != store.LeaseActive || l.ExpiresAt <= x.Now.UnixMilli()) {
		var payload protocol.ManagerNativeClaimPayload
		if err := decodePayload(x.Env, &payload); err != nil {
			return err
		}
		if len(payload.AcknowledgedIDs) == 0 && !payload.AcknowledgeOnly {
			return protocol.Errorf(protocol.CodeForbidden, "manager lease is not active")
		}
	}
	return nil
}
func (c ManagerNativeCommand) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	run := x.Env.Target.ID
	switch c.OperationName {
	case "manager.native_enqueue":
		if x.Principal.Kind != protocol.PrincipalSystem {
			return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "only the coordinator queues native operations")
		}
		var p protocol.ManagerNativeRequest
		if err := decodePayload(x.Env, &p); err != nil {
			return Outcome{}, err
		}
		r, err := store.GetRun(ctx, tx.Q(), run)
		if err != nil {
			return Outcome{}, err
		}
		if r.Role != protocol.RoleIssueManager {
			return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "native coordination requires a manager run")
		}
		if p.Method == "close" {
			prior, err := parkTeardownClose(ctx, tx.Q(), run)
			if err != nil {
				return Outcome{}, err
			}
			if prior != nil {
				return Outcome{NoEffect: true, Result: *prior}, nil
			}
			prior, err = unresolvedManagerClose(ctx, tx.Q(), run)
			if err != nil {
				return Outcome{}, err
			}
			if prior != nil {
				return Outcome{NoEffect: true, Result: *prior}, nil
			}
		}
		if r.ObservedState == protocol.RunFinished || r.ObservedState == protocol.RunFailed {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "manager native process already stopped")
		}
		if p.Method == "fork" || p.Method == "release" {
			runtime, e := store.GetHostRuntime(ctx, tx.Q(), r.HostID)
			if e != nil || !slices.Contains(runtime.Runtime.Capabilities, protocol.CapabilityNativeUsage) {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "runner does not support persistent private summaries")
			}
		}
		if !managerNativeReceiptMethod(p.Method) {
			if parked, err := store.ManagerUpgradeParked(ctx, tx.Q()); err != nil {
				return Outcome{}, err
			} else if parked {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "upgrade manager park blocks new native work")
			}
		}
		allowed, err := managerNativeBusinessAllowed(ctx, tx.Q(), r)
		if err != nil {
			return Outcome{}, err
		}
		if !allowed && !managerNativeReceiptMethod(p.Method) {
			return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "fenced manager may only read and reconcile native receipts")
		}
		if managerNativeObservationMethod(p.Method) {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "observational RPCs use the in-flight transport")
		}
		if p.Method == "tool_result" {
			if err := tx.PutManagerToolResult(ctx, run, p.ID, string(p.Payload), false, x.Now.UnixMilli()); err != nil {
				return Outcome{}, err
			}
		}
		payload, err := tx.RetainJSON(ctx, string(p.Payload))
		if err != nil {
			return Outcome{}, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO manager_native_requests(request_id,run_id,method,payload_json,status,created_at) VALUES(?,?,?,?,'pending',?)`, p.ID, run, p.Method, payload, x.Now.UnixMilli())
		return Outcome{Result: p}, err
	case protocol.OpManagerNativeClaim:
		var claim protocol.ManagerNativeClaimPayload
		if err := decodePayload(x.Env, &claim); err != nil {
			return Outcome{}, err
		}
		if len(claim.AcknowledgedIDs) > 128 {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "too many command acknowledgements")
		}
		changed := false
		for _, id := range dedupe(claim.AcknowledgedIDs) {
			if id == "" {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "command acknowledgement identity is required")
			}
			// The runner reports only after receiving success for this original
			// receipt. A lost acknowledgement request is safe to repeat.
			result, err := tx.Exec(ctx, `UPDATE manager_native_requests SET runner_acked=1 WHERE run_id=? AND request_id=? AND status='completed' AND runner_acked=0`, run, id)
			if err != nil {
				return Outcome{}, err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return Outcome{}, err
			}
			changed = changed || n > 0
			if _, err := tx.Exec(ctx, `DELETE FROM manager_native_requests WHERE run_id=? AND request_id=? AND coordinator_consumed=1 AND runner_acked=1`, run, id); err != nil {
				return Outcome{}, err
			}
		}
		lease, err := store.GetLeaseByRun(ctx, tx.Q(), run)
		if err != nil {
			return Outcome{}, err
		}
		if claim.AcknowledgeOnly || lease.Status != store.LeaseActive || lease.ExpiresAt <= x.Now.UnixMilli() {
			return Outcome{NoEffect: !changed, Result: map[string]any{"request": nil}}, nil
		}
		r, err := store.GetRun(ctx, tx.Q(), run)
		if err != nil {
			return Outcome{}, err
		}
		allowed, err := managerNativeBusinessAllowed(ctx, tx.Q(), r)
		if err != nil {
			return Outcome{}, err
		}
		for {
			var p protocol.ManagerNativeRequest
			var payload, status string
			err := tx.Q().QueryRowContext(ctx, `SELECT request_id,method,payload_json,status FROM manager_native_requests WHERE run_id=? AND status IN ('pending','started') ORDER BY CASE WHEN status='started' THEN 0 ELSE 1 END,created_at,rowid LIMIT 1`, run).Scan(&p.ID, &p.Method, &payload, &status)
			if errors.Is(err, sql.ErrNoRows) {
				return Outcome{NoEffect: !changed, Result: map[string]any{"request": nil}}, nil
			}
			if err != nil {
				return Outcome{}, err
			}
			payload, err = store.ResolveJSON(ctx, tx.Q(), payload)
			if err != nil {
				return Outcome{}, err
			}
			p.Payload = json.RawMessage(payload)
			if status == "started" {
				return Outcome{NoEffect: !changed, Result: p}, nil
			}

			if !allowed && !managerNativeReceiptMethod(p.Method) {
				raw, err := json.Marshal(protocol.ManagerNativeReply{ID: p.ID, Error: "Manager was fenced before native execution", Rejected: true})
				if err != nil {
					return Outcome{}, err
				}
				if _, err := tx.Exec(ctx, `UPDATE manager_native_requests SET status='completed',result_json=?,completion_error='Manager was fenced before native execution',completion_rejected=1,runner_acked=1 WHERE request_id=?`, string(raw), p.ID); err != nil {
					return Outcome{}, err
				}
				changed = true
				continue
			}
			if _, err := tx.Exec(ctx, `UPDATE runs SET observed_state='running' WHERE run_id=? AND observed_state='starting'`, run); err != nil {
				return Outcome{}, err
			}
			if p.Method != "close" {
				if _, err := tx.Exec(ctx, `UPDATE runs SET native_activity=native_activity+1 WHERE run_id=?`, run); err != nil {
					return Outcome{}, err
				}
			}
			_, err = tx.Exec(ctx, `UPDATE manager_native_requests SET status='started' WHERE request_id=? AND status='pending'`, p.ID)
			return Outcome{Result: p}, err
		}
	case protocol.OpManagerNativeReply:
		var p protocol.ManagerNativeReply
		if err := decodePayload(x.Env, &p); err != nil {
			return Outcome{}, err
		}
		var method, status, payload string
		if err := tx.Q().QueryRowContext(ctx, `SELECT method,status,payload_json FROM manager_native_requests WHERE run_id=? AND request_id=?`, run, p.ID).Scan(&method, &status, &payload); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				confirmed, err := consumedManagerNativeReceipt(ctx, tx.Q(), run, p)
				if err != nil {
					return Outcome{}, err
				}
				if confirmed {
					return Outcome{NoEffect: true, Result: p}, nil
				}
				return Outcome{}, protocol.Errorf(protocol.CodeUnknownOutcome, "native command is no longer pending; reconcile its original session, tool call or physical stop")
			}
			return Outcome{}, err
		}
		if p.Method != "" && p.Method != method {
			return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "native receipt names another command method")
		}
		var raw []byte
		var err error
		if status == "completed" {
			var retained sql.NullString
			if err = tx.Q().QueryRowContext(ctx, `SELECT result_json FROM manager_native_requests WHERE run_id=? AND request_id=?`, run, p.ID).Scan(&retained); err != nil {
				return Outcome{}, err
			}
			if !retained.Valid {
				confirmed, err := consumedManagerNativeReceipt(ctx, tx.Q(), run, p)
				if err != nil {
					return Outcome{}, err
				}
				if !confirmed {
					return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "native receipt differs from its completed business fact")
				}
				return Outcome{NoEffect: true, Result: p}, nil
			}
			prior := retained.String
			prior, err = store.ResolveJSON(ctx, tx.Q(), prior)
			if err != nil {
				return Outcome{}, err
			}
			var before protocol.ManagerNativeReply
			if err := json.Unmarshal([]byte(prior), &before); err != nil {
				return Outcome{}, err
			}
			before.Method = ""
			current := p
			current.Method = ""
			if !sameBusinessDefinition(before, current) {
				return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "native result identity has different content")
			}
			return Outcome{Result: p}, nil
		}
		if status != "started" && status != "unknown" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "native operation is not awaiting a result")
		}
		raw, err = json.Marshal(p)
		if err != nil {
			return Outcome{}, err
		}
		if method == "close" && p.Error == "" {
			var last, pending int64
			if err = tx.Q().QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0),COALESCE(SUM(CASE WHEN applied=0 THEN 1 ELSE 0 END),0) FROM manager_native_events WHERE run_id=?`, run).Scan(&last, &pending); err != nil {
				return Outcome{}, err
			}
			if last != p.LastSequence {
				return Outcome{}, protocol.Errorf(protocol.CodeUnknownOutcome, "native close must cover all applied events")
			}
			if err := settleClosedManagerToolResults(ctx, tx, run); err != nil {
				return Outcome{}, err
			}
			if err = tx.Q().QueryRowContext(ctx, `SELECT COUNT(*) FROM manager_native_events WHERE run_id=? AND applied=0`, run).Scan(&pending); err != nil {
				return Outcome{}, err
			}
			if pending != 0 {
				if err := consumeClosedManagerRepairFacts(ctx, x, tx, run); err != nil {
					return Outcome{}, err
				}
				if err = tx.Q().QueryRowContext(ctx, `SELECT COUNT(*) FROM manager_native_events WHERE run_id=? AND applied=0`, run).Scan(&pending); err != nil {
					return Outcome{}, err
				}
			}
			if pending != 0 {
				return Outcome{}, protocol.Errorf(protocol.CodeUnknownOutcome, "native close must cover all applied events")
			}
		}
		if err := tx.CompleteManagerNative(ctx, run, p.ID, method, payload, p, x.Now.UnixMilli()); err != nil {
			return Outcome{}, err
		}
		result, err := tx.RetainJSON(ctx, string(raw))
		if err != nil {
			return Outcome{}, err
		}
		completionStatus := "completed"
		if p.Error != "" && !p.Rejected {
			completionStatus = "unknown"
		}
		_, err = tx.Exec(ctx, `UPDATE manager_native_requests SET status=?,result_json=?,completion_error=?,completion_rejected=? WHERE run_id=? AND request_id=?`, completionStatus, result, p.Error, p.Rejected, run, p.ID)
		if err != nil {
			return Outcome{}, err
		}
		if method == "close" && p.Error == "" {
			if err := settleStoppedManagerRequests(ctx, tx, run); err != nil {
				return Outcome{}, err
			}
			r, err := store.GetRun(ctx, tx.Q(), run)
			if err != nil {
				return Outcome{}, err
			}
			// A stopped receipt may already have released this transport. Keep
			// the validated original close without transitioning it a second time.
			if r.ObservedState == protocol.RunFinished || r.ObservedState == protocol.RunFailed {
				return Outcome{Result: p}, nil
			}
			preserved, err := preserveMaintenancePhysicalStop(ctx, x, tx, run, p.LastSequence)
			if err != nil {
				return Outcome{}, err
			}
			if preserved {
				if x.repeatedPhysicalStop {
					return Outcome{NoEffect: true, Result: p}, nil
				}
				return Outcome{Result: p, Events: []Event{{ObjectType: "run", ObjectID: run, Kind: "run.maintenance_physical_stop", Body: map[string]any{"last_sequence": p.LastSequence}}}}, nil
			}
			uncertain, err := managerBusinessReceiptsPending(ctx, tx.Q(), run)
			if err != nil {
				return Outcome{}, err
			}
			if uncertain {
				ended, err := finishStoppedManager(ctx, x, tx, run, "Native process closed; original business receipts require reconciliation", p.LastSequence)
				ended.Result = p
				return ended, err
			}
			if v, e := store.GetExternalWait(ctx, tx.Q(), run); e != nil {
				return Outcome{}, e
			} else if v != nil && v.Status == "stopping" {
				ended, err := settleExternalWaitStopped(ctx, x, tx, r, v, "")
				ended.Result = p
				return ended, err
			}
			ended, err := (FinishManagerTurn{}).Apply(ctx, x, tx)
			if err != nil {
				return Outcome{}, err
			}
			ended.Result = p
			return ended, nil
		}
		return Outcome{Result: p}, nil
	case protocol.OpManagerNativeStopped:
		var p protocol.ManagerStoppedPayload
		if err := decodePayload(x.Env, &p); err != nil {
			return Outcome{}, err
		}
		if p.Reason == "" || p.LastSequence < 0 {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "stop proof requires reason and event cursor")
		}
		var last int64
		if err := tx.Q().QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM manager_native_events WHERE run_id=?`, run).Scan(&last); err != nil {
			return Outcome{}, err
		}
		if last != p.LastSequence {
			return Outcome{}, protocol.Errorf(protocol.CodeUnknownOutcome, "native events must be acknowledged before stop")
		}
		return finishStoppedManager(ctx, x, tx, run, p.Reason, p.LastSequence)
	case protocol.OpManagerNativeEvent:
		var p protocol.ManagerNativeEvent
		if err := decodePayload(x.Env, &p); err != nil {
			return Outcome{}, err
		}
		if p.Sequence < 1 || !json.Valid(p.Body) {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid native event")
		}
		var last int64
		if err := tx.Q().QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM manager_native_events WHERE run_id=?`, run).Scan(&last); err != nil {
			return Outcome{}, err
		}
		if p.Sequence <= last {
			var body string
			if err := tx.Q().QueryRowContext(ctx, `SELECT body_json FROM manager_native_events WHERE run_id=? AND seq=?`, run, p.Sequence).Scan(&body); err != nil {
				return Outcome{}, err
			}
			body, err := store.ResolveJSON(ctx, tx.Q(), body)
			if err != nil {
				return Outcome{}, err
			}
			if !store.SemanticJSONEqual(body, string(p.Body)) {
				return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "native event identity has different content")
			}
			return Outcome{ProjectionOnly: true, Result: map[string]any{"ack": p.Sequence}}, nil
		}
		r, err := store.GetRun(ctx, tx.Q(), run)
		if err != nil {
			return Outcome{}, err
		}
		if r.ObservedState == protocol.RunFinished || r.ObservedState == protocol.RunFailed {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "stopped manager cannot append new native facts")
		}
		if p.Sequence != last+1 {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "native events must be contiguous")
		}
		var event struct {
			ThreadID string `json:"thread_id"`
			Native   *struct {
				Kind       string
				TurnID     string
				TurnStatus string
				Failure    *protocol.AgentFailure
			} `json:"native"`
		}
		parsed := json.Unmarshal(p.Body, &event) == nil
		if parsed && event.Native != nil && event.ThreadID != "" {
			f := protocol.ConcurrencyFeedback{SessionID: event.ThreadID, TurnID: event.Native.TurnID, Attempt: "run", Failure: event.Native.Failure}
			switch event.Native.Kind {
			case "turn_error":
				f.Kind = "congestion"
			case "turn_completed":
				if event.Native.TurnStatus == "completed" {
					f.Kind = "success"
				}
			case "turn_started":
				f.Kind = "running"
			}
			if f.Kind != "" {
				body, _ := json.Marshal(f)
				if err := concurrencyRunFeedback(ctx, tx, r, protocol.RunEvent{Kind: "worker.concurrency", Body: body}, x.Now.UnixMilli()); err != nil {
					return Outcome{}, err
				}
			}
		}
		projectionOnly := false
		if parsed && event.Native != nil {
			switch event.Native.Kind {
			case "agent_message_delta", "item_started", "item_completed", "context_usage", "request_usage", "child_rollout":
				closing, err := managerRunClosing(ctx, tx.Q(), run)
				if err != nil {
					return Outcome{}, err
				}
				projectionOnly = !closing
			}
		}
		body, err := tx.RetainEventJSON(ctx, string(p.Body))
		if err != nil {
			return Outcome{}, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO manager_native_events(run_id,seq,body_json) VALUES(?,?,?)`, run, p.Sequence, body)
		return Outcome{ProjectionOnly: projectionOnly, Result: map[string]any{"ack": p.Sequence}}, err
	}
	return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "unknown manager transport operation")
}

// A caller timing out cannot authorize another physical close. Preserve the
// first unresolved command without changing completed-error legacy semantics.
func unresolvedManagerClose(ctx context.Context, q store.Querier, run string) (*protocol.ManagerNativeRequest, error) {
	var prior protocol.ManagerNativeRequest
	var payload string
	err := q.QueryRowContext(ctx, `SELECT request_id,payload_json FROM manager_native_requests WHERE run_id=? AND method='close' AND status IN ('pending','started','unknown') ORDER BY created_at,rowid LIMIT 1`, run).Scan(&prior.ID, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	payload, err = store.ResolveJSON(ctx, q, payload)
	if err != nil {
		return nil, err
	}
	prior.Method, prior.Payload = "close", json.RawMessage(payload)
	return &prior, nil
}

// A queued tool_result proves the server finished the business tool handler.
// Once the native process has closed, its unclaimed delivery is definitively
// rejected. Account for that result without requiring the closed process to
// consume its own callback. Started deliveries retain their uncertain identity.
func settleClosedManagerToolResults(ctx context.Context, tx *store.Tx, run string) error {
	ids, err := store.FollowupIDs(ctx, tx.Q(), `SELECT request_id FROM manager_native_requests WHERE run_id=? AND method='tool_result' AND status='pending'`, run)
	if err != nil {
		return err
	}
	for _, id := range ids {
		raw, err := json.Marshal(protocol.ManagerNativeReply{ID: id, Error: "Native process closed before tool result delivery", Rejected: true})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE manager_native_requests SET status='completed',result_json=?,completion_error='Native process closed before tool result delivery',completion_rejected=1,runner_acked=1 WHERE request_id=?`, string(raw), id); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE manager_native_events AS e SET applied=1 WHERE e.run_id=? AND e.applied=0 AND EXISTS(
		SELECT 1 FROM manager_native_requests r WHERE r.run_id=e.run_id AND r.method='tool_result'
		AND json_extract(r.payload_json,'$.thread_id')=json_extract(e.body_json,'$.call.ThreadID')
		AND json_extract(r.payload_json,'$.turn_id')=json_extract(e.body_json,'$.call.TurnID')
		AND json_extract(r.payload_json,'$.call_id')=json_extract(e.body_json,'$.call.CallID'))`, run)
	return err
}

func (a *App) QueueManagerNative(ctx context.Context, run, method string, payload json.RawMessage) (string, error) {
	id := a.ids.New(protocol.PrefixRequest)
	p := protocol.ManagerNativeRequest{ID: id, Method: method, Payload: payload}
	if managerNativeObservationMethod(method) {
		if err := a.nativeCalls.add(ctx, run, p); err != nil {
			return "", err
		}
		err := a.store.Read(ctx, func(tx *store.ReadTx) error {
			r, err := store.GetRun(ctx, tx.Q(), run)
			if err != nil {
				return err
			}
			if r.Role != protocol.RoleIssueManager || r.ObservedState == protocol.RunFinished || r.ObservedState == protocol.RunFailed {
				return protocol.Errorf(protocol.CodeInvalidTransition, "native observation requires a running transport")
			}
			return nil
		})
		if err != nil {
			a.nativeCalls.remove(id)
			return "", err
		}
		a.changes.Notify()
		return id, nil
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	op, err := a.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalSystem}, &protocol.Envelope{RequestID: id, Operation: "manager.native_enqueue", Target: protocol.Target{Type: "run", ID: run}, Payload: raw}, ManagerNativeCommand{OperationName: "manager.native_enqueue"})
	if err == nil && op.Status != protocol.OpSucceeded {
		err = op.Error
	}
	if err == nil {
		var request protocol.ManagerNativeRequest
		if err = json.Unmarshal(op.Result, &request); err == nil {
			id = request.ID
		}
	}
	if err != nil {
		a.nativeCalls.remove(id)
	}
	return id, err
}

// A completed command remains only until its business coordinator consumes it.
// Pending observations use the reader; they never queue behind the writer.
func (a *App) ManagerNativeResult(ctx context.Context, run, id string) (protocol.ManagerNativeReply, bool, error) {
	if reply, ready, present := a.nativeCalls.result(run, id); present {
		return reply, ready, nil
	}
	var reply protocol.ManagerNativeReply
	var ready, completed bool
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var raw sql.NullString
		var status string
		var consumed bool
		err := tx.Q().QueryRowContext(ctx, `SELECT result_json,status,coordinator_consumed FROM manager_native_requests WHERE run_id=? AND request_id=?`, run, id).Scan(&raw, &status, &consumed)
		if errors.Is(err, sql.ErrNoRows) {
			return protocol.Errorf(protocol.CodeUnknownOutcome, "native response is unavailable; reconcile the original execution or tool call")
		}
		if err != nil {
			return err
		}
		if consumed {
			return protocol.Errorf(protocol.CodeUnknownOutcome, "native handoff was already consumed; read its current session, tool or stop fact")
		}
		if !raw.Valid {
			return nil
		}
		if err := store.DecodeJSON(ctx, tx.Q(), raw.String, &reply); err != nil {
			return err
		}
		ready, completed = true, status == "completed"
		return nil
	})
	if err != nil || !completed {
		return reply, ready, err
	}
	err = a.write(ctx, func(tx *store.Tx) error {
		compact := true
		result, err := tx.Exec(ctx, `UPDATE manager_native_requests SET coordinator_consumed=1,completion_error=?,completion_rejected=?,payload_json=CASE WHEN ? THEN '{}' ELSE payload_json END,result_json=CASE WHEN ? THEN NULL ELSE result_json END WHERE run_id=? AND request_id=? AND status='completed' AND coordinator_consumed=0`, reply.Error, reply.Rejected, compact, compact, run, id)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return protocol.Errorf(protocol.CodeUnknownOutcome, "native response was consumed by another coordinator; reconcile its business fact")
		}
		_, err = tx.Exec(ctx, `DELETE FROM manager_native_requests WHERE run_id=? AND request_id=? AND coordinator_consumed=1 AND runner_acked=1`, run, id)
		return err
	})
	return reply, ready && err == nil, err
}

// The original command's accepted completion class is enough for a duplicate
// transport ACK. No response body/hash or request-to-object cache is recreated.
func consumedManagerNativeReceipt(ctx context.Context, q store.Querier, run string, p protocol.ManagerNativeReply) (bool, error) {
	var accepted bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM manager_native_requests WHERE run_id=? AND request_id=? AND status='completed' AND coordinator_consumed=1 AND completion_error=? AND completion_rejected=?)`, run, p.ID, p.Error, p.Rejected).Scan(&accepted)
	return accepted, err
}

func (a *App) ManagerNativeEvents(ctx context.Context, run string, after int64) ([]protocol.ManagerNativeEvent, error) {
	var events []protocol.ManagerNativeEvent
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		rows, err := tx.Q().QueryContext(ctx, `SELECT seq,body_json FROM manager_native_events WHERE run_id=? AND seq>? ORDER BY seq LIMIT 100`, run, after)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e protocol.ManagerNativeEvent
			var body string
			if err := rows.Scan(&e.Sequence, &body); err != nil {
				return err
			}
			body, err = store.ResolveJSON(ctx, tx.Q(), body)
			if err != nil {
				return err
			}
			e.Body = json.RawMessage(body)
			events = append(events, e)
		}
		return rows.Err()
	})
	return events, err
}

func (a *App) LastManagerNativeEvent(ctx context.Context, run string) (int64, error) {
	var last int64
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM manager_native_events WHERE run_id=?`, run).Scan(&last)
	})
	return last, err
}

// ManagerReceiptCredential grants only the manager receipt RPCs selected by
// runnerapi. It never authenticates new native claims or business tools.
func (a *App) ManagerReceiptCredential(ctx context.Context, run string) (leaseID, hostID string, role protocol.RunRole, err error) {
	err = a.store.Read(ctx, func(tx *store.ReadTx) error {
		r, e := store.GetRun(ctx, tx.Q(), run)
		if e != nil {
			return e
		}
		if r.Role != protocol.RoleIssueManager {
			return store.ErrNotFound
		}
		l, e := store.GetLeaseByRun(ctx, tx.Q(), run)
		if e != nil {
			return e
		}
		leaseID, hostID, role = l.LeaseID, r.HostID, r.Role
		return nil
	})
	return
}

func authorizeRunnerCoordination(ctx context.Context, q store.Querier, p protocol.Principal, e store.HostedExecution, mutating bool, now int64, operations ...string) error {
	if p.Kind != protocol.PrincipalRun || p.Role != protocol.RoleIssueManager || p.Subject != e.Selection.RunnerRunID || p.ConversationID != e.ConversationID {
		return protocol.Errorf(protocol.CodeForbidden, "tool caller does not own the manager run")
	}
	r, err := store.GetRun(ctx, q, p.Subject)
	if err != nil {
		return err
	}
	m, err := store.GetIssueManagerTask(ctx, q, r.TaskID)
	if err != nil {
		return err
	}
	if r.Role != protocol.RoleIssueManager || m.IssueID != e.IssueID || m.ConversationID != e.ConversationID || m.AssignmentEpoch != e.AssignmentEpoch {
		return protocol.Errorf(protocol.CodeForbidden, "manager run scope differs")
	}
	if !mutating {
		return nil
	}
	valid, err := store.IssueManagerAuthorizationValid(ctx, q, r.TaskID)
	if err != nil {
		return err
	}
	if !valid {
		return protocol.Errorf(protocol.CodeForbidden, "manager publication source is no longer valid")
	}
	l, err := store.GetLeaseByRun(ctx, q, r.RunID)
	if err != nil {
		return err
	}
	t, err := store.GetTask(ctx, q, r.TaskID)
	if err != nil {
		return err
	}
	if l.Status != store.LeaseActive || l.ExpiresAt <= now || t.ControlMode != protocol.ControlManaged || t.Lifecycle.Terminal() {
		return protocol.Errorf(protocol.CodeForbidden, "manager execution is fenced")
	}
	allowed, err := managerNativeBusinessAllowed(ctx, q, r)
	if err != nil {
		return err
	}
	if !allowed {
		settlement := false
		if len(operations) == 1 && closedIssueReceiptOperation(operations[0]) {
			issue, err := store.GetCanonicalIssue(ctx, q, e.IssueID)
			if err != nil {
				return err
			}
			settlement = issue.State == protocol.IssueClosed && issue.AssignmentEpoch == e.AssignmentEpoch
		}
		if !settlement {
			return protocol.Errorf(protocol.CodeForbidden, "receipt-only manager cannot initiate business operations")
		}
	}
	return nil
}

func (a *App) AuthorizeRunnerCoordination(ctx context.Context, p protocol.Principal, e store.HostedExecution) error {
	return a.store.Read(ctx, func(tx *store.ReadTx) error {
		return authorizeRunnerCoordination(ctx, tx.Q(), p, e, false, a.now().UnixMilli())
	})
}

func (a *App) ManagerRunEnded(ctx context.Context, run string) (bool, error) {
	var ended bool
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		r, err := store.GetRun(ctx, tx.Q(), run)
		if err != nil {
			return err
		}
		ended = r.ObservedState == protocol.RunFinished || r.ObservedState == protocol.RunFailed
		return nil
	})
	return ended, err
}

func (a *App) ManagerAppliedCursor(ctx context.Context, run string) (int64, error) {
	var cursor int64
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT COALESCE((SELECT MIN(seq)-1 FROM manager_native_events WHERE run_id=? AND applied=0),(SELECT MAX(seq) FROM manager_native_events WHERE run_id=?),0)`, run, run).Scan(&cursor)
	})
	return cursor, err
}

// AcknowledgeManagerEvent is invoked only after the server applied the event.
// Completing an item/transport flush also covers its previously buffered deltas.
func (a *App) AcknowledgeManagerEvent(ctx context.Context, run string, seq int64, thread, turn, item string, flush bool) error {
	ctx = store.WithWriteOperation(store.WithWriteEvent(ctx, run, seq), "manager.event.ack")
	changed, scheduling := false, false
	err := a.write(ctx, func(tx *store.Tx) error {
		result, err := tx.Exec(ctx, `UPDATE manager_native_events SET applied=1 WHERE run_id=? AND seq=? AND applied=0`, run, seq)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		changed = n > 0
		if flush {
			result, err = tx.Exec(ctx, `UPDATE manager_native_events SET applied=1 WHERE run_id=? AND seq<? AND applied=0 AND json_extract(body_json,'$.native.Kind')='agent_message_delta' AND (?='' OR json_extract(body_json,'$.thread_id')=?) AND (?='' OR json_extract(body_json,'$.native.TurnID')=?) AND (?='' OR json_extract(body_json,'$.native.ItemID')=?)`, run, seq, thread, thread, turn, turn, item, item)
			if err != nil {
				return err
			}
			n, err = result.RowsAffected()
			if err != nil {
				return err
			}
			changed = changed || n > 0
		}
		if !changed {
			return nil
		}
		closing, err := managerRunClosing(ctx, tx.Q(), run)
		if err != nil {
			return err
		}
		var terminal bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE run_id=? AND observed_state IN ('finished','failed'))`, run).Scan(&terminal); err != nil {
			return err
		}
		scheduling = closing || terminal
		return nil
	})
	if err == nil && changed {
		a.changes.Notify()
		if scheduling {
			a.scheduling.Notify()
		}
	}
	return err
}

// finishStoppedManager releases physical capacity while retaining uncertain
// business receipts for a new, read-only reconciliation run.
func finishStoppedManager(ctx context.Context, x *Exec, tx *store.Tx, run, reason string, lastSequence int64) (Outcome, error) {
	if v, err := store.GetExternalWait(ctx, tx.Q(), run); err != nil {
		return Outcome{}, err
	} else if v != nil && v.Status == "stopping" {
		r, err := store.GetRun(ctx, tx.Q(), run)
		if err != nil {
			return Outcome{}, err
		}
		return settleExternalWaitStopped(ctx, x, tx, r, v, "")
	}
	preserved, err := preserveMaintenancePhysicalStop(ctx, x, tx, run, lastSequence)
	if err != nil {
		return Outcome{}, err
	}
	if preserved {
		if x.repeatedPhysicalStop {
			return Outcome{NoEffect: true, Result: map[string]any{"run_id": run}}, nil
		}
		if err := settleStoppedManagerRequests(ctx, tx, run); err != nil {
			return Outcome{}, err
		}
		return Outcome{Result: map[string]any{"run_id": run}, Events: []Event{{ObjectType: "run", ObjectID: run, Kind: "run.maintenance_physical_stop", Body: map[string]any{"reason": reason, "last_sequence": lastSequence}}}}, nil
	}
	r, err := store.GetRun(ctx, tx.Q(), run)
	if err != nil {
		return Outcome{}, err
	}
	if r.ObservedState != protocol.RunFinished && r.ObservedState != protocol.RunFailed {
		if err = endRun(ctx, x, tx, r, protocol.RunFailed); err != nil {
			return Outcome{}, err
		}
		if err = tx.SetActionStatus(ctx, r.ActionID, protocol.ActionDone, x.Now.UnixMilli()); err != nil {
			return Outcome{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE tasks SET lifecycle='waiting',updated_at=? WHERE task_id=? AND lifecycle NOT IN ('completed','cancelled')`, x.Now.UnixMilli(), r.TaskID); err != nil {
			return Outcome{}, err
		}
	}
	if err := settleStoppedManagerRequests(ctx, tx, run); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: map[string]any{"run_id": run}, Events: []Event{{ObjectType: "run", ObjectID: run, Kind: "run.manager_stopped", Body: map[string]any{"reason": reason, "last_sequence": lastSequence}}}}, nil
}

// Process exit rejects commands that were never claimed, while retaining the
// uncertain identity of claimed commands. Every blocked caller gets a receipt.
func settleStoppedManagerRequests(ctx context.Context, tx *store.Tx, run string) error {
	ids, err := store.FollowupIDs(ctx, tx.Q(), `SELECT request_id FROM manager_native_requests WHERE run_id=? AND status IN ('pending','started')`, run)
	if err != nil {
		return err
	}
	for _, id := range ids {
		var status string
		if err := tx.Q().QueryRowContext(ctx, `SELECT status FROM manager_native_requests WHERE request_id=? AND run_id=?`, id, run).Scan(&status); err != nil {
			return err
		}
		unclaimed := status == "pending"
		raw, err := json.Marshal(protocol.ManagerNativeReply{ID: id, Error: "Native process stopped; reconcile the original operation before continuing", Rejected: unclaimed})
		if err != nil {
			return err
		}
		next := "unknown"
		if unclaimed {
			next = "completed"
		}
		if _, err = tx.Exec(ctx, `UPDATE manager_native_requests SET status=?,result_json=?,completion_error='Native process stopped; reconcile the original operation before continuing',completion_rejected=?,runner_acked=CASE WHEN ? THEN 1 ELSE runner_acked END WHERE request_id=?`, next, string(raw), unclaimed, unclaimed, id); err != nil {
			return err
		}
	}
	return nil
}

// managerNativeObservationMethod never submits input or changes execution state.
// Its unknown result remains recorded without invalidating an independent stop.
func managerNativeObservationMethod(method string) bool {
	return method == "models" || method == "find" || method == "usage"
}

func managerNativeReceiptMethod(method string) bool {
	switch method {
	case "models", "resume", "find", "usage", "interrupt", "close", "tool_result", "release":
		return true
	}
	return false
}
func managerNativeBusinessAllowed(ctx context.Context, q store.Querier, r store.RunRow) (bool, error) {
	if r.PauseHandoff != nil {
		return false, nil
	}
	if fenced, err := maintenanceNativeBusinessFenced(ctx, q, r); err != nil || fenced {
		return false, err
	}
	var current bool
	err := q.QueryRowContext(ctx, `SELECT m.state='assigned' AND i.assignment_epoch=m.assignment_epoch AND i.state IN ('active','cancelling') AND i.assignment_state='active' AND t.control_mode='managed' AND t.lifecycle NOT IN ('completed','cancelled') AND a.status='dispatched' AND COALESCE(json_extract(a.trigger_json,'$.receipt_only'),0)=0 AND r.desired_state='run' AND r.observed_state IN ('starting','running','waiting') FROM runs r JOIN issue_manager_tasks m USING(task_id) JOIN tasks t USING(task_id) JOIN plan_actions a ON a.action_id=r.action_id JOIN issue_controls i ON i.issue_id=m.issue_id WHERE r.run_id=?`, r.RunID).Scan(&current)
	if err != nil || !current {
		return current, err
	}
	closing, err := managerRunClosing(ctx, q, r.RunID)
	if err != nil || closing {
		return false, err
	}
	return store.IssueManagerAuthorizationValid(ctx, q, r.TaskID)
}

// These receipts can settle only the same admitted turn after its Issue closes.
// HostedCoordinationCommand still checks thread, execution, assignment and turn
// identity; no new admission, task, delegation or lifecycle mutation is allowed.
func closedIssueReceiptOperation(operation string) bool {
	switch operation {
	case "issues.review_context", "conversation.reply", "conversation.publish", "conversation.notify", "conversation.commit_turn", "attentions.create":
		return true
	}
	return false
}
