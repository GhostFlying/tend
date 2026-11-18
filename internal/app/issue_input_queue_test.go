package app

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestIssueInputQueueKeepsBackgroundOutOfOwnerProjection(t *testing.T) {
	ctx := t.Context()
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	conversation := "issue:" + issue.IssueID
	setup, _, claimed, err := h.app.NextDelivery(ctx, store.ConversationDestination(conversation))
	if err != nil || !claimed || setup.Operation != OpHandleEvent {
		t.Fatal("initial assignment wake was not claimable", setup, claimed, err)
	}
	initial := startHostedReview(t, h, conversation, setup)
	initial.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(ctx, initial); err != nil {
		t.Fatal(err)
	}
	if err := h.app.FinishDelivery(ctx, setup, DeliveryResult{Outcome: Delivered}); err != nil {
		t.Fatal(err)
	}
	var reviewPosition int64
	var reviewEvent string
	// Three independent producer groups model the recorded 20:44 queue shape.
	// Enqueue through the real background producer, then claim from the outbox.
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		x := &Exec{IDs: h.ids, Now: h.clock.Now(), Env: &protocol.Envelope{}}
		for n := range 3 {
			event := h.ids.New(protocol.PrefixEvent)
			kind := "issue.delivery_review_changed"
			position, err := tx.InsertEvent(ctx, store.EventRow{EventID: event, Source: "test", ObjectType: "issue", ObjectID: issue.IssueID, Kind: kind, BodyJSON: `{}`, CreatedAt: x.Now.UnixMilli()})
			if err != nil {
				return err
			}
			if n == 0 {
				reviewEvent, reviewPosition = event, position
			}
			source := h.ids.New(protocol.PrefixMessage)
			if _, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: source, ConversationID: conversation, Author: "system", Body: "Review returned", CreatedAt: x.Now.UnixMilli()}); err != nil {
				return err
			}
			if err := appendBackgroundSource(ctx, x, tx, fmt.Sprintf("fixture:%d", n), issue.IssueID, source, event, kind, nil); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	readQueue := func(category string) protocol.ConversationQueuePage {
		t.Helper()
		var out protocol.ConversationQueuePage
		if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
			var err error
			out, err = views.ConversationQueue(ctx, tx.Q(), category, 0, 50, conversation)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	readNotice := func() protocol.IssueEventNotification {
		t.Helper()
		var page protocol.IssueEventNotificationPage
		if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
			var err error
			page, err = views.IssueEventNotification(ctx, tx.Q(), issue.IssueID, reviewPosition, 0)
			return err
		}); err != nil || page.EventID != reviewEvent || len(page.Notifications) != 1 {
			t.Fatal(page, err)
		}
		return page.Notifications[0]
	}
	if notice := readNotice(); notice.Delivery != "unknown" || notice.StartedAt != 0 || notice.BatchStatus != "pending" {
		t.Fatal("enqueue alone was treated as a receipt", notice)
	}
	d, _, claimed, err := h.app.NextDelivery(ctx, store.ConversationDestination(conversation))
	if err != nil || !claimed || d.Operation != OpHandleEvent {
		t.Fatal(d, claimed, err)
	}
	turn := startHostedReview(t, h, conversation, d)
	if page := readQueue("background"); page.Total != 3 || page.Active == nil || page.Active.Status != "processing" ||
		page.Items[1].Status != "queued" || page.Items[2].Status != "queued" {
		t.Fatal("background fixture must include one processing and two queued", page)
	}
	if page := readQueue("owner"); page.Total != 0 || page.Active != nil || len(page.Items) != 0 || page.InterruptibleMessageID != turn.MessageID {
		t.Fatal("background turn leaked into the real owner input view", page)
	}
	if notice := readNotice(); notice.MessageID != turn.MessageID || notice.Delivery != "received" ||
		notice.StartedAt == 0 || notice.Source != "unknown" || notice.Settlement == "completed" {
		t.Fatal("start receipt was missing or falsely settled the original event", notice)
	}

	submitted := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "message"},
		protocol.SubmitMessagePayload{ConversationID: conversation, Body: "Real owner follow-up"}, nil), SubmitMessage{})
	var sent struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(submitted.Result, &sent); err != nil || submitted.Status != protocol.OpAccepted {
		t.Fatal(submitted, err)
	}
	page := readQueue("owner")
	if page.Total != 1 || page.Active != nil || len(page.Items) != 1 ||
		page.Items[0].Item.MessageID != sent.MessageID || page.Items[0].Status != "queued" || !page.Items[0].CanWithdraw {
		t.Fatal("background work obscured the actual pending owner input", page)
	}
	if withdrawn := h.exec(owner, h.env("conversation.withdraw_message", protocol.Target{Type: "message", ID: sent.MessageID},
		protocol.WithdrawMessagePayload{}, rev(page.Items[0].Revision)), WithdrawMessage{}); withdrawn.Status != protocol.OpSucceeded {
		t.Fatal(withdrawn)
	}
	if page := readQueue("background"); page.Total != 3 {
		t.Fatal("withdrawing the owner message changed background delivery", page)
	}
	sources := BackgroundCommand{Action: "sources", MessageID: turn.MessageID, TurnID: turn.TurnID, DeliveryID: d.RequestID}
	principal := protocol.Principal{Kind: protocol.PrincipalBackgroundAgent, ConversationID: conversation, Subject: "issue-reader", Thread: turn.ThreadID}
	if op := h.exec(principal, h.env(sources.Operation(), protocol.Target{Type: "background", ID: turn.MessageID}, map[string]any{}, nil), sources); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if notice := readNotice(); notice.Source != "read" || notice.BatchStatus != "frozen" {
		t.Fatal("the original source read failed to appear independently of settlement", notice)
	}
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	if err := h.app.FinishDelivery(ctx, d, DeliveryResult{Outcome: Delivered}); err != nil {
		t.Fatal(err)
	}
	if notice := readNotice(); notice.BatchStatus != "completed" || notice.Source != "read" || notice.NativeStatus != "completed" {
		t.Fatal("the actual terminal receipt did not settle the original batch", notice)
	}

	// Inject a retained adoption chain after exercising the original notification.
	// Its successor carries handling state only. It must not replace native receipt evidence.
	original := readNotice()
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		var parent string
		if err := tx.Q().QueryRowContext(ctx, `SELECT logical_turn_id FROM logical_turns WHERE message_id=?`, turn.MessageID).Scan(&parent); err != nil {
			return err
		}
		for _, successor := range []string{"adopted-once", "adopted-twice"} {
			if _, err := tx.Exec(ctx, `INSERT INTO logical_turns (logical_turn_id,message_id,status,reason,updated_at)
 VALUES(?,?,'awaiting_clarification','Waiting for the original report',?)`, successor, sent.MessageID, h.clock.Now().UnixMilli()); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE logical_turns SET status='awaiting_completion',resumed_by=? WHERE logical_turn_id=?`, successor, parent); err != nil {
				return err
			}
			parent = successor
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if adopted := readNotice(); adopted.Settlement != "awaiting_clarification" || adopted.Reason != "Waiting for the original report" ||
		adopted.StartedAt != original.StartedAt || adopted.Source != original.Source || adopted.NativeStatus != original.NativeStatus {
		t.Fatal("adoption lost the current wait reason or changed original notification facts", adopted)
	}
	if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
		page, err := views.IssueActivity(ctx, tx.Q(), issue.IssueID, 0)
		if err != nil {
			return err
		}
		for _, entry := range page.Items {
			if entry.Position == reviewPosition && entry.Kind == "issue.delivery_review_changed" {
				return nil
			}
		}
		t.Fatal("review event absent from activity", page)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIssueEventNotificationDoesNotGuessCoalescedMembership(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	ctx := t.Context()
	positions := make([]int64, 2)
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		x := &Exec{IDs: h.ids, Now: h.clock.Now(), Env: &protocol.Envelope{}}
		event := Event{ObjectType: "issue", ObjectID: issue.IssueID, Kind: "issue.delivery_review_changed"}
		for n := range positions {
			id := h.ids.New(protocol.PrefixEvent)
			pos, err := tx.InsertEvent(ctx, store.EventRow{EventID: id, Source: "test", ObjectType: event.ObjectType, ObjectID: event.ObjectID, Kind: event.Kind, BodyJSON: `{}`, CreatedAt: x.Now.UnixMilli()})
			if err != nil {
				return err
			}
			positions[n] = pos
			if err := recordIssueChangeEvent(ctx, x, tx, event, id); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	read := func(ctx context.Context, n int) protocol.IssueEventNotificationPage {
		t.Helper()
		var out protocol.IssueEventNotificationPage
		if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
			var err error
			out, err = views.IssueEventNotification(ctx, tx.Q(), issue.IssueID, positions[n], 0)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if exact := read(ctx, 1); exact.CoalescingUntil == 0 || len(exact.Notifications) != 0 {
		t.Fatal("missing exact pending coalescing evidence", exact)
	}
	h.clock.Advance(3 * time.Minute)
	sweepStatus(t, h)
	if old := read(ctx, 0); old.CoalescingUntil != 0 || len(old.Notifications) != 0 {
		t.Fatal("event range was treated as exact delivery evidence", old)
	}
	if exact := read(ctx, 1); exact.CoalescingUntil != 0 || len(exact.Notifications) != 1 || exact.Notifications[0].Delivery != "unknown" {
		t.Fatal("missing exact flushed notification", exact)
	}
}

func TestIssueEventNotificationShowsRetainedRateLimitWait(t *testing.T) {
	f := newUrgentQueueFixture(t)
	ctx := t.Context()
	finishHostedReview(t, f.harness, f.turn)
	var position int64
	if err := f.store.Write(ctx, func(tx *store.Tx) error {
		x := &Exec{IDs: f.ids, Now: f.clock.Now(), Env: &protocol.Envelope{}}
		id := f.ids.New(protocol.PrefixEvent)
		source := f.ids.New(protocol.PrefixMessage)
		if _, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: source, ConversationID: f.conversation, Author: "system", Body: "Review returned", CreatedAt: x.Now.UnixMilli()}); err != nil {
			return err
		}
		var err error
		position, err = tx.InsertEvent(ctx, store.EventRow{EventID: id, Source: "test", ObjectType: "issue", ObjectID: f.issue.IssueID, Kind: "issue.delivery_review_changed", BodyJSON: `{}`, CreatedAt: x.Now.UnixMilli()})
		if err != nil {
			return err
		}
		return appendBackgroundSource(ctx, x, tx, "rate-limited-review", f.issue.IssueID, source, id, "issue.delivery_review_changed", nil)
	}); err != nil {
		t.Fatal(err)
	}
	d, _, claimed, err := f.app.NextDelivery(ctx, store.ConversationDestination(f.conversation))
	if err != nil || !claimed || d.Operation != OpHandleEvent {
		t.Fatal(d, claimed, err)
	}
	turn := startHostedReview(t, f.harness, f.conversation, d)
	turn.Status = store.TurnFailed
	turn.Failure = &protocol.AgentFailure{Code: "rate_limit_exceeded", HTTPStatus: 429}
	if err := f.app.PutMainTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	if err := f.app.FinishDelivery(ctx, d, DeliveryResult{Outcome: Delivered}); err != nil {
		t.Fatal(err)
	}
	recovery, found, err := f.app.AgentRecovery(ctx, turn.RequestID)
	if err != nil || !found || recovery.Status != "waiting" || recovery.Reason == "" {
		t.Fatal(recovery, found, err)
	}
	if err := f.store.Read(ctx, func(tx *store.ReadTx) error {
		page, err := views.IssueEventNotification(ctx, tx.Q(), f.issue.IssueID, position, 0)
		if err != nil || len(page.Notifications) != 1 {
			t.Fatal(page, err)
		}
		n := page.Notifications[0]
		if n.Reason != recovery.Reason || n.Delivery != "received" || n.NativeStatus != "failed" || n.MessageID != turn.MessageID {
			t.Fatal(n, recovery)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIssueEventNotificationUsesReconciledNativeDelivery(t *testing.T) {
	f := newUrgentQueueFixture(t)
	ctx := t.Context()
	finishHostedReview(t, f.harness, f.turn)
	var position int64
	if err := f.store.Write(ctx, func(tx *store.Tx) error {
		x := &Exec{IDs: f.ids, Now: f.clock.Now(), Env: &protocol.Envelope{}}
		id, source := f.ids.New(protocol.PrefixEvent), f.ids.New(protocol.PrefixMessage)
		if _, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: source, ConversationID: f.conversation, Author: "system", Body: "Review returned while reconnecting", CreatedAt: x.Now.UnixMilli()}); err != nil {
			return err
		}
		var err error
		position, err = tx.InsertEvent(ctx, store.EventRow{EventID: id, Source: "test", ObjectType: "issue", ObjectID: f.issue.IssueID, Kind: "issue.delivery_review_changed", BodyJSON: `{}`, CreatedAt: x.Now.UnixMilli()})
		if err != nil {
			return err
		}
		return appendBackgroundSource(ctx, x, tx, "reconnected-review", f.issue.IssueID, source, id, "issue.delivery_review_changed", nil)
	}); err != nil {
		t.Fatal(err)
	}
	assertDiagnosis := func(delivery, native string) {
		t.Helper()
		if err := f.store.Read(ctx, func(tx *store.ReadTx) error {
			page, err := views.IssueEventNotification(ctx, tx.Q(), f.issue.IssueID, position, 0)
			if err != nil || len(page.Notifications) != 1 {
				t.Fatal(page, err)
			}
			item := page.Notifications[0]
			if item.Delivery != delivery || item.NativeStatus != native || item.StartedAt != 0 || item.Source != "unknown" {
				t.Fatalf("delivery %q native %q, want %q / %q without assumed start or source read: %+v", item.Delivery, item.NativeStatus, delivery, native, item)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	assertDiagnosis("unknown", "")
	d, _, claimed, err := f.app.NextDelivery(ctx, store.ConversationDestination(f.conversation))
	if err != nil || !claimed || d.Operation != OpHandleEvent {
		t.Fatal(d, claimed, err)
	}
	var p HandleMessage
	if err := json.Unmarshal(d.Payload, &p); err != nil {
		t.Fatal(err)
	}
	selected, named, err := f.app.NamedHostedSelection(ctx, f.conversation)
	if err != nil || !named {
		t.Fatal(selected, named, err)
	}
	turn := store.MainTurn{RequestID: d.RequestID, MessageID: p.MessageID, ThreadID: f.turn.ThreadID, Status: store.TurnStarting, Hosted: &selected}
	if err := f.app.PutMainTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	if err := f.app.RecordRolloutEvent(ctx, f.conversation, turn.ThreadID, "other-native-turn", "", "native_terminal", json.RawMessage(`{"status":"completed"}`)); err != nil {
		t.Fatal(err)
	}
	assertDiagnosis("unknown", store.TurnStarting)
	turn.TurnID, turn.Status = "reconnected-native-turn", store.TurnCompleted
	if err := f.app.ObserveNativeTurnTerminal(ctx, turn); err != nil {
		t.Fatal(err)
	}
	assertDiagnosis("received", store.TurnCompleted)
	turn.NativeReconciled = true
	if err := f.app.PutMainTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	assertDiagnosis("received", store.TurnCompleted)
}
