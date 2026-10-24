package views

import (
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestSystemTurnStatesProjectIntoConversationAndEvents(t *testing.T) {
	ctx := t.Context()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "ledger.db"), testkit.NewClock().Now)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Write(ctx, func(tx *store.Tx) error {
		for _, id := range []string{"ordinary-notice", "event-turn"} {
			if _, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: id, Author: "system", Body: "fact"}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, state := range []protocol.MessageState{protocol.MessageProcessing, protocol.MessageInterrupted} {
		if err := s.Write(ctx, func(tx *store.Tx) error {
			_, err := tx.InsertEvent(ctx, store.EventRow{EventID: "event-" + string(state), ObjectType: "message", ObjectID: "event-turn",
				Kind: "conversation.message_state", Source: "mainthread", BodyJSON: `{"state":"` + string(state) + `"}`})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.Read(ctx, func(tx *store.ReadTx) error {
			page, err := Conversation(ctx, tx.Q(), 0, 0, 50)
			if err != nil {
				return err
			}
			if len(page.Items) != 2 || page.Items[0].State != "" || page.Items[1].State != state {
				t.Fatalf("history state = %+v, want ordinary notice empty and event %s", page.Items, state)
			}
			events, err := Events(ctx, tx.Q(), 0, 50)
			if err != nil {
				return err
			}
			last := events[len(events)-1]
			if last.Item == nil || last.Item.Author != "system" || last.Item.State != state {
				t.Fatalf("event state = %+v, want system %s", last.Item, state)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}
