package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

// RetirePublishedBackgroundTool removes a display cache only after the actual
// published ToolUse proves its contents. Arrival order and publication identity
// remain in background_replies; transcript readers use conversation_tool_uses.
func (t *Tx) RetirePublishedBackgroundTool(ctx context.Context, batch, thread, turn, item string) (bool, error) {
	var seq int64
	var raw, status string
	err := t.Q().QueryRowContext(ctx, `SELECT seq,tool_json,status FROM background_replies
		WHERE batch_id=? AND thread_id=? AND turn_id=? AND item_id=? AND kind='tool_use' AND published=1`, batch, thread, turn, item).Scan(&seq, &raw, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return t.retireBackgroundTool(ctx, seq, batch, thread, turn, item, status, raw)
}

func (t *Tx) retireBackgroundTool(ctx context.Context, seq int64, batch, thread, turn, item, status, raw string) (bool, error) {
	if raw == "" {
		return false, nil
	}
	full, err := ResolveJSON(ctx, t.Q(), raw)
	if err != nil {
		return false, err
	}
	// This is the existing seven-field display projection, not an arbitrary
	// provider record. Preserve any unrecognized fields or missing values.
	var fields map[string]json.RawMessage
	if err = json.Unmarshal([]byte(full), &fields); err != nil {
		return false, err
	}
	if len(fields) != 7 {
		return false, nil
	}
	p := make(map[string]string, 7)
	for _, key := range []string{"turn_id", "item_id", "reply_to", "tool", "input", "output", "status"} {
		value := fields[key]
		if len(value) == 0 || value[0] != '"' {
			return false, nil
		}
		var text string
		if err = json.Unmarshal(value, &text); err != nil {
			return false, err
		}
		p[key] = text
	}
	settledStart := p["status"] == string(protocol.ToolRunning) && status == string(protocol.ToolInterrupted)
	if p["turn_id"] != turn || p["item_id"] != item || p["status"] != status && !settledStart {
		return false, nil
	}
	var actualThread, tool, input, output, actualStatus, reply, parent string
	err = t.Q().QueryRowContext(ctx, `SELECT u.thread_id,u.tool,u.input,u.output,u.status,COALESCE(m.reply_to,''),b.message_id
		FROM conversation_tool_uses u JOIN conversation_messages m USING(message_id)
		JOIN background_batches b ON b.batch_id=? WHERE u.turn_id=? AND u.item_id=?`, batch, turn, item).
		Scan(&actualThread, &tool, &input, &output, &actualStatus, &reply, &parent)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if actualThread != thread || tool != p["tool"] || actualStatus != status || reply != p["reply_to"] || parent != reply {
		return false, nil
	}
	input, err = ResolveJSON(ctx, t.Q(), input)
	if err == nil {
		output, err = ResolveJSON(ctx, t.Q(), output)
	}
	if err == nil {
		p["input"], err = ResolveJSON(ctx, t.Q(), p["input"])
	}
	if err == nil {
		p["output"], err = ResolveJSON(ctx, t.Q(), p["output"])
	}
	if err != nil {
		return false, err
	}
	if !recoverableBackgroundText(p["input"], input) || !recoverableBackgroundText(p["output"], output) {
		return false, nil
	}
	if _, err = t.Exec(ctx, `UPDATE background_replies SET tool_json='' WHERE seq=? AND published=1 AND tool_json=?`, seq, raw); err != nil {
		return false, err
	}
	_, err = t.Exec(ctx, `DELETE FROM retained_body_migration_exceptions WHERE table_name='background_replies' AND column_name='tool_json' AND original_rowid=?`, seq)
	return err == nil, err
}

func recoverableBackgroundText(display, original string) bool {
	return display == original || exactClippedPrefix(display, original)
}

// migratePublishedBackgroundTools precedes ordinary body staging. Only this
// cache is retired, and no new original or filesystem copy is created. A failed
// read rolls back the migration rather than treating an unreadable body as empty.
func migratePublishedBackgroundTools(ctx context.Context, tx *Tx) error {
	var after int64
	changed := false
	for {
		rows, err := tx.Q().QueryContext(ctx, `SELECT seq,batch_id,thread_id,turn_id,item_id,status,tool_json
			FROM background_replies WHERE seq>? AND published=1 AND kind='tool_use' AND tool_json<>'' ORDER BY seq LIMIT 100`, after)
		if err != nil {
			return err
		}
		type cached struct {
			seq                                    int64
			batch, thread, turn, item, status, raw string
		}
		var page []cached
		for rows.Next() {
			var r cached
			if err = rows.Scan(&r.seq, &r.batch, &r.thread, &r.turn, &r.item, &r.status, &r.raw); err != nil {
				rows.Close()
				return err
			}
			page = append(page, r)
		}
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, r := range page {
			var retired bool
			if retired, err = tx.retireBackgroundTool(ctx, r.seq, r.batch, r.thread, r.turn, r.item, r.status, r.raw); err != nil {
				return fmt.Errorf("store: verify published background tool %d: %w", r.seq, err)
			}
			changed = changed || retired
			after = r.seq
		}
		if len(page) < 100 {
			if changed {
				_, err = tx.Exec(ctx, `UPDATE retained_body_migration SET revision=revision+1 WHERE singleton=1`)
			}
			return err
		}
	}
}
