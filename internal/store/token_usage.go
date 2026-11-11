package store

import (
	"context"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

// UsageFactColumns and UsageFactDestinations share the typed ledger layout.
// alias must be a compile-time SQL alias of token_usage_fact_details.
func UsageFactColumns(alias string) string {
	fields := []string{"seq", "subject", "role", "received_at", "record_id", "provider", "native_content_hash", "native_version", "session_id", "ordinal", "model_source", "time_source", "counter_source", "purpose", "root_turn_id", "parent_session_id", "parent_thread_id", "source", "response_id", "thread_id", "turn_id", "revision", "settlement", "profile", "model", "occurred_at", "input", "cache_read", "cache_write", "output", "total"}
	for i := range fields {
		fields[i] = alias + "." + fields[i]
	}
	return strings.Join(fields, ",")
}

func UsageFactDestinations(r *protocol.TokenUsageRecord) []any {
	return []any{&r.Seq, &r.Subject, &r.Role, &r.ReceivedAt, &r.RecordID, &r.Provider, &r.NativeContentHash, &r.NativeVersion, &r.SessionID, &r.Ordinal, &r.ModelSource, &r.TimeSource, &r.CounterSource, &r.Purpose, &r.RootTurnID, &r.ParentSessionID, &r.ParentThreadID, &r.Source, &r.RequestID, &r.ThreadID, &r.TurnID, &r.Revision, &r.Settlement, &r.Profile, &r.Model, &r.OccurredAt, &r.Input, &r.CacheRead, &r.CacheWrite, &r.Output, &r.Total}
}

// InsertUsageFact stores one real measurement variant. Business deduplication
// compares the typed facts in the caller's transaction, not a request hash.
func InsertUsageFact(ctx context.Context, tx *Tx, s protocol.TokenUsageSample, subject, role string, receivedAt int64) (int64, error) {
	meta := []any{s.Provider, s.NativeVersion, s.SessionID, s.ModelSource, s.TimeSource, s.CounterSource, s.Purpose, s.ParentSessionID, s.ParentThreadID, s.Profile, s.Model, subject, role}
	const columns = `provider,native_version,session_id,model_source,time_source,counter_source,purpose,parent_session_id,parent_thread_id,profile,model,subject,role`
	if _, err := tx.Exec(ctx, `INSERT INTO token_usage_contexts(`+columns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, meta...); err != nil {
		return 0, err
	}
	var contextID int64
	if err := tx.Q().QueryRowContext(ctx, `SELECT context_id FROM token_usage_contexts WHERE (`+columns+`)=(?,?,?,?,?,?,?,?,?,?,?,?,?)`, meta...).Scan(&contextID); err != nil {
		return 0, err
	}
	identity := s.RequestID
	if s.RecordID != "" {
		identity = s.RecordID
	}
	r, err := tx.Exec(ctx, `INSERT INTO token_usage_facts(source,request_id,revision,context_id,record_id,response_id,thread_id,turn_id,root_turn_id,native_content_hash,ordinal,settlement,occurred_at,input,cache_read,cache_write,output,total,received_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, s.Source, identity, s.Revision, contextID, s.RecordID, s.RequestID, s.ThreadID, s.TurnID, s.RootTurnID, s.NativeContentHash, s.Ordinal, s.Settlement, s.OccurredAt, s.Input, s.CacheRead, s.CacheWrite, s.Output, s.Total, receivedAt)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}
