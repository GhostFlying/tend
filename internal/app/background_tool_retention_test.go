package app

import (
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestBackgroundPublishedToolReadsFormalOriginalAfterCacheRetirement(t *testing.T) {
	for _, silent := range []bool{false, true} {
		t.Run(map[bool]string{false: "visible", true: "silent"}[silent], func(t *testing.T) {
			h := newHarness(t)
			w := h.createWork(workPlan())
			r := h.followup(w, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().UnixMilli()})
			d, turn := h.startBackground()
			input, output := `{"inspect":"actual"}`, strings.Repeat("complete 中文😀 tool result\n", 2000)
			record := func() {
				t.Helper()
				op := h.exec(mainAgent, h.env("conversation.record_tool_use", protocol.Target{Type: "conversation"}, recordToolUsePayload{ReplyTo: turn.MessageID, TurnID: turn.TurnID, ItemID: "actual-tool", Tool: "inspect", Input: input, Output: output, Status: protocol.ToolCompleted}, nil), RecordToolUse{})
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			record()
			var cache string
			var seq, created int64
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				return tx.Q().QueryRowContext(t.Context(), `SELECT seq,tool_json,created_at FROM background_replies WHERE turn_id=? AND item_id='actual-tool'`, turn.TurnID).Scan(&seq, &cache, &created)
			}); err != nil || cache == "" {
				t.Fatal("unpublished frozen input was retired", err)
			}
			if silent {
				if op := h.backgroundCall(d, turn, "sources", map[string]any{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				if op := h.backgroundCall(d, turn, "checkin", protocol.BackgroundCheckin{Note: "checked", EvidenceIDs: []string{r.RuleID}}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			turn.Status = store.TurnCompleted
			if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
				t.Fatal(err)
			}
			h.app = New(h.store, h.ids, h.clock.Now, WithFollowups(true))
			// Lost callback acknowledgement after publication does not read an
			// already retired cache or create another visible ToolUse.
			record()
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				var published int
				var actualSeq, actualCreated int64
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT seq,tool_json,created_at,published FROM background_replies WHERE turn_id=? AND item_id='actual-tool'`, turn.TurnID).Scan(&actualSeq, &cache, &actualCreated, &published); err != nil {
					return err
				}
				if cache != "" || published != 1 || actualSeq != seq || actualCreated != created {
					t.Fatal("publication identity changed or cache remained")
				}
				rows, err := store.TranscriptReplies(t.Context(), tx.Q(), turn.MessageID, 0, 10)
				if err == nil && (len(rows) != 1 || rows[0].Tool == nil || rows[0].Tool.Input != input || rows[0].Tool.Output != output || rows[0].CreatedAt != created) {
					t.Fatal("formal transcript did not retain the complete tool original")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
