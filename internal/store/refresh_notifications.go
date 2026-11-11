package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// RefreshKind identifies notices whose payload is rebuilt from current SoT.
// Business transitions, authorization and results never belong in this set.
func RefreshKind(kind string) bool {
	switch kind {
	case "token_usage.changed", "storage.originals_changed", "host.runtime_changed":
		return true
	}
	return false
}

// nextStreamCursor shares ordering with business events without recording a
// permanent refresh history. It is called only inside the single writer.
func (t *Tx) nextStreamCursor(ctx context.Context) (int64, error) {
	var seq int64
	if err := t.Q().QueryRowContext(ctx, `UPDATE refresh_stream_state SET cursor=cursor+1 WHERE singleton=1 RETURNING cursor`).Scan(&seq); err != nil {
		return 0, fmt.Errorf("store: advance refresh stream: %w", err)
	}
	if _, err := t.Exec(ctx, `UPDATE sqlite_sequence SET seq=MAX(seq,?) WHERE name='events'`, seq); err != nil {
		return 0, fmt.Errorf("store: reserve event cursor: %w", err)
	}
	return seq, nil
}

// RefreshCursorExpired means a removed refresh position cannot serve as a
// complete delta. Readers must rebuild current views from authoritative facts.
func RefreshCursorExpired(ctx context.Context, q Querier, after int64) (bool, error) {
	var expired bool
	err := q.QueryRowContext(ctx, `SELECT ?<expired_through OR ?>cursor FROM refresh_stream_state WHERE singleton=1`, after, after).Scan(&expired)
	return expired, err
}

func (t *Tx) advanceRefresh(ctx context.Context, e EventRow) (int64, error) {
	var previous int64
	err := t.Q().QueryRowContext(ctx, `SELECT event_seq FROM refresh_notifications WHERE kind=? AND object_type=? AND object_id=?`, e.Kind, e.ObjectType, e.ObjectID).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	seq, err := t.nextStreamCursor(ctx)
	if err != nil {
		return 0, err
	}
	if _, err := t.Exec(ctx, `UPDATE refresh_stream_state SET expired_through=MAX(expired_through,?) WHERE singleton=1`, previous); err != nil {
		return 0, err
	}
	_, err = t.Exec(ctx, `INSERT INTO refresh_notifications(kind,object_type,object_id,revision,event_seq,pending,changed_at) VALUES(?,?,?,1,?,1,?)
 ON CONFLICT(kind,object_type,object_id) DO UPDATE SET revision=revision+1,event_seq=excluded.event_seq,pending=1,changed_at=excluded.changed_at`, e.Kind, e.ObjectType, e.ObjectID, seq, e.CreatedAt)
	if err != nil {
		return 0, fmt.Errorf("store: coalesce refresh notice: %w", err)
	}
	return seq, nil
}

// AcknowledgeRefreshes clears pending delivery only through the witnessed
// cursor. A concurrent newer revision stays pending; no acknowledgement rows
// or processed notification history are created. Other readers use the current
// scoped version, or rebuild after an expired cursor.
func (t *Tx) AcknowledgeRefreshes(ctx context.Context, through int64) error {
	_, err := t.Exec(ctx, `UPDATE refresh_notifications SET pending=0 WHERE pending=1 AND event_seq<=?`, through)
	return err
}

// RunLogAt reads an immutable run fact by its actual business key, resolving
// the original body so retries can validate identity without a second ledger.
func RunLogAt(ctx context.Context, q Querier, runID string, sequence int64) (RunLogRow, bool, error) {
	row := RunLogRow{Seq: sequence}
	err := q.QueryRowContext(ctx, `SELECT COALESCE(native_id,''),kind,body_json,created_at FROM run_logs WHERE run_id=? AND seq=?`, runID, sequence).Scan(&row.NativeID, &row.Kind, &row.BodyJSON, &row.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return row, false, nil
	}
	if err != nil {
		return row, false, fmt.Errorf("store: read run log identity: %w", err)
	}
	row.BodyJSON, err = ResolveJSON(ctx, q, row.BodyJSON)
	return row, true, err
}

// IssueRevisionReason resolves canonical reasons through their authoritative
// control version; unique historical legacy reasons remain inline.
func IssueRevisionReason(ctx context.Context, q Querier, issue string, revision int64) (string, error) {
	var reason string
	err := q.QueryRowContext(ctx, `SELECT CASE WHEN w.reason_control_revision IS NULL THEN w.reason_json ELSE c.reason_json END
 FROM work_item_revisions w LEFT JOIN issue_control_revisions c ON c.issue_id=w.work_item_id AND c.revision=w.reason_control_revision
 WHERE w.work_item_id=? AND w.revision=?`, issue, revision).Scan(&reason)
	if err != nil {
		return "", notFound(err, "Issue revision reason")
	}
	return reason, nil
}
