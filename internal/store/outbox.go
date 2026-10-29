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

// HeadOutbox returns the first message, in commit order, of a destination
// that still awaits delivery, whether or not it is due. Failed and unknown messages do not
// hold back the ones behind them.
func HeadOutbox(ctx context.Context, q Querier, destination string) (OutboxMessage, error) {
	return HeadOutboxPrioritized(ctx, q, destination, "")
}

const outboxPriorityOrder = ` ORDER BY CASE WHEN status = 'sending' THEN 0 ELSE 1 END,
        CASE WHEN operation='conversation.compact' THEN -1 WHEN operation='conversation.repair_turn' THEN 2 WHEN operation = ? THEN 1 ELSE 0 END, seq`

// HeadOutboxPrioritized keeps in-flight work first, then foreground work,
// then backgroundOperation. Each group retains commit order. An empty
// backgroundOperation preserves the normal destination FIFO.
func HeadOutboxPrioritized(ctx context.Context, q Querier, destination, backgroundOperation string) (OutboxMessage, error) {
	m, err := scanOutbox(q.QueryRowContext(ctx, `SELECT `+outboxColumns+` FROM outbox WHERE destination=? AND status IN ('pending','sending')`+outboxPriorityOrder+` LIMIT 1`, destination, backgroundOperation))
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
	rows, err := q.QueryContext(ctx, `SELECT `+outboxColumns+` FROM outbox WHERE destination=? AND status IN ('pending','sending')`+outboxPriorityOrder, destination, backgroundOperation)
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
