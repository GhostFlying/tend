package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestScriptFindsOriginalOwnerSourceBeyondLatestPage(t *testing.T) {
	calls := 0
	h := testkit.NewHarness()
	session, err := h.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	thread, err := session.StartThread(t.Context(), harness.ThreadConfig{OnToolCall: func(_ context.Context, call harness.ToolCall) harness.ToolResult {
		calls++
		if call.Tool != "conversation_read" {
			t.Fatal("source lookup performed a business mutation", call.Tool)
		}
		var args struct {
			Before int64 `json:"before_seq"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			t.Fatal(err)
		}
		var page any
		if calls == 1 {
			if args.Before != 0 {
				t.Fatal(args)
			}
			items := make([]protocol.ConversationItem, 50)
			for i := range items {
				items[i] = protocol.ConversationItem{Seq: int64(42 + i), MessageID: fmt.Sprintf("unrelated-%d", i), Author: "main_agent"}
			}
			items[0].Author, items[0].Body = "owner", "派发其他请求"
			page = struct {
				protocol.ConversationPage
				NextBefore int64 `json:"next_before_seq"`
			}{protocol.ConversationPage{Items: items, More: true}, 42}
		} else {
			if args.Before != 42 {
				t.Fatal(args)
			}
			page = protocol.ConversationPage{Items: []protocol.ConversationItem{{Seq: 41, MessageID: "original-source", Author: "owner", Body: "派发原请求"}}}
		}
		raw, err := json.Marshal(map[string]any{"ok": true, "result": page})
		if err != nil {
			t.Fatal(err)
		}
		return harness.ToolResult{Success: true, Text: string(raw)}
	}})
	if err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	caller := toolCaller{ctx: t.Context(), thread: thread.(*testkit.FakeThread), turn: "test-source", log: slog.Default(), coordination: gate}
	message, ok := caller.ownerMessage("original-source")
	if !ok || message == nil || message.Body != "派发原请求" || calls != 2 {
		t.Fatalf("original owner request was lost outside the latest page: message=%+v ok=%v reads=%d", message, ok, calls)
	}
}

func TestScriptReviewDropsObligationsOmittedByFreshRead(t *testing.T) {
	h := testkit.NewHarness()
	session, err := h.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	reads, reviews := 0, 0
	thread, err := session.StartThread(t.Context(), harness.ThreadConfig{OnToolCall: func(_ context.Context, call harness.ToolCall) harness.ToolResult {
		switch call.Tool {
		case "issues_read":
			reads++
			issue := protocol.IssueView{ContextRevision: int64(reads)}
			issue.IssueID = "wi_fixture"
			if reads == 1 {
				issue.SourceReviews = []protocol.IssueSourceReview{{MessageID: "original-source", ContentRevision: 1, AssociationRevision: 1}}
			}
			raw, _ := json.Marshal(map[string]any{"ok": true, "result": issue})
			return harness.ToolResult{Success: true, Text: string(raw)}
		case "issues_context_review":
			reviews++
			if reviews == 1 {
				return harness.ToolResult{Text: `{"ok":false,"error":{"code":"stale_revision"}}`}
			}
			var args struct {
				Payload protocol.IssueContextReview `json:"payload"`
			}
			if err := json.Unmarshal(call.Arguments, &args); err != nil {
				t.Fatal(err)
			}
			if len(args.Payload.Sources) != 0 || args.Payload.ContextRevision != 2 {
				t.Fatal("fresh review retained an obsolete source obligation", args)
			}
			return harness.ToolResult{Success: true, Text: `{"ok":true,"result":{}}`}
		default:
			t.Fatal(call.Tool)
		}
		return harness.ToolResult{}
	}})
	if err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	caller := toolCaller{ctx: t.Context(), thread: thread.(*testkit.FakeThread), turn: "test-fresh-review", log: slog.Default(), coordination: gate}
	var issue protocol.IssueView
	if !caller.reviewCurrentIssue(&issue) || reads != 2 || reviews != 2 || len(issue.SourceReviews) != 0 {
		t.Fatal("review did not use a fresh Issue snapshot", reads, reviews, issue)
	}
}
