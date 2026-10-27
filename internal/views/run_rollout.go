package views

import (
	"context"
	"encoding/json"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"slices"
	"time"
)

// RunRollout returns either the newest window, its predecessor or its successor.
func RunRollout(ctx context.Context, q store.Querier, id string, before, after int64) (protocol.RunRolloutPage, error) {
	v := protocol.RunRolloutPage{RunID: id, Logs: []protocol.RunLogView{}}
	run, err := store.GetRun(ctx, q, id)
	if err != nil {
		return v, err
	}
	v.Observed = run.ObservedState
	if err = q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM run_logs WHERE run_id=?`, id).Scan(&v.Watermark); err != nil {
		return v, err
	}
	query := `SELECT seq,COALESCE(native_id,''),kind,body_json,created_at FROM run_logs WHERE run_id=?`
	args := []any{id}
	descending := after == 0
	if before > 0 {
		query += ` AND seq<?`
		args = append(args, before)
	} else if after > 0 {
		query += ` AND seq>?`
		args = append(args, after)
	}
	if descending {
		query += ` ORDER BY seq DESC LIMIT 101`
	} else {
		query += ` ORDER BY seq LIMIT 101`
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	for rows.Next() {
		var r protocol.RunLogView
		var body string
		var at int64
		if err = rows.Scan(&r.Seq, &r.NativeID, &r.Kind, &body, &at); err != nil {
			return v, err
		}
		r.Body = json.RawMessage(body)
		r.CreatedAt = time.UnixMilli(at)
		v.Logs = append(v.Logs, r)
	}
	if err = rows.Err(); err != nil {
		return v, err
	}
	if len(v.Logs) > 100 {
		v.Logs = v.Logs[:100]
	}
	if descending {
		slices.Reverse(v.Logs)
	}
	if len(v.Logs) > 0 {
		v.Before = v.Logs[0].Seq
		v.After = v.Logs[len(v.Logs)-1].Seq
	} else {
		v.After = after
		v.Before = before
	}
	if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_logs WHERE run_id=? AND seq<?),EXISTS(SELECT 1 FROM run_logs WHERE run_id=? AND seq>?)`, id, v.Before, id, v.After).Scan(&v.MoreBefore, &v.MoreAfter); err != nil {
		return v, err
	}
	return v, nil
}
