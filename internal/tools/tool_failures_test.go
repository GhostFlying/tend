package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestToolFailuresRestoreRetainedErrorAndRejectMissingOriginal(t *testing.T) {
	e := newEnv(t)
	output := `{"error":{"code":"invalid_request","message":"original failure"},"private":"` + strings.Repeat("PRIVATE_RESULT", 500) + `"}`
	ctx, err := e.store.StageOriginals(t.Context(), "failed-tool-original", []byte(output))
	if err != nil {
		t.Fatal(err)
	}
	if err = e.store.Write(ctx, func(tx *store.Tx) error {
		if _, err := tx.InsertMessage(ctx, store.MessageRow{Kind: store.KindToolUse, MessageID: "retained-failure", Author: "main_agent"}); err != nil {
			return err
		}
		return tx.PutToolUse(ctx, store.ToolUseRow{MessageID: "retained-failure", ThreadID: "thread", TurnID: "turn", ItemID: "retained-call", Tool: "tasks_create", Input: "PRIVATE_INPUT", Output: output, Status: "failed"})
	}); err != nil {
		t.Fatal(err)
	}
	page := e.call("tool_failures_list", toolFailuresArgs{}, "")
	var got struct {
		Items []toolFailure `json:"items"`
	}
	if !page.OK || json.Unmarshal(page.Result, &got) != nil || len(got.Items) != 1 || got.Items[0].Error == nil || got.Items[0].Error.Message != "original failure" || strings.Contains(string(page.Result), "PRIVATE_") {
		t.Fatal("retained error lost or private output leaked", page)
	}
	object, err := e.store.FileStore().Path(strings.Repeat("0", 64))
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(filepath.Dir(filepath.Dir(object)), "archives", fmt.Sprintf("%x", sha256.Sum256([]byte("failed-tool-original"))))
	if err := os.Remove(archive); err != nil {
		t.Fatal(err)
	}
	if page := e.call("tool_failures_list", toolFailuresArgs{}, ""); page.OK {
		t.Fatal("missing original silently erased the failure", page)
	}
}

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
