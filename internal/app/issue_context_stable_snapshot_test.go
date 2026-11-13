package app

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

// Real Store event writes interleave with the actual projection; only external
// execution is absent. The snapshot fixes membership, not historical values.
func TestIncrementalSnapshotSurvivesRefreshAndTwoBusinessIntervals(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint(legacy), func(t *testing.T) {
			h := newHarness(t)
			issue := createCanonicalOwnerTestIssue(t, h)
			seedIncrementalContext(t, h, issue, 3, 1)
			refresh := func() {
				t.Helper()
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					return contextTestEvent(t, h, tx, "token_usage", "unrelated", "token_usage.changed", nil)
				}); err != nil {
					t.Fatal(err)
				}
			}
			refresh()
			page := readIncrementalContext(t, h, issue.IssueID, "", 1)
			cursor := page.NextCursor
			initial := append([]protocol.IssueContextChange{}, page.Items...)
			if legacy {
				// A deployed v1 snapshot can be pinned to a now-coalesced refresh.
				raw, _ := base64.RawURLEncoding.DecodeString(cursor)
				var c map[string]any
				_ = json.Unmarshal(raw, &c)
				c["v"] = 1
				var latest int64
				_ = h.store.Read(t.Context(), func(tx *store.ReadTx) error {
					var err error
					latest, err = store.LatestEventSeq(t.Context(), tx.Q())
					return err
				})
				c["t"] = latest
				raw, _ = json.Marshal(c)
				cursor = base64.RawURLEncoding.EncodeToString(raw)
			}
			refresh()
			if legacy {
				// The exact coalesced refresh is still witnessed by expired_through.
				// Migration pins a durable boundary before further coalescence.
				page = readIncrementalContext(t, h, issue.IssueID, cursor, 1)
				cursor = page.NextCursor
			}
			refresh()
			refresh()
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				_, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: "during-snapshot", Author: "owner", Body: "concurrent new original", CreatedAt: h.clock.Now().UnixMilli()})
				if err != nil {
					return err
				}
				if _, err = tx.Exec(t.Context(), `INSERT INTO message_work_items(message_id,work_item_id,source,updated_at) VALUES(?,?,'explicit',1)`, "during-snapshot", issue.IssueID); err != nil {
					return err
				}
				fact := fmt.Sprintf("context-fact-%s-000", issue.IssueID)
				if _, err := tx.Exec(t.Context(), `UPDATE work_item_context_facts SET body='updated during snapshot' WHERE fact_id=?`, fact); err != nil {
					return err
				}
				if err := contextTestEvent(t, h, tx, "work_item", issue.IssueID, "work_item.context_changed", map[string]any{"changed_fact_ids": []string{fact}}); err != nil {
					return err
				}
				return contextTestEvent(t, h, tx, "message", "during-snapshot", "conversation.message", nil)
			}); err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			for _, v := range initial {
				seen[v.Kind+"/"+v.ID] = true
			}
			if legacy {
				for _, v := range page.Items {
					seen[v.Kind+"/"+v.ID] = true
				}
			}
			for n := 0; n < 30; n++ {
				refresh()
				refresh()
				page = readIncrementalContext(t, h, issue.IssueID, cursor, 1)
				cursor = page.NextCursor
				for _, v := range page.Items {
					key := v.Kind + "/" + v.ID
					if seen[key] {
						t.Fatal("duplicate snapshot member", key)
					}
					seen[key] = true
				}
				if !page.More {
					break
				}
				if n == 29 {
					t.Fatal("snapshot rebuild loop")
				}
			}
			if len(seen) != 5 || seen["source/during-snapshot"] {
				t.Fatal("snapshot membership moved", seen)
			}
			items, next := finishIncrementalContext(t, h, issue.IssueID, cursor, 1)
			found := false
			factChanged := false
			for _, v := range items {
				if v.Fact != nil && v.Fact.Body == "updated during snapshot" {
					factChanged = true
				}
				if v.ID == "during-snapshot" {
					found = true
				}
			}
			if !found || !factChanged {
				t.Fatal("business source/fact lost at snapshot handoff", items)
			}
			refresh()
			refresh()
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				if _, err := tx.Exec(t.Context(), `UPDATE conversation_messages SET body='updated original' WHERE message_id='during-snapshot'`); err != nil {
					return err
				}
				unlinked := fmt.Sprintf("context-source-%s-002", issue.IssueID)
				if _, err := tx.Exec(t.Context(), `UPDATE message_work_items SET active=0 WHERE message_id=? AND work_item_id=?`, unlinked, issue.IssueID); err != nil {
					return err
				}
				if err := contextTestEvent(t, h, tx, "message", unlinked, "conversation.associations_changed", nil); err != nil {
					return err
				}
				return contextTestEvent(t, h, tx, "message", "during-snapshot", "conversation.corrected", nil)
			}); err != nil {
				t.Fatal(err)
			}
			items, next = finishIncrementalContext(t, h, issue.IssueID, next, 1)
			found = false
			unlinkedSeen := false
			for _, v := range items {
				if v.Source != nil && v.Removed && !v.Source.Linked {
					unlinkedSeen = true
				}
				if v.ID == "during-snapshot" {
					found = true
				}
			}
			if !found || !unlinkedSeen {
				t.Fatal("second delta lost correction/tombstone", items)
			}
			refresh()
			refresh()
			last := readIncrementalContext(t, h, issue.IssueID, next, 1)
			if last.More || len(last.Items) != 0 {
				t.Fatal("unrelated refresh became Issue changes", last)
			}
		})
	}
}

func TestIncrementalLegacySnapshotRejectsUnknownBusinessBoundary(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	seedIncrementalContext(t, h, issue, 2, 1)
	first := readIncrementalContext(t, h, issue.IssueID, "", 1)
	raw, _ := base64.RawURLEncoding.DecodeString(first.NextCursor)
	var cursor map[string]any
	_ = json.Unmarshal(raw, &cursor)
	cursor["v"] = 1
	raw, _ = json.Marshal(cursor)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM events WHERE event_seq=?`, first.Through)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		_, err := views.CanonicalIssueContextIncremental(t.Context(), tx.Q(), issue.IssueID, protocol.IssueContextQuery{View: "incremental", Cursor: base64.RawURLEncoding.EncodeToString(raw)})
		if protocol.CodeOf(err) != protocol.CodeStaleRevision {
			t.Fatal("legacy unknown endpoint silently clamped", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIncrementalLegacyRefreshBoundaryMigrationAndAmbiguity(t *testing.T) {
	for _, updates := range []int{0, 1, 3} {
		t.Run(fmt.Sprint(updates), func(t *testing.T) {
			h := newHarness(t)
			issue := createCanonicalOwnerTestIssue(t, h)
			seedIncrementalContext(t, h, issue, 2, 1)
			refresh := func() {
				t.Helper()
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					return contextTestEvent(t, h, tx, "token_usage", "unrelated", "token_usage.changed", nil)
				}); err != nil {
					t.Fatal(err)
				}
			}
			refresh()
			first := readIncrementalContext(t, h, issue.IssueID, "", 1)
			raw, _ := base64.RawURLEncoding.DecodeString(first.NextCursor)
			var c map[string]any
			_ = json.Unmarshal(raw, &c)
			c["v"] = 1
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				position, err := store.LatestEventSeq(t.Context(), tx.Q())
				c["t"] = position
				return err
			}); err != nil {
				t.Fatal(err)
			}
			raw, _ = json.Marshal(c)
			cursor := base64.RawURLEncoding.EncodeToString(raw)
			for range updates {
				refresh()
			}
			if updates == 3 {
				if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
					_, err := views.CanonicalIssueContextIncremental(t.Context(), tx.Q(), issue.IssueID, protocol.IssueContextQuery{View: "incremental", Cursor: cursor})
					if protocol.CodeOf(err) != protocol.CodeStaleRevision {
						t.Fatal("ambiguous old refresh token was silently guessed", err)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				// One fresh snapshot uses v2 and remains usable under continued refresh.
				fresh := readIncrementalContext(t, h, issue.IssueID, "", 1)
				refresh()
				refresh()
				items, _ := finishIncrementalContext(t, h, issue.IssueID, fresh.NextCursor, 1)
				if len(items) != 3 {
					t.Fatal("fresh snapshot did not recover", items)
				}
				return
			}
			items, next := finishIncrementalContext(t, h, issue.IssueID, cursor, 1)
			if len(items) != 3 {
				t.Fatal("witnessed v1 snapshot lost members", items)
			}
			raw, _ = base64.RawURLEncoding.DecodeString(next)
			_ = json.Unmarshal(raw, &c)
			if c["v"] != float64(2) {
				t.Fatal("legacy migration did not pin durable position", c)
			}
		})
	}
}
