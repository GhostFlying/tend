package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

// LatestNativeTerminal reads a retained legacy fact without changing or
// acknowledging it. New workers persist their outcome separately from events.
func (j *Journal) LatestNativeTerminal(ctx context.Context, run, thread string) (protocol.RunEvent, bool, error) {
	var event protocol.RunEvent
	var body string
	err := j.db.QueryRowContext(ctx, `SELECT seq,kind,body_json FROM events
 WHERE run_id=? AND kind='native.turn_completed' AND json_extract(body_json,'$.thread_id')=?
 ORDER BY seq DESC LIMIT 1`, run, thread).Scan(&event.Seq, &event.Kind, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return event, false, nil
	}
	if err != nil {
		return event, false, fmt.Errorf("journal: original native terminal: %w", err)
	}
	event.Body = []byte(body)
	return event, true, nil
}
