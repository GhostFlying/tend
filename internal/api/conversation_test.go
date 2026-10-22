package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
)

func (e *env) conversation(token, query string) (int, protocol.ConversationPage) {
	e.t.Helper()
	var page protocol.ConversationPage
	st := e.do("GET", "/api/v1/conversation"+query, token, nil, &page)
	return st, page
}

func seqs(page protocol.ConversationPage) []int64 {
	var out []int64
	for _, it := range page.Items {
		out = append(out, it.Seq)
	}
	return out
}

func TestConversationPages(t *testing.T) {
	e := newEnv(t)
	tok := e.login(protocol.ScopeOwner)
	exec := func(p protocol.Principal, op string, payload any, cmd app.Command) {
		t.Helper()
		b, _ := json.Marshal(payload)
		res, err := e.app.Execute(context.Background(), p, &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: op,
			Target: protocol.Target{Type: "message"}, Payload: b}, cmd)
		if err != nil || res.Status == protocol.OpFailed {
			t.Fatalf("%s: %+v %v", op, res, err)
		}
	}
	owner := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: ownerLogin, Client: clientID}
	for i := range 4 {
		exec(owner, "conversation.submit_message", protocol.SubmitMessagePayload{Body: fmt.Sprintf("message %d", i+1)}, app.SubmitMessage{})
	}
	exec(protocol.Principal{Kind: protocol.PrincipalSystem}, "conversation.record_tool_use",
		map[string]any{"turn_id": "turn_1", "item_id": "item_1", "tool": "tasks_list", "input": "{}", "status": "running"}, app.RecordToolUse{})

	st, page := e.conversation(tok, "?limit=2")
	if st != http.StatusOK || fmt.Sprint(seqs(page)) != "[4 5]" || !page.More {
		t.Fatalf("latest = %d %v more %v", st, seqs(page), page.More)
	}
	if it := page.Items[1]; it.Kind != protocol.ItemToolUse || it.Tool == nil || it.Tool.Name != "tasks_list" || it.Tool.Status != protocol.ToolRunning || it.State != "" {
		t.Errorf("tool use = %+v", it)
	}
	if it := page.Items[0]; it.Kind != protocol.ItemMessage || it.Body != "message 4" || it.State != protocol.MessageQueued {
		t.Errorf("owner message = %+v", it)
	}
	if st, page = e.conversation(tok, "?before=4&limit=2"); st != http.StatusOK || fmt.Sprint(seqs(page)) != "[2 3]" || !page.More {
		t.Errorf("before 4 = %d %v more %v", st, seqs(page), page.More)
	}
	if st, page = e.conversation(tok, "?before=2"); st != http.StatusOK || fmt.Sprint(seqs(page)) != "[1]" || page.More {
		t.Errorf("before 2 = %d %v more %v", st, seqs(page), page.More)
	}
	if st, page = e.conversation(tok, "?after=2&limit=2"); st != http.StatusOK || fmt.Sprint(seqs(page)) != "[3 4]" || !page.More {
		t.Errorf("after 2 = %d %v more %v", st, seqs(page), page.More)
	}
	if st, page = e.conversation(tok, "?after=5"); st != http.StatusOK || len(page.Items) != 0 || page.More {
		t.Errorf("after the last = %d %v more %v", st, seqs(page), page.More)
	}
	for _, q := range []string{"?before=1&after=1", "?limit=-1", "?after=x"} {
		if st, _ := e.conversation(tok, q); st != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", q, st)
		}
	}
	if st, _ := e.conversation("", ""); st != http.StatusUnauthorized {
		t.Errorf("without a token = %d, want 401", st)
	}
}
