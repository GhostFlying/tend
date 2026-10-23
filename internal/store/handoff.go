package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// HandoffSettings is an independent owner override. Revision zero means default.
type HandoffSettings struct{ IdleMinutes, Revision int64 }

func GetHandoffSettings(ctx context.Context, q Querier) (HandoffSettings, error) {
	var v HandoffSettings
	err := q.QueryRowContext(ctx, `SELECT idle_minutes,revision FROM conversation_handoff_settings WHERE singleton=1`).Scan(&v.IdleMinutes, &v.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return v, err
}

// ThreadAncestor preserves the native history index through every handoff.
type ThreadAncestor struct {
	ThreadID    string `json:"thread_id"`
	RolloutPath string `json:"rollout_path"`
}

// MainThreadBinding enriches the authority in main_threads without replacing it.
type MainThreadBinding struct {
	Instance, ThreadID, ToolsetHash, RolloutPath string
	Generation                                   int64
	Ancestors                                    []ThreadAncestor
}

func GetMainThreadBinding(ctx context.Context, q Querier, instance string) (MainThreadBinding, error) {
	v := MainThreadBinding{Instance: instance}
	var ancestors string
	err := q.QueryRowContext(ctx, `SELECT m.thread_id,COALESCE(b.toolset_hash,''),COALESCE(b.path,''),COALESCE(b.generation,0),COALESCE(b.ancestors_json,'[]') FROM main_threads m LEFT JOIN main_thread_bindings b ON b.instance=m.instance AND b.thread_id=m.thread_id WHERE m.instance=?`, instance).Scan(&v.ThreadID, &v.ToolsetHash, &v.RolloutPath, &v.Generation, &ancestors)
	if err != nil {
		return v, err
	}
	err = json.Unmarshal([]byte(ancestors), &v.Ancestors)
	return v, err
}
func (t *Tx) PutMainThreadBinding(ctx context.Context, v MainThreadBinding, now int64) error {
	b, err := json.Marshal(v.Ancestors)
	if err != nil {
		return err
	}
	_, err = t.Exec(ctx, `INSERT INTO main_thread_bindings(instance,thread_id,toolset_hash,path,generation,ancestors_json,created_at) VALUES(?,?,?,?,?,?,?)`, v.Instance, v.ThreadID, v.ToolsetHash, v.RolloutPath, v.Generation, string(b), now)
	return err
}

// HandoffSnapshot captures a transactionally consistent content watermark.
type HandoffSnapshot struct {
	Instance            string `json:"instance"`
	ThreadID            string `json:"thread_id"`
	Version             int64  `json:"version"`
	MaxSeq              int64  `json:"max_seq"`
	LastCompletedTurnID string `json:"last_completed_turn_id"`
	LastActivityMillis  int64  `json:"last_activity_millis"`
	BindingGeneration   int64  `json:"binding_generation"`
	Pending             bool   `json:"pending"`
	Active              bool   `json:"active"`
}

// Covers excludes a changed conversation or binding, and any unhandled activity.
func (v HandoffSnapshot) Covers(other HandoffSnapshot) bool {
	return v.Instance == other.Instance && v.ThreadID == other.ThreadID && v.BindingGeneration == other.BindingGeneration && v.Version == other.Version && v.MaxSeq == other.MaxSeq && v.LastCompletedTurnID == other.LastCompletedTurnID && !v.Pending && !v.Active && !other.Pending && !other.Active
}
func GetHandoffSnapshot(ctx context.Context, q Querier, instance string) (HandoffSnapshot, error) {
	v := HandoffSnapshot{Instance: instance}
	binding, err := GetMainThreadBinding(ctx, q, instance)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return v, err
	}
	v.ThreadID, v.BindingGeneration = binding.ThreadID, binding.Generation
	err = q.QueryRowContext(ctx, `SELECT version,MAX(last_activity,COALESCE((SELECT updated_at FROM main_threads WHERE instance=?),0)),COALESCE((SELECT MAX(seq) FROM conversation_messages),0),EXISTS(SELECT 1 FROM outbox WHERE destination='main_thread' AND operation='main_thread.handle_message' AND status IN ('pending','sending')) FROM conversation_coverage WHERE singleton=1`, instance).Scan(&v.Version, &v.LastActivityMillis, &v.MaxSeq, &v.Pending)
	if err != nil {
		return v, err
	}
	v.Active, err = HasActiveMainTurn(ctx, q, v.ThreadID)
	if err != nil {
		return v, err
	}
	err = q.QueryRowContext(ctx, `SELECT turn_id FROM main_turns WHERE thread_id=? AND status='completed' AND turn_id IS NOT NULL ORDER BY updated_at DESC,request_id DESC LIMIT 1`, v.ThreadID).Scan(&v.LastCompletedTurnID)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return v, err
}

// HandoffSummary is immutable model prose tied to its exact input snapshot.
type HandoffSummary struct {
	ID, Instance                   string
	Snapshot                       HandoffSnapshot
	Body                           string
	FormatVersion, CreatedAtMillis int64
}

func GetHandoffSummary(ctx context.Context, q Querier, instance string) (HandoffSummary, error) {
	var v HandoffSummary
	var snapshot string
	err := q.QueryRowContext(ctx, `SELECT summary_id,instance,snapshot_json,body,format_version,created_at FROM conversation_summaries WHERE instance=? ORDER BY source_version DESC,format_version DESC,created_at DESC,summary_id DESC LIMIT 1`, instance).Scan(&v.ID, &v.Instance, &snapshot, &v.Body, &v.FormatVersion, &v.CreatedAtMillis)
	if err != nil {
		return v, err
	}
	err = json.Unmarshal([]byte(snapshot), &v.Snapshot)
	return v, err
}

// ThreadHandoff is a durable create/inject/switch intent.
type ThreadHandoff struct {
	ID, Instance, SourceThreadID, ToolsetHash, TargetThreadID, TargetRolloutPath, Prompt, Phase string
	SourceGeneration, SourceVersion, CreatedAtMillis, UpdatedAtMillis                           int64
}

func scanHandoff(row *sql.Row) (ThreadHandoff, error) {
	var v ThreadHandoff
	err := row.Scan(&v.ID, &v.Instance, &v.SourceThreadID, &v.SourceGeneration, &v.SourceVersion, &v.ToolsetHash, &v.TargetThreadID, &v.TargetRolloutPath, &v.Prompt, &v.Phase, &v.CreatedAtMillis, &v.UpdatedAtMillis)
	return v, err
}

const handoffColumns = `handoff_id,instance,source_thread_id,source_generation,source_version,toolset_hash,target_thread_id,target_path,prompt,phase,created_at,updated_at`

func GetThreadHandoff(ctx context.Context, q Querier, id string) (ThreadHandoff, error) {
	return scanHandoff(q.QueryRowContext(ctx, `SELECT `+handoffColumns+` FROM main_thread_handoffs WHERE handoff_id=?`, id))
}
func GetPendingHandoff(ctx context.Context, q Querier, instance string) (ThreadHandoff, error) {
	return scanHandoff(q.QueryRowContext(ctx, `SELECT `+handoffColumns+` FROM main_thread_handoffs WHERE instance=? AND phase<>'committed'`, instance))
}

// SummaryObservationIDs selects every required current item or deletion marker.
// A nil version requests the entire conversation; deltas include changed IDs
// even when their sequence did not change or the item was deleted.
func SummaryObservationIDs(ctx context.Context, q Querier, since *int64, limit int) ([]string, error) {
	query := `SELECT message_id FROM conversation_messages ORDER BY seq LIMIT ?`
	args := []any{limit}
	if since != nil {
		query = `SELECT c.message_id FROM conversation_changes c LEFT JOIN conversation_messages m ON m.message_id=c.message_id WHERE c.last_version>? ORDER BY COALESCE(m.seq,0),c.message_id LIMIT ?`
		args = []any{*since, limit}
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// HasActiveMainTurn excludes an abandoned uncertain start only when its exact
// delivery records Unknown. That outcome follows closing the old session;
// absent delivery evidence remains conservatively active.
func HasActiveMainTurn(ctx context.Context, q Querier, threadID string) (bool, error) {
	var active bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM main_turns t WHERE t.thread_id=? AND (t.status='started' OR (t.status='starting' AND NOT EXISTS(SELECT 1 FROM outbox o WHERE o.request_id=t.request_id AND o.destination='main_thread' AND o.operation='main_thread.handle_message' AND o.status='unknown'))))`, threadID).Scan(&active)
	return active, err
}
