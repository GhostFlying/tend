package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestContextIndexesToolOriginalWithoutRecirculatingIt(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	ownerID := materialMessage(t, h, issue.IssueID)
	original := strings.Repeat(`{"sources":[{"tool":{"output":"prior context"}}]}`, 2000)
	const toolID = "context-tool-original"
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: toolID, Kind: store.KindToolUse, Author: "main_agent", ReplyTo: ownerID}); err != nil {
			return err
		}
		if err := tx.PutToolUse(t.Context(), store.ToolUseRow{MessageID: toolID, ThreadID: "original-thread", TurnID: "original-turn", ItemID: "original-item", Tool: "issues_context_read", Input: `{"after_source":0}`, Output: original, Status: "completed"}); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO message_work_items(message_id,work_item_id,source,updated_at) VALUES(?,?,'explicit',1)`, toolID, issue.IssueID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		got := canonicalContextProjection(t, h, issue.IssueID)
		var found, ownerFound bool
		for _, s := range got.Sources {
			if s.MessageID == ownerID {
				ownerFound = s.Body != ""
			}
			if s.MessageID == toolID {
				found = true
				raw, _ := json.Marshal(s)
				if strings.Contains(string(raw), "prior context") {
					t.Fatal("context recirculated the tool original")
				}
				var fields map[string]json.RawMessage
				_ = json.Unmarshal(raw, &fields)
				var tool map[string]json.RawMessage
				_ = json.Unmarshal(fields["tool"], &tool)
				if string(tool["original_message_id"]) != `"`+toolID+`"` || string(tool["content_omitted"]) != "true" {
					t.Fatal("omitted original not explicitly discoverable", string(raw))
				}
			}
		}
		if !found || !ownerFound {
			t.Fatal("source identities or owner original lost")
		}
	}
	exact, err := h.app.ReadIssueMaterials(t.Context(), issue.IssueID, protocol.IssueMaterialsQuery{Kind: "messages", ID: toolID})
	if err != nil || len(exact.Messages) != 1 || exact.Messages[0].Tool == nil || exact.Messages[0].Tool.Output != original {
		t.Fatal("full original unavailable", err)
	}
	// An unrelated Issue cannot acquire this original by knowing the ID.
	other := createCanonicalOwnerTestIssue(t, h)
	if _, err := h.app.ReadIssueMaterials(t.Context(), other.IssueID, protocol.IssueMaterialsQuery{Kind: "messages", ID: toolID}); protocol.CodeOf(err) != protocol.CodeNotFound {
		t.Fatal("original escaped Issue material scope", err)
	}
}
