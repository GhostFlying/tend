package cli

import (
	"encoding/json"
	"github.com/GhostFlying/tend/internal/protocol"
	"net/http"
	"testing"
)

func TestFeishuTypedBindKeepsCompleteRequestOnRetry(t *testing.T) {
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	f.answer("GET /api/v1/messaging/feishu", protocol.FeishuBindingView{Revision: 7})
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, body []byte) {
		var env protocol.Envelope
		if err := json.Unmarshal(body, &env); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded}})
	})
	args := []string{"channel", "feishu", "bind", "--app-id", "application", "--tenant-key", "tenant", "--sender-id", "owner-openid", "--chat-id", "private-chat", "--reason", "approved"}
	if code := e.main(append(args, "--dry-run")...); code != 0 {
		t.Fatal(code, e.errOut.String())
	}
	var original protocol.Envelope
	if err := json.Unmarshal(e.out.Bytes(), &original); err != nil {
		t.Fatal(err, e.out.String())
	}
	if original.RequestID == "" || original.ExpectedControlRevision == nil || *original.ExpectedControlRevision != 7 || original.Target.ID != "feishu" {
		t.Fatal(original)
	}
	e.out.Reset()
	e.errOut.Reset()
	retry := append(append([]string{}, args...), "--request-id", original.RequestID, "--revision", "7")
	if code := e.main(retry...); code != 0 {
		t.Fatal(code, e.errOut.String())
	}
	requests := f.seen("POST /api/v1/ops")
	if len(requests) != 1 {
		t.Fatal(len(requests))
	}
	var actual protocol.Envelope
	if err := json.Unmarshal(requests[0].body, &actual); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(original)
	after, _ := json.Marshal(actual)
	if string(before) != string(after) {
		t.Fatalf("request changed\n%s\n%s", before, after)
	}
	if len(f.seen("GET /api/v1/messaging/feishu")) != 1 {
		t.Fatal("retry fetched a new revision")
	}
}

func TestFeishuRejectsIncompleteRetryAndDestinationFlags(t *testing.T) {
	for _, args := range [][]string{
		{"bind", "--request-id", "original"},
		{"bind", "--revision", "1", "--app-id", "application"},
		{"unbind", "--chat-id", "new-chat"},
		{"show", "--revision", "1"},
	} {
		t.Run(args[0]+args[1], func(t *testing.T) {
			f := newFakeServer(t)
			e := newTestEnv(t, f.srv.URL)
			if e.main(append([]string{"channel", "feishu"}, args...)...) == 0 {
				t.Fatal("accepted invalid input")
			}
			if len(f.requests) != 0 {
				t.Fatal("invalid input issued network request")
			}
		})
	}
}
