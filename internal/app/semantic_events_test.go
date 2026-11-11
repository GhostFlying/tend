package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestSemanticManagerEventDuplicatePreservesOriginalFactAfterAck(t *testing.T) {
	h, _, _, as, _ := reviewManagerAssignment(t)
	text := strings.Repeat("JSON 文本😀", 400)
	encoded, _ := json.Marshal(text)
	first := `{"thread_id":"native","native":{"Kind":"agent_message_delta","TurnID":"turn","ItemID":"reply","Text":` + string(encoded) + `,"Failure":null}}`
	reordered := `{ "native": {"Failure":null,"Text":` + string(encoded) + `,"ItemID":"reply","TurnID":"turn","Kind":"agent_message_delta"}, "thread_id":"native" }`
	event := func(body string) protocol.Operation {
		t.Helper()
		op, err := h.app.Execute(t.Context(), runPrincipal(as.RunID, as.Role), h.env(protocol.OpManagerNativeEvent, runTarget(as.RunID), protocol.ManagerNativeEvent{Sequence: 1, Body: json.RawMessage(body)}, nil), ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent})
		if err != nil {
			return protocol.Operation{Status: protocol.OpFailed, Error: protocol.Errorf(protocol.CodeOf(err), "rejected")}
		}
		return op
	}
	if op := event(first); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.app.AcknowledgeManagerEvent(t.Context(), as.RunID, 1, "native", "turn", "reply", false); err != nil {
		t.Fatal(err)
	}
	h.app = New(h.store, h.ids, h.clock.Now)
	if op := event(reordered); op.Status != protocol.OpSucceeded {
		t.Fatal("lost ACK treated formatting as conflict", op)
	}
	if h.countWhere(`SELECT COUNT(*) FROM manager_native_events WHERE run_id=?`, as.RunID) != 1 {
		t.Fatal("duplicate created another fact")
	}
	for _, changed := range []string{strings.Replace(first, `"Failure":null`, `"Failure":[]`, 1), strings.Replace(first, `,"Failure":null`, "", 1), strings.Replace(first, `"reply"`, `"other"`, 1), strings.Replace(first, "JSON 文本", "different", 1)} {
		if op := event(changed); op.Status != protocol.OpFailed || op.Error == nil || op.Error.Code != protocol.CodeConflictPayload {
			t.Fatal("real conflicting event accepted", op)
		}
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var stored string
		if e := tx.Q().QueryRowContext(t.Context(), `SELECT body_json FROM manager_native_events WHERE run_id=? AND seq=1`, as.RunID).Scan(&stored); e != nil {
			return e
		}
		hash, ok := store.EventOriginalSHA256(stored)
		if !ok || hash != closedFactHash([]byte(first)) {
			t.Fatal("original transport hash changed after semantic retry")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSemanticRolloutCompletedIdentitySurvivesLostAcknowledgement(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	original := json.RawMessage(`{"id":"item","text":"original😀","nullable":null}`)
	reordered := json.RawMessage(`{ "nullable": null, "text":"original😀", "id":"item" }`)
	for _, body := range []json.RawMessage{original, reordered} {
		if err := h.app.RecordRolloutEvent(ctx, "main", "thread", "turn", "item", "item_completed", body); err != nil {
			t.Fatal(err)
		}
		h.app = New(h.store, h.ids, h.clock.Now)
	}
	if h.count("agent_rollout_events") != 1 {
		t.Fatal("native replay appended another event")
	}
	for _, changed := range []json.RawMessage{json.RawMessage(`{"id":"item","text":"changed😀","nullable":null}`), json.RawMessage(`{"id":"item","text":"original😀"}`)} {
		if err := h.app.RecordRolloutEvent(ctx, "main", "thread", "turn", "item", "item_completed", changed); protocol.CodeOf(err) != protocol.CodeConflictPayload {
			t.Fatal("real conflict accepted", err)
		}
	}
}
func TestSemanticRolloutDoesNotInventDeltaOrMissingIdentityKeys(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	for range 2 {
		for _, v := range []struct{ item, kind string }{{"item", "agent_message_delta"}, {"", "item_completed"}, {"item", "unclassified"}} {
			if err := h.app.RecordRolloutEvent(ctx, "main", "thread", "turn", v.item, v.kind, json.RawMessage(`{"text":"same"}`)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if h.count("agent_rollout_events") != 6 {
		t.Fatal("distinct observations were guessed to be duplicates")
	}
}
