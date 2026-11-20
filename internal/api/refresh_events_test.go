package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestChatEventStreamResumesAcrossCoalescedHiddenProcessChanges(t *testing.T) {
	e := newEnv(t)
	token := e.login(protocol.ScopeOwner)
	ctx := t.Context()
	const trigger = "main-background-trigger"
	if err := e.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: trigger, Author: "system", Body: "internal trigger"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	e.say("visible before coalescing")
	live := openStream(t, e.url, token, "?projection=chat&after=1")
	if start := live.next(); start.name != "start" {
		t.Fatal(start)
	}
	coalesce := func() int64 {
		t.Helper()
		var seq int64
		if err := e.store.Write(ctx, func(tx *store.Tx) error {
			for range 2 {
				var err error
				seq, err = tx.InsertEvent(ctx, store.EventRow{
					Kind: "conversation.process_changed", ObjectType: "message", ObjectID: trigger,
				})
				if err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		e.app.Changes().Notify()
		return seq
	}
	assertCheckpoint := func(s *stream, seq int64) {
		t.Helper()
		frame := s.next()
		var at protocol.StreamStart
		if frame.name != "cursor" || frame.id != strconv.FormatInt(seq, 10) ||
			json.Unmarshal([]byte(frame.data), &at) != nil || at.EventSeq != seq {
			t.Fatalf("hidden process changes must only advance the cursor: %+v", frame)
		}
	}
	assertMessage := func(s *stream, seq int64, body string) {
		t.Helper()
		frame := s.next()
		var ev protocol.LedgerEvent
		if frame.name != "ledger" || json.Unmarshal([]byte(frame.data), &ev) != nil ||
			ev.EventSeq != seq || ev.Item == nil || ev.Item.Body != body {
			t.Fatalf("expected visible message %q at %d: %+v", body, seq, frame)
		}
	}
	last := coalesce()
	assertCheckpoint(live, last)
	e.say("visible while connected")
	assertMessage(live, last+1, "visible while connected")
	live.cancel()

	missed := coalesce()
	reconnect := openStream(t, e.url, token, fmt.Sprintf("?projection=chat&after=%d", last+1))
	if reconnect.status != http.StatusOK || reconnect.next().name != "start" {
		t.Fatalf("hidden events rejected a reconnect: %d", reconnect.status)
	}
	assertCheckpoint(reconnect, missed)
	e.say("visible after reconnect")
	assertMessage(reconnect, missed+1, "visible after reconnect")
	e.say("next visible message")
	assertMessage(reconnect, missed+2, "next visible message")
	if raw := openStream(t, e.url, token, "?after=1"); raw.status != http.StatusConflict {
		t.Fatalf("the original stream must still detect missing process notifications: %d", raw.status)
	}
}

func TestChatEventStreamResetsForCoalescedIssueProcessChanges(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	ctx := t.Context()
	const trigger = "issue-background-trigger"
	issue := createOwnerIssueHTTP(t, e, token, "")
	conversation := issueDetailHTTP(t, e, token, issue.IssueID).Conversation.ConversationID
	if err := e.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.InsertMessage(ctx, store.MessageRow{
			MessageID: trigger, Author: "system", ConversationID: conversation,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	e.say("visible before Issue changes")
	var snapshot protocol.IssueConversationPage
	if status := e.do("GET", "/api/v1/conversation?projection=chat", token, nil, &snapshot); status != http.StatusOK {
		t.Fatalf("snapshot status %d", status)
	}
	query := fmt.Sprintf("?projection=chat&after=%d", snapshot.EventSeq)
	stream := openStream(t, e.url, token, query)
	if start := stream.next(); start.name != "start" {
		t.Fatal(start)
	}
	if err := e.store.Write(ctx, func(tx *store.Tx) error {
		for range 2 {
			if _, err := tx.InsertEvent(ctx, store.EventRow{
				Kind: "conversation.process_changed", ObjectType: "message", ObjectID: trigger,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.app.Changes().Notify()
	if reset := stream.next(); reset.name != "reset" {
		t.Fatal("Issue activity missed its required reset", reset)
	}
	if stale := openStream(t, e.url, token, query); stale.status != http.StatusConflict {
		t.Fatal("expired Issue refresh cursor was accepted", stale.status)
	}
	retained := openStream(t, e.url, token, fmt.Sprintf("?projection=chat&after=%d", snapshot.EventSeq+1))
	if start := retained.next(); start.name != "start" {
		t.Fatal(start)
	}
	frame := retained.next()
	var ev protocol.LedgerEvent
	if frame.name != "ledger" || json.Unmarshal([]byte(frame.data), &ev) != nil ||
		ev.ConversationID != conversation || ev.Kind != "conversation.process_changed" || ev.Item != nil {
		t.Fatal("Issue process notice must remain a scoped invalidation", frame)
	}
}

func TestEventStreamCoalescedRefreshRequiresSourceResync(t *testing.T) {
	e := newEnv(t)
	token := e.login(protocol.ScopeOwner)
	e.say("Original business message")
	s := openStream(t, e.url, token, "?after=1")
	if start := s.next(); start.name != "start" {
		t.Fatal(start)
	}
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		for range 10 {
			if _, err := tx.InsertEvent(t.Context(), store.EventRow{Kind: "token_usage.changed", ObjectType: "token_usage", ObjectID: "all", CreatedAt: e.clock.Now().UnixMilli()}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.app.Changes().Notify()
	if reset := s.next(); reset.name != "reset" {
		t.Fatal("live reader did not request an explicit SoT reload", reset)
	}
	if stale := openStream(t, e.url, token, "?after=1"); stale.status != http.StatusConflict {
		t.Fatal("expired reconnect cursor accepted as complete", stale.status)
	}
	fresh := openStream(t, e.url, token, "")
	var start protocol.StreamStart
	frame := fresh.next()
	if frame.name != "start" || json.Unmarshal([]byte(frame.data), &start) != nil || start.EventSeq != 11 {
		t.Fatal("fresh SoT read has the wrong watermark", frame)
	}
	e.say("New business message")
	frame = fresh.next()
	var event protocol.LedgerEvent
	if frame.name != "ledger" || json.Unmarshal([]byte(frame.data), &event) != nil || event.Kind != "conversation.message" || event.EventSeq != 12 {
		t.Fatal("business delivery lost after resync", frame)
	}
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var business, refresh, notices int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT (SELECT COUNT(*) FROM events WHERE kind='conversation.message'),(SELECT COUNT(*) FROM events WHERE kind='token_usage.changed'),(SELECT COUNT(*) FROM refresh_notifications)`).Scan(&business, &refresh, &notices); err != nil {
			return err
		}
		if business != 2 || refresh != 0 || notices != 1 {
			t.Fatal("refresh history grew or business facts disappeared", business, refresh, notices)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestEventStreamContinuesWhileRefreshAcknowledgementWaits(t *testing.T) {
	e := newEnv(t)
	token := e.login(protocol.ScopeOwner)
	ctx := t.Context()
	if err := e.store.Write(ctx, func(tx *store.Tx) error {
		if _, err := tx.InsertEvent(ctx, store.EventRow{
			Kind: "token_usage.changed", ObjectType: "token_usage", ObjectID: "all",
		}); err != nil {
			return err
		}
		for range eventBatch {
			if _, err := tx.InsertEvent(ctx, store.EventRow{
				EventID: e.ids.New(protocol.PrefixEvent), Kind: "task.test", ObjectType: "task", ObjectID: "test",
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	writerHeld, releaseWriter := make(chan struct{}), make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- e.store.Write(ctx, func(*store.Tx) error {
			close(writerHeld)
			<-releaseWriter
			return nil
		})
	}()
	defer func() {
		close(releaseWriter)
		if err := <-writerDone; err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-writerHeld:
	case <-time.After(10 * time.Second):
		t.Fatal("writer never acquired the connection")
	}

	first := openStream(t, e.url, token, "?after=0")
	if start := first.next(); start.name != "start" {
		t.Fatal(start)
	}
	if refresh := first.next(); refresh.name != "ledger" || refresh.id != "1" {
		t.Fatalf("refresh frame = %+v", refresh)
	}
	started := time.Now()
	for cursor := 2; cursor <= eventBatch+1; cursor++ {
		if frame := first.next(); frame.name != "ledger" || frame.id != strconv.Itoa(cursor) {
			t.Fatalf("event %d = %+v", cursor, frame)
		}
	}
	t.Logf("refresh to next batch while writer is held: %v", time.Since(started))
	concurrent := openStream(t, e.url, token, "?after=0")
	if start := concurrent.next(); start.name != "start" {
		t.Fatal(start)
	}
	if refresh := concurrent.next(); refresh.name != "ledger" || refresh.id != "1" {
		t.Fatalf("concurrent refresh frame = %+v", refresh)
	}
	if err := e.store.Read(ctx, func(tx *store.ReadTx) error {
		var pending int
		if err := tx.Q().QueryRowContext(ctx, `SELECT pending FROM refresh_notifications WHERE event_seq=1`).Scan(&pending); err != nil {
			return err
		}
		if pending != 1 {
			t.Fatalf("refresh acknowledged while the writer was held: pending=%d", pending)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	concurrent.cancel()
	first.cancel()

	resumed := openStream(t, e.url, token, "?after=1")
	if start := resumed.next(); start.name != "start" || start.data != `{"event_seq":1}` {
		t.Fatal(start)
	}
	if replay := resumed.next(); replay.id != "2" {
		t.Fatalf("replayed frame = %+v", replay)
	}
	resumed.cancel()
}
