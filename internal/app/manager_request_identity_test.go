package app

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestManagerCloseRetainsOriginalUnresolvedIdentity(t *testing.T) {
	for _, status := range []string{"pending", "started", "unknown"} {
		t.Run(status, func(t *testing.T) {
			h, _, _, as, _ := reviewManagerAssignment(t)
			original, err := h.app.QueueManagerNative(t.Context(), as.RunID, "close", json.RawMessage(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			if status != "pending" {
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE manager_native_requests SET status=? WHERE request_id=?`, status, original)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			for range 3 {
				again, err := h.app.QueueManagerNative(t.Context(), as.RunID, "close", json.RawMessage(`{"config":{},"input":{},"tool_result":{}}`))
				if err != nil || again != original {
					t.Fatal("close timeout created another physical operation identity", original, again, err)
				}
			}
			if h.countWhere(`SELECT COUNT(*) FROM manager_native_requests WHERE run_id=? AND method='close'`, as.RunID) != 1 {
				t.Fatal("unresolved physical close accumulated duplicate commands")
			}
			if _, err := h.app.QueueManagerNative(t.Context(), as.RunID, "turn", json.RawMessage(`{}`)); protocol.CodeOf(err) != protocol.CodeForbidden {
				t.Fatal("unresolved physical close admitted fresh business work", status, err)
			}
		})
	}
	t.Run("ambiguous_completed_error", func(t *testing.T) {
		h, _, _, as, _ := reviewManagerAssignment(t)
		original, err := h.app.QueueManagerNative(t.Context(), as.RunID, "close", json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		if op := h.runAs(as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		if op := h.runAs(as, protocol.OpManagerNativeReply, protocol.ManagerNativeReply{ID: original, Error: "Native coordinator operation failed"}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		if _, err := h.app.QueueManagerNative(t.Context(), as.RunID, "turn", json.RawMessage(`{}`)); protocol.CodeOf(err) != protocol.CodeForbidden {
			t.Fatal("unconfirmed close admitted new business work", err)
		}
		next, err := h.app.QueueManagerNative(t.Context(), as.RunID, "close", json.RawMessage(`{}`))
		if err != nil || next != original {
			t.Fatal("unconfirmed close error replaced the original physical identity", original, next, err)
		}
	})
}

func TestManagerFindIsVolatileAndNeverReplaysBusiness(t *testing.T) {
	h, _, _, as, _ := reviewManagerAssignment(t)
	tables := []string{"manager_native_requests", "manager_native_events", "events", "outbox", "runs", "run_logs", "retained_original_bodies"}
	before := map[string]int{}
	for _, table := range tables {
		before[table] = h.count(table)
	}
	queryCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	queries := []json.RawMessage{
		json.RawMessage(`{"thread_id":"original-thread","message_id":"original-repair","turn_id":"original-turn"}`),
		json.RawMessage(`{"thread_id":"another-thread","message_id":"original-repair","turn_id":"original-turn"}`),
		json.RawMessage(`{"thread_id":"original-thread","message_id":"original-repair","turn_id":"another-turn"}`),
	}
	seen := map[string]bool{}
	for _, payload := range queries {
		id, err := h.app.QueueManagerNative(queryCtx, as.RunID, "find", payload)
		if err != nil || seen[id] {
			t.Fatal("distinct observation lost its identity", id, err)
		}
		seen[id] = true
		if reply, ready, err := h.app.ManagerNativeResult(t.Context(), as.RunID, id); err != nil || ready || reply.ID != "" {
			t.Fatal("unobserved result became certain", reply, ready, err)
		}
		var command protocol.ManagerNativeRequest
		for range 2 {
			op := h.runAs(as, protocol.OpManagerNativeClaim, struct{}{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
			if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &command) != nil || command.ID != id || command.Method != "find" || !bytes.Equal(command.Payload, payload) {
				t.Fatal("claim retry changed original read identity", op, command)
			}
		}
		reply := protocol.ManagerNativeReply{ID: id, Method: "find", Result: json.RawMessage(`{"Found":false}`)}
		foreign, err := h.app.Execute(t.Context(), runPrincipal("foreign-run", protocol.RoleIssueManager), h.env(protocol.OpManagerNativeReply, protocol.Target{Type: "run", ID: as.RunID}, reply, nil), ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply})
		if protocol.CodeOf(err) != protocol.CodeForbidden && (foreign.Error == nil || foreign.Error.Code != protocol.CodeForbidden) {
			t.Fatal("foreign Run delivered an observation", foreign, err)
		}
		if op := h.runAs(as, protocol.OpManagerNativeReply, reply, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		got, ready, err := h.app.ManagerNativeResult(t.Context(), as.RunID, id)
		if err != nil || !ready || got.ID != id || !bytes.Equal(got.Result, reply.Result) {
			t.Fatal("negative observation lost its actual result", got, ready, err)
		}
	}
	fresh, err := h.app.QueueManagerNative(queryCtx, as.RunID, "find", queries[0])
	if err != nil || seen[fresh] {
		t.Fatal("completed observation blocked a new read", fresh, err)
	}
	restarted := New(h.store, h.ids, h.clock.Now)
	if reply, ready, err := restarted.ManagerNativeResult(t.Context(), as.RunID, fresh); protocol.CodeOf(err) != protocol.CodeUnknownOutcome || ready || reply.ID != "" {
		t.Fatal("restart invented volatile response certainty", reply, ready, err)
	}
	cancel()
	if _, _, present := h.app.nativeCalls.result(as.RunID, fresh); present {
		t.Fatal("cancelled observation retained its body")
	}
	for _, table := range tables {
		if h.count(table) != before[table] {
			t.Fatal("observation persisted transport history or changed business", table)
		}
	}
}
