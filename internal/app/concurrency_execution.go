package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func concurrencyRunSource(ctx context.Context, q store.Querier, run string, p protocol.RunExecutionPin) (protocol.ConcurrencySource, error) {
	s := protocol.ConcurrencySource{ExecutionID: "run:" + run, RunID: run, SessionID: "run:" + run, EnvironmentID: p.Identity.ExecutionEnvironmentID, RuntimeID: p.Runtime.RuntimeID, AgentID: p.Identity.LocalID, Attempt: "run"}
	r, err := store.GetRun(ctx, q, run)
	if err != nil {
		return s, err
	}
	s.TaskID = r.TaskID
	s.Purpose = string(r.Role)
	responsibility, err := store.RunIssueResponsibility(ctx, q, run)
	if err == nil {
		s.IssueID = responsibility.IssueID
	} else if !errors.Is(err, store.ErrNotFound) {
		return s, err
	}
	if r.Role == protocol.RoleIssueManager {
		manager, err := store.GetIssueManagerTask(ctx, q, r.TaskID)
		if err != nil {
			return s, err
		}
		s.IssueID = manager.IssueID
		s.ConversationID = manager.ConversationID
	}
	if s.IssueID == "" {
		s.IssueID, s.IssueTitle, err = store.TaskIssueLabel(ctx, q, r.TaskID)
		if err != nil {
			return s, err
		}
	}
	if s.IssueID != "" && s.IssueTitle == "" {
		var title string
		err = q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(body_json,'$.title'),'') FROM work_items WHERE work_item_id=?`, s.IssueID).Scan(&title)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return s, err
		}
		s.IssueTitle = title
	}
	return s, nil
}
func concurrencyHostedSource(ctx context.Context, q store.Querier, request string, selection hostedconfig.Selection) (protocol.ConcurrencySource, error) {
	s := protocol.ConcurrencySource{ExecutionID: "hosted:" + request, SessionID: "hosted:" + request, EnvironmentID: selection.Identity.ExecutionEnvironmentID, RuntimeID: selection.Runtime.ID, AgentID: selection.Identity.LocalID, IssueID: selection.IssueID, Attempt: request, Purpose: string(selection.Purpose)}
	if selection.RunnerRunID != "" {
		p, err := store.GetRunExecution(ctx, q, selection.RunnerRunID)
		if err != nil {
			return s, err
		}
		return concurrencyRunSource(ctx, q, selection.RunnerRunID, *p)
	}
	execution, err := store.GetHostedExecution(ctx, q, request)
	if err == nil {
		s.ConversationID = execution.ConversationID
		s.IssueID = execution.IssueID
	} else if !errors.Is(err, store.ErrNotFound) {
		return s, err
	}
	if s.IssueID != "" {
		err = q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(body_json,'$.title'),'') FROM work_items WHERE work_item_id=?`, s.IssueID).Scan(&s.IssueTitle)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return s, err
		}
	}
	return s, nil
}

// syncConcurrencyExecutions adopts preexisting executions, and derives closure
// from the original ledger's terminal proof rather than an elapsed permit TTL.
func syncConcurrencyExecutions(ctx context.Context, tx *store.Tx, now int64) error {

	rows, err := tx.Q().QueryContext(ctx, `SELECT r.run_id,e.pin_json,r.observed_state,COALESCE(h.last_seen_at,0),COALESCE(json_extract(a.trigger_json,'$.receipt_only'),0) FROM runs r JOIN run_agent_executions e USING(run_id) LEFT JOIN plan_actions a ON a.action_id=r.action_id LEFT JOIN hosts h ON h.host_id=r.host_id WHERE r.observed_state NOT IN ('finished','failed') OR EXISTS(SELECT 1 FROM concurrency_executions c WHERE c.execution_id='run:'||r.run_id AND c.state<>'stopped')`)
	if err != nil {
		return err
	}
	type runFact struct {
		id, raw, state string
		seen           int64
		receiptOnly    bool
	}
	var facts []runFact
	for rows.Next() {
		var f runFact
		if err := rows.Scan(&f.id, &f.raw, &f.state, &f.seen, &f.receiptOnly); err != nil {
			rows.Close()
			return err
		}
		facts = append(facts, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, f := range facts {
		var pin protocol.RunExecutionPin
		if err := json.Unmarshal([]byte(f.raw), &pin); err != nil {
			return err
		}
		source, err := concurrencyRunSource(ctx, tx.Q(), f.id, pin)
		if err != nil {
			return err
		}
		state := "reserved"
		switch f.state {
		case "finished", "failed":
			state = "stopped"
		case "running":
			state = "running"
		case "unknown", "interrupted":
			state = "unknown"
		}
		if f.receiptOnly {
			state = "stopped"
		}
		if err := putConcurrencyExecutionObserved(ctx, tx, source, state, f.seen, now); err != nil {
			return err
		}
	}
	rows, err = tx.Q().QueryContext(ctx, `SELECT t.request_id,h.private_snapshot,t.thread_id,COALESCE(t.turn_id,''),t.status,t.updated_at,EXISTS(SELECT 1 FROM native_turn_receipts_pending p WHERE p.request_id=t.request_id) FROM main_turns t JOIN hosted_executions h ON h.execution_id=t.request_id WHERE t.status IN ('starting','started','unknown') OR EXISTS(SELECT 1 FROM native_turn_receipts_pending p WHERE p.request_id=t.request_id) OR EXISTS(SELECT 1 FROM concurrency_executions c WHERE c.execution_id='hosted:'||t.request_id AND c.state<>'stopped')`)
	if err != nil {
		return err
	}
	type hostedFact struct {
		id, raw, thread, turn, status string
		seen                          int64
		pending                       bool
	}
	var hosted []hostedFact
	for rows.Next() {
		var f hostedFact
		if err := rows.Scan(&f.id, &f.raw, &f.thread, &f.turn, &f.status, &f.seen, &f.pending); err != nil {
			rows.Close()
			return err
		}
		hosted = append(hosted, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, f := range hosted {
		var sel hostedconfig.Selection
		if err := json.Unmarshal([]byte(f.raw), &sel); err != nil {
			return err
		}
		if sel.RunnerRunID != "" {
			continue
		}
		source, err := concurrencyHostedSource(ctx, tx.Q(), f.id, sel)
		if err != nil {
			return err
		}
		source.NativeSessionID = f.thread
		source.TurnID = f.turn
		state := "stopped"
		switch f.status {
		case "starting":
			state = "reserved"
		case "started":
			state = "running"
		case "unknown":
			state = "unknown"
		}
		seen := f.seen
		if f.status == "unknown" || f.pending {
			state = "unknown"
		}
		if err := putConcurrencyExecutionObserved(ctx, tx, source, state, seen, now); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE concurrency_executions SET state='stopped',last_seen=? WHERE execution_id LIKE 'aux:%' AND (parent_execution_id IN (SELECT 'run:'||run_id FROM runs WHERE observed_state IN ('finished','failed')) OR json_extract(source_json,'$.run_id') IN (SELECT run_id FROM runs WHERE observed_state IN ('finished','failed'))) AND state<>'stopped'`, now)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE concurrency_executions SET group_id=COALESCE((SELECT group_id FROM concurrency_bindings b WHERE b.environment_id=concurrency_executions.environment_id AND b.runtime_id=concurrency_executions.runtime_id),'') WHERE state<>'stopped'`)
	return err
}

// ObserveConcurrencyActivity records live native feedback, never a stored snapshot.
// It cannot restore an unknown execution or release an occupied reservation.
func (a *App) ObserveConcurrencyActivity(ctx context.Context, conversation, thread, turn string) error {
	if thread == "" {
		return nil
	}
	return a.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE concurrency_executions SET last_seen=MAX(last_seen,?) WHERE state='running' AND json_extract(source_json,'$.conversation_id')=? AND json_extract(source_json,'$.native_session_id')=? AND (?='' OR json_extract(source_json,'$.turn_id')=? OR execution_id LIKE 'aux:compact:%')`, a.now().UnixMilli(), store.ConversationID(conversation), thread, turn, turn)
		return err
	})
}
func concurrencyClaimHost(ctx context.Context, tx *store.Tx, h *scheduler.Host, r *protocol.RunnerRuntime, now int64) error {
	if err := syncConcurrencyExecutions(ctx, tx, now); err != nil {
		return err
	}
	h.ConcurrencyBlocked = map[string]bool{}
	ids, err := concurrencyIDs(ctx, tx.Q())
	if err != nil {
		return err
	}
	for _, id := range ids {
		bindings, err := concurrencyBindings(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		for _, b := range bindings {
			if b.EnvironmentID != h.ID {
				continue
			}
			available, err := concurrencyAvailable(ctx, tx.Q(), b.EnvironmentID, b.RuntimeID, "", now)
			if err != nil {
				return err
			}
			h.ConcurrencyBlocked[b.RuntimeID] = !available || r == nil || !slices.Contains(r.Capabilities, protocol.CapabilityAdaptiveConcurrency)
		}
	}
	return nil
}
func (a *App) concurrencyAdmitHosted(ctx context.Context, tx *store.Tx, m store.MainTurn, now int64) error {
	execution, err := store.GetHostedExecution(ctx, tx.Q(), m.RequestID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if execution.Selection.RunnerRunID != "" {
		if m.Status == store.TurnStarting {
			var borrowed bool
			if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM concurrency_executions WHERE parent_execution_id=? AND state<>'stopped')`, "run:"+execution.Selection.RunnerRunID).Scan(&borrowed); err != nil {
				return err
			}
			if borrowed {
				return ErrConcurrencyCapacity
			}
		}
		return nil
	}
	source, err := concurrencyHostedSource(ctx, tx.Q(), m.RequestID, execution.Selection)
	if err != nil {
		return err
	}
	group, err := concurrencyBinding(ctx, tx.Q(), source.EnvironmentID, source.RuntimeID)
	if err != nil || group == "" {
		return err
	}
	if m.Status == store.TurnStarting {
		ok, err := concurrencyAvailable(ctx, tx.Q(), source.EnvironmentID, source.RuntimeID, source.ExecutionID, now)
		if err != nil {
			return err
		}
		if !ok {
			return ErrConcurrencyCapacity
		}
	}
	state := "stopped"
	switch m.Status {
	case store.TurnStarting:
		state = "reserved"
	case store.TurnStarted:
		state = "running"
	}
	var pending bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM native_turn_receipts_pending WHERE request_id=?)`, m.RequestID).Scan(&pending); err != nil {
		return err
	}
	if pending {
		state = "unknown"
	}
	source.NativeSessionID = m.ThreadID
	source.TurnID = m.TurnID
	if err := putConcurrencyExecution(ctx, tx, source, state, now); err != nil {
		return err
	}
	f := protocol.ConcurrencyFeedback{SessionID: m.ThreadID, TurnID: m.TurnID, Attempt: m.RequestID, Failure: m.Failure}
	if m.Status == store.TurnCompleted {
		f.Kind = "success"
		f.Failure = nil
	}
	if f.Kind != "" || f.Failure != nil {
		return recordConcurrencyFeedback(ctx, tx, source, f, now)
	}
	return nil
}
func (a *App) RecordHostedConcurrencyEvent(ctx context.Context, request string, event protocol.ConcurrencyFeedback) error {
	if request == "" {
		return a.store.Write(ctx, func(tx *store.Tx) error {
			var raw string
			err := tx.Q().QueryRowContext(ctx, `SELECT e.source_json FROM concurrency_executions e JOIN agent_compactions c ON e.execution_id='aux:compact:'||c.operation_id WHERE c.thread_id=? AND c.status IN ('starting','running','unknown') AND e.state<>'stopped'`, event.SessionID).Scan(&raw)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			var source protocol.ConcurrencySource
			if err := json.Unmarshal([]byte(raw), &source); err != nil {
				return err
			}
			event.Attempt = source.Attempt
			return recordConcurrencyFeedback(ctx, tx, source, event, a.now().UnixMilli())
		})
	}
	return a.store.Write(ctx, func(tx *store.Tx) error {
		execution, err := store.GetHostedExecution(ctx, tx.Q(), request)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		source, err := concurrencyHostedSource(ctx, tx.Q(), request, execution.Selection)
		if err != nil {
			return err
		}
		event.Attempt = request
		if source.RunID != "" {
			event.Attempt = "run"
		}
		return recordConcurrencyFeedback(ctx, tx, source, event, a.now().UnixMilli())
	})
}
func concurrencyRunFeedback(ctx context.Context, tx *store.Tx, run store.RunRow, e protocol.RunEvent, now int64) error {
	if e.Kind != "worker.concurrency" {
		return nil
	}
	var receiptOnly bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT COALESCE(json_extract(trigger_json,'$.receipt_only'),0) FROM plan_actions WHERE action_id=?`, run.ActionID).Scan(&receiptOnly); err != nil {
		return err
	}
	if receiptOnly {
		return nil
	}

	pin, err := store.GetRunExecution(ctx, tx.Q(), run.RunID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var f protocol.ConcurrencyFeedback
	if err := json.Unmarshal(e.Body, &f); err != nil {
		return protocol.Errorf(protocol.CodeInvalidRequest, "invalid concurrency feedback")
	}
	if f.Kind != "success" && f.Kind != "running" && f.Kind != "congestion" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "invalid concurrency feedback kind")
	}
	if f.SessionID == "" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "native session identity required")
	}
	source, err := concurrencyRunSource(ctx, tx.Q(), run.RunID, *pin)
	if err != nil {
		return err
	}
	source.NativeSessionID = f.SessionID
	source.TurnID = f.TurnID
	state := "reserved"
	switch run.ObservedState {
	case protocol.RunRunning:
		state = "running"
	case protocol.RunUnknown, protocol.RunInterrupted:
		state = "unknown"
	case protocol.RunFinished, protocol.RunFailed:
		state = "stopped"
	}
	if f.Kind == "running" && state == "reserved" {
		state = "running"
	}
	if err := putConcurrencyExecution(ctx, tx, source, state, now); err != nil {
		return err
	}
	if f.Kind == "running" {
		return nil
	}
	if f.Attempt == "" {
		f.Attempt = "run"
	}
	return recordConcurrencyFeedback(ctx, tx, source, f, now)
}

func (a *App) updateConcurrencyWaiting(ctx context.Context, tx *store.Tx) error {
	waiting, err := views.ConcurrencyWaiting(ctx, tx.Q(), a.now().UnixMilli())
	if err != nil {
		return err
	}
	rows, err := tx.Q().QueryContext(ctx, `SELECT b.group_id,count(*) FROM concurrency_waiters w JOIN concurrency_bindings b USING(environment_id,runtime_id) JOIN outbox o ON o.request_id=w.request_id WHERE w.last_seen>=? AND o.status IN ('pending','sending') GROUP BY b.group_id`, a.now().UnixMilli()-30000)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		var n int64
		if err := rows.Scan(&id, &n); err != nil {
			rows.Close()
			return err
		}
		waiting[id] += n
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	ids, err := concurrencyIDs(ctx, tx.Q())
	if err != nil {
		return err
	}
	for _, id := range ids {
		s, err := concurrencyState(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		s.Group.Waiting = waiting[id]
		if err := saveConcurrency(ctx, tx, s); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) noteConcurrencyWait(ctx context.Context, m store.MainTurn) error {
	if m.Hosted == nil {
		return nil
	}
	return a.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO concurrency_waiters(request_id,environment_id,runtime_id,last_seen) VALUES(?,?,?,?) ON CONFLICT(request_id) DO UPDATE SET last_seen=excluded.last_seen`, m.RequestID, m.Hosted.Identity.ExecutionEnvironmentID, m.Hosted.Runtime.ID, a.now().UnixMilli())
		return err
	})
}
