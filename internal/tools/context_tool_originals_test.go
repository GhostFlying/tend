package tools

import (
	"encoding/json"
	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"testing"
)

func TestManagedContextIndexesOriginalAndExactMaterialKeepsContent(t *testing.T) {
	e := newEnv(t)
	raw, _ := json.Marshal(protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "Context originals", Project: "default", Intent: "Read existing originals"}})
	op, err := e.svc.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "context"}, &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: "issues.create", Target: protocol.Target{Type: "issue", ID: e.ids.New(protocol.PrefixWorkItem)}, Payload: raw}, app.CanonicalOwnerIssueCommand{Action: "create"})
	if err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
	var issue protocol.Issue
	if err := json.Unmarshal(op.Result, &issue); err != nil {
		t.Fatal(err)
	}
	const id = "managed-context-original"
	const original = `{"sources":[{"body":"prior tool original"}]}`
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: id, Kind: store.KindToolUse, Author: "main_agent"}); err != nil {
			return err
		}
		if err := tx.PutToolUse(t.Context(), store.ToolUseRow{MessageID: id, TurnID: "managed-turn", ItemID: "managed-item", Tool: "issues_context_read", Output: original, Status: "completed"}); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO message_work_items(message_id,work_item_id,source,updated_at) VALUES(?,?,'explicit',1)`, id, issue.IssueID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	execution := store.HostedExecution{IssueID: issue.IssueID, Selection: hostedconfig.Selection{Purpose: agentconfig.IssueManager}}
	call := func(tool string, args any) answerView {
		t.Helper()
		raw, _ := json.Marshal(args)
		res, _ := e.svc.canonicalIssueRead(t.Context(), Call{Tool: tool, Arguments: raw}, execution)
		var answer answerView
		if err := json.Unmarshal([]byte(res.Text), &answer); err != nil || !answer.OK {
			t.Fatal(answer, err)
		}
		return answer
	}
	answer := call("issues_context_read", map[string]any{"after_source": 0})
	var page protocol.IssueContextView
	if err := json.Unmarshal(answer.Result, &page); err != nil || len(page.Sources) != 1 || page.Sources[0].Tool == nil || !page.Sources[0].Tool.ContentOmitted || page.Sources[0].Tool.OriginalMessageID != id || page.Sources[0].Tool.Output != "" {
		t.Fatal("managed context recirculated original", page, err)
	}
	exact := call("issues_materials_read", map[string]any{"kind": "messages", "id": id})
	var materials protocol.IssueMaterials
	if err := json.Unmarshal(exact.Result, &materials); err != nil || len(materials.Messages) != 1 || materials.Messages[0].Tool.Output != original {
		t.Fatal("managed original reference unusable", err)
	}
}
