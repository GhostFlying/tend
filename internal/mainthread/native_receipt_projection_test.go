package mainthread

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/traex"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

type unavailableReceiptThread struct {
	harness.Thread
	reads int
}

func (t *unavailableReceiptThread) FindTurn(context.Context, string, string) (harness.TurnReceipt, bool, error) {
	t.reads++
	return harness.TurnReceipt{}, false, errors.New("provider is unavailable after the original observation")
}

func TestObservedNativeTerminalSurvivesProjectionFailureAndRetriesWithoutProvider(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	e.m.cfg.DecodeRetainedTurnReceipt = traex.DecodeRetainedTurnReceipt
	message := e.say("original result request")
	request := e.ids.New(protocol.PrefixRequest)
	const thread, turn = "observed-thread", "observed-turn"
	if err := e.app.PutMainTurn(t.Context(), store.MainTurn{RequestID: request, MessageID: message, ThreadID: thread, TurnID: turn, Status: store.TurnStarted}); err != nil {
		t.Fatal(err)
	}
	if err := e.app.AwaitNativeReceipts(t.Context(), thread); err != nil {
		t.Fatal(err)
	}
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE TRIGGER reject_receipt_projection BEFORE INSERT ON conversation_agent_messages BEGIN SELECT RAISE(FAIL,'projection unavailable'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	base := &historyReference{id: thread}
	reader := &receiptThread{Thread: base, found: true, receipt: harness.TurnReceipt{ID: turn, Status: "completed", Raw: json.RawMessage(`{"id":"observed-turn","status":"completed","items":[{"id":"final","type":"agentMessage","text":"Original observed answer"}]}`), Items: []harness.Event{{ItemID: "final", TurnID: turn, ItemType: harness.ItemAgentMessage, Text: "Original observed answer"}}}}
	if err := e.m.reconcileNativeReceipts(t.Context(), reader); err == nil {
		t.Fatal("fixture did not reject message projection")
	}
	rec, ok, err := e.app.MainTurn(t.Context(), request)
	if err != nil || !ok || rec.Status != store.TurnCompleted {
		t.Fatal("observed native terminal disappeared with its presentation", rec, err)
	}
	if pending, err := e.app.NativeReceiptPending(t.Context(), request); err != nil || !pending {
		t.Fatal("failed output was reported delivered", pending, err)
	}
	if e.count(`SELECT COUNT(*) FROM agent_rollout_events WHERE thread_id=? AND turn_id=? AND kind='native_receipt'`, thread, turn) != 1 || e.count(`SELECT COUNT(*) FROM agent_rollout_events WHERE thread_id=? AND turn_id=? AND kind='native_receipt_projection'`, thread, turn) != 0 {
		t.Fatal("expected exactly one original and no duplicate projection body")
	}
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `DROP TRIGGER reject_receipt_projection`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.app.SetMainThread(t.Context(), e.m.bindingInstance(), thread); err != nil {
		t.Fatal(err)
	}
	retained, err := e.m.retainedReceiptThread(t.Context())
	if err != nil || retained == nil {
		t.Fatal("startup could not recover the presentation without provider", err)
	}
	if _, err := retained.StartTurn(t.Context(), harness.TurnInput{}); !errors.Is(err, harness.ErrRejected) {
		t.Fatal("retained presentation became executable", err)
	}
	opens := 0
	e.m.cfg.Open = func(context.Context) (harness.Session, error) {
		opens++
		return nil, errors.New("provider startup unavailable")
	}
	e.m.cfg.OpenNamed = func(context.Context, hostedconfig.Selection) (harness.Session, error) {
		opens++
		return nil, errors.New("provider startup unavailable")
	}
	if err := e.m.serve(t.Context()); err != nil {
		t.Fatal("real startup required provider to reproject original", err)
	}
	if opens != 0 {
		t.Fatal("original output retry opened provider", opens)
	}
	unavailable := &unavailableReceiptThread{Thread: retained}
	if err := e.m.reconcileNativeReceipts(t.Context(), unavailable); err != nil {
		t.Fatal("retained original could not finish its output projection", err)
	}
	if unavailable.reads != 0 {
		t.Fatal("projection retry needlessly queried provider")
	}
	if pending, err := e.app.NativeReceiptPending(t.Context(), request); err != nil || pending {
		t.Fatal("successful original projection did not settle", pending, err)
	}
	if e.count(`SELECT COUNT(*) FROM agent_rollout_events WHERE thread_id=? AND turn_id=? AND kind='native_receipt'`, thread, turn) != 1 {
		t.Fatal("retry duplicated original bytes")
	}
	if e.count(`SELECT COUNT(*) FROM conversation_messages WHERE body='Original observed answer'`) != 1 || len(e.h.Starts()) != 0 {
		t.Fatal("projection duplicated or replayed the original result")
	}
}
