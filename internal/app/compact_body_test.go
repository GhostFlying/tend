package app

import (
	"context"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func compactBodyFollowup(t *testing.T, h *harness, w protocol.WorkItem) {
	t.Helper()
	source := h.ids.New(protocol.PrefixMessage)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: source, Author: "system", Body: "Existing background notice", CreatedAt: h.clock.Now().UnixMilli()})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	h.followup(w, protocol.FollowupSpec{Purpose: "Publish the actual background result", Instruction: "Keep the original full result", SourceID: source, Timezone: "UTC", Kind: "at", At: h.clock.Now().UnixMilli()})
}

func TestRetainedMessageBodiesReachTranscriptAndIssueSources(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	body := strings.Repeat("已验收的完整结果😀\n", 500)
	ownerLiteral := `{"tend:retained-json:v2":999999}`
	ctx, err := h.store.StageOriginals(t.Context(), "retained-visible-result", []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		for _, m := range []store.MessageRow{
			{MessageID: "source-owner", Author: "owner", Body: ownerLiteral},
			{MessageID: "source-result", Author: "main_agent", ReplyTo: "source-owner", Body: body},
		} {
			if _, err := tx.InsertMessage(ctx, m); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO message_context_obligations(message_id,work_item_id,needs_review) VALUES(?,?,1)`, m.MessageID, issue.IssueID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
		var saved string
		if err := tx.Q().QueryRowContext(ctx, `SELECT body FROM conversation_messages WHERE message_id='source-result'`).Scan(&saved); err != nil {
			return err
		}
		if saved == body || len(saved) > 100 {
			t.Fatal("fixture did not retain a shared original")
		}
		replies, err := store.TranscriptReplies(ctx, tx.Q(), "source-owner", 0, 10)
		if err != nil {
			return err
		}
		if len(replies) != 1 || replies[0].MessageID != "source-result" || replies[0].Body != body {
			t.Fatal("transcript returned an internal reference", replies)
		}
		view, err := views.CanonicalIssue(ctx, tx.Q(), issue.IssueID, 0)
		if err != nil {
			return err
		}
		if len(view.SourceReviews) != 2 || view.SourceReviews[0].Body != ownerLiteral || view.SourceReviews[1].Body != body {
			t.Fatal("source view changed owner text or omitted the original", view.SourceReviews)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBackgroundToolAndProsePublishSharedFullOriginals(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	compactBodyFollowup(t, h, w)
	_, turn := h.startBackground()
	output := strings.Repeat("完整工具输出😀\n", 10000)
	input := `{"query":"` + strings.Repeat("query", 1000) + `"}`
	op := h.exec(mainAgent, h.env("conversation.record_tool_use", protocol.Target{Type: "conversation"}, recordToolUsePayload{ReplyTo: turn.MessageID, TurnID: turn.TurnID, ItemID: "tool", Tool: "tasks_list", Input: input, Output: output, Status: protocol.ToolCompleted}, nil), RecordToolUse{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	prose := strings.Repeat("最终结果，不截断。😀", 1000)
	op = h.exec(mainAgent, h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, recordAgentMessagePayload{ReplyTo: turn.MessageID, TurnID: turn.TurnID, ItemID: "reply", Body: prose, Status: protocol.AssistantCompleted}, nil), RecordAgentMessage{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var bufferedIdentity, bgInput, bgOutput string
	var originalBodies int
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var published int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT json_array(seq,batch_id,thread_id,turn_id,item_id,status,request_id,created_at),json_extract(tool_json,'$.input'),json_extract(tool_json,'$.output'),published FROM background_replies WHERE turn_id=? AND item_id='tool' AND kind='tool_use'`, turn.TurnID).Scan(&bufferedIdentity, &bgInput, &bgOutput, &published); err != nil {
			return err
		}
		if published != 0 {
			t.Fatal("tool published before turn completion")
		}
		return tx.Q().QueryRowContext(t.Context(), `SELECT count(*) FROM retained_original_values`).Scan(&originalBodies)
	}); err != nil {
		t.Fatal(err)
	}
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		tool, err := store.GetToolUse(t.Context(), tx.Q(), turn.TurnID, "tool")
		if err != nil {
			return err
		}
		if tool.Input != input || tool.Output != output {
			t.Fatal("full tool evidence lost")
		}
		var publishedIdentity, cachedTool, savedInput, savedOutput, bgBody, body string
		var published, bodies int
		if err = tx.Q().QueryRowContext(t.Context(), `SELECT json_array(seq,batch_id,thread_id,turn_id,item_id,status,request_id,created_at),tool_json,published FROM background_replies WHERE turn_id=? AND item_id='tool' AND kind='tool_use'`, turn.TurnID).Scan(&publishedIdentity, &cachedTool, &published); err != nil {
			return err
		}
		if publishedIdentity != bufferedIdentity || published != 1 || cachedTool != "" {
			t.Fatal("publication changed identity or retained a redundant tool cache")
		}
		if tool.ThreadID != mainAgent.Thread || tool.Tool != "tasks_list" || tool.Status != string(protocol.ToolCompleted) {
			t.Fatal("formal tool publication changed identity or status")
		}
		if err = tx.Q().QueryRowContext(t.Context(), `SELECT input,output FROM conversation_tool_uses WHERE turn_id=? AND item_id='tool'`, turn.TurnID).Scan(&savedInput, &savedOutput); err != nil {
			return err
		}
		if savedInput != bgInput || savedOutput != bgOutput || len(savedInput) > 100 || len(savedOutput) > 100 {
			t.Fatal("publication copied tool body")
		}
		if err = tx.Q().QueryRowContext(t.Context(), `SELECT count(*) FROM retained_original_values`).Scan(&bodies); err != nil {
			return err
		}
		if bodies != originalBodies {
			t.Fatal("publication retained a second body")
		}
		if err = tx.Q().QueryRowContext(t.Context(), `SELECT r.body,m.body FROM background_replies r JOIN conversation_agent_messages a ON a.thread_id=r.thread_id AND a.turn_id=r.turn_id AND a.item_id=r.item_id JOIN conversation_messages m ON m.message_id=a.message_id WHERE r.turn_id=? AND r.kind='message'`, turn.TurnID).Scan(&bgBody, &body); err != nil {
			return err
		}
		if bgBody != body || len(body) > 100 {
			t.Fatal("publication copied prose body")
		}
		page, err := store.ListConversation(t.Context(), tx.Q(), 0, 0, 100)
		if err != nil {
			return err
		}
		found, foundTool := false, false
		for _, r := range page {
			if r.MessageID == tool.MessageID {
				foundTool = true
			}
			if r.Body == prose {
				if !foundTool {
					t.Fatal("publication reordered tool and prose")
				}
				found = true
			}
		}
		if !found {
			t.Fatal("visible report could not restore original")
		}
		return durableManagerReplyPrefix(t.Context(), tx.Q(), mainAgent.Thread, turn.TurnID, "reply", prose)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestContextPacketKeepsUnknownInputAndReleasesConfirmedDerivedBody(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	body := strings.Repeat("frozen context 中文", 1000)
	for _, id := range []string{"unknown-packet", "injected-packet"} {
		if err := h.app.RecordPacket(ctx, store.Packet{PacketID: id, ThreadKey: id, Epoch: 1, Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.app.FailPacket(ctx, "unknown-packet", true); err != nil {
		t.Fatal(err)
	}
	if err := h.app.ConfirmPacket(ctx, "injected-packet"); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
		unknown, err := store.GetPacket(ctx, tx.Q(), "unknown-packet")
		if err != nil {
			return err
		}
		confirmed, err := store.GetPacket(ctx, tx.Q(), "injected-packet")
		if err != nil {
			return err
		}
		if unknown.Body != body || unknown.Status != store.PacketUnknown || confirmed.Body != "" || confirmed.Status != store.PacketInjected {
			t.Fatal("wrong frozen-input lifecycle")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBackgroundToolPublicationFailureDoesNotMarkOriginalPublished(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	compactBodyFollowup(t, h, w)
	_, turn := h.startBackground()
	op := h.exec(mainAgent, h.env("conversation.record_tool_use", protocol.Target{Type: "conversation"}, recordToolUsePayload{ReplyTo: turn.MessageID, TurnID: turn.TurnID, ItemID: "tool", Tool: "tasks_list", Output: strings.Repeat("original output", 1000), Status: protocol.ToolCompleted}, nil), RecordToolUse{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE TRIGGER reject_visible_tool BEFORE INSERT ON conversation_tool_uses BEGIN SELECT RAISE(ABORT,'visible publication failed'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err == nil {
		t.Fatal("failed publication was treated as delivered")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var published, visible int
		err := tx.Q().QueryRowContext(t.Context(), `SELECT published,(SELECT count(*) FROM conversation_tool_uses WHERE turn_id=?) FROM background_replies WHERE turn_id=? AND item_id='tool'`, turn.TurnID, turn.TurnID).Scan(&published, &visible)
		if err == nil && (published != 0 || visible != 0) {
			t.Fatal("partial publication escaped rollback", published, visible)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
