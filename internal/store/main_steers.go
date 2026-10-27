package store

import (
	"context"
	"fmt"
)

// MainSteer is an attempt to append an owner message to one fenced native turn.
type MainSteer struct {
	RequestID, MessageID, ThreadID, TurnID, InputText string
	Status, NativeItemID, Reason                      string
}

func GetMainSteer(ctx context.Context, q Querier, requestID string) (MainSteer, error) {
	m := MainSteer{RequestID: requestID}
	err := q.QueryRowContext(ctx, `SELECT message_id,thread_id,turn_id,input_text,status,native_item_id,reason FROM main_steers WHERE request_id=?`, requestID).Scan(&m.MessageID, &m.ThreadID, &m.TurnID, &m.InputText, &m.Status, &m.NativeItemID, &m.Reason)
	if err != nil {
		return m, notFound(err, "main steer")
	}
	return m, nil
}

func (t *Tx) PutMainSteer(ctx context.Context, m MainSteer, now int64) error {
	_, err := t.Exec(ctx, `INSERT INTO main_steers(request_id,message_id,thread_id,turn_id,input_text,status,native_item_id,reason,updated_at) VALUES(?,?,?,?,?,?,?,?,?)
 ON CONFLICT(request_id) DO UPDATE SET thread_id=excluded.thread_id,turn_id=excluded.turn_id,input_text=excluded.input_text,status=excluded.status,native_item_id=excluded.native_item_id,reason=excluded.reason,updated_at=excluded.updated_at`, m.RequestID, m.MessageID, m.ThreadID, m.TurnID, m.InputText, m.Status, m.NativeItemID, m.Reason, now)
	if err != nil {
		return fmt.Errorf("store: put main steer: %w", err)
	}
	return nil
}

// HeadSteer excludes the active delivery, preserves owner order, and respects
// earlier handoffs. Background facts are never selected as owner input.
func HeadSteer(ctx context.Context, q Querier, activeRequestID string) (OutboxMessage, error) {
	m, err := scanOutbox(q.QueryRowContext(ctx, `SELECT `+outboxColumns+` FROM outbox WHERE destination='main_thread' AND request_id<>? AND operation<>'main_thread.handle_event' AND status IN ('pending','sending') ORDER BY seq LIMIT 1`, activeRequestID))
	if err != nil {
		return m, notFound(err, "pending steer")
	}
	return m, nil
}

// MainExecutionRequest keeps review obligations on the native execution while
// its audit parent identifies a consumed owner supplement.
func MainExecutionRequest(ctx context.Context, q Querier, request string) (string, error) {
	var execution string
	err := q.QueryRowContext(ctx, `SELECT COALESCE((SELECT t.request_id FROM main_steers s JOIN main_turns t ON t.thread_id=s.thread_id AND t.turn_id=s.turn_id WHERE s.request_id=? AND s.native_item_id<>''),?)`, request, request).Scan(&execution)
	return execution, err
}
