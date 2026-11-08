package tools

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestIssueContextAfterAliasActuallyAdvances(t *testing.T) {
	e := newEnv(t)
	raw, _ := json.Marshal(protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "Context paging", Project: "default", Intent: "Read sources"}})
	op, err := e.svc.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "context-test"}, &protocol.Envelope{RequestID: "req_01M6VTQR5BF51VNGH14DTYHMPW", Operation: "issues.create", Target: protocol.Target{Type: "issue"}, Payload: raw}, app.CanonicalOwnerIssueCommand{Action: "create"})
	if err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
	var issue protocol.Issue
	if err := json.Unmarshal(op.Result, &issue); err != nil {
		t.Fatal(err)
	}
	var firstSeq int64
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		for _, id := range []string{"msg_alias_first", "msg_alias_second"} {
			seq, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: id, Author: "owner", Body: id})
			if err != nil {
				return err
			}
			if firstSeq == 0 {
				firstSeq = seq
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO message_work_items(message_id,work_item_id,source,updated_at) VALUES(?,?,'explicit',1)`, id, issue.IssueID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	call := func(args map[string]any) answerView {
		args["issue_id"] = issue.IssueID
		raw, _ := json.Marshal(args)
		res, _ := e.svc.canonicalIssueRead(t.Context(), Call{Tool: "issues_context_read", Arguments: raw}, store.HostedExecution{IssueID: issue.IssueID, Selection: hostedconfig.Selection{Purpose: agentconfig.IssueManager}})
		var a answerView
		if err := json.Unmarshal([]byte(res.Text), &a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	// The historical schema exposes after as a numeric string.
	a := call(map[string]any{"after": strconv.FormatInt(firstSeq, 10)})
	var page protocol.IssueContextView
	if !a.OK || json.Unmarshal(a.Result, &page) != nil || len(page.Sources) != 1 || page.Sources[0].MessageID != "msg_alias_second" {
		t.Fatalf("after alias ignored: %s %+v", a.Result, a.Error)
	}
	same := call(map[string]any{"after_source": firstSeq})
	if !same.OK || string(same.Result) != string(a.Result) {
		t.Fatal("equivalent aliases differ", same, a)
	}
	if conflict := call(map[string]any{"after": strconv.FormatInt(firstSeq, 10), "after_source": firstSeq + 1}); conflict.OK || conflict.Error == nil || conflict.Error.Code != protocol.CodeInvalidRequest {
		t.Fatalf("conflicting cursors accepted: %+v", conflict)
	}
	incremental := call(map[string]any{"view": "incremental", "limit": 1})
	var snapshot protocol.IssueContextIncremental
	if !incremental.OK || json.Unmarshal(incremental.Result, &snapshot) != nil || snapshot.Kind != "snapshot" || !snapshot.More || len(snapshot.Items) != 1 {
		t.Fatal("managed incremental read unavailable", incremental)
	}
	raw, _ = json.Marshal(map[string]any{"issue_id": "outside-manager", "view": "incremental"})
	denied, _ := e.svc.canonicalIssueRead(t.Context(), Call{Tool: "issues_context_read", Arguments: raw}, store.HostedExecution{IssueID: issue.IssueID, Selection: hostedconfig.Selection{Purpose: agentconfig.IssueManager}})
	var forbidden answerView
	if json.Unmarshal([]byte(denied.Text), &forbidden) != nil || forbidden.OK || forbidden.Error == nil || forbidden.Error.Code != protocol.CodeForbidden {
		t.Fatal("incremental cursor expanded manager scope", denied)
	}

}

func TestIssueContextHasDedicatedArguments(t *testing.T) {
	for _, p := range []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager} {
		found := false
		for _, spec := range NamedSpecs(p) {
			if spec.Name != "issues_context_read" {
				continue
			}
			found = true
			var schema map[string]any
			if err := json.Unmarshal(spec.InputSchema, &schema); err != nil {
				t.Fatal(err)
			}
			raw := string(spec.InputSchema)
			for _, field := range []string{"after_source", "after", "view", "cursor", "limit"} {
				if !strings.Contains(raw, `"`+field+`"`) {
					t.Fatal("missing context argument", field, raw)
				}
			}
			for _, field := range []string{"rule_id", "after_revision", "after_occurrence"} {
				if strings.Contains(raw, `"`+field+`"`) {
					t.Fatal("other resource arguments leaked", raw)
				}
			}
		}
		if !found {
			t.Fatal("context tool missing", p)
		}
	}
}
