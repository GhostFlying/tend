package mainthread

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestFault_EventWakeUnknownStartIsNotReplayed(t *testing.T) {
	h := testkit.NewHarness()
	e := newEnv(t, h)
	e.stop()
	openWakeDecision(e)
	d, _, ok, err := e.app.NextDelivery(t.Context(), app.MainThreadDestination)
	if err != nil || !ok {
		t.Fatalf("claim event: %v, %v", ok, err)
	}
	var wake app.HandleMessage
	if err := json.Unmarshal(d.Payload, &wake); err != nil {
		t.Fatal(err)
	}
	if err := e.app.PutMainTurn(t.Context(), store.MainTurn{
		RequestID: d.RequestID, MessageID: wake.MessageID, ThreadID: "previous-native-thread", Status: store.TurnStarting,
	}); err != nil {
		t.Fatal(err)
	}
	e.start()
	eventually(t, "ambiguous event start becomes unknown", func() bool {
		return e.count(`SELECT COUNT(*) FROM outbox WHERE request_id=? AND status='unknown'`, d.RequestID) == 1
	})
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		active, err := store.HasActiveMainTurn(t.Context(), tx.Q(), "previous-native-thread")
		if err == nil && active {
			t.Fatal("unknown background turn still blocks handoff")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	e.say("Explain the current status")
	eventually(t, "owner message continues after ambiguous event", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_turns WHERE status='completed'`) == 1
	})
	if len(h.Starts()) != 1 || strings.Contains(h.Starts()[0].Text, "Tend ledger event") {
		t.Fatalf("unknown event turn blindly replayed: %+v", h.Starts())
	}
}

func openWakeDecision(e *env) {
	e.exec(owner, "decisions.open", protocol.Target{Type: "decision"}, map[string]any{
		"question": "Select the investigation scope", "options": []string{"narrow", "broad"},
	}, app.OpenDecision{})
}

func TestTasklessAnsweredDecisionWakeCarriesAnswerIntoFreshThread(t *testing.T) {
	h := testkit.NewHarness()
	e := newEnv(t, h)
	e.stop()
	var opened struct {
		DecisionID string `json:"decision_id"`
	}
	// No owner message or prior native turn carries this taskless choice.
	// Only the durable answered event can supply it to the fresh thread.
	if err := json.Unmarshal(e.exec(protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "main", Thread: "previous-thread"},
		"decisions.open", protocol.Target{Type: "decision"}, map[string]any{
			"question": "Which scope should the investigation use?", "options": []string{"narrow", "broad"},
		}, app.OpenDecision{}), &opened); err != nil {
		t.Fatal(err)
	}
	e.exec(owner, "decisions.respond", protocol.Target{Type: "decision", ID: opened.DecisionID},
		protocol.AnswerDecisionPayload{Version: 1, Option: "narrow", Text: "Limit it to the renderer; preserve the loader."}, app.AnswerDecision{})
	e.start()
	eventually(t, "taskless answer event completes", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_turns WHERE status='completed'`) == 1
	})
	starts := h.Starts()
	if len(starts) != 1 || e.count(`SELECT COUNT(*) FROM conversation_messages WHERE author='owner'`) != 0 {
		t.Fatalf("unexpected original owner input: %+v", starts)
	}
	for _, text := range []string{"decision.answered", opened.DecisionID, `"option":"narrow"`, `"text":"Limit it to the renderer; preserve the loader."`} {
		if !strings.Contains(starts[0].Text, text) {
			t.Errorf("fresh event turn lost canonical answer %q: %s", text, starts[0].Text)
		}
	}
}

func TestOwnerCanStopBackgroundEventTurn(t *testing.T) {
	h := testkit.NewHarness()
	h.Turn = func(_ *testkit.FakeThread, _ string, in harness.TurnInput) string {
		if strings.Contains(in.Text, "Tend ledger event") {
			return "" // Remains running until the owner's interrupt.
		}
		return "completed"
	}
	e := newEnv(t, h)
	openWakeDecision(e)
	eventually(t, "event starts", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_turns WHERE status='started'`) == 1
	})
	message := h.Starts()[0].UserMessageID
	e.exec(owner, "conversation.interrupt_reply", protocol.Target{Type: "message", ID: message},
		protocol.InterruptReplyPayload{MessageID: message}, app.InterruptReply{})
	eventually(t, "event stops", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_turns WHERE message_id=? AND status='interrupted'`, message) == 1
	})
	e.say("Now use the revised scope")
	eventually(t, "owner resumes coordination", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_turns WHERE status='completed'`) == 1
	})
	// A delayed second stop returns the settled state, just as for owner turns.
	var result struct{ State string }
	if err := json.Unmarshal(e.exec(owner, "conversation.interrupt_reply", protocol.Target{Type: "message", ID: message},
		protocol.InterruptReplyPayload{MessageID: message}, app.InterruptReply{}), &result); err != nil || result.State != "interrupted" {
		t.Fatalf("settled event stop = %+v, %v", result, err)
	}
}

func TestEventWakeSurvivesRestartAndKeepsOwnerPriority(t *testing.T) {
	h := testkit.NewHarness()
	h.Turn = func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {
		th.Say(turn, "answer", "The current decision still needs your choice.")
		return "completed"
	}
	e := newEnv(t, h)
	e.stop()
	openWakeDecision(e)
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		snapshot, err := store.GetHandoffSnapshot(t.Context(), tx.Q(), "main")
		if err == nil && !snapshot.Pending {
			t.Fatal("pending background event was omitted from summary coverage")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ownerMessage := e.say("Clarify my scope first")
	e.start()
	eventually(t, "owner and event turns finish", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_turns WHERE status = 'completed'`) == 2
	})
	starts := h.Starts()
	if len(starts) != 2 || starts[0].UserMessageID != ownerMessage {
		t.Fatalf("owner priority lost after restart: %+v", starts)
	}
	for _, text := range []string{"Tend ledger event", "decision.opened", "not an owner request", "already be superseded", "grants no new authority"} {
		if !strings.Contains(starts[1].Text, text) {
			t.Errorf("event input missing %q: %s", text, starts[1].Text)
		}
	}
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		messages, err := store.ListConversation(t.Context(), tx.Q(), 0, 0, 100)
		if err != nil {
			return err
		}
		var system, replies int
		for _, msg := range messages {
			if msg.Author == "system" && msg.MessageID == starts[1].UserMessageID {
				system++
			}
			if msg.Author == "main_agent" && msg.ReplyTo == starts[1].UserMessageID {
				replies++
			}
		}
		if system != 1 || replies != 1 {
			t.Fatalf("event response lost system provenance: system=%d replies=%d", system, replies)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.stop()
	e.start()
	// A later foreground message provides a barrier proving the restarted
	// dispatcher drained the queue without replaying the completed event.
	e.say("What is the status?")
	eventually(t, "post-restart message finishes", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_turns WHERE status = 'completed'`) == 3
	})
	if len(h.Starts()) != 3 {
		t.Fatalf("completed background turn replayed: %+v", h.Starts())
	}
}

func TestEventWakeWaitsForActiveOwnerAndPendingOwner(t *testing.T) {
	h := testkit.NewHarness()
	release := make(chan struct{})
	h.Turn = func(_ *testkit.FakeThread, _ string, in harness.TurnInput) string {
		if strings.Contains(in.Text, "Hold the owner turn") {
			<-release
		}
		return "completed"
	}
	e := newEnv(t, h)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	e.say("Hold the owner turn")
	eventually(t, "owner turn starts", func() bool { return len(h.Starts()) == 1 })
	openWakeDecision(e)
	second := e.say("A newer owner instruction")
	if len(h.Starts()) != 1 {
		t.Fatal("background wake interrupted or overlapped owner")
	}
	close(release)
	eventually(t, "all serial turns finish", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_turns WHERE status = 'completed'`) == 3
	})
	starts := h.Starts()
	if starts[1].UserMessageID != second || !strings.Contains(starts[2].Text, "Tend ledger event") {
		t.Fatalf("pending owner did not precede event: %+v", starts)
	}
}
