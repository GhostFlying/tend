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

// Original receipts are read through the host control channel after physical
// close. They neither acquire a lease nor create another business execution.
func managerHistoryRun(ctx context.Context, q store.Querier, task string) (string, error) {
	var run string
	err := q.QueryRowContext(ctx, `SELECT r.run_id FROM runs r WHERE r.task_id=? AND r.role='issue_manager' AND r.observed_state IN ('finished','failed') AND r.native_closed_at>0 AND r.native_close_activity=r.native_activity AND (EXISTS(SELECT 1 FROM manager_native_events e WHERE e.run_id=r.run_id AND e.applied=0) OR EXISTS(SELECT 1 FROM hosted_executions h JOIN native_turn_receipts_pending p ON p.request_id=h.execution_id WHERE json_extract(h.private_snapshot,'$.runner_run_id')=r.run_id)) ORDER BY r.created_at DESC LIMIT 1`, task).Scan(&run)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return run, err
}

func managerHistoryActive(ctx context.Context, q store.Querier, run string) (bool, error) {
	r, err := store.GetRun(ctx, q, run)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	original, err := managerHistoryRun(ctx, q, r.TaskID)
	return original == run, err
}

func (a *App) ManagerTransportEnded(ctx context.Context, run string) (bool, error) {
	ended, err := a.ManagerRunEnded(ctx, run)
	if err != nil || !ended {
		return ended, err
	}
	err = a.store.Read(ctx, func(tx *store.ReadTx) error {
		active, e := managerHistoryActive(ctx, tx.Q(), run)
		ended = !active
		return e
	})
	return ended, err
}

// A confirmed retirement lets only the accepting journal read the original
// native history. An overlapping live predecessor continues to serve its own.
func canReadHistoryJournal(ctx context.Context, q store.Querier, host, reader, owner string) (bool, error) {
	if reader == owner {
		return true, nil
	}
	var allowed bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runner_retirements r JOIN host_runtime h USING(host_id)
 WHERE r.host_id=? AND r.journal_id=? AND r.confirmed_at IS NOT NULL AND json_extract(h.runtime_json,'$.journal_id')=?)`, host, owner, reader).Scan(&allowed)
	return allowed, err
}

// Snapshot volatile read calls without holding the broker mutex during SQLite
// reads. Repeating a history read is safe; the original native send is untouched.
func (a *App) managerHistoryExchange(ctx context.Context, q store.Querier, host, journal string, replies []protocol.ManagerHistoryReply) ([]protocol.ManagerHistoryRead, []string, error) {
	a.nativeCalls.mu.Lock()
	a.nativeCalls.prune()
	calls := make(map[string]nativePendingCall, len(a.nativeCalls.calls))
	for id, call := range a.nativeCalls.calls {
		calls[id] = *call
	}
	a.nativeCalls.mu.Unlock()
	var reads []protocol.ManagerHistoryRead
	var acks []string
	for _, reply := range replies {
		call, ok := calls[reply.ID]
		if !ok {
			acks = append(acks, reply.ID)
			continue
		}
		r, err := store.GetRun(ctx, q, call.run)
		if err != nil {
			return nil, nil, err
		}
		owner, err := store.RunJournal(ctx, q, call.run)
		if err != nil {
			return nil, nil, err
		}
		allowed, err := canReadHistoryJournal(ctx, q, host, journal, owner)
		if err != nil {
			return nil, nil, err
		}
		if r.HostID != host || call.request.Method != "find" {
			return nil, nil, protocol.Errorf(protocol.CodeForbidden, "receipt belongs to another host or operation")
		}
		// A reader can lose admission while its observation is in flight.
		// Drop that local reply without consuming the original broker call.
		if allowed {
			a.nativeCalls.deliver(call.run, protocol.ManagerNativeReply{ID: reply.ID, Method: "find", Result: reply.Result, Error: reply.Error})
		}
		acks = append(acks, reply.ID)
	}
	for id, call := range calls {
		if call.consumed || call.reply != nil || call.request.Method != "find" || slices.Contains(acks, id) {
			continue
		}
		r, err := store.GetRun(ctx, q, call.run)
		if err != nil {
			return nil, nil, err
		}
		owner, err := store.RunJournal(ctx, q, call.run)
		if err != nil {
			return nil, nil, err
		}
		allowed, err := canReadHistoryJournal(ctx, q, host, journal, owner)
		if err != nil {
			return nil, nil, err
		}
		if !allowed || r.HostID != host {
			continue
		}
		active, err := managerHistoryActive(ctx, q, call.run)
		if err != nil {
			return nil, nil, err
		}
		if !active {
			continue
		}
		var input struct {
			ThreadID  string `json:"thread_id"`
			MessageID string `json:"message_id"`
			TurnID    string `json:"turn_id"`
		}
		if err := json.Unmarshal(call.request.Payload, &input); err != nil {
			return nil, nil, err
		}
		// The reader gets exactly the admitted original request, not arbitrary host history.
		var original bool
		err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM main_turns t JOIN hosted_executions h ON h.execution_id=t.request_id JOIN native_turn_receipts_pending p ON p.request_id=t.request_id WHERE json_extract(h.private_snapshot,'$.runner_run_id')=? AND t.thread_id=? AND CASE WHEN EXISTS(SELECT 1 FROM logical_turn_executions e WHERE e.request_id=t.request_id AND e.repair=1) THEN t.request_id ELSE t.message_id END=? AND COALESCE(t.turn_id,'')=?)`, call.run, input.ThreadID, input.MessageID, input.TurnID).Scan(&original)
		if err != nil {
			return nil, nil, err
		}
		if !original {
			return nil, nil, protocol.Errorf(protocol.CodeForbidden, "history read differs from original pending request")
		}
		pin, err := store.GetRunExecution(ctx, q, call.run)
		if err != nil {
			return nil, nil, err
		}
		m, err := store.GetIssueManagerTask(ctx, q, r.TaskID)
		if err != nil {
			return nil, nil, err
		}
		if pin == nil {
			return nil, nil, protocol.Errorf(protocol.CodeUnknownOutcome, "original history executor is missing")
		}
		read := protocol.ManagerHistoryRead{ID: id, RunID: call.run, ConversationID: m.ConversationID, Execution: *pin, ThreadID: input.ThreadID, MessageID: input.MessageID, TurnID: input.TurnID}
		if owner != journal {
			read.RetiredJournal = &owner
		}
		reads = append(reads, read)
	}
	return reads, acks, nil
}
