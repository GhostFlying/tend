package app

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestContextWatermarkIgnoresToolRowsButRetainsMessageBarrier(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	var reviewed, pending string
	var beforePending, latest int64
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		for _, entry := range []struct{ kind, author string }{{"message", "system"}, {"tool_use", "main_agent"}, {"message", "system"}, {"message", "owner"}, {"message", "system"}} {
			id := h.ids.New(protocol.PrefixMessage)
			seq, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: id, Kind: entry.kind, Author: entry.author, Body: "fixture source"})
			if err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO message_work_items(message_id,work_item_id,source,active,updated_at) VALUES(?,?,'explicit',1,0)`, id, w.WorkItemID); err != nil {
				return err
			}
			if entry.author == "owner" {
				pending = id
				continue
			}
			if pending == "" && entry.kind == "message" {
				beforePending = seq
				reviewed = id
			}
			latest = seq
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	read := func() int64 {
		t.Helper()
		var watermark int64
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			v, err := views.IssueContext(t.Context(), tx.Q(), w.WorkItemID, 0)
			watermark = v.Watermark
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return watermark
	}
	if got := read(); got != beforePending {
		t.Fatalf("tool blocked or pending message hidden: %d, want %d (%s)", got, beforePending, reviewed)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE message_dispositions SET disposition='linked',classified_revision=content_revision,facts_revision=content_revision WHERE message_id=?`, pending)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != latest {
		t.Fatalf("watermark %d, want %d", got, latest)
	}
}
