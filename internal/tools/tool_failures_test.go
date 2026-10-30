package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestToolFailuresPaginationAndRedaction(t *testing.T) {
	e := newEnv(t)
	err := e.store.Write(context.Background(), func(tx *store.Tx) error {
		for i := 0; i < 3; i++ {
			id := fmt.Sprintf("msg_failure_%d", i)
			if _, err := tx.InsertMessage(context.Background(), store.MessageRow{Kind: store.KindToolUse, MessageID: id, Author: "main_agent"}); err != nil {
				return err
			}
			if err := tx.PutToolUse(context.Background(), store.ToolUseRow{MessageID: id, ThreadID: "thread", TurnID: "turn", ItemID: fmt.Sprintf("call_%d", i), Tool: "tasks_create", Input: "PRIVATE_INPUT", Output: `{"ok":false,"error":{"code":"invalid_request","message":"spec.workspace.host: unknown field","details":{"field":"spec.workspace.host"}},"current":"PRIVATE_RESULT"}`, Status: "failed"}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	first := e.call("tool_failures_list", toolFailuresArgs{Limit: 2, Tool: "tasks_create"}, "")
	var page struct {
		Items []toolFailure `json:"items"`
		Next  int64         `json:"next_before_seq"`
	}
	if !first.OK || json.Unmarshal(first.Result, &page) != nil || len(page.Items) != 2 || page.Next == 0 {
		t.Fatalf("page: %+v", first)
	}
	if strings.Contains(string(first.Result), "PRIVATE_") || page.Items[0].Error == nil || page.Items[0].ItemID != "call_2" {
		t.Fatalf("leak or lost identity: %s", first.Result)
	}
	second := e.call("tool_failures_list", toolFailuresArgs{Limit: 2, BeforeSeq: page.Next}, "")
	if !second.OK || json.Unmarshal(second.Result, &page) != nil || len(page.Items) != 1 || page.Items[0].ItemID != "call_0" {
		t.Fatalf("second: %+v", second)
	}
}

func TestToolFailuresOwnerConversationScopeAndRunIsolation(t *testing.T) {
	e := newEnv(t)
	for _, principal := range []protocol.Principal{hosted} {
		result := e.svc.Handle(context.Background(), Call{Principal: principal, Tool: "tool_failures_list", Arguments: json.RawMessage(`{}`)})
		if !result.Success || !strings.Contains(result.Text, `"scope":"hosted_conversation"`) {
			t.Fatalf("missing conversation scope: %s", result.Text)
		}
	}
	for _, kind := range []protocol.PrincipalKind{protocol.PrincipalRun, protocol.PrincipalRunner} {
		result := e.svc.Handle(context.Background(), Call{Principal: protocol.Principal{Kind: kind, Subject: "worker"}, Tool: "tool_failures_list", Arguments: json.RawMessage(`{}`)})
		if result.Success || !strings.Contains(result.Text, `"code":"forbidden"`) {
			t.Fatalf("worker accessed planning history: %s", result.Text)
		}
	}
}

func TestManagerCannotFallThroughToUnscopedToolsWithoutExecution(t *testing.T) {
	e := newEnv(t)
	for _, execution := range []string{"", "missing-execution"} {
		result := e.svc.Handle(t.Context(), Call{Principal: protocol.Principal{Kind: protocol.PrincipalRun, Subject: "manager-run", Role: protocol.RoleIssueManager}, ExecutionRequestID: execution, Tool: "repos_list", Arguments: json.RawMessage(`{}`)})
		if result.Success || !strings.Contains(result.Text, `"code":"forbidden"`) {
			t.Fatal("manager read global facts without a persisted scope", result.Text)
		}
	}
}
