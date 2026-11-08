package app

import (
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestBackgroundDeadlinePreservesDueWorkAndFutureRetry(t *testing.T) {
	h := newHarness(t)
	var original string
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		for i, offset := range []time.Duration{0, 8 * time.Second, 30 * time.Second} {
			id := h.ids.New(protocol.PrefixOutbox)
			if i == 0 {
				original = id
			}
			if err := tx.InsertOutbox(t.Context(), store.OutboxRow{OutboxID: id, RequestID: h.ids.New(protocol.PrefixRequest), Destination: "background_thread:original", Operation: OpHandleEvent, PayloadJSON: `{}`, CreatedAt: h.clock.Now().Add(offset).UnixMilli()}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []time.Duration{8 * time.Second, 22 * time.Second, 0} {
		delay, err := h.app.BackgroundDelay(t.Context())
		if err != nil || delay != expected {
			t.Fatalf("next deadline = %v (%v), want %v", delay, err, expected)
		}
		h.clock.Advance(delay)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var status string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT status FROM outbox WHERE outbox_id=?`, original).Scan(&status); err != nil {
			return err
		}
		if status != store.OutboxPending {
			t.Fatal("scheduling fabricated progress for due work", status)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
