package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestConversationReadRecoversOlderOwnerConstraintAcrossHandoffPages(t *testing.T) {
	e := newEnv(t)
	constraint := "Do not merge or deploy without my decision.\n" + strings.Repeat("Preserve the original input. ", 300)
	file := protocol.ConversationAttachment{FileID: "file_original", Name: "evidence.gif", MediaType: "image/gif", Size: 1000, SHA256: strings.Repeat("a", 64)}
	err := e.store.Write(context.Background(), func(tx *store.Tx) error {
		if err := tx.InsertConversationAttachment(context.Background(), store.ConversationAttachmentRow{ConversationAttachment: file}); err != nil {
			return err
		}
		for i := 1; i <= 51; i++ {
			author, body, reply := "main_agent", fmt.Sprintf("progress %d", i), "msg_1"
			if i == 1 {
				author, body, reply = "owner", constraint, ""
			}
			id := fmt.Sprintf("msg_%d", i)
			if _, err := tx.InsertMessage(context.Background(), store.MessageRow{MessageID: id, Author: author, Body: body, ReplyTo: reply}); err != nil {
				return err
			}
			if i == 1 {
				if err := tx.BindConversationAttachment(context.Background(), id, 0, file.FileID); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	read := func(args conversationArgs) conversationPage {
		t.Helper()
		raw, _ := json.Marshal(args)
		// The newly created handoff thread has no shared native history.
		result := e.svc.Handle(context.Background(), Call{Principal: protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "new-harness", Thread: "new-thread"}, Tool: "conversation_read", Arguments: raw})
		var answer answerView
		if err := json.Unmarshal([]byte(result.Text), &answer); err != nil || !answer.OK {
			t.Fatalf("read: %s %v", result.Text, err)
		}
		var page conversationPage
		if err := json.Unmarshal(answer.Result, &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	first := read(conversationArgs{})
	if len(first.Items) != 20 || first.Items[0].Seq != 32 || !first.More || first.NextBeforeSeq != 32 {
		t.Fatalf("latest page: %+v", first)
	}
	page := first
	seen := map[int64]bool{}
	found := false
	for {
		for i, item := range page.Items {
			if seen[item.Seq] || (i > 0 && item.Seq <= page.Items[i-1].Seq) {
				t.Fatal("pagination repeated or reordered history")
			}
			seen[item.Seq] = true
			if item.Seq == 1 {
				found = true
				if item.Author != "owner" || item.Body != constraint || len(item.Attachments) != 1 || item.Attachments[0] != file {
					t.Fatalf("owner constraint or input lost: %+v", item)
				}
			}
			if item.Seq > 1 && item.ReplyTo != "msg_1" {
				t.Fatal("reply provenance lost")
			}
		}
		if !page.More {
			break
		}
		page = read(conversationArgs{BeforeSeq: page.NextBeforeSeq})
	}
	if !found || len(seen) != 51 {
		t.Fatalf("history coverage %d found=%v", len(seen), found)
	}
	forward := read(conversationArgs{AfterSeq: 1, Limit: new(7)})
	if len(forward.Items) != 7 || forward.Items[0].Seq != 2 || forward.NextAfterSeq != 8 || forward.NextBeforeSeq != 0 || !forward.More {
		t.Fatalf("forward page %+v", forward)
	}
	last := read(conversationArgs{AfterSeq: 51})
	if len(last.Items) != 0 || last.More || last.NextBeforeSeq != 0 || last.NextAfterSeq != 0 {
		t.Fatalf("empty page %+v", last)
	}
	// This tool only reads. It neither creates new requests nor plans tasks.
	if e.tasks() != 0 {
		t.Fatal("history lookup created work")
	}
}

func TestConversationReadBoundariesAndToolPayloads(t *testing.T) {
	e := newEnv(t)
	for _, args := range []conversationArgs{{BeforeSeq: -1}, {AfterSeq: -1}, {BeforeSeq: 1, AfterSeq: 2}, {Limit: new(0)}, {Limit: new(51)}} {
		result := e.call("conversation_read", args, "")
		if result.OK || result.Error == nil || result.Error.Code != protocol.CodeInvalidRequest {
			t.Fatalf("invalid args %+v: %+v", args, result)
		}
	}
	err := e.store.Write(context.Background(), func(tx *store.Tx) error {
		if _, err := tx.InsertMessage(context.Background(), store.MessageRow{Kind: store.KindToolUse, MessageID: "msg_tool", Author: "main_agent"}); err != nil {
			return err
		}
		return tx.PutToolUse(context.Background(), store.ToolUseRow{MessageID: "msg_tool", ThreadID: "old-thread", TurnID: "old-turn", ItemID: "tool-call", Tool: "conversation_read", Input: "recursive input", Output: "recursive output", Status: "completed"})
	})
	if err != nil {
		t.Fatal(err)
	}
	result := e.call("conversation_read", conversationArgs{Limit: new(50)}, "")
	var page conversationPage
	if !result.OK || json.Unmarshal(result.Result, &page) != nil {
		t.Fatalf("tool read %+v", result)
	}
	if len(page.Items) != 1 || page.Items[0].Tool == nil || page.Items[0].Tool.Name != "conversation_read" || page.Items[0].Tool.Input != "" || page.Items[0].Tool.Output != "" {
		t.Fatalf("recursive tool payload leaked: %+v", page)
	}
}

func TestConversationReadAvailableToHostedAndAddon(t *testing.T) {
	if !slices.ContainsFunc(Specs(), func(s harness.ToolSpec) bool { return s.Name == "conversation_read" }) {
		t.Fatal("hosted dispatcher cannot recover ledger history")
	}
	if !slices.ContainsFunc(AddonSpecs(), func(s protocol.ToolSpec) bool { return s.Name == "conversation_read" }) {
		t.Fatal("addon cannot read history")
	}
	e := newEnv(t)
	for _, p := range []protocol.Principal{hosted, {Kind: protocol.PrincipalMainAgent, Subject: "addon", Agent: protocol.AgentCodex}} {
		result := e.svc.Handle(context.Background(), Call{Principal: p, Tool: "conversation_read", Arguments: json.RawMessage(`{}`)})
		if !result.Success {
			t.Fatalf("principal %+v: %s", p, result.Text)
		}
	}
}
