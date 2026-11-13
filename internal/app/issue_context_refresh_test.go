package app

import (
	"encoding/base64"
	"encoding/json"
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
	// A fresh snapshot pins permanent business history even when the latest
	// global stream position is an auxiliary refresh.
	items, before := finishIncrementalContext(t, h, issue.IssueID, "", 1)
	if len(items) != 4 {
		t.Fatal("SoT snapshot omitted business context", items)
	}
	refresh()
	delta := readIncrementalContext(t, h, issue.IssueID, before, 1)
	if delta.More || len(delta.Items) != 0 || delta.Kind != "delta" {
		t.Fatal("pure refresh looked like missing business history", delta)
	}
	// Later coalescence cannot retire the durable business boundary.
	refresh()
	delta = readIncrementalContext(t, h, issue.IssueID, delta.NextCursor, 1)
	if delta.More || len(delta.Items) != 0 {
		t.Fatal("consumed refresh boundary could not resume", delta)
	}
	// Durable context positions remain valid under any amount of auxiliary
	// refresh coalescence; those notices do not carry business context changes.
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		_, err := views.CanonicalIssueContextIncremental(t.Context(), tx.Q(), issue.IssueID, protocol.IssueContextQuery{View: "incremental", Cursor: before})
		if err != nil {
			t.Fatal("durable context expired with unrelated refresh", err)
		}
		// Legacy delta tokens still explicitly resync if their refresh history
		// cannot be established. New cursors no longer acquire that dependency.
		raw, _ := base64.RawURLEncoding.DecodeString(before)
		var c map[string]any
		_ = json.Unmarshal(raw, &c)
		c["v"] = 1
		raw, _ = json.Marshal(c)
		_, err = views.CanonicalIssueContextIncremental(t.Context(), tx.Q(), issue.IssueID, protocol.IssueContextQuery{View: "incremental", Cursor: base64.RawURLEncoding.EncodeToString(raw)})
		if protocol.CodeOf(err) != protocol.CodeStaleRevision {
			t.Fatal("legacy missing refresh history silently passed", err)
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
