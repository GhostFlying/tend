package app

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func bindFeishuFixture(t *testing.T, h *harness) {
	t.Helper()
	if err := h.app.ConfigureFeishuRuntime(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	op := h.exec(owner, h.env("messaging.feishu_bind", protocol.Target{Type: "channel", ID: "feishu"}, protocol.BindFeishu{AppID: "app", TenantKey: "tenant", SenderID: "person", ChatID: "chat"}, rev(1)), FeishuCommand{})
	if op.Error != nil {
		t.Fatal(op.Error)
	}
}

func TestFeishuReplayKeepsOriginalRequestAndOneMessage(t *testing.T) {
	h := newHarness(t)
	bindFeishuFixture(t, h)
	in := FeishuIncoming{EventID: "event-1", MessageID: "external-1", AppID: "app", TenantKey: "tenant", SenderID: "person", ChatID: "chat", Text: "Please investigate"}
	const n = 6
	results := make([]protocol.Operation, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { results[i], errs[i] = h.app.ReceiveFeishu(t.Context(), in) })
	}
	wg.Wait()
	for i, op := range results {
		if errs[i] != nil || op.Error != nil || op.RequestID != results[0].RequestID || string(op.Result) != string(results[0].Result) {
			t.Fatalf("result %d: %+v %v", i, op, errs[i])
		}
	}
	if h.count("feishu_inputs") != 1 || h.count("messaging_messages") != 1 {
		t.Fatal("callback was duplicated")
	}
	in.Text = "changed"
	if _, err := h.app.ReceiveFeishu(t.Context(), in); protocol.CodeOf(err) != protocol.CodeConflictPayload {
		t.Fatal(err)
	}
	in.EventID = "event-2"
	in.MessageID = "external-2"
	in.SenderID = "unbound-person"
	if _, err := h.app.ReceiveFeishu(t.Context(), in); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal(err)
	}
	if h.count("feishu_inputs") != 1 || h.count("messaging_messages") != 1 {
		t.Fatal("unauthorized callback persisted")
	}
}

func TestFeishuUnknownDeliveryAndBindingChange(t *testing.T) {
	h := newHarness(t)
	bindFeishuFixture(t, h)
	op := h.exec(owner, h.env("messaging.notify", protocol.Target{Type: "message"}, protocol.MessagingPublication{Body: "Result ready", Purpose: "final", Reason: "requested_result"}, nil), MessagingCommand{Action: "notify"})
	if op.Error != nil {
		t.Fatal(op.Error)
	}
	d, _, ok, err := h.app.NextDelivery(t.Context(), FeishuDestination)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	var part FeishuPart
	if json.Unmarshal(d.Payload, &part) != nil || part.Revision != 2 || part.ChatID != "chat" {
		t.Fatalf("%s", d.Payload)
	}
	if err = h.app.ValidateFeishuPart(t.Context(), part); err != nil {
		t.Fatal(err)
	}
	if err = h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Unknown, Reason: "missing_receipt"}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err = h.app.NextDelivery(t.Context(), FeishuDestination); err != nil || ok {
		t.Fatal("unknown replayed", ok, err)
	}
	op = h.exec(owner, h.env("messaging.feishu_unbind", protocol.Target{Type: "channel", ID: "feishu"}, protocol.BindFeishu{}, rev(2)), FeishuCommand{Unbind: true})
	if op.Error != nil {
		t.Fatal(op.Error)
	}
	if err = h.app.ValidateFeishuPart(t.Context(), part); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal(err)
	}
}

func TestFeishuReplyUsesBoundOriginalMessage(t *testing.T) {
	h := newHarness(t)
	bindFeishuFixture(t, h)
	in := FeishuIncoming{EventID: "event-1", MessageID: "external-1", AppID: "app", TenantKey: "tenant", SenderID: "person", ChatID: "chat", Text: "A new request"}
	op, err := h.app.ReceiveFeishu(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	first := messageID(t, op)
	in.EventID = "event-2"
	in.MessageID = "external-2"
	in.ParentID = "external-1"
	in.Text = "A followup"
	op, err = h.app.ReceiveFeishu(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	second := messageID(t, op)
	m, err := h.app.MessagingMessage(t.Context(), second)
	if err != nil || m.ReplyTo != first || m.ChannelID != "feishu" {
		t.Fatal(m, err)
	}
	in.EventID = "event-3"
	in.MessageID = "external-3"
	in.ParentID = "unrelated-external"
	op, err = h.app.ReceiveFeishu(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if op.Error == nil || op.Error.Code != protocol.CodeNotFound {
		t.Fatal(op)
	}
}

func TestFeishuStagedInputRecoversOnlyBeforeRequestCommit(t *testing.T) {
	h := newHarness(t)
	bindFeishuFixture(t, h)
	input := FeishuIncoming{EventID: "durable-event", MessageID: "durable-message", AppID: "app", TenantKey: "tenant", SenderID: "person", ChatID: "chat", Text: "recover persisted input"}
	payload, _ := json.Marshal(input)
	request := h.ids.New(protocol.PrefixRequest)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO feishu_inputs(event_id,external_message_id,request_id,payload,owner,binding_revision) VALUES(?,?,?,?,?,2)`, input.EventID, input.MessageID, request, string(payload), owner.Subject)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	pending, err := h.app.PendingFeishuInputs(t.Context())
	if err != nil || len(pending) != 1 {
		t.Fatal(pending, err)
	}
	op, err := h.app.ReceiveFeishu(t.Context(), pending[0])
	if err != nil || op.Error != nil || op.RequestID != request {
		t.Fatal(op, err)
	}
	if pending, err = h.app.PendingFeishuInputs(t.Context()); err != nil || len(pending) != 0 {
		t.Fatal(pending, err)
	}
	// A recorded unknown is an outcome, not a missing transaction. Recovery must
	// never turn it into a new owner request or another message.
	if err = h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE request_records SET status='unknown' WHERE domain='owner' AND request_id=?`, request)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if pending, err = h.app.PendingFeishuInputs(t.Context()); err != nil || len(pending) != 0 {
		t.Fatal("unknown replayed", pending, err)
	}
}

func TestFeishuReplyDoesNotAdoptReplacementChat(t *testing.T) {
	h := newHarness(t)
	bindFeishuFixture(t, h)
	input := FeishuIncoming{EventID: "original-event", MessageID: "original-message", AppID: "app", TenantKey: "tenant", SenderID: "person", ChatID: "chat", Text: "private original"}
	op, err := h.app.ReceiveFeishu(t.Context(), input)
	if err != nil || op.Error != nil {
		t.Fatal(op, err)
	}
	source := messageID(t, op)
	d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	turn := store.MainTurn{RequestID: d.RequestID, MessageID: source, ThreadID: mainAgent.Thread, TurnID: "admitted-native", Status: store.TurnStarted}
	if err = h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	replacement := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "replacement-owner", Client: "fixture"}
	if op := h.exec(replacement, h.env("messaging.feishu_bind", protocol.Target{Type: "channel", ID: "feishu"}, protocol.BindFeishu{AppID: "app", TenantKey: "tenant", SenderID: "other-person", ChatID: "other-chat"}, rev(2)), FeishuCommand{}); op.Error != nil {
		t.Fatal(op)
	}
	env := h.env("conversation.publish", protocol.Target{Type: "logical_turn", ID: turn.RequestID}, protocol.MessagingPublication{ReplyTo: source, Body: "original owner's private result", Purpose: "final"}, nil)
	env.ParentRequestID = turn.RequestID
	op = h.exec(mainAgent, env, PublishCommunication{})
	if op.Error != nil {
		t.Fatal(op)
	}
	var state string
	var revision int64
	if err = h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT state,binding_revision FROM messaging_deliveries WHERE message_id=? AND channel_id='feishu'`, messageID(t, op)).Scan(&state, &revision)
	}); err != nil {
		t.Fatal(err)
	}
	if state != "unavailable" || revision != 2 {
		t.Fatal("reply adopted replacement binding", state, revision)
	}
	if _, _, ready, err := h.app.NextDelivery(t.Context(), FeishuDestination); err != nil || ready {
		t.Fatal("private reply queued to replacement", ready, err)
	}
}

func TestFeishuStagedParentDoesNotPermanentlyFailChild(t *testing.T) {
	h := newHarness(t)
	bindFeishuFixture(t, h)
	parent := FeishuIncoming{EventID: "parent-event", MessageID: "parent-message", AppID: "app", TenantKey: "tenant", SenderID: "person", ChatID: "chat", Text: "original staged parent"}
	payload, _ := json.Marshal(parent)
	request := h.ids.New(protocol.PrefixRequest)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO feishu_inputs(event_id,external_message_id,request_id,payload,owner,binding_revision) VALUES(?,?,?,?,?,2)`, parent.EventID, parent.MessageID, request, string(payload), owner.Subject)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	child := parent
	child.EventID = "child-event"
	child.MessageID = "child-message"
	child.ParentID = parent.MessageID
	child.Text = "reply before parent committed"
	if _, err := h.app.ReceiveFeishu(t.Context(), child); protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
		t.Fatal(err)
	}
	pending, err := h.app.PendingFeishuInputs(t.Context())
	if err != nil || len(pending) != 1 || pending[0].EventID != parent.EventID {
		t.Fatal("dependency recovery order", pending, err)
	}
	parentOp, err := h.app.ReceiveFeishu(t.Context(), parent)
	if err != nil || parentOp.Error != nil {
		t.Fatal(parentOp, err)
	}
	pending, err = h.app.PendingFeishuInputs(t.Context())
	if err != nil || len(pending) != 1 || pending[0].EventID != child.EventID {
		t.Fatal(pending, err)
	}
	childOp, err := h.app.ReceiveFeishu(t.Context(), child)
	if err != nil || childOp.Error != nil {
		t.Fatal(childOp, err)
	}
	message, err := h.app.MessagingMessage(t.Context(), messageID(t, childOp))
	if err != nil || message.ReplyTo != messageID(t, parentOp) {
		t.Fatal(message, err)
	}
}

func TestFeishuInteractionAnswersKeepSourceAndLateText(t *testing.T) {
	for _, answeredElsewhere := range []bool{false, true} {
		t.Run(map[bool]string{false: "open", true: "closed"}[answeredElsewhere], func(t *testing.T) {
			h := newHarness(t)
			bindFeishuFixture(t, h)
			a := attentionForTest(t, h, protocol.CreateAttentionPayload{Kind: "action", Title: "Check report"})
			page, err := h.app.MessagingMessages(t.Context(), 0, "", "", "")
			if err != nil {
				t.Fatal(err)
			}
			var question protocol.MessagingMessage
			for _, m := range page.Items {
				if m.Interaction != nil && m.Interaction.ObjectID == a.AttentionID {
					question = m
				}
			}
			if question.MessageID == "" {
				t.Fatal("missing interaction publication")
			}
			for {
				d, _, ok, err := h.app.NextDelivery(t.Context(), FeishuDestination)
				if err != nil {
					t.Fatal(err)
				}
				if !ok {
					break
				}
				if err = h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Delivered, ExternalMessageReference: "external-question"}); err != nil {
					t.Fatal(err)
				}
			}
			if answeredElsewhere {
				h.exec(owner, h.env((AttentionCommand{Action: "confirm"}).Operation(), protocol.Target{Type: "attention", ID: a.AttentionID}, protocol.ConfirmAttentionPayload{Summary: "checked elsewhere"}, rev(a.Revision)), AttentionCommand{Action: "confirm"})
			}
			text := "已完成：checked report"
			if answeredElsewhere {
				text = "Please investigate a new discrepancy"
			}
			in := FeishuIncoming{EventID: "answer", MessageID: "external-answer", AppID: "app", TenantKey: "tenant", SenderID: "person", ChatID: "chat", ParentID: "external-question", Text: text}
			op, err := h.app.ReceiveFeishu(t.Context(), in)
			if err != nil || op.Error != nil {
				t.Fatalf("%+v %v", op, err)
			}
			again, err := h.app.ReceiveFeishu(t.Context(), in)
			if err != nil || again.RequestID != op.RequestID || string(again.Result) != string(op.Result) {
				t.Fatalf("replayed reply changed: %+v %v", again, err)
			}
			id := messageID(t, op)
			if id == question.MessageID {
				t.Fatal("answer reused original question")
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				var body, source string
				var acknowledgements int
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT body FROM conversation_messages WHERE message_id=?`, id).Scan(&body); err != nil {
					return err
				}
				if body != text {
					t.Fatalf("owner text changed: %q", body)
				}
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT message_id FROM feishu_inputs WHERE event_id='answer'`).Scan(&source); err != nil {
					return err
				}
				if source != id {
					t.Fatal("answer source not bound")
				}
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT count(*) FROM messaging_deliveries d JOIN messaging_messages m USING(message_id) WHERE d.channel_id='feishu' AND m.reply_to=?`, id).Scan(&acknowledgements); err != nil {
					return err
				}
				if acknowledgements != 1 {
					t.Fatalf("answer acknowledgement count = %d", acknowledgements)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFeishuFailedParentSettlesChildButUnknownRemainsPending(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed", true: "unknown"}[uncertain], func(t *testing.T) {
			h := newHarness(t)
			bindFeishuFixture(t, h)
			parent := FeishuIncoming{EventID: "parent", MessageID: "external-parent", AppID: "app", TenantKey: "tenant", SenderID: "person", ChatID: "chat", ParentID: "unmapped-old-message", Text: "first"}
			first, err := h.app.ReceiveFeishu(t.Context(), parent)
			if err != nil || first.Status != protocol.OpFailed || first.Error == nil || first.Error.Code != protocol.CodeNotFound {
				t.Fatal(first, err)
			}
			if uncertain {
				if err = h.store.Write(t.Context(), func(tx *store.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE request_records SET status='unknown' WHERE request_id=?`, first.RequestID)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			child := parent
			child.EventID = "child"
			child.MessageID = "external-child"
			child.ParentID = parent.MessageID
			op, err := h.app.ReceiveFeishu(t.Context(), child)
			if uncertain {
				if protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
					t.Fatal("uncertain parent lost safety guard", op, err)
				}
			} else {
				if err != nil || op.Status != protocol.OpFailed || op.Error == nil || op.Error.Code != protocol.CodeNotFound {
					t.Fatal("failed parent stranded child", op, err)
				}
				replay, err := h.app.ReceiveFeishu(t.Context(), child)
				if err != nil || replay.RequestID != op.RequestID || replay.Status != protocol.OpFailed {
					t.Fatal("failed child replay changed identity", replay, err)
				}
			}
			pending, err := h.app.PendingFeishuInputs(t.Context())
			if err != nil || len(pending) != 0 {
				t.Fatal("failed or uncertain parent incorrectly retried", pending, err)
			}
		})
	}
}
