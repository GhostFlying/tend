package app

import (
	"testing"

	"github.com/GhostFlying/tend/internal/store"
)

func TestReviewBackgroundActivityDoesNotInvalidateMainSummary(t *testing.T) {
	h := newHarness(t)
	if _, err := h.app.BindMainThread(t.Context(), "main", "main-native", "hash", ""); err != nil {
		t.Fatal(err)
	}
	before, err := h.app.HandoffSnapshot(t.Context(), "main")
	if err != nil {
		t.Fatal(err)
	}
	if err = h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO agent_conversations(conversation_id,created_at) VALUES('bg-only',0)`); err != nil {
			return err
		}
		_, err := tx.InsertMessage(t.Context(), store.MessageRow{ConversationID: "bg-only", MessageID: "bg-only-message", Author: "owner", Body: "background only"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	after, err := h.app.HandoffSnapshot(t.Context(), "main")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("main summary snapshot version=%d -> %d maxseq=%d -> %d", before.Version, after.Version, before.MaxSeq, after.MaxSeq)
	if !before.Covers(after) {
		t.Error("background-only message invalidated main summary coverage")
	}
}
