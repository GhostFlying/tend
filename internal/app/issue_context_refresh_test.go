package app

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestIncrementalIssueContextRefreshPositionsAndExpiredSourceResync(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	seedIncrementalContext(t, h, issue, 2, 1)
	refresh := func() {
		t.Helper()
		if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
			return contextTestEvent(t, h, tx, "token_usage", "all", "token_usage.changed", nil)
		}); err != nil {
			t.Fatal(err)
		}
	}
	refresh()
	// A fresh snapshot and all its pages can be pinned to a refresh cursor,
	// even though that cursor is intentionally absent from permanent events.
	items, before := finishIncrementalContext(t, h, issue.IssueID, "", 1)
	if len(items) != 4 {
		t.Fatal("SoT snapshot omitted business context", items)
	}
	refresh()
	delta := readIncrementalContext(t, h, issue.IssueID, before, 1)
	if delta.More || len(delta.Items) != 0 || delta.Kind != "delta" {
		t.Fatal("pure refresh looked like missing business history", delta)
	}
	// The reader consumed that interval. A second coalescence retains its
	// exact last position as the expiration boundary and remains resumable.
	refresh()
	delta = readIncrementalContext(t, h, issue.IssueID, delta.NextCursor, 1)
	if delta.More || len(delta.Items) != 0 {
		t.Fatal("consumed refresh boundary could not resume", delta)
	}
	// A reader further behind must explicitly reload current SoT, rather than
	// report an empty synchronized delta or lose real retained business facts.
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		_, err := views.CanonicalIssueContextIncremental(t.Context(), tx.Q(), issue.IssueID, protocol.IssueContextQuery{View: "incremental", Cursor: before})
		if err == nil || protocol.CodeOf(err) != protocol.CodeStaleRevision {
			t.Fatal("expired refresh cursor did not request SoT resync", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	items, _ = finishIncrementalContext(t, h, issue.IssueID, "", 1)
	if len(items) != 4 {
		t.Fatal("resync omitted original sources or fact", items)
	}
}
