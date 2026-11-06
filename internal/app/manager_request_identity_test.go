package app

import (
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
	t.Run("legacy_completed_error", func(t *testing.T) {
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
		if _, err := h.app.QueueManagerNative(t.Context(), as.RunID, "turn", json.RawMessage(`{}`)); err != nil {
			t.Fatal("unversioned coalescing changed a valid legacy completed-error request", err)
		}
		next, err := h.app.QueueManagerNative(t.Context(), as.RunID, "close", json.RawMessage(`{}`))
		if err != nil || next == original {
			t.Fatal("completed-error close inherited a new generic compatibility rule", original, next, err)
		}
	})
}

func TestManagerFindCoalescesOnlyTheSameUnresolvedRead(t *testing.T) {
	h, _, _, as, _ := reviewManagerAssignment(t)
	payload := json.RawMessage(`{"thread_id":"original-thread","message_id":"original-repair","turn_id":"original-turn"}`)
	original, err := h.app.QueueManagerNative(t.Context(), as.RunID, "find", payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"pending", "started", "unknown"} {
		if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE manager_native_requests SET status=? WHERE request_id=?`, state, original)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		for range 3 {
			again, err := h.app.QueueManagerNative(t.Context(), as.RunID, "find", payload)
			if err != nil || again != original {
				t.Fatal("identical unfinished read accumulated another request", state, original, again, err)
			}
		}
	}
	for _, other := range []json.RawMessage{
		json.RawMessage(`{"thread_id":"another-thread","message_id":"original-repair","turn_id":"original-turn"}`),
		json.RawMessage(`{"thread_id":"original-thread","message_id":"original-repair","turn_id":"another-turn"}`),
	} {
		id, err := h.app.QueueManagerNative(t.Context(), as.RunID, "find", other)
		if err != nil || id == original {
			t.Fatal("distinct native receipt query was combined with another identity", id, err)
		}
	}
	if h.countWhere(`SELECT COUNT(*) FROM manager_native_requests WHERE run_id=? AND method='find'`, as.RunID) != 3 || h.countWhere(`SELECT COUNT(*) FROM manager_native_requests WHERE request_id=? AND status='unknown' AND result_json IS NULL`, original) != 1 {
		t.Fatal("coalescing rewrote unknown evidence or combined distinct queries")
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE manager_native_requests SET status='completed',result_json=? WHERE request_id=?`, `{"id":"`+original+`","result":{"Found":false}}`, original)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	fresh, err := h.app.QueueManagerNative(t.Context(), as.RunID, "find", payload)
	if err != nil || fresh == original {
		t.Fatal("finished observation prevented a fresh read", fresh, err)
	}
}
