package app

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func queueMessage(t *testing.T, h *harness, text string) string {
	t.Helper()
	op := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{Body: text}, nil), SubmitMessage{})
	if op.Status != protocol.OpAccepted {
		t.Fatalf("submit: %+v", op)
	}
	var r struct {
		ID string `json:"message_id"`
	}
	if err := json.Unmarshal(op.Result, &r); err != nil {
		t.Fatal(err)
	}
	return r.ID
}
func readMessageQueue(t *testing.T, h *harness, category string) protocol.ConversationQueuePage {
	t.Helper()
	var out protocol.ConversationQueuePage
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		out, err = views.ConversationQueue(t.Context(), tx.Q(), category, 0, 50)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}
func TestWithdrawMessagePreservesHistoryAndFencesDelivery(t *testing.T) {
	h := newHarness(t)
	id := queueMessage(t, h, "do not execute")
	q := readMessageQueue(t, h, "owner")
	if q.Total != 1 || !q.Items[0].CanWithdraw {
		t.Fatalf("queue: %+v", q)
	}
	env := h.env("conversation.withdraw_message", protocol.Target{Type: "message", ID: id}, protocol.WithdrawMessagePayload{}, rev(q.Items[0].Revision))
	op := h.exec(owner, env, WithdrawMessage{})
	if op.Status != protocol.OpSucceeded {
		t.Fatalf("withdraw: %+v", op)
	}
	again := h.exec(owner, env, WithdrawMessage{})
	if string(again.Result) != string(op.Result) {
		t.Fatal("withdraw replay changed receipt")
	}
	if q := readMessageQueue(t, h, "owner"); q.Total != 0 {
		t.Fatal(q)
	}
	if _, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination); err != nil || ok {
		t.Fatalf("withdrawn delivery claimed: %v %v", ok, err)
	}
	if err := h.app.PutMainTurn(t.Context(), store.MainTurn{RequestID: "late", MessageID: id, ThreadID: "test", Status: store.TurnStarting}); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		t.Fatalf("late start: %v", err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		page, err := views.Conversation(t.Context(), tx.Q(), 0, 0, 50)
		if err != nil {
			return err
		}
		it := page.Items[0]
		if it.Body != "do not execute" || it.State != protocol.MessageInterrupted || it.Control.WithdrawnAt == 0 {
			t.Fatalf("audit: %+v", it)
		}
		if err := store.HideUndeliveredOwner(t.Context(), tx.Q(), &it); err != nil {
			return err
		}
		if it.Body == "do not execute" {
			t.Fatal("withdrawn owner text leaked")
		}
		transcript, err := views.Transcript(t.Context(), tx.Q(), 0, 20)
		if err == nil && len(transcript.Blocks) != 0 {
			t.Fatal("withdrawn message in transcript")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
func TestFault_WithdrawAndClaimHaveOnlyOneWinner(t *testing.T) {
	for i := 0; i < 20; i++ {
		h := newHarness(t)
		id := queueMessage(t, h, "one winner")
		q := readMessageQueue(t, h, "owner")
		env := h.env("conversation.withdraw_message", protocol.Target{Type: "message", ID: id}, protocol.WithdrawMessagePayload{}, rev(q.Items[0].Revision))
		var op protocol.Operation
		var opErr, claimErr error
		var claimed bool
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			op, opErr = h.app.Execute(context.Background(), owner, env, WithdrawMessage{})
		}()
		go func() {
			defer wg.Done()
			_, _, claimed, claimErr = h.app.NextDelivery(context.Background(), MainThreadDestination)
		}()
		wg.Wait()
		if opErr != nil || claimErr != nil {
			t.Fatalf("race errors: %v %v", opErr, claimErr)
		}
		if (op.Status == protocol.OpSucceeded) == claimed {
			t.Fatalf("withdraw=%s claimed=%v", op.Status, claimed)
		}
	}
}
func TestMessageQueueAdmissionAndTranscriptGrouping(t *testing.T) {
	h := newHarness(t)
	first := queueMessage(t, h, "first")
	d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok {
		t.Fatal(err)
	}
	second := queueMessage(t, h, "second")
	q := readMessageQueue(t, h, "owner")
	if q.Total != 2 || q.Items[0].CanWithdraw || q.Items[0].Status != "starting" {
		t.Fatal(q)
	}
	for _, state := range []string{store.TurnStarting, store.TurnStarted} {
		if err := h.app.PutMainTurn(t.Context(), store.MainTurn{RequestID: d.RequestID, MessageID: first, ThreadID: "thread", TurnID: "turn", Status: state}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: "answer", Author: "main_agent", ReplyTo: first, Body: "answer first", CreatedAt: h.clock.Now().UnixMilli()})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		page, err := views.Transcript(t.Context(), tx.Q(), 0, 20)
		if err != nil {
			return err
		}
		if len(page.Blocks) != 1 || page.Blocks[0].Trigger.MessageID != first || len(page.Blocks[0].Items) != 1 {
			t.Fatalf("transcript %+v", page)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	q = readMessageQueue(t, h, "owner")
	if q.Items[0].Status != "processing" || q.Items[1].Item.MessageID != second || !q.Items[1].CanWithdraw {
		t.Fatal(q)
	}
}

func TestWithdrawnMessageInvalidatesSummaryAndRedactsRecovery(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	if _, err := h.app.BindMainThread(ctx, "main", "source", "schema", "source.rollout"); err != nil {
		t.Fatal(err)
	}
	snap, err := h.app.HandoffSnapshot(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.SaveHandoffSummary(ctx, store.HandoffSummary{Instance: "main", Snapshot: snap, Body: "prior summary", FormatVersion: 2}); err != nil {
		t.Fatal(err)
	}
	id := queueMessage(t, h, "withdrawn instruction marker")
	q := readMessageQueue(t, h, "owner")
	op := h.exec(owner, h.env("conversation.withdraw_message", protocol.Target{Type: "message", ID: id}, protocol.WithdrawMessagePayload{}, rev(q.Items[0].Revision)), WithdrawMessage{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if _, ok, err := h.app.HandoffSummary(ctx, "main"); err != nil || ok {
		t.Fatalf("old summary reused: %v %v", ok, err)
	}
	input, err := h.app.HandoffSummaryInput(ctx, "main", 256<<10)
	if err != nil {
		t.Fatal(err)
	}
	if input.PreviousSummary != nil || strings.Contains(string(input.Observations), "withdrawn instruction marker") {
		t.Fatalf("recovery exposed withdrawn instruction: %s", input.Observations)
	}
}

func TestBlockReadPreservesPosition(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: "notice", Author: "system", Body: "notice"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
		page, err := views.Transcript(ctx, tx.Q(), 0, 20)
		if err != nil {
			return err
		}
		single, err := views.TranscriptBlock(ctx, tx.Q(), "notice", 0, 50)
		if err != nil {
			return err
		}
		if single.Position != page.Blocks[0].Position {
			t.Errorf("block lookup position=%d, transcript position=%d", single.Position, page.Blocks[0].Position)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBackgroundRepliesRespectProcessingOrder(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		if _, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: "background", Author: "system", Body: "background fact"}); err != nil {
			return err
		}
		return tx.InsertOutbox(ctx, store.OutboxRow{OutboxID: h.ids.New(protocol.PrefixOutbox), RequestID: h.ids.New(protocol.PrefixRequest), Destination: MainThreadDestination, Operation: OpHandleEvent, PayloadJSON: `{"message_id":"background","event_id":"event","event_kind":"task.completed"}`})
	}); err != nil {
		t.Fatal(err)
	}
	foreground := queueMessage(t, h, "prioritized foreground")
	first, _, ok, err := h.app.NextDelivery(ctx, MainThreadDestination)
	if err != nil || !ok || first.Operation != OpHandleMessage {
		t.Fatalf("first claim %+v %v %v", first, ok, err)
	}
	for _, status := range []string{store.TurnStarting, store.TurnStarted, store.TurnCompleted} {
		if err := h.app.PutMainTurn(ctx, store.MainTurn{RequestID: first.RequestID, MessageID: foreground, ThreadID: "thread", TurnID: "first", Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.app.FinishDelivery(ctx, first, DeliveryResult{Outcome: Delivered}); err != nil {
		t.Fatal(err)
	}
	second, _, ok, err := h.app.NextDelivery(ctx, MainThreadDestination)
	if err != nil || !ok || second.Operation != OpHandleEvent {
		t.Fatalf("second claim %+v %v %v", second, ok, err)
	}
	for _, status := range []string{store.TurnStarting, store.TurnStarted} {
		if err := h.app.PutMainTurn(ctx, store.MainTurn{RequestID: second.RequestID, MessageID: "background", ThreadID: "thread", TurnID: "second", Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: "answer", Author: "main_agent", ReplyTo: "background", Body: "later answer"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
		page, err := views.Transcript(ctx, tx.Q(), 0, 20)
		if err != nil {
			return err
		}
		t.Logf("processing order: [%s background]; transcript order: [%s %s]", foreground, page.Blocks[1].Trigger.MessageID, page.Blocks[2].Trigger.MessageID)
		if len(page.Blocks) != 3 || !page.Blocks[0].NoticeOnly || len(page.Blocks[0].Items) != 0 || page.Blocks[1].Trigger.MessageID != foreground || page.Blocks[2].NoticeOnly {
			t.Errorf("later background answer is displayed before the foreground turn")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAddonRelayInTranscript(t *testing.T) {
	h := newHarness(t)
	c := h.askChoice(h.createWork(workPlan()), h.ownerEvidence().Reference)
	cmd := UpdateWorkItemChoice{Action: "respond", FromMessage: true}
	addon := protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "addon", Thread: "native", Agent: protocol.AgentCodex}
	op := h.exec(addon, h.env(cmd.Operation(), protocol.Target{Type: "work_item_choice", ID: c.ChoiceID}, protocol.UpdateWorkItemChoicePayload{Answer: "OWNER_RELAY_MARKER"}, rev(c.Version)), cmd)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var answered protocol.WorkItemChoice
	if err := json.Unmarshal(op.Result, &answered); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		page, err := views.Transcript(t.Context(), tx.Q(), 0, 20)
		if err != nil {
			return err
		}
		found := false
		for _, b := range page.Blocks {
			if b.Trigger.MessageID == answered.AnswerSourceID {
				found = true
			}
			for _, it := range b.Items {
				if it.MessageID == answered.AnswerSourceID {
					found = true
				}
			}
		}
		t.Logf("source=%s choice=%s transcript=%+v", answered.AnswerSourceID, c.MessageID, page.Blocks)
		if !found {
			t.Error("recorded addon relay answer is completely absent from transcript")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWithdrawalNotifiesDispatcher(t *testing.T) {
	h := newHarness(t)
	id := queueMessage(t, h, "pending head")
	d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Retry, RetryAfter: 60 * 1000000000}); err != nil {
		t.Fatal(err)
	}
	queueMessage(t, h, "ready second")
	q := readMessageQueue(t, h, "owner")
	n := &choiceNotifier{}
	h.app.notifier = n
	op := h.exec(owner, h.env("conversation.withdraw_message", protocol.Target{Type: "message", ID: id}, protocol.WithdrawMessagePayload{}, rev(q.Items[0].Revision)), WithdrawMessage{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	t.Logf("notifications after withdrawal=%d", n.calls)
	if n.calls == 0 {
		t.Error("withdrawing a blocked head does not wake the dispatcher")
	}
}
