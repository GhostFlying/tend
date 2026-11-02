package app

import (
	"context"
	"database/sql"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func usageProofRank(proof string) int {
	switch proof {
	case "recorded-native-turn":
		return 4
	case "registered-thread":
		return 3
	case "delegated-session":
		return 2
	case "conversation-history":
		return 1
	}
	return 0
}

func bindNativeUsage(ctx context.Context, tx *store.Tx, s protocol.TokenUsageSample, run, conversation, host, proof string, now int64) error {
	var previousRun, previousConversation, previousProof string
	var conflicted bool
	err := tx.Q().QueryRowContext(ctx, `SELECT run_id,conversation_id,proof,attribution_conflict FROM token_usage_bindings WHERE source=? AND request_id=?`, s.Source, s.RecordID).Scan(&previousRun, &previousConversation, &previousProof, &conflicted)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if conversation == "" && previousConversation != "" && (run == previousRun || usageProofRank(proof) < usageProofRank(previousProof)) {
		conversation = previousConversation
	}
	if err == nil && previousRun == run && previousConversation == conversation && usageProofRank(previousProof) >= usageProofRank(proof) {
		return nil
	}
	if _, err = tx.Exec(ctx, `INSERT INTO token_usage_binding_revisions(source,request_id,run_id,conversation_id,host_id,proof,created_at) VALUES(?,?,?,?,?,?,?)`, s.Source, s.RecordID, run, conversation, host, proof, now); err != nil {
		return err
	}
	if previousRun != "" && previousRun != run && usageProofRank(previousProof) > usageProofRank(proof) {
		return nil
	}
	if previousRun != "" && run != "" && previousRun != run && usageProofRank(previousProof) == usageProofRank(proof) {
		conflicted = true
		run = ""
		conversation = ""
	}
	if usageProofRank(proof) > usageProofRank(previousProof) {
		conflicted = false
	}
	_, err = tx.Exec(ctx, `INSERT INTO token_usage_bindings(source,request_id,run_id,conversation_id,host_id,proof,attribution_conflict) VALUES(?,?,?,?,?,?,?) ON CONFLICT(source,request_id) DO UPDATE SET run_id=excluded.run_id,conversation_id=excluded.conversation_id,host_id=excluded.host_id,proof=excluded.proof,attribution_conflict=excluded.attribution_conflict`, s.Source, s.RecordID, run, conversation, host, proof, conflicted)
	return err
}

func nativeUsageRun(ctx context.Context, q store.Querier, s protocol.TokenUsageSample, root, run, conversation string) (string, string, error) {
	var recorded string
	err := q.QueryRowContext(ctx, `SELECT json_extract(e.private_snapshot,'$.runner_run_id') FROM main_turns t JOIN runner_coordination_executions e ON e.execution_id=t.request_id WHERE t.thread_id=? AND t.turn_id=? LIMIT 1`, s.ThreadID, s.TurnID).Scan(&recorded)
	if err == nil && recorded != "" {
		return recorded, "recorded-native-turn", nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}
	var logged int
	err = q.QueryRowContext(ctx, `SELECT COUNT(DISTINCT run_id),COALESCE(MIN(run_id),'') FROM run_logs WHERE kind IN ('native.turn_started','native.turn_completed','item.completed') AND json_extract(body_json,'$.thread_id')=? AND json_extract(body_json,'$.turn_id')=?`, s.ThreadID, s.TurnID).Scan(&logged, &recorded)
	if err != nil {
		return "", "", err
	}
	if logged == 1 {
		return recorded, "recorded-native-turn", nil
	}
	var count int
	err = q.QueryRowContext(ctx, `SELECT COUNT(DISTINCT run_id),COALESCE(MIN(run_id),'') FROM token_usage_sources WHERE thread_id=? AND session_id=? AND run_id<>''`, s.ThreadID, s.SessionID).Scan(&count, &recorded)
	if err != nil {
		return "", "", err
	}
	if count == 1 {
		return recorded, "registered-thread", nil
	}
	if s.ThreadID != root {
		err = q.QueryRowContext(ctx, `SELECT json_extract(e.private_snapshot,'$.runner_run_id') FROM main_turns t JOIN runner_coordination_executions e ON e.execution_id=t.request_id WHERE t.thread_id=? AND t.turn_id=? LIMIT 1`, root, s.RootTurnID).Scan(&recorded)
		if err == nil && recorded != "" {
			return recorded, "delegated-session", nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", "", err
		}
		return run, "delegated-session", nil
	}
	if count > 1 && s.Purpose != "internal_summary" {
		return "", "conversation-history", nil
	}
	if conversation == "" || s.Purpose == "internal_summary" {
		return run, "registered-thread", nil
	}
	return "", "conversation-history", nil
}
