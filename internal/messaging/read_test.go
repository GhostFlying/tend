package messaging

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

type marker struct{ called bool }

func (m *marker) MarkRead(_ context.Context, p app.MessagingPart) app.DeliveryResult {
	m.called = true
	return app.DeliveryResult{Outcome: app.Delivered}
}
func TestReadControlRunsBeforeAnyReplyAndCannotBeForged(t *testing.T) {
	clock := testkit.NewClock()
	s, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "test.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a := app.New(s, testkit.NewIDGen(clock, 91), clock.Now)
	if err = s.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE messaging_channels SET bot_id=1,user_id=2,chat_id=2,status='ready' WHERE channel_id='telegram'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	m := &marker{}
	h := ReadHandler{App: a, Adapter: m}
	p := app.MessagingPart{DeliveryID: "read-operation", Kind: "read", BotID: 1, ChatID: 2, BindingRevision: 1, ReplyExternalID: 3}
	raw, _ := json.Marshal(p)
	d := app.Delivery{OutboxID: p.DeliveryID, Payload: raw}
	if r := h.Deliver(t.Context(), d); r.Outcome != app.Delivered || !m.called {
		t.Fatal(r)
	}
	m.called = false
	d.OutboxID = "other"
	if r := h.Deliver(t.Context(), d); r.Outcome != app.Failed || m.called {
		t.Fatal(r)
	}
}
