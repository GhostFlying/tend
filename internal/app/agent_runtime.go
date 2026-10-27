package app

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// RegisterAgentThread retains the native generation chain independently of processes.
func (a *App) RegisterAgentThread(ctx context.Context, id, thread string) error {
	return a.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `INSERT OR IGNORE INTO agent_thread_history(thread_id,conversation_id,created_at) VALUES(?,?,?)`, thread, store.ConversationID(id), a.now().UnixMilli())
		return err
	})
}
func (a *App) RecordAgentUsage(ctx context.Context, id string, usage protocol.AgentContextUsage) error {
	return a.store.Write(ctx, func(tx *store.Tx) error {
		usage.UpdatedAt = a.now().UnixMilli()
		raw, err := json.Marshal(usage)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO agent_context_usage(conversation_id,thread_id,revision,snapshot_json,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(conversation_id) DO UPDATE SET thread_id=excluded.thread_id,revision=excluded.revision,snapshot_json=excluded.snapshot_json,updated_at=excluded.updated_at WHERE agent_context_usage.thread_id<>excluded.thread_id OR agent_context_usage.revision<=excluded.revision`, store.ConversationID(id), usage.ThreadID, usage.Revision, string(raw), usage.UpdatedAt)
		return err
	})
}
func (a *App) RecordRolloutEvent(ctx context.Context, id, thread, turn, item, kind string, data json.RawMessage) error {
	return a.store.Write(ctx, func(tx *store.Tx) error {
		if len(data) == 0 {
			data = json.RawMessage(`{}`)
		}
		_, err := tx.Exec(ctx, `INSERT INTO agent_rollout_events(conversation_id,thread_id,turn_id,item_id,kind,data_json,created_at) VALUES(?,?,?,?,?,?,?)`, store.ConversationID(id), thread, turn, item, kind, string(data), a.now().UnixMilli())
		return err
	})
}

type CompactConversation struct{}

func (CompactConversation) Operation() string { return "conversation.compact" }
func (CompactConversation) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (CompactConversation) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	id := store.ConversationID(x.Env.Target.ID)
	conv, err := store.AgentConversation(ctx, tx.Q(), id)
	if err != nil {
		return Outcome{}, err
	}
	if x.Env.ExpectedControlRevision == nil || *x.Env.ExpectedControlRevision != conv.Revision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "conversation changed")
	}
	var active bool
	if err = tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_compactions WHERE conversation_id=? AND status IN ('queued','starting','running','unknown'))`, id).Scan(&active); err != nil {
		return Outcome{}, err
	}
	if active {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "compact is pending or unknown; reconcile first")
	}
	usage, err := store.ContextUsage(ctx, tx.Q(), id)
	if err != nil || !usage.CompactSupported {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "native compaction capability has not been observed")
	}
	raw, _ := json.Marshal(usage)
	op := x.IDs.New(protocol.Prefix("compact"))
	if _, err = tx.Exec(ctx, `INSERT INTO agent_compactions(operation_id,conversation_id,status,source,before_json,created_at,updated_at) VALUES(?,?,'queued','manual',?,?,?)`, op, id, string(raw), x.Now.UnixMilli(), x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	return Outcome{Accepted: true, Result: map[string]any{"operation_id": op}, Outbox: []OutboxMessage{{Destination: store.ConversationDestination(id), Operation: "conversation.compact", Payload: map[string]string{"operation_id": op}}}, Events: []Event{{ObjectType: "conversation", ObjectID: id, Kind: "conversation.compact_queued"}}}, nil
}
func (a *App) CompactState(ctx context.Context, op string) (string, error) {
	var state string
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT status FROM agent_compactions WHERE operation_id=?`, op).Scan(&state)
	})
	return state, err
}
func (a *App) SetCompactState(ctx context.Context, op, thread, state string) error {
	return a.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE agent_compactions SET started_after_seq=CASE WHEN ?='starting' AND status='queued' THEN COALESCE((SELECT MAX(seq) FROM agent_rollout_events e WHERE e.conversation_id=agent_compactions.conversation_id),0) ELSE started_after_seq END,status=?,thread_id=?,updated_at=? WHERE operation_id=? AND status<>'completed'`, state, state, thread, a.now().UnixMilli(), op)
		return err
	})
}
func (a *App) CompleteNativeCompact(ctx context.Context, id, thread, item string) error {
	return a.store.Write(ctx, func(tx *store.Tx) error {
		var exists bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_compactions WHERE thread_id=? AND item_id=?)`, thread, item).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return nil
		}
		keys, err := store.FollowupIDs(ctx, tx.Q(), `SELECT thread_key FROM thread_baselines WHERE substr(thread_key,-length(?)-1)='/'||?`, thread, thread)
		if err != nil {
			return err
		}
		for _, key := range keys {
			if err = invalidate(ctx, tx, key, a.now().UnixMilli()); err != nil {
				return err
			}
		}
		usage, err := store.ContextUsage(ctx, tx.Q(), id)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		raw, _ := json.Marshal(usage)
		result, err := tx.Exec(ctx, `UPDATE agent_compactions SET status='completed',item_id=?,after_json=?,updated_at=? WHERE conversation_id=? AND thread_id=? AND status IN ('starting','running','unknown')`, item, string(raw), a.now().UnixMilli(), store.ConversationID(id), thread)
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		if n > 0 {
			return nil
		}
		_, err = tx.Exec(ctx, `INSERT INTO agent_compactions(operation_id,conversation_id,thread_id,status,source,item_id,after_json,created_at,updated_at) VALUES(?,?,?,'completed','automatic',?,?,?,?)`, a.ids.New(protocol.Prefix("compact")), store.ConversationID(id), thread, item, string(raw), a.now().UnixMilli(), a.now().UnixMilli())
		return err
	})
}
func (a *App) AgentCompactUncertain(ctx context.Context, id string) (bool, error) {
	var blocked bool
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_compactions WHERE conversation_id=? AND status IN ('starting','running','unknown'))`, store.ConversationID(id)).Scan(&blocked)
	})
	return blocked, err
}

// ReconcileCompactEvidence consumes only persisted native completion evidence
// newer than the request's starting watermark. It never issues native compact.
func (a *App) ReconcileCompactEvidence(ctx context.Context, op string) (string, error) {
	var conversation, thread, item string
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		rows, err := tx.Q().QueryContext(ctx, `SELECT c.conversation_id,c.thread_id,e.item_id FROM agent_compactions c JOIN agent_rollout_events e ON e.conversation_id=c.conversation_id AND e.thread_id=c.thread_id WHERE c.operation_id=? AND c.status IN ('starting','running','unknown') AND e.seq>c.started_after_seq AND e.kind='item_completed' AND json_extract(e.data_json,'$.type')='contextCompaction' AND NOT EXISTS(SELECT 1 FROM agent_compactions done WHERE done.thread_id=e.thread_id AND done.item_id=e.item_id AND done.status='completed') ORDER BY e.seq LIMIT 2`, op)
		if err != nil {
			return err
		}
		defer rows.Close()
		count := 0
		for rows.Next() {
			count++
			if err = rows.Scan(&conversation, &thread, &item); err != nil {
				return err
			}
		}
		if count != 1 {
			thread = ""
		}
		return rows.Err()
	})
	if err != nil || thread == "" {
		return "", err
	}
	if err = a.CompleteNativeCompact(ctx, conversation, thread, item); err != nil {
		return "", err
	}
	return thread, nil
}

// AgentCompactionsUncertain blocks process replacement until every started native
// compaction is reconciled, including conversations with no attached manager.
func (a *App) AgentCompactionsUncertain(ctx context.Context) (bool, error) {
	var blocked bool
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_compactions WHERE status IN ('starting','running','unknown'))`).Scan(&blocked)
	})
	return blocked, err
}
