package mainthread

import (
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
	"testing"
)

func TestLateOrdinaryReplyDuringRepairAdmission(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	source := e.say("original")
	s, err := e.h.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	th, err := s.StartThread(t.Context(), harness.ThreadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	req := e.ids.New(protocol.PrefixRequest)
	rec := store.MainTurn{RequestID: req, MessageID: source, ThreadID: th.ID(), TurnID: "original-turn", Status: store.TurnStarted}
	if err = e.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	rec.Status = store.TurnCompleted
	if err = e.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	repair := retainHistoricalRepairForTest(t, e, req)
	e.m.mu.Lock()
	e.m.active = &turn{requestID: repair, messageID: source}
	e.m.mu.Unlock()
	ev := harness.Event{TurnID: rec.TurnID, ItemID: "late-original", ItemType: harness.ItemAgentMessage, Text: "Late final from original turn"}
	if err = e.m.recordReplyState(t.Context(), th, ev, protocol.AssistantCompleted); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM conversation_messages WHERE body=?`, ev.Text); n != 1 {
		t.Fatalf("late original reply lost during repair admission: rows=%d", n)
	}
}

// retainHistoricalRepairForTest seeds immutable pre-upgrade history. It is not
// an assertion that current completion should schedule a metadata-only model.
func retainHistoricalRepairForTest(t *testing.T, e *env, logical string) string {
	t.Helper()
	repair := e.ids.New(protocol.PrefixRequest)
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO logical_turn_executions(request_id,logical_turn_id,repair,settled) VALUES(?,?,1,0)`, repair, logical)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return repair
}
