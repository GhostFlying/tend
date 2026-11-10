package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// AcknowledgeManagerRepairPrefix consumes retained internal repair output. The
// raw events remain the durable text; the audit records consumption, never a
// user reply, native terminal result or successful business outcome.
func (a *App) AcknowledgeManagerRepairPrefix(ctx context.Context, run string, sequence int64, thread, turn, item, body string, consumed []int64) error {
	err := a.write(ctx, func(tx *store.Tx) error {
		covered := map[int64]bool{}
		for _, delta := range consumed {
			if delta <= 0 || delta > sequence || covered[delta] {
				return protocol.Errorf(protocol.CodeUnknownOutcome, "repair prefix has an invalid consumed cursor")
			}
			covered[delta] = true
		}
		if !covered[sequence] {
			return protocol.Errorf(protocol.CodeUnknownOutcome, "repair prefix did not consume its last delta")
		}
		rows, err := tx.Q().QueryContext(ctx, `SELECT seq,body_json FROM manager_native_events WHERE run_id=? AND seq<=? AND json_extract(body_json,'$.thread_id')=? AND json_extract(body_json,'$.native.Kind')='agent_message_delta' AND json_extract(body_json,'$.native.TurnID')=? AND json_extract(body_json,'$.native.ItemID')=? ORDER BY seq`, run, sequence, thread, turn, item)
		if err != nil {
			return err
		}
		var text strings.Builder
		var actual []int64
		for rows.Next() {
			var delta int64
			var raw string
			if err := rows.Scan(&delta, &raw); err != nil {
				rows.Close()
				return err
			}
			if !covered[delta] {
				rows.Close()
				return protocol.Errorf(protocol.CodeUnknownOutcome, "repair prefix contains an unconsumed delta")
			}
			var event struct{ Native struct{ Text string } }
			if err := store.DecodeJSON(ctx, tx.Q(), raw, &event); err != nil {
				rows.Close()
				return err
			}
			text.WriteString(event.Native.Text)
			actual = append(actual, delta)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(actual) != len(covered) || text.String() != body {
			return protocol.Errorf(protocol.CodeUnknownOutcome, "repair prefix differs from its original raw events")
		}
		requests, err := store.FollowupIDs(ctx, tx.Q(), `SELECT h.execution_id FROM hosted_executions h JOIN main_turns t ON t.request_id=h.execution_id JOIN logical_turn_executions e ON e.request_id=t.request_id WHERE json_extract(h.private_snapshot,'$.runner_run_id')=? AND t.thread_id=? AND t.turn_id=? AND e.repair=1 ORDER BY h.execution_id`, run, thread, turn)
		if err != nil {
			return err
		}
		if len(requests) != 1 {
			return protocol.Errorf(protocol.CodeUnknownOutcome, "repair prefix has no unambiguous original repair execution")
		}
		digest := sha256.Sum256([]byte(body))
		hash := hex.EncodeToString(digest[:])
		var audited bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE object_id=? AND kind='run.manager_repair_prefix_consumed' AND object_version=? AND json_extract(body_json,'$.thread_id')=? AND json_extract(body_json,'$.turn_id')=? AND json_extract(body_json,'$.item_id')=? AND json_extract(body_json,'$.repair_request_id')=? AND json_extract(body_json,'$.prefix_sha256')=?)`, run, sequence, thread, turn, item, requests[0], hash).Scan(&audited); err != nil {
			return err
		}
		if audited {
			return nil
		}
		conversations, err := store.FollowupIDs(ctx, tx.Q(), `SELECT m.conversation_id FROM runs r JOIN issue_manager_tasks m USING(task_id) JOIN hosted_executions h ON h.execution_id=? AND h.conversation_id=m.conversation_id AND h.issue_id=m.issue_id AND h.assignment_epoch=m.assignment_epoch JOIN native_turn_receipts_pending p ON p.request_id=h.execution_id WHERE r.run_id=? AND r.role='issue_manager'`, requests[0], run)
		if err != nil {
			return err
		}
		if len(conversations) != 1 {
			return protocol.Errorf(protocol.CodeUnknownOutcome, "repair prefix has no matching pending original responsibility")
		}
		scope, err := managerReplyReplay(ctx, tx.Q(), conversations[0], run, true)
		if err != nil {
			return err
		}
		if scope.ThreadID != thread {
			return protocol.Errorf(protocol.CodeUnknownOutcome, "repair prefix original thread binding differs")
		}
		closing, err := managerRunClosing(ctx, tx.Q(), run)
		if err != nil {
			return err
		}
		if !closing {
			return protocol.Errorf(protocol.CodeUnknownOutcome, "repair prefix requires its original closing responsibility")
		}
		audit, err := json.Marshal(map[string]any{"thread_id": thread, "turn_id": turn, "item_id": item, "repair_request_id": requests[0], "consumed_sequences": actual, "prefix_sha256": hash})
		if err != nil {
			return err
		}
		if _, err := tx.InsertEvent(ctx, store.EventRow{EventID: a.ids.New(protocol.PrefixEvent), Source: "mainthread", ObjectType: "run", ObjectID: run, ObjectVersion: sequence, Kind: "run.manager_repair_prefix_consumed", BodyJSON: string(audit), RequestID: requests[0], CreatedAt: a.now().UnixMilli()}); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE manager_native_events SET applied=1 WHERE run_id=? AND seq<=? AND json_extract(body_json,'$.thread_id')=? AND json_extract(body_json,'$.native.Kind')='agent_message_delta' AND json_extract(body_json,'$.native.TurnID')=? AND json_extract(body_json,'$.native.ItemID')=?`, run, sequence, thread, turn, item)
		return err
	})
	if err == nil {
		a.notifyChange()
	}
	return err
}
