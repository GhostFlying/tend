package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

// NativeThread is a real provider session bound to its producing execution.
func NativeThread(ctx context.Context, q Querier, run string) (string, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT native_thread_json FROM runs WHERE run_id=?`, run).Scan(&raw)
	return raw, err
}

func (t *Tx) PutManagerToolResult(ctx context.Context, run, command, payload string, delivered bool, when int64) error {
	state := "produced"
	if delivered {
		state = "delivered"
	}
	return t.putManagerToolResult(ctx, run, command, payload, state, when)
}

func (t *Tx) putManagerToolResult(ctx context.Context, run, command, payload, state string, when int64) error {
	delivered := state == "delivered"
	var p struct {
		ThreadID   string `json:"thread_id"`
		TurnID     string `json:"turn_id"`
		CallID     string `json:"call_id"`
		ToolResult struct {
			Success bool
			Text    string
		} `json:"tool_result"`
	}
	payload, err := ResolveJSON(ctx, t.Q(), payload)
	if err != nil {
		return err
	}
	if err = json.Unmarshal([]byte(payload), &p); err != nil {
		return err
	}
	if p.ThreadID == "" || p.TurnID == "" || p.CallID == "" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "tool result requires its native thread, turn and call")
	}
	u, err := GetToolUse(ctx, t.Q(), p.TurnID, p.CallID)
	if errors.Is(err, ErrNotFound) {
		// This is the projection of the original provider callback, never a new Run.
		var conversation string
		if err = t.Q().QueryRowContext(ctx, `SELECT m.conversation_id FROM runs r JOIN issue_manager_tasks m USING(task_id) WHERE r.run_id=? AND r.role='issue_manager'`, run).Scan(&conversation); err != nil {
			return err
		}
		u = ToolUseRow{MessageID: protocol.NativeObjectID(protocol.PrefixMessage, "native-tool:"+p.ThreadID, p.TurnID+"/"+p.CallID), ThreadID: p.ThreadID, TurnID: p.TurnID, ItemID: p.CallID}
		_, err = t.InsertMessage(ctx, MessageRow{MessageID: u.MessageID, ConversationID: conversation, Author: "agent", Kind: "tool_use", CreatedAt: when})
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if u.ThreadID != p.ThreadID {
		return protocol.Errorf(protocol.CodeConflictPayload, "native tool belongs to another session")
	}
	var priorProduced, priorDelivery, priorState string
	var priorSuccess bool
	if err = t.Q().QueryRowContext(ctx, `SELECT native_result_output,native_delivery_id,native_delivery_status,native_result_success FROM conversation_tool_uses WHERE message_id=?`, u.MessageID).Scan(&priorProduced, &priorDelivery, &priorState, &priorSuccess); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if priorDelivery != "" {
		before, e := ResolveJSON(ctx, t.Q(), priorProduced)
		if e != nil {
			return e
		}
		if priorState == "delivered" && (priorSuccess != p.ToolResult.Success || !sameManagerToolText(before, p.ToolResult.Text)) {
			return protocol.Errorf(protocol.CodeConflictPayload, "native callback already has another server result")
		}
	}
	original, e := ResolveJSON(ctx, t.Q(), u.Output)
	if e != nil {
		return e
	}
	sharedOutput := delivered && sameManagerToolText(original, p.ToolResult.Text)
	replaceOutput := delivered && original == "" || original == p.ToolResult.Text || exactClippedPrefix(original, p.ToolResult.Text)
	if replaceOutput && !sharedOutput {
		u.Output = p.ToolResult.Text
	}
	if replaceOutput || sharedOutput {
		u.Status = "completed"
		if !p.ToolResult.Success {
			u.Status = "failed"
		}
	}
	u.UpdatedAt = when
	if err = t.PutToolUse(ctx, u); err != nil {
		return err
	}
	var produced string
	if state == "rejected" && reconstructibleTaskRead(u.Tool, u.Input, p.ToolResult.Text) {
		// A definite rejection never delivered this ordinary read snapshot.
		// Its original arguments and current business object replace the body.
		state = NativeDeliveryRejectedCurrentTask
	} else if sharedOutput {
		// This is the same delivered value, not another response original. Keep
		// the provider's original representation and its already-bound reference.
		err = t.Q().QueryRowContext(ctx, `SELECT output FROM conversation_tool_uses WHERE message_id=?`, u.MessageID).Scan(&produced)
	} else {
		produced, err = t.RetainJSON(ctx, p.ToolResult.Text)
	}
	if err != nil {
		return err
	}
	_, err = t.Exec(ctx, `UPDATE conversation_tool_uses SET native_run_id=?,native_delivery_id=?,native_delivery_status=?,native_result_output=?,native_result_success=? WHERE message_id=?`, run, command, state, produced, p.ToolResult.Success, u.MessageID)
	return err
}

// CompleteManagerNative promotes execution and tool-call facts before the
// coordinator consumes the real asynchronous command's outstanding receipt.
func (t *Tx) CompleteManagerNative(ctx context.Context, run, id, method, payload string, reply protocol.ManagerNativeReply, when int64) error {
	if method == "tool_result" {
		state := "produced"
		if reply.Rejected {
			state = "rejected"
		} else if reply.Error == "" {
			state = "delivered"
		}
		if err := t.putManagerToolResult(ctx, run, id, payload, state, when); err != nil {
			return err
		}
		if reply.Rejected {
			_, err := t.Exec(ctx, `UPDATE conversation_tool_uses SET native_delivery_error=? WHERE native_run_id=? AND native_delivery_id=?`, reply.Error, run, id)
			return err
		}
		return nil
	}
	if reply.Error != "" || reply.Rejected {
		return nil
	}
	switch method {
	case "start", "resume":
		var thread struct{ ID string }
		if json.Unmarshal(reply.Result, &thread) != nil || thread.ID == "" {
			return protocol.Errorf(protocol.CodeInvalidRequest, "native session result lacks its actual identity")
		}
		_, err := t.Exec(ctx, `UPDATE runs SET native_thread_id=?,native_thread_json=? WHERE run_id=?`, thread.ID, string(reply.Result), run)
		return err
	case "turn":
		var turn string
		if json.Unmarshal(reply.Result, &turn) != nil || turn == "" {
			return protocol.Errorf(protocol.CodeInvalidRequest, "native turn result lacks its actual identity")
		}
		_, err := t.Exec(ctx, `UPDATE runs SET native_turn_id=? WHERE run_id=?`, turn, run)
		return err
	case "close":
		_, err := t.Exec(ctx, `UPDATE runs SET native_close_id=?,native_close_sequence=?,native_closed_at=?,native_close_activity=native_activity WHERE run_id=?`, id, reply.LastSequence, when, run)
		return err
	case "tool_result":
		return t.PutManagerToolResult(ctx, run, id, payload, true, when)
	}
	return nil
}

// ManagerNativeClose is a physical execution fact, independent of RPC history.
func ManagerNativeClose(ctx context.Context, q Querier, run string) (id string, sequence int64, current bool, err error) {
	err = q.QueryRowContext(ctx, `SELECT native_close_id,native_close_sequence,native_closed_at>0 AND native_close_activity=native_activity FROM runs WHERE run_id=?`, run).Scan(&id, &sequence, &current)
	return
}

func migrateRetiredRequests(ctx context.Context, tx *Tx) error {
	rows, err := tx.Q().QueryContext(ctx, `SELECT request_id,run_id,method,payload_json,result_json,status,created_at FROM manager_native_requests ORDER BY rowid`)
	if err != nil {
		return err
	}
	type command struct {
		id, run, method, payload, status string
		result                           sql.NullString
		when                             int64
	}
	var commands []command
	for rows.Next() {
		var c command
		if err = rows.Scan(&c.id, &c.run, &c.method, &c.payload, &c.result, &c.status, &c.when); err != nil {
			break
		}
		commands = append(commands, c)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	for _, c := range commands {
		if c.method == "models" || c.method == "find" || c.method == "usage" {
			if _, err = tx.Exec(ctx, `DELETE FROM manager_native_requests WHERE request_id=?`, c.id); err != nil {
				return err
			}
			continue
		}
		if c.status != "completed" || !c.result.Valid {
			if c.status != "pending" && c.method != "close" {
				if _, err = tx.Exec(ctx, `UPDATE runs SET native_activity=native_activity+1 WHERE run_id=?`, c.run); err != nil {
					return err
				}
			}
			continue
		}
		raw, err := ResolveJSON(ctx, tx.Q(), c.result.String)
		if err != nil {
			return fmt.Errorf("retire native command %s: %w", c.id, err)
		}
		var reply protocol.ManagerNativeReply
		if err = json.Unmarshal([]byte(raw), &reply); err != nil {
			return err
		}
		if reply.ID != c.id {
			return fmt.Errorf("retire native command %s: reply identity differs", c.id)
		}
		if !reply.Rejected && c.method != "close" {
			if _, err = tx.Exec(ctx, `UPDATE runs SET native_activity=native_activity+1 WHERE run_id=?`, c.run); err != nil {
				return err
			}
		}
		if reply.Error != "" && !reply.Rejected {
			if _, err = tx.Exec(ctx, `UPDATE manager_native_requests SET status='unknown' WHERE request_id=?`, c.id); err != nil {
				return err
			}
			continue
		}
		if err = tx.CompleteManagerNative(ctx, c.run, c.id, c.method, c.payload, reply, c.when); err != nil {
			return fmt.Errorf("promote native command %s: %w", c.id, err)
		}
		var ended bool
		if err = tx.Q().QueryRowContext(ctx, `SELECT observed_state IN ('finished','failed','interrupted') FROM runs WHERE run_id=?`, c.run).Scan(&ended); err != nil {
			return err
		}
		// Ending the Run proves the business coordinator has finished, never
		// that the runner saw its final HTTP acknowledgement. Keep only this
		// outstanding handoff identity until that acknowledgement arrives.
		if ended {
			if _, err = tx.Exec(ctx, `UPDATE manager_native_requests SET coordinator_consumed=1,payload_json='{}',result_json=NULL,completion_error=?,completion_rejected=? WHERE request_id=?`, reply.Error, reply.Rejected, c.id); err != nil {
				return err
			}
		} else if _, err = tx.Exec(ctx, `UPDATE manager_native_requests SET completion_error=?,completion_rejected=? WHERE request_id=?`, reply.Error, reply.Rejected, c.id); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `DROP TABLE request_records; DROP TABLE native_requests;`)
	return err
}

// Historical UI clipping is recoverable only from the same callback's exact
// original prefix and byte counts, never from a merely similar result.
func exactClippedPrefix(clipped, full string) bool {
	i := strings.LastIndex(clipped, "\n[truncated: ")
	if i < 0 || !strings.HasPrefix(full, clipped[:i]) {
		return false
	}
	expected := fmt.Sprintf("\n[truncated: %d of %d bytes shown]", i, len(full))
	return clipped[i:] == expected
}

// sameManagerToolText ignores only JSON representation differences. Plain text,
// absent/null/array distinctions and real external phase changes remain exact.
func sameManagerToolText(a, b string) bool {
	return a == b || SemanticJSONEqual(a, b)
}
