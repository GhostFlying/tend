package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestAnsweredDecisionWakeCarriesCanonicalEffectResult(t *testing.T) {
	h := newHarness(t)
	_, decision := h.failImplementation()
	answer := h.answer(decision, "retry the implementation")
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		items, err := store.ListConversation(t.Context(), tx.Q(), 0, 0, 20)
		if err != nil {
			return err
		}
		last := items[len(items)-1]
		if !strings.Contains(last.Body, "retry the implementation") || !strings.Contains(last.Body, "已执行：重新实现") || strings.Contains(last.Body, `"applied"`) || strings.Contains(last.Body, "```json") {
			t.Fatalf("chat notice omitted readable answer or exposed protocol JSON: %s", last.Body)
		}
		var raw string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT payload_json FROM outbox WHERE operation=? AND json_extract(payload_json,'$.message_id')=?`, OpHandleEvent, last.MessageID).Scan(&raw); err != nil {
			return err
		}
		var wake HandleMessage
		if err := json.Unmarshal([]byte(raw), &wake); err != nil {
			return err
		}
		if string(wake.DecisionAnswer) != string(answer.Answer) || !strings.Contains(string(wake.DecisionAnswer), `"applied":true`) {
			t.Fatalf("durable event wake lost canonical answer: %s", wake.DecisionAnswer)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestEventWakeFailureDecisionAndOwnerAnswerAreDurable(t *testing.T) {
	h := newHarness(t)
	_, decision := h.failImplementation()
	d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok || d.Operation != OpHandleEvent {
		t.Fatalf("failure decision wake: %+v, %v, %v", d, ok, err)
	}
	var wake HandleMessage
	if err := json.Unmarshal(d.Payload, &wake); err != nil || wake.EventKind != "decision.opened" || wake.EventID == "" {
		t.Fatalf("wake = %+v, %v", wake, err)
	}
	msg, err := h.app.Message(t.Context(), wake.MessageID)
	if err != nil || msg.Author != "system" {
		t.Fatalf("event provenance = %+v, %v", msg, err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		metadata, err := store.GetMessageNotice(t.Context(), tx.Q(), wake.MessageID)
		if err == nil && metadata.NoticeType != "decision_opened" {
			t.Fatalf("decision notice type = %q", metadata.NoticeType)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Delivered}); err != nil {
		t.Fatal(err)
	}
	env := h.env("decisions.respond", protocol.Target{Type: "decision", ID: decision.DecisionID}, protocol.AnswerDecisionPayload{Version: decision.Version, Option: "revise the spec"}, nil)
	h.exec(owner, env, AnswerDecision{})
	h.exec(owner, env, AnswerDecision{})
	// Reconstruct the app as on restart; pending outbox work survives it.
	h.app = New(h.store, h.ids, h.clock.Now)
	d, _, ok, err = h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok {
		t.Fatalf("answer wake after restart = %+v, %v, %v", d, ok, err)
	}
	if err := json.Unmarshal(d.Payload, &wake); err != nil || wake.EventKind != "decision.answered" {
		t.Fatalf("answer wake = %+v, %v", wake, err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		metadata, err := store.GetMessageNotice(t.Context(), tx.Q(), wake.MessageID)
		if err == nil && metadata.NoticeType != "decision_answered" {
			t.Fatalf("answer notice type = %q", metadata.NoticeType)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Delivered}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination); err != nil || ok {
		t.Fatalf("replay duplicated event wake: %v, %v", ok, err)
	}
}

func TestEventWakeDoesNotLoopOrPollProgress(t *testing.T) {
	addon := mainAgent
	addon.Agent = "codex"
	if !wakesMain(addon, "decision.answered") {
		t.Fatal("external agent decision answer did not wake hosted main")
	}
	for _, kind := range []string{"run.progress", "run.heartbeat", "run.failed", "conversation.message", "decision.opened", "decision.answered"} {
		if wakesMain(mainAgent, kind) {
			t.Errorf("main agent event %s woke itself", kind)
		}
	}
	h := newHarness(t)
	h.exec(mainAgent, h.env("decisions.open", protocol.Target{Type: "decision"}, openDecisionPayload{Question: "Need owner scope", Options: []string{"a", "b"}}, nil), OpenDecision{})
	if _, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination); err != nil || ok {
		t.Fatalf("main agent decision queued itself: %v, %v", ok, err)
	}
}

func TestOwnerDeliveryPrecedesPendingEventButNotInflightWork(t *testing.T) {
	h := newHarness(t)
	h.failImplementation()
	h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{Body: "Pause and clarify first"}, nil), SubmitMessage{})
	d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok || d.Operation != OpHandleMessage {
		t.Fatalf("owner not prioritized: %+v, %v", d, err)
	}
	if _, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination); err != nil || ok {
		t.Fatalf("event overtook in-flight owner: %v, %v", ok, err)
	}
	if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Delivered}); err != nil {
		t.Fatal(err)
	}
	d, _, ok, err = h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok || d.Operation != OpHandleEvent {
		t.Fatalf("event lost after owner: %+v, %v", d, err)
	}
	h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{Body: "Another owner message"}, nil), SubmitMessage{})
	if _, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination); err != nil || ok {
		t.Fatalf("owner overlapped in-flight event: %v, %v", ok, err)
	}
}

func TestFault_EventWakeQueueFailureRollsBackDecision(t *testing.T) {
	h := newHarness(t)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE TRIGGER reject_event_wake BEFORE INSERT ON outbox
			WHEN NEW.operation = 'main_thread.handle_event' BEGIN SELECT RAISE(ABORT, 'queue unavailable'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	env := h.env("decisions.open", protocol.Target{Type: "decision"}, openDecisionPayload{Question: "Scope?"}, nil)
	if _, err := h.app.Execute(t.Context(), owner, env, OpenDecision{}); err == nil {
		t.Fatal("decision committed without durable wake")
	}
	if h.count("decisions") != 0 || h.count("conversation_messages") != 0 {
		t.Fatal("failed wake transaction leaked decision or message")
	}
}

func TestInterruptRejectsSystemNoticeWithoutEventTurn(t *testing.T) {
	h := newHarness(t)
	id := h.ids.New(protocol.PrefixMessage)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: id, Author: "system", Body: "Upgrade completed", CreatedAt: h.clock.Now().UnixMilli()})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	op := h.exec(owner, h.env("conversation.interrupt_reply", protocol.Target{Type: "message", ID: id}, protocol.InterruptReplyPayload{MessageID: id}, nil), InterruptReply{})
	if op.Error == nil || op.Error.Code != protocol.CodeInvalidRequest {
		t.Fatalf("unrelated notice admitted as stoppable event turn: %+v", op)
	}
}
