package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// recordTokenUsage deduplicates native request facts within the caller's write
// transaction. Model and ownership are attributes, not deduplication keys.
func recordTokenUsage(ctx context.Context, tx *store.Tx, sample protocol.TokenUsageSample, subject, role string, now int64) error {
	if sample.Source == "" || sample.RequestID == "" || sample.ThreadID == "" || sample.Revision < 1 || (sample.Settlement != "final" && sample.Settlement != "provisional") {
		return protocol.Errorf(protocol.CodeInvalidRequest, "invalid request usage identity")
	}
	for _, n := range []*int64{sample.Input, sample.Output, sample.CacheRead, sample.CacheWrite, sample.Total} {
		if n != nil && (*n < 0 || *n > 1<<50) {
			return protocol.Errorf(protocol.CodeInvalidRequest, "invalid token counter")
		}
	}
	if sample.OccurredAt != nil && (*sample.OccurredAt < 0 || *sample.OccurredAt > now) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "invalid usage instant")
	}
	raw, err := json.Marshal(sample)
	if err != nil {
		return err
	}
	// Attribution is immutable provenance too: an identical native payload
	// reported under a different subject/role must survive deduplication so
	// the conflicting ownership is visible and cannot be silently accepted.
	hash := sha256.Sum256(append(append(append([]byte{}, raw...), []byte("\x00"+subject)...), []byte("\x00"+role)...))
	digest := hex.EncodeToString(hash[:])
	result, err := tx.Exec(ctx, `INSERT OR IGNORE INTO token_usage_facts(source,request_id,revision,payload_hash,payload_json,subject,role,received_at) VALUES(?,?,?,?,?,?,?,?)`, sample.Source, sample.RequestID, sample.Revision, digest, string(raw), subject, role, now)
	if err != nil {
		return err
	}
	fresh, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if fresh == 0 {
		return nil
	}
	seq, err := result.LastInsertId()
	if err != nil {
		return err
	}
	var oldRaw, oldSubject, oldRole string
	err = tx.Q().QueryRowContext(ctx, `SELECT f.payload_json,f.subject,f.role FROM token_usage_current c JOIN token_usage_facts f ON f.seq=c.fact_seq WHERE c.source=? AND c.request_id=?`, sample.Source, sample.RequestID).Scan(&oldRaw, &oldSubject, &oldRole)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		var old protocol.TokenUsageSample
		if err = json.Unmarshal([]byte(oldRaw), &old); err != nil {
			return fmt.Errorf("decode persisted usage: %w", err)
		}
		if oldSubject != subject || oldRole != role || (old.Revision == sample.Revision && oldRaw != string(raw)) {
			_, err = tx.Exec(ctx, `UPDATE token_usage_current SET conflict=1 WHERE source=? AND request_id=?`, sample.Source, sample.RequestID)
			return err
		}
		if sample.Revision <= old.Revision || (old.Settlement == "final" && sample.Settlement != "final") {
			return nil
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO token_usage_current(source,request_id,fact_seq) VALUES(?,?,?) ON CONFLICT(source,request_id) DO UPDATE SET fact_seq=excluded.fact_seq`, sample.Source, sample.RequestID, seq)
	return err
}

// RecordTokenUsage persists an observed request from a hosted native session.
// It is internal maintenance, not an owner-requested business operation.
func (a *App) RecordTokenUsage(ctx context.Context, conversation string, sample protocol.TokenUsageSample) error {
	return a.store.Write(ctx, func(tx *store.Tx) error {
		role := "main"
		if store.ConversationID(conversation) != "main" {
			role = "background"
		}
		if sample.ParentThreadID != "" {
			role = "subagent"
		}
		return recordTokenUsage(ctx, tx, sample, "conversation:"+store.ConversationID(conversation), role, a.now().UnixMilli())
	})
}
