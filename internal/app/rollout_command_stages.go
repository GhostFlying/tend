package app

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// A backgrounded command can emit item/completed again when its process exits.
// Both originals remain immutable; only the latter represents a terminal result.
func rolloutAlreadyRecorded(ctx context.Context, q store.Querier, conversation, thread, turn, item, kind string, data json.RawMessage) (bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT data_json FROM agent_rollout_events WHERE conversation_id=? AND thread_id=? AND COALESCE(turn_id,'')=? AND COALESCE(item_id,'')=? AND kind=? ORDER BY seq`, store.ConversationID(conversation), thread, turn, item, kind)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	duplicate, terminalAlreadyRecorded := false, false
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return false, err
		}
		before, err := store.ResolveJSON(ctx, q, raw)
		if err != nil {
			return false, err
		}
		switch {
		case store.SemanticJSONEqual(before, string(data)):
			duplicate = true
		case kind == "item_completed" && commandStageAdvance([]byte(before), data):
			// Preserve the new terminal observation after the background stage.
		case kind == "item_completed" && commandStageAdvance(data, []byte(before)):
			// An exact older-stage retry is allowed only if already recorded.
			terminalAlreadyRecorded = true
		default:
			return false, protocol.Errorf(protocol.CodeConflictPayload, "native rollout identity has different content")
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if terminalAlreadyRecorded && !duplicate {
		return false, protocol.Errorf(protocol.CodeConflictPayload, "native rollout identity has different content")
	}
	return duplicate, nil
}

func commandStageAdvance(before, after []byte) bool {
	var old, next map[string]json.RawMessage
	if json.Unmarshal(before, &old) != nil || json.Unmarshal(after, &next) != nil {
		return false
	}
	var oldType, nextType, oldStatus, nextStatus, oldID, nextID string
	if json.Unmarshal(old["type"], &oldType) != nil || json.Unmarshal(next["type"], &nextType) != nil ||
		json.Unmarshal(old["status"], &oldStatus) != nil || json.Unmarshal(next["status"], &nextStatus) != nil ||
		json.Unmarshal(old["id"], &oldID) != nil || json.Unmarshal(next["id"], &nextID) != nil {
		return false
	}
	if oldType != "commandExecution" || nextType != oldType || oldID == "" || nextID != oldID || oldStatus != "backgrounded" {
		return false
	}
	if nextStatus != "completed" && nextStatus != "failed" && nextStatus != "declined" {
		return false
	}
	// These provider result fields change when the same command finishes.
	// Command, cwd, process identity and all other fields must remain equal.
	for _, key := range []string{"aggregatedOutput", "durationMs", "exitCode", "rawOutput", "status", "yieldTimeMs"} {
		delete(old, key)
		delete(next, key)
	}
	a, err := json.Marshal(old)
	if err != nil {
		return false
	}
	b, err := json.Marshal(next)
	return err == nil && store.SemanticJSONEqual(string(a), string(b))
}
