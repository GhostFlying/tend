package app

import (
	"context"
	"encoding/json"

	workerharness "github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// TrackAgentProcessText exposes the current local native attachment to the
// authorized process reader. Stale attachments can only clear their own entry.
func (a *App) TrackAgentProcessText(conversation string, text *workerharness.WorkerProcessTextBuffer) func() {
	conversation = store.ConversationID(conversation)
	a.agentProcessText.Store(conversation, text)
	return func() { a.agentProcessText.CompareAndDelete(conversation, text) }
}

func (a *App) AgentProcessText(conversation, thread, turn string) []protocol.WorkerProcessText {
	source, ok := a.agentProcessText.Load(store.ConversationID(conversation))
	if !ok {
		return nil
	}
	return source.(*workerharness.WorkerProcessTextBuffer).Snapshot(thread, turn)
}

func (a *App) RecordAgentProcessText(ctx context.Context, conversation string, parts []protocol.WorkerProcessText) error {
	for _, part := range parts {
		data, err := json.Marshal(part)
		if err != nil {
			return err
		}
		if err := a.RecordRolloutEvent(ctx, conversation, part.ThreadID, part.TurnID, part.ItemID, "agent_message_fragment", data); err != nil {
			return err
		}
	}
	return nil
}

// Close text is an optional observation on the Run. Keeping it out of the
// native request result avoids archiving auxiliary text on the close path.
func retainManagerCloseProcessText(ctx context.Context, tx *store.Tx, run string, seq int64, parts []protocol.WorkerProcessText) error {
	if len(parts) > protocol.MaxWorkerProcessTextItems {
		return nil
	}
	for _, part := range parts {
		if len(part.Text) > protocol.MaxWorkerProcessTextBytes || !validProcessTextID(part.ThreadID) ||
			!validProcessTextID(part.TurnID) || !validProcessTextID(part.ItemID) {
			return nil
		}
	}
	raw, err := json.Marshal(parts)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE runs SET native_close_process_text_json=? WHERE run_id=? AND native_close_sequence=? AND native_closed_at>0 AND native_close_process_text_json='[]'
		AND EXISTS(SELECT 1 FROM issue_manager_tasks m WHERE m.task_id=runs.task_id AND m.conversation_id NOT IN ('','main'))`, string(raw), run, seq)
	return err
}

func validProcessTextID(id string) bool {
	return len(id) > 0 && len(id) <= 512
}
