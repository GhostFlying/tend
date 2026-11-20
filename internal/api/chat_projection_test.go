package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestChatProjectionToolDenseHistoryAndStop(t *testing.T) {
	e := newEnv(t)
	e.server.cfg.HostedMainAgent = true
	token := e.login(protocol.ScopeOwner)
	ctx := t.Context()
	id := e.ids.New(protocol.PrefixMessage)
	submit := e.envelope("conversation.submit_message", protocol.Target{Type: "conversation"}, nil,
		protocol.SubmitMessagePayload{MessageID: id, Body: "original owner input"})
	var response protocol.APIResponse
	if status := e.do("POST", "/api/v1/ops", token, submit, &response); status != http.StatusOK || response.Operation.Status != protocol.OpAccepted {
		t.Fatalf("submit receipt: %d %+v", status, response)
	}
	d, _, ok, err := e.app.NextDelivery(ctx, app.MainThreadDestination)
	if err != nil || !ok {
		t.Fatalf("claim owner input: %v %v", ok, err)
	}
	turn := store.MainTurn{RequestID: d.RequestID, MessageID: id, ThreadID: "thread", TurnID: "turn", Status: store.TurnStarting}
	if err := e.app.PutMainTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	turn.Status = store.TurnStarted
	if err := e.app.PutMainTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	seed := func(tx *store.Tx, msg, author, kind, body string) error {
		seq, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: msg, Author: author, Kind: kind,
			Body: body, ReplyTo: id, CreatedAt: e.clock.Now().UnixMilli()})
		if err != nil {
			return err
		}
		event := "conversation.message"
		if kind == store.KindToolUse {
			event = "conversation.tool_use"
			if err := tx.PutToolUse(ctx, store.ToolUseRow{MessageID: msg, ThreadID: "thread", TurnID: "turn", ItemID: msg,
				Tool: "test_tool", Input: "tool original input", Output: strings.Repeat("tool original output ", 1024),
				Status: "completed", UpdatedAt: e.clock.Now().UnixMilli()}); err != nil {
				return err
			}
		}
		_, err = tx.InsertEvent(ctx, store.EventRow{EventID: e.ids.New(protocol.PrefixEvent), ObjectType: "message",
			ObjectID: msg, ObjectVersion: seq, Kind: event, BodyJSON: "{}"})
		return err
	}
	if err := e.store.Write(ctx, func(tx *store.Tx) error {
		for i := range 52 {
			name := fmt.Sprintf("reply-%02d", i)
			if err := seed(tx, name, "main_agent", "", name); err != nil {
				return err
			}
		}
		for i := range 42 {
			name := fmt.Sprintf("tool-%02d", i)
			if err := seed(tx, name, "main_agent", store.KindToolUse, ""); err != nil {
				return err
			}
		}
		for i := 52; i < 60; i++ {
			name := fmt.Sprintf("reply-%02d", i)
			if err := seed(tx, name, "main_agent", "", name); err != nil {
				return err
			}
		}
		return seed(tx, "internal-wake", "system", "", "internal trigger text")
	}); err != nil {
		t.Fatal(err)
	}

	read := func(path string, dst any) int {
		t.Helper()
		var raw json.RawMessage
		if status := e.do("GET", path, token, nil, &raw); status != http.StatusOK {
			t.Fatalf("GET %s: %d", path, status)
		}
		if err := json.Unmarshal(raw, dst); err != nil {
			t.Fatal(err)
		}
		return len(raw)
	}
	assertVisible := func(p protocol.IssueConversationPage) {
		t.Helper()
		for _, item := range p.Items {
			if item.Tool != nil || item.Kind != protocol.ItemMessage || item.MessageID == "internal-wake" {
				t.Fatalf("non-chat item in page: %s", item.MessageID)
			}
		}
	}

	var latest, before, after protocol.IssueConversationPage
	var queue, withdrawn protocol.IssueConversationQueuePage
	chatLatest := read("/api/v1/conversation?projection=chat&limit=50", &latest)
	chatBytes := chatLatest + read("/api/v1/conversation/queue?category=owner&projection=chat", &queue) +
		read("/api/v1/conversation/queue?category=withdrawn&projection=chat", &withdrawn)
	if len(latest.Items) != 50 || latest.Items[0].MessageID != "reply-10" || latest.Items[49].MessageID != "reply-59" || !latest.More || latest.EventSeq == 0 {
		t.Fatal("latest 50 visible replies were not returned")
	}
	if queue.InterruptibleMessageID != id || queue.ProcessingStartedAt != e.clock.Now().UnixMilli() || queue.Active != nil || queue.Total != 0 {
		t.Fatalf("stop target missing outside the latest page: %+v", queue)
	}
	read(fmt.Sprintf("/api/v1/conversation?projection=chat&limit=50&before=%d", latest.Items[0].Seq), &before)
	read(fmt.Sprintf("/api/v1/conversation?projection=chat&limit=50&after=%d", before.Items[10].Seq), &after)
	if len(before.Items) != 11 || before.More || before.Items[0].MessageID != id ||
		len(after.Items) != 50 || after.More || after.Items[0].Seq != latest.Items[0].Seq || after.Items[49].Seq != latest.Items[49].Seq {
		t.Fatal("visible history cursors did not cover the original turn exactly")
	}
	for _, page := range []protocol.IssueConversationPage{latest, before, after} {
		assertVisible(page)
	}

	// Reproduce the old browser's two paths on the same fixture and count
	// compact response bytes before compression, including its control reads.
	var legacy protocol.IssueConversationPage
	legacyHistory := read("/api/v1/conversation?limit=50", &legacy)
	legacyFirst, legacyPages := legacyHistory, 1
	for legacy.More && legacy.Items[0].MessageID != id {
		legacyHistory += read(fmt.Sprintf("/api/v1/conversation?limit=50&before=%d", legacy.Items[0].Seq), &legacy)
		legacyPages++
	}
	for _, category := range []string{"owner", "background", "withdrawn"} {
		legacyHistory += read("/api/v1/conversation/queue?category="+category, &queue)
	}
	var legacyTranscript protocol.IssueTranscriptPage
	legacyHistory += read("/api/v1/conversation/transcript", &legacyTranscript)
	t.Logf("compact JSON before compression: legacy first history %d B, visible first history %d B; legacy Chat reads %d requests / %d B / %d serial rounds; visible Chat reads 3 requests / %d B / 1 parallel round (after initial SSE start)",
		legacyFirst, chatLatest, legacyPages+4, legacyHistory, max(4, legacyPages), chatBytes)
	if legacyPages <= 1 || legacyFirst <= chatBytes*5 {
		t.Fatal("fixture did not exercise a tool-dense history scan")
	}

	var original protocol.IssueConversationItem
	read("/api/v1/messages/tool-41", &original)
	if original.Tool == nil || original.Tool.Input != "tool original input" || len(original.Tool.Output) == 0 {
		t.Fatal("diagnostic original missing")
	}
	if status := e.do("GET", "/api/v1/messages/tool-41", "", nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("diagnostic authorization changed: %d", status)
	}

	// Start at the end of the snapshot and cross a suppressed trailing event.
	if err := e.store.Write(ctx, func(tx *store.Tx) error {
		if err := seed(tx, "next-visible", "main_agent", "", "new assistant reply"); err != nil {
			return err
		}
		return seed(tx, "next-tool", "main_agent", store.KindToolUse, "")
	}); err != nil {
		t.Fatal(err)
	}
	stream := openStream(t, e.url, token, fmt.Sprintf("?projection=chat&after=%d", latest.EventSeq))
	if ev := stream.next(); ev.name != "start" {
		t.Fatal(ev)
	}
	first := stream.next()
	var event protocol.LedgerEvent
	if first.name != "ledger" || json.Unmarshal([]byte(first.data), &event) != nil || event.Item == nil ||
		event.Item.MessageID != "next-visible" || event.Item.Tool != nil {
		t.Fatalf("visible event: %v", first)
	}
	checkpoint := stream.next()
	if checkpoint.name != "cursor" || checkpoint.id != strconv.FormatInt(event.EventSeq+1, 10) || strings.Contains(checkpoint.data, "next-tool") {
		t.Fatalf("suppressed event should advance the cursor only: %v", checkpoint)
	}
	stream.cancel()

	stop := e.envelope("conversation.interrupt_reply", protocol.Target{Type: "message", ID: id}, nil,
		protocol.InterruptReplyPayload{MessageID: id})
	if status := e.do("POST", "/api/v1/ops", token, stop, &response); status != http.StatusOK || response.Operation.Status != protocol.OpAccepted {
		t.Fatalf("stop receipt: %d %+v", status, response)
	}
	control, _, ok, err := e.app.NextDelivery(ctx, app.MainThreadControlDestination)
	if err != nil || !ok || !strings.Contains(string(control.Payload), id) {
		t.Fatalf("original turn did not receive stop: %v %v %+v", ok, err, control)
	}
	turn.Status = store.TurnInterrupted
	if err := e.app.PutMainTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	var state map[string]string
	read("/api/v1/messages/"+id+"?projection=state", &state)
	if state["state"] != "interrupted" || state["message_id"] != id || len(state) != 2 {
		t.Fatalf("original stop state: %v", state)
	}
	resumed := openStream(t, e.url, token, "?projection=chat&after="+checkpoint.id)
	if ev := resumed.next(); ev.name != "start" {
		t.Fatal(ev)
	}
	for {
		ev := resumed.next()
		var update protocol.LedgerEvent
		if ev.name != "ledger" || json.Unmarshal([]byte(ev.data), &update) != nil {
			t.Fatal(ev)
		}
		if update.ObjectID == "next-visible" || update.ObjectID == "next-tool" {
			t.Fatalf("resuming replayed earlier event: %+v", update)
		}
		if update.Kind == "conversation.message_state" && update.ObjectID == id {
			if update.Item == nil || update.Item.State != protocol.MessageInterrupted {
				t.Fatalf("resumed stop receipt missing: %+v", update)
			}
			break
		}
	}
}

func TestChatProjectionOpenChoicesAndScopedIssueInvalidation(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	ctx := t.Context()
	issue := createOwnerIssueHTTP(t, e, token, "")
	conversation := issueDetailHTTP(t, e, token, issue.IssueID).Conversation.ConversationID
	choice := protocol.WorkItemChoice{
		ChoiceID: "choice", MessageID: "choice-notice", WorkItemID: issue.IssueID,
		Status: "open", Version: 1, Question: "Original question?", Options: []string{"Yes", "No"},
	}
	var cursor int64
	if err := e.store.Write(ctx, func(tx *store.Tx) error {
		if _, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: choice.MessageID, Author: "system", Body: choice.Question}); err != nil {
			return err
		}
		if err := tx.SaveWorkItemChoice(ctx, choice, "source", 0); err != nil {
			return err
		}
		if _, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: "issue-tool", ConversationID: conversation, Author: "main_agent", Kind: store.KindToolUse}); err != nil {
			return err
		}
		if err := tx.PutToolUse(ctx, store.ToolUseRow{
			MessageID: "issue-tool", ThreadID: "issue-thread", TurnID: "issue-turn", ItemID: "issue-item",
			Tool: "example", Input: "original Issue input", Output: "original Issue output", Status: "completed",
		}); err != nil {
			return err
		}
		var err error
		cursor, err = store.LatestEventSeq(ctx, tx.Q())
		if err != nil {
			return err
		}
		_, err = tx.InsertEvent(ctx, store.EventRow{EventID: e.ids.New(protocol.PrefixEvent),
			ObjectType: "message", ObjectID: "issue-tool", Kind: "conversation.tool_use", BodyJSON: "{}"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/conversation?projection=chat"
	var page protocol.IssueConversationPage
	if status := e.do("GET", path, token, nil, &page); status != http.StatusOK || len(page.Items) != 1 ||
		len(page.Items[0].Choices) != 1 || page.Items[0].Choices[0].ChoiceID != choice.ChoiceID {
		t.Fatalf("original open choice was not shown: status=%d page=%+v", status, page)
	}

	stream := openStream(t, e.url, token, fmt.Sprintf("?projection=chat&after=%d", cursor))
	if ev := stream.next(); ev.name != "start" {
		t.Fatal(ev)
	}
	frame := stream.next()
	var activity protocol.LedgerEvent
	if frame.name != "ledger" || json.Unmarshal([]byte(frame.data), &activity) != nil ||
		activity.ConversationID != conversation || activity.ObjectID != "issue-tool" || activity.Item != nil {
		t.Fatalf("Issue activity needs a scoped invalidation without any tool content: %+v", frame)
	}
	stream.cancel()
	var original protocol.IssueConversationItem
	if status := e.do("GET", "/api/v1/messages/issue-tool", token, nil, &original); status != http.StatusOK ||
		original.Tool == nil || original.Tool.Output != "original Issue output" {
		t.Fatalf("authorized original missing: status=%d item=%+v", status, original)
	}

	choice.Status, choice.Version = "answered", 2
	if err := e.store.Write(ctx, func(tx *store.Tx) error { return tx.SaveWorkItemChoice(ctx, choice, "", 1) }); err != nil {
		t.Fatal(err)
	}
	if status := e.do("GET", path, token, nil, &page); status != http.StatusOK || len(page.Items) != 0 {
		t.Fatalf("answered notice stayed in Chat: status=%d page=%+v", status, page)
	}
}
