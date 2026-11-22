package store

import (
	"context"
	"errors"
	"fmt"
)

// Outbox delivery states.
const (
	OutboxPending   = "pending"
	OutboxSending   = "sending"
	OutboxDelivered = "delivered"
	OutboxFailed    = "failed"
	OutboxUnknown   = "unknown"
)

// ErrStale is returned when a conditional update finds that the row no
// longer is in the expected state.
var ErrStale = errors.New("store: row changed concurrently")

// OutboxMessage is an outbox row together with its delivery state.
type OutboxMessage struct {
	OutboxRow
	Status        string
	Attempts      int64
	NextAttemptAt int64
}

const outboxColumns = `outbox_id, request_id, COALESCE(parent_request_id, ''), destination, operation, payload_json,
	status, attempts, next_attempt_at, created_at`

func scanOutbox(row interface{ Scan(...any) error }) (OutboxMessage, error) {
	var m OutboxMessage
	err := row.Scan(&m.OutboxID, &m.RequestID, &m.ParentRequestID, &m.Destination, &m.Operation, &m.PayloadJSON,
		&m.Status, &m.Attempts, &m.NextAttemptAt, &m.CreatedAt)
	return m, err
}

// GetOutboxByRequest reads one original delivery independently of queue priority.
func GetOutboxByRequest(ctx context.Context, q Querier, request string) (OutboxMessage, error) {
	m, err := scanOutbox(q.QueryRowContext(ctx, `SELECT `+outboxColumns+` FROM outbox WHERE request_id=?`, request))
	if err != nil {
		return OutboxMessage{}, notFound(err, "outbox request")
	}
	return m, nil
}

// HeadOutbox returns the first message, in commit order, of a destination
// that still awaits delivery, whether or not it is due. Failed and unknown messages do not
// hold back the ones behind them.
func HeadOutbox(ctx context.Context, q Querier, destination string) (OutboxMessage, error) {
	return HeadOutboxPrioritized(ctx, q, destination, "")
}

const outboxPriorityOrder = ` ORDER BY CASE WHEN status = 'sending' THEN 0 ELSE 1 END,
        CASE WHEN operation='conversation.compact' THEN -1 WHEN operation='conversation.repair_turn' THEN 2 WHEN operation = ? THEN 1 ELSE 0 END, seq`

// PendingOutboxServiceable excludes redundant starts and recovery wakes whose
// original authority or sole successor is gone. In-flight rows still reconcile.
const PendingOutboxServiceable = `(NOT ` + IssueOwnerConsumptionMatchesOutbox + ` OR EXISTS(
 SELECT 1 FROM main_turns t WHERE t.request_id=o.request_id AND t.status<>'rejected'))
 AND NOT EXISTS(
 SELECT 1 FROM manager_transport_recoveries c
 WHERE c.wake_request_id=o.request_id AND NOT EXISTS(
  SELECT 1 FROM plan_actions p JOIN tasks t ON t.task_id=p.task_id
  JOIN issue_manager_tasks m ON m.task_id=p.task_id
  JOIN issue_controls i ON i.issue_id=m.issue_id JOIN work_items w ON w.work_item_id=i.issue_id
  JOIN main_turns mt ON mt.request_id=c.original_execution_id
  LEFT JOIN conversation_message_controls mc ON mc.message_id=mt.message_id
  LEFT JOIN logical_turn_executions le ON le.request_id=mt.request_id
  LEFT JOIN logical_turns lt ON lt.logical_turn_id=le.logical_turn_id
  WHERE p.task_id=c.task_id AND p.generation=c.successor_generation AND p.status IN ('pending','dispatched')
  AND t.control_revision=c.task_revision AND w.revision=c.issue_revision
  AND i.assignment_epoch=c.assignment_epoch AND i.assignment_state='active'
  AND i.state='active' AND i.work_status<>'backlog' AND COALESCE(mc.withdrawn_at,0)=0
  AND (lt.logical_turn_id IS NULL OR (lt.resumed_by IS NULL AND lt.status NOT IN ('stopped','awaiting_clarification','unknown')))
  AND NOT EXISTS(SELECT 1 FROM manager_native_requests n WHERE n.run_id=c.original_run_id
   AND n.status IN ('pending','started','unknown') AND n.method NOT IN ('models','find','usage'))))`

// HeadOutboxPrioritized keeps in-flight work first, then foreground work,
// then backgroundOperation. Each group retains commit order. An empty
// backgroundOperation preserves the normal destination FIFO.
func HeadOutboxPrioritized(ctx context.Context, q Querier, destination, backgroundOperation string) (OutboxMessage, error) {
	m, err := scanOutbox(q.QueryRowContext(ctx, `SELECT `+outboxColumns+` FROM outbox o WHERE destination=? AND (status='sending' OR (status='pending' AND `+PendingOutboxServiceable+`))`+outboxPriorityOrder+` LIMIT 1`, destination, backgroundOperation))
	if err != nil {
		return OutboxMessage{}, notFound(err, "outbox head")
	}
	return m, nil
}

// ListOutboxByStatus returns the messages in a delivery state in commit order.
func ListOutboxByStatus(ctx context.Context, q Querier, status string) ([]OutboxMessage, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+outboxColumns+` FROM outbox WHERE status = ? ORDER BY seq`, status)
	if err != nil {
		return nil, fmt.Errorf("store: list outbox: %w", err)
	}
	defer rows.Close()
	var out []OutboxMessage
	for rows.Next() {
		m, err := scanOutbox(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan outbox: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// OutboxTransition moves a message from one delivery state to another.
type OutboxTransition struct {
	OutboxID string
	// From and FromAttempts are the state the caller last saw; the update
	// applies only if the row is still in it.
	From          string
	FromAttempts  int64
	To            string
	Attempts      int64
	NextAttemptAt int64
	Now           int64
}

// TransitionOutbox applies a conditional state change. It returns ErrStale
// when the row has moved on since the caller read it.
func (t *Tx) TransitionOutbox(ctx context.Context, tr OutboxTransition) error {
	res, err := t.Exec(ctx, `UPDATE outbox SET status = ?, attempts = ?, next_attempt_at = ?, updated_at = ?
		WHERE outbox_id = ? AND status = ? AND attempts = ?`,
		tr.To, tr.Attempts, tr.NextAttemptAt, tr.Now, tr.OutboxID, tr.From, tr.FromAttempts)
	if err != nil {
		return fmt.Errorf("store: transition outbox: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: transition outbox: %w", err)
	}
	if n == 0 {
		return ErrStale
	}
	return nil
}

// ListOutboxPrioritized returns the same priority order as HeadOutboxPrioritized
// so canonical admission can skip an unavailable new request for a frozen continuation.
func ListOutboxPrioritized(ctx context.Context, q Querier, destination, backgroundOperation string) ([]OutboxMessage, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+outboxColumns+` FROM outbox o WHERE destination=? AND (status='sending' OR (status='pending' AND `+PendingOutboxServiceable+`))`+outboxPriorityOrder, destination, backgroundOperation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []OutboxMessage
	for rows.Next() {
		m, err := scanOutbox(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}
