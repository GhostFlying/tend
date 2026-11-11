package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

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
