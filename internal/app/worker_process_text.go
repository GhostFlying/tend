package app

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type agentProcessTextReader interface {
	Snapshot(thread, turn string) []protocol.WorkerProcessText
}

type agentProcessTextAttachment struct {
	reader agentProcessTextReader
}

// TrackAgentProcessText exposes the current local native attachment to the
// authorized process reader. Stale attachments can only clear their own entry.
func (a *App) TrackAgentProcessText(conversation string, reader agentProcessTextReader) func() {
	conversation = store.ConversationID(conversation)
	attachment := &agentProcessTextAttachment{reader: reader}
	a.agentProcessText.Store(conversation, attachment)
	return func() { a.agentProcessText.CompareAndDelete(conversation, attachment) }
}

func (a *App) AgentProcessText(conversation, thread, turn string) []protocol.WorkerProcessText {
	source, ok := a.agentProcessText.Load(store.ConversationID(conversation))
	if !ok {
		return nil
	}
	return source.(*agentProcessTextAttachment).reader.Snapshot(thread, turn)
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
	if !validWorkerProcessText(parts) {
		return nil
	}
	raw, err := json.Marshal(parts)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE runs SET native_close_process_text_json=? WHERE run_id=? AND native_close_sequence=? AND native_closed_at>0 AND native_close_process_text_json='[]'
		AND EXISTS(SELECT 1 FROM issue_manager_tasks m WHERE m.task_id=runs.task_id AND m.conversation_id NOT IN ('','main'))`, string(raw), run, seq)
	return err
}

func validWorkerProcessText(parts []protocol.WorkerProcessText) bool {
	if len(parts) == 0 || len(parts) > protocol.MaxWorkerProcessTextItems {
		return false
	}
	for _, part := range parts {
		if len(part.Text) > protocol.MaxWorkerProcessTextBytes || !validProcessTextID(part.ThreadID) ||
			!validProcessTextID(part.TurnID) || !validProcessTextID(part.ItemID) {
			return false
		}
	}
	return true
}

func validProcessTextID(id string) bool {
	return len(id) > 0 && len(id) <= 512
}
