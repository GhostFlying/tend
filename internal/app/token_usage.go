package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// recordTokenUsage deduplicates native request facts within the caller's write
// transaction. Model and ownership are attributes, not deduplication keys.
func recordTokenUsage(ctx context.Context, tx *store.Tx, sample protocol.TokenUsageSample, subject, role string, now int64) error {
	if sample.RecordID != "" {
		sample.Revision = 1
		subject = "session:" + sample.SessionID
		role = "native"
	}
	if sample.Source == "" || sample.RequestID == "" || sample.ThreadID == "" || sample.Revision < 1 || (sample.Settlement != "final" && sample.Settlement != "provisional") {
		return protocol.Errorf(protocol.CodeInvalidRequest, "invalid request usage identity")
	}
	for _, n := range []*int64{sample.Input, sample.Output, sample.CacheRead, sample.CacheWrite, sample.Total} {
		if n != nil && (*n < 0 || *n > 1<<50) {
			return protocol.Errorf(protocol.CodeInvalidRequest, "invalid token counter")
		}
	}
	if sample.OccurredAt != nil && (*sample.OccurredAt < 0 || (sample.RecordID == "" && *sample.OccurredAt > now)) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "invalid usage instant")
	}
	identity := sample.RequestID
	if sample.RecordID != "" {
		identity = sample.RecordID
	}
	// Resolve the current measurement from typed facts, independent of prices
	// and derived attribution. Model/purpose enrichment retains real revisions.
	var old protocol.TokenUsageRecord
	err := tx.Q().QueryRowContext(ctx, `SELECT `+store.UsageFactColumns("f")+` FROM token_usage_current c JOIN token_usage_fact_details f ON f.seq=c.fact_seq WHERE c.source=? AND c.request_id=?`, sample.Source, identity).Scan(store.UsageFactDestinations(&old)...)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	hasOld := err == nil
	if sample.RecordID != "" && hasOld && old.Subject == subject && old.Role == role {
		sample = mergeNativeModel(old.TokenUsageSample, sample)
	}
	// Retain contradictory variants, including stale revisions. Exact typed
	// equality is the native business key's consistency check, not a cache of
	// HTTP parameters or serialized TokenUsageSample data.
	rows, err := tx.Q().QueryContext(ctx, `SELECT `+store.UsageFactColumns("f")+` FROM token_usage_fact_details f WHERE f.source=? AND f.request_id=? AND f.revision=?`, sample.Source, identity, sample.Revision)
	if err != nil {
		return err
	}
	var duplicate, conflict bool
	for rows.Next() {
		var previous protocol.TokenUsageRecord
		if err = rows.Scan(store.UsageFactDestinations(&previous)...); err != nil {
			break
		}
		equal := previous.Subject == subject && previous.Role == role && reflect.DeepEqual(previous.TokenUsageSample, sample)
		duplicate = duplicate || equal
		conflict = conflict || !equal
	}
	if err == nil {
		err = rows.Err()
	}
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if duplicate {
		return nil
	}
	seq, err := store.InsertUsageFact(ctx, tx, sample, subject, role, now)
	if err != nil {
		return err
	}
	if conflict || (hasOld && (old.Subject != subject || old.Role != role)) {
		_, err = tx.Exec(ctx, `UPDATE token_usage_current SET conflict=1 WHERE source=? AND request_id=?`, sample.Source, identity)
		return err
	}
	if hasOld && (sample.Revision <= old.Revision || (old.Settlement == "final" && sample.Settlement != "final")) {
		return nil
	}
	_, err = tx.Exec(ctx, `INSERT INTO token_usage_current(source,request_id,fact_seq) VALUES(?,?,?) ON CONFLICT(source,request_id) DO UPDATE SET fact_seq=excluded.fact_seq`, sample.Source, identity, seq)
	if err != nil {
		return err
	}
	return reconcileUsageAliases(ctx, tx, sample, now)
}

// RecordTokenUsage persists an observed request from a hosted native session.
// It is internal maintenance, not an owner-requested business operation.
func (a *App) RecordTokenUsage(ctx context.Context, conversation string, sample protocol.TokenUsageSample) error {
	return a.write(ctx, func(tx *store.Tx) error {
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

// mergeNativeModel accepts model provenance and optional source-purpose
// enrichment. Neither changes the immutable native request measurement. Native
// identity, time, counters, or contradictory nonempty metadata still conflict.
func mergeNativeModel(old, next protocol.TokenUsageSample) protocol.TokenUsageSample {
	if old.NativeContentHash != "" && old.NativeContentHash == next.NativeContentHash && old.NativeVersion != "" {
		next.NativeVersion = old.NativeVersion
	}
	if next.Model == old.Model && next.ModelSource != old.ModelSource && next.ModelSource != "" {
		// Both files can describe the same request using different model evidence.
		if old.ModelSource == "native-turn-context" {
			next.ModelSource = old.ModelSource
		}
	}
	a, b := old, next
	purposeChanged := old.Purpose == "" && next.Purpose != ""
	if old.Purpose == "" || next.Purpose == "" {
		a.Purpose, b.Purpose = "", ""
	}
	a.Model = ""
	a.ModelSource = ""
	a.Revision = 0
	b.Model = ""
	b.ModelSource = ""
	b.Revision = 0
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	if string(x) != string(y) {
		return next
	}
	if next.Purpose == "" {
		next.Purpose = old.Purpose
	}
	metadataRevision := old.Revision
	if purposeChanged {
		metadataRevision++
	}
	rank := func(source string) int {
		switch source {
		case "native-turn-context":
			return 3
		case "recorded-turn-execution", "native-preceding-context":
			return 2
		case "frozen-execution":
			return 1
		}
		return 0
	}
	if next.Model == old.Model && next.ModelSource == old.ModelSource {
		next.Revision = metadataRevision
		return next
	}
	if next.Model == "" || rank(next.ModelSource) < rank(old.ModelSource) {
		next.Model = old.Model
		next.ModelSource = old.ModelSource
		next.Revision = metadataRevision
		return next
	}
	if old.Model == "" || rank(next.ModelSource) > rank(old.ModelSource) {
		next.Revision = old.Revision + 1
	}
	return next
}
