package api

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"net/http"
	"testing"
)

func TestContextHTTPIndexesToolAndMaterialsReadsOriginal(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	issue := createOwnerIssueHTTP(t, e, token, "")
	const id = "context-http-tool"
	const original = `{"sources":[{"body":"original tool result"}]}`
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: id, Kind: store.KindToolUse, Author: "main_agent"}); err != nil {
			return err
		}
		if err := tx.PutToolUse(t.Context(), store.ToolUseRow{MessageID: id, TurnID: "http-turn", ItemID: "http-item", Tool: "issues_context_read", Input: `{"after_source":0}`, Output: original, Status: "completed"}); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO message_work_items(message_id,work_item_id,source,updated_at) VALUES(?,?,'explicit',1)`, id, issue.IssueID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var page protocol.IssueContextView
	if code := e.do(http.MethodGet, "/api/v1/issues/"+issue.IssueID+"/context?after_source=0", token, nil, &page); code != http.StatusOK || len(page.Sources) != 1 {
		t.Fatal(code, page)
	}
	tool := page.Sources[0].Tool
	if tool == nil || !tool.ContentOmitted || tool.OriginalMessageID != id || tool.Input != "" || tool.Output != "" {
		t.Fatal("context is not an explicit original index", tool)
	}
	var exact protocol.IssueMaterials
	if code := e.do(http.MethodGet, "/api/v1/issues/"+issue.IssueID+"/materials?kind=messages&id="+id, token, nil, &exact); code != http.StatusOK || len(exact.Messages) != 1 || exact.Messages[0].Tool == nil || exact.Messages[0].Tool.Output != original {
		t.Fatal("original unavailable", code, exact)
	}
	if code := e.do(http.MethodGet, "/api/v1/issues/"+issue.IssueID+"/context", "", nil, nil); code != http.StatusUnauthorized {
		t.Fatal("projection bypassed auth", code)
	}
}
