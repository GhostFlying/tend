package app

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestBackgroundRetainedEventDoesNotCreateAnotherModelWake(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	h.followup(w, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().UnixMilli()})
	_, turn := h.startBackground()
	before := h.count("background_batches")
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		var group, item, message, event, kind string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT b.group_key,b.work_item_id,s.message_id,s.event_id,s.kind FROM background_sources s JOIN background_batches b USING(batch_id) WHERE b.message_id=? LIMIT 1`, turn.MessageID).Scan(&group, &item, &message, &event, &kind); err != nil {
			return err
		}
		x := &Exec{Env: &protocol.Envelope{RequestID: h.ids.New(protocol.PrefixRequest)}, IDs: h.ids, Now: h.clock.Now()}
		return appendBackgroundSource(t.Context(), x, tx, group, item, message, event, kind, nil)
	}); err != nil {
		t.Fatal(err)
	}
	if h.count("background_batches") != before {
		t.Fatal("a retained event acquired a second model wake after its original batch froze")
	}
}
