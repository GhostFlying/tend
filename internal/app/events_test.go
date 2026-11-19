package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type eventView struct {
	Seq      int64
	ObjectID string
	Body     map[string]any
}

// events returns the events of one kind in commit order.
func (h *harness) events(kind string) []eventView {
	h.t.Helper()
	var out []eventView
	if err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		rows, err := tx.Q().QueryContext(context.Background(), `SELECT event_seq, object_id, body_json FROM events WHERE kind = ? ORDER BY event_seq`, kind)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v eventView
			var body string
			if err := rows.Scan(&v.Seq, &v.ObjectID, &body); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(body), &v.Body); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	}); err != nil {
		h.t.Fatal(err)
	}
	return out
}

// messageStates returns the states a message went through.
func (h *harness) messageStates(messageID string) []string {
	h.t.Helper()
	var out []string
	for _, e := range h.events("conversation.message_state") {
		if e.ObjectID == messageID {
			out = append(out, e.Body["state"].(string))
		}
	}
	return out
}

// say submits an owner message and claims its delivery to the main thread.
func (h *harness) say(body string) (string, Delivery) {
	h.t.Helper()
	op := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "message"}, map[string]string{"body": body}, nil), SubmitMessage{})
	var res struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(op.Result, &res); err != nil {
		h.t.Fatal(err)
	}
	d, _, ok, err := h.app.NextDelivery(context.Background(), MainThreadDestination)
	if err != nil || !ok {
		h.t.Fatalf("NextDelivery = %v, %v", ok, err)
	}
	return res.MessageID, d
}

// woken reports whether ch was closed.
func woken(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestFault_DomainDecisionVisible(t *testing.T) {
	h := newHarness(t)
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	impl, _, _ := h.claim("linux-1")
	h.runAs(impl, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	h.runAs(impl, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "worker_crashed"}, Release{})

	var decisions []store.DecisionRow
	if err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		var err error
		decisions, err = store.ListDecisions(context.Background(), tx.Q(), res.TaskID)
		return err
	}); err != nil || len(decisions) != 1 {
		t.Fatalf("decisions = %+v, %v", decisions, err)
	}
	opened := h.events("decision.opened")
	if len(opened) != 1 || opened[0].ObjectID != decisions[0].DecisionID || opened[0].Body["task_id"] != res.TaskID {
		t.Errorf("decision.opened events = %+v, want one for %s", opened, decisions[0].DecisionID)
	}
}

func TestMessageStatesFollowTheTurn(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id, d := h.say("hello")
	turn := store.MainTurn{RequestID: d.RequestID, MessageID: id, ThreadID: "th-1"}
	for _, status := range []string{store.TurnStarting, store.TurnStarted, store.TurnStarted, store.TurnCompleted} {
		if status == store.TurnCompleted {
			h.associate(mainAgent, id)
			commitFixtureTurn(t, h, d.RequestID)
		}
		turn.Status = status
		turn.TurnID = "turn-1"
		if err := h.app.PutMainTurn(ctx, turn); err != nil {
			t.Fatal(err)
		}
	}
	if got := h.messageStates(id); len(got) != 2 || got[0] != "processing" || got[1] != "replied" {
		t.Errorf("states = %v, want [processing replied]", got)
	}
}

func TestSettledTurnInterruptsItsMessage(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id, d := h.say("hello")
	if err := h.app.PutMainTurn(ctx, store.MainTurn{RequestID: d.RequestID, MessageID: id, ThreadID: "th-1", TurnID: "turn-1", Status: store.TurnStarted}); err != nil {
		t.Fatal(err)
	}
	var observation store.WriteObservation
	store.WithWriteObserver(time.Now, 0, func(o store.WriteObservation) { observation = o })(h.store)
	changed := h.app.Changes().Wait()
	if err := h.app.SettleOpenTurns(ctx, "th-1"); err != nil {
		t.Fatal(err)
	}
	if observation.Source.Operation != "mainthread.settle_open_turns" {
		t.Fatalf("settlement write source = %q", observation.Source.Operation)
	}
	wantItems := map[store.WritePhase]int{
		store.WriteSettleTurns: 1, store.WriteSettleTools: 0,
		store.WriteSettleMessages: 0, store.WriteSettleLogicalTurns: 0,
	}
	sawReceipts := false
	t.Logf("settlement writer wait=%s callback=%s", observation.ConnectionWait, observation.Callback)
	for _, phase := range observation.Phases {
		if phase.Phase == store.WriteSettleNativeReceipts {
			sawReceipts = true
			t.Logf("%s duration=%s", phase.Phase, phase.Duration)
		}
		if expected, ok := wantItems[phase.Phase]; ok {
			if phase.Items == nil || *phase.Items != expected {
				t.Fatalf("write phase %s = %+v, want %d items", phase.Phase, phase, expected)
			}
			t.Logf("%s selected=%d duration=%s", phase.Phase, *phase.Items, phase.Duration)
			delete(wantItems, phase.Phase)
		}
	}
	if !sawReceipts || len(wantItems) != 0 {
		t.Fatalf("settlement phases missing: receipts=%v counted=%v", sawReceipts, wantItems)
	}
	if got := h.messageStates(id); len(got) != 2 || got[1] != "interrupted" {
		t.Errorf("states = %v, want processing then interrupted", got)
	}
	if !woken(changed) {
		t.Error("settling turns did not notify changes")
	}
}

func TestSettleOpenTurnsDistinguishesQueryFailureFromEmptySelection(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	id, delivery := h.say("hello")
	turn := store.MainTurn{
		RequestID: delivery.RequestID, MessageID: id,
		ThreadID: "th-1", TurnID: "turn-1", Status: store.TurnStarted,
	}
	if err := h.app.PutMainTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `CREATE TRIGGER fail_turn_settlement
			BEFORE UPDATE OF status ON main_turns WHEN NEW.status = 'interrupted'
			BEGIN SELECT RAISE(ABORT, 'fixture prevented settlement'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var observation store.WriteObservation
	store.WithWriteObserver(time.Now, 0, func(o store.WriteObservation) { observation = o })(h.store)
	settlementPhase := func() store.WritePhaseDuration {
		t.Helper()
		for _, phase := range observation.Phases {
			if phase.Phase == store.WriteSettleTurns {
				return phase
			}
		}
		t.Fatalf("missing open turn phase in %+v", observation.Phases)
		return store.WritePhaseDuration{}
	}

	err := h.app.SettleOpenTurns(ctx, turn.ThreadID)
	if err == nil || !strings.Contains(err.Error(), "fixture prevented settlement") {
		t.Fatalf("expected injected query failure, got %v", err)
	}
	current, found, err := h.app.TurnDelivery(ctx, turn.ThreadID, turn.TurnID)
	if err != nil || !found || current.Status != store.TurnStarted {
		t.Fatalf("failing write changed the open turn: found=%v status=%q err=%v", found, current.Status, err)
	}
	if observation.Outcome != "error" || observation.Stage != "callback" {
		t.Fatalf("expected callback error, got outcome=%q stage=%q", observation.Outcome, observation.Stage)
	}
	failed := settlementPhase()
	if failed.Items != nil {
		t.Fatalf("failed selection reported %d items, want omitted count", *failed.Items)
	}
	t.Logf("failed selection rolled back, stage=%s duration=%s, count omitted", observation.Stage, failed.Duration)

	observation = store.WriteObservation{}
	if err := h.app.SettleOpenTurns(ctx, "empty-thread"); err != nil {
		t.Fatal(err)
	}
	empty := settlementPhase()
	if empty.Items == nil || *empty.Items != 0 {
		t.Fatalf("empty selection phase = %+v, want zero items", empty)
	}
	t.Logf("successful empty selection counted %d items, duration=%s", *empty.Items, empty.Duration)
}

func TestUndeliveredMessageStates(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, c := range []struct {
		outcome DeliveryOutcome
		want    string
	}{{Unknown, "unknown"}, {Failed, "failed"}} {
		id, d := h.say("hello")
		changed := h.app.Changes().Wait()
		if err := h.app.FinishDelivery(ctx, d, DeliveryResult{Outcome: c.outcome}); err != nil {
			t.Fatal(err)
		}
		if got := h.messageStates(id); len(got) != 1 || got[0] != c.want {
			t.Errorf("states = %v, want [%s]", got, c.want)
		}
		if !woken(changed) {
			t.Error("finishing a delivery did not notify changes")
		}
	}
}

func TestMaintenanceWritesWake(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})

	logs := h.app.RunLogs().Wait()
	changed := h.app.Changes().Wait()
	if _, err := h.app.RecordRunEvents(ctx, runPrincipal(as.RunID, as.Role), as.RunID, []protocol.RunEvent{{Seq: 1, Kind: "item.completed"}}); err != nil {
		t.Fatal(err)
	}
	if !woken(logs) {
		t.Error("recording a run log did not notify run logs")
	}
	if woken(changed) {
		t.Error("a log alone notified ledger changes")
	}

	h.clock.Advance(DefaultLeaseTTL + time.Second)
	if _, err := h.app.ExpireLeases(ctx); err != nil {
		t.Fatal(err)
	}
	if !woken(changed) {
		t.Error("expiring a lease did not notify changes")
	}
}
