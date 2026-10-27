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
	var repair string
	if err = e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT request_id FROM logical_turn_executions WHERE logical_turn_id=? AND repair=1`, req).Scan(&repair)
	}); err != nil {
		t.Fatal(err)
	}
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
