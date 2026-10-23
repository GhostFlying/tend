package views

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestNoticeMetadataIsAtomicAndProjectsIntoHistoryAndEvents(t *testing.T) {
	ctx := t.Context()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "ledger.db"), testkit.NewClock().Now)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Write(ctx, func(tx *store.Tx) error {
		for _, id := range []string{"legacy", "upgrade"} {
			if _, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: id, Author: "system", Body: "fact"}); err != nil {
				return err
			}
		}
		_, err := tx.InsertEvent(ctx, store.EventRow{EventID: "notice_event", ObjectType: "message", ObjectID: "upgrade", Kind: "conversation.message", Source: "app", ActorJSON: `{"kind":"system"}`, BodyJSON: `{}`})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	notice := store.MessageNoticeRow{MessageID: "upgrade", NoticeType: "upgrade_started", UpgradeID: "batch", HostID: "runner"}
	aborted := errors.New("abort notice")
	if err := s.Write(ctx, func(tx *store.Tx) error {
		if err := tx.PutMessageNotice(ctx, notice); err != nil {
			return err
		}
		return aborted
	}); !errors.Is(err, aborted) {
		t.Fatal(err)
	}
	if err := s.Read(ctx, func(tx *store.ReadTx) error {
		_, err := store.GetMessageNotice(ctx, tx.Q(), "upgrade")
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("rolled back notice = %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ctx, func(tx *store.Tx) error { return tx.PutMessageNotice(ctx, notice) }); err != nil {
		t.Fatal(err)
	}
	if err := s.Read(ctx, func(tx *store.ReadTx) error {
		page, err := Conversation(ctx, tx.Q(), 0, 0, 50)
		if err != nil {
			return err
		}
		if len(page.Items) != 2 || page.Items[0].NoticeType != "" || page.Items[1].NoticeType != "upgrade_started" || page.Items[1].UpgradeID != "batch" || page.Items[1].HostID != "runner" {
			t.Fatalf("history metadata = %+v", page.Items)
		}
		events, err := Events(ctx, tx.Q(), 0, 50)
		if err != nil {
			return err
		}
		if len(events) != 1 || events[0].Item == nil || events[0].Item.NoticeType != "upgrade_started" || events[0].Item.UpgradeID != "batch" || events[0].Item.HostID != "runner" {
			t.Fatalf("event metadata = %+v", events)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
