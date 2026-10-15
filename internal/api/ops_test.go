package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"sync"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

var system = protocol.Principal{Kind: protocol.PrincipalSystem}

// exec runs a command directly, for setting up the ledger.
func (e *env) exec(op string, target protocol.Target, payload any, cmd app.Command) json.RawMessage {
	e.t.Helper()
	b, _ := json.Marshal(payload)
	res, err := e.app.Execute(context.Background(), system, &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: op, Target: target, Payload: b}, cmd)
	if err != nil || res.Status == protocol.OpFailed {
		e.t.Fatalf("%s: %+v, %v", op, res, err)
	}
	return res.Result
}

// task creates a task and returns its ID.
func (e *env) task() string {
	e.t.Helper()
	e.exec("config.sync", protocol.Target{Type: "config"}, app.ConfigPayload{
		Repos: []app.RepoConfig{{Repo: "tend", RemoteURL: "https://example.com/tend.git"}},
	}, app.SyncConfig{})
	var res struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(e.exec("tasks.create", protocol.Target{Type: "task"}, map[string]any{"title": "fix it", "spec": domain.Spec{
		Goal: "fix the build", Acceptance: []string{"build passes"}, Delivery: domain.DeliveryCommit,
		Workspace: domain.Workspace{Mounts: []domain.Mount{{Repo: "tend"}}},
	}}, app.CreateTask{}), &res)
	return res.TaskID
}

// client is a second owner client with its own session.
func (e *env) client2(id string) *env {
	e.t.Helper()
	jar, _ := cookiejar.New(nil)
	c := *e
	c.client = &http.Client{Jar: jar}
	var v protocol.SessionView
	if st := c.do("POST", "/api/v1/session", protocol.SessionRequest{ClientID: id}, http.Header{HeaderClient: {id}}, &v); st != http.StatusOK {
		e.t.Fatalf("second session = %d", st)
	}
	return &c
}

// op posts an envelope as the client with ID client.
func (e *env) op(client string, env protocol.Envelope) (int, protocol.APIResponse) {
	e.t.Helper()
	var res protocol.APIResponse
	st := e.do("POST", "/api/v1/ops", env, http.Header{HeaderClient: {client}}, &res)
	return st, res
}

func (e *env) envelope(op string, target protocol.Target, revision *int64, payload any) protocol.Envelope {
	b, _ := json.Marshal(payload)
	return protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: op, Target: target, ExpectedControlRevision: revision, Payload: b}
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.store.Read(context.Background(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(context.Background(), query, args...).Scan(&n)
	}); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestFault_ClientRetrySameRequest(t *testing.T) {
	e := newEnv(t)
	e.login()
	env := e.envelope("conversation.submit_message", protocol.Target{Type: "message"}, nil, protocol.SubmitMessagePayload{Body: "hello"})
	st, first := e.op(clientID, env)
	if st != http.StatusOK || first.Operation == nil || first.Operation.Status != protocol.OpAccepted {
		t.Fatalf("submit = %d %+v", st, first)
	}
	// The outbox resends after a lost response.
	st, again := e.op(clientID, env)
	if st != http.StatusOK || again.Operation == nil || string(again.Operation.Result) != string(first.Operation.Result) {
		t.Errorf("resubmit = %d %+v, want the original result %s", st, again, first.Operation.Result)
	}
	if n := e.count(`SELECT COUNT(*) FROM conversation_messages`); n != 1 {
		t.Errorf("messages = %d, want 1", n)
	}
	// Reusing the request ID for a different message is a conflict.
	env.Payload = json.RawMessage(`{"body":"something else"}`)
	if st, res := e.op(clientID, env); st != http.StatusConflict || res.Error == nil || res.Error.Code != protocol.CodeConflictPayload {
		t.Errorf("same request ID, other body = %d %+v", st, res)
	}
}

func TestOwnerOpsWhitelist(t *testing.T) {
	e := newEnv(t)
	e.login()
	for _, op := range []string{"tasks.create", "decisions.open", "runs.release", "config.sync"} {
		env := e.envelope(op, protocol.Target{Type: "task"}, nil, map[string]any{})
		if st, res := e.op(clientID, env); st != http.StatusBadRequest || res.Error == nil || res.Error.Code != protocol.CodeInvalidRequest {
			t.Errorf("%s = %d %+v, want invalid_request", op, st, res)
		}
	}
	if st, _ := e.op("", e.envelope("conversation.submit_message", protocol.Target{Type: "message"}, nil, protocol.SubmitMessagePayload{Body: "hi"})); st != http.StatusForbidden {
		t.Errorf("write without the client header = %d, want 403", st)
	}
}

func TestFault_ConcurrentDecisionAnswers(t *testing.T) {
	e := newEnv(t)
	e.login()
	other := e.client2("laptop-0123456789")
	var opened struct {
		DecisionID string `json:"decision_id"`
	}
	_ = json.Unmarshal(e.exec("decisions.open", protocol.Target{Type: "decision"}, map[string]any{"question": "which?", "options": []string{"a", "b"}}, app.OpenDecision{}), &opened)
	target := protocol.Target{Type: "decision", ID: opened.DecisionID}

	clients := []struct {
		e      *env
		id     string
		option string
	}{{e, clientID, "a"}, {other, "laptop-0123456789", "b"}}
	results := make([]protocol.APIResponse, len(clients))
	var wg sync.WaitGroup
	for i, c := range clients {
		wg.Go(func() {
			_, results[i] = c.e.op(c.id, e.envelope("decisions.respond", target, nil, protocol.AnswerDecisionPayload{Version: 1, Option: c.option}))
		})
	}
	wg.Wait()
	winner := -1
	for i, r := range results {
		switch {
		case r.Operation == nil:
			t.Fatalf("answer %d = %+v", i, r)
		case r.Operation.Status == protocol.OpSucceeded:
			if winner >= 0 {
				t.Fatal("both answers took effect")
			}
			winner = i
		case r.Operation.Error == nil || r.Operation.Error.Code != protocol.CodeStaleRevision:
			t.Errorf("losing answer = %+v, want stale_revision", r.Operation)
		}
	}
	if winner < 0 {
		t.Fatal("neither answer took effect")
	}
	// The winner's client sends the same answer again under a new request.
	w := clients[winner]
	_, res := w.e.op(w.id, e.envelope("decisions.respond", target, nil, protocol.AnswerDecisionPayload{Version: 1, Option: w.option}))
	if res.Operation == nil || res.Operation.Status != protocol.OpSucceeded || string(res.Operation.Result) != string(results[winner].Operation.Result) {
		t.Errorf("repeated answer = %+v, want the recorded result", res)
	}
}

func TestFault_StaleControlFromUI(t *testing.T) {
	e := newEnv(t)
	e.login()
	taskID := e.task()
	target := protocol.Target{Type: "task", ID: taskID}
	rev := int64(1)
	pause := protocol.SetControlModePayload{Mode: protocol.ControlPaused}
	if _, res := e.op(clientID, e.envelope("control.set_mode", target, &rev, pause)); res.Operation == nil || res.Operation.Status != protocol.OpSucceeded {
		t.Fatalf("pause = %+v", res)
	}
	// Another client, still showing revision 1, resumes.
	_, res := e.op(clientID, e.envelope("control.set_mode", target, &rev, protocol.SetControlModePayload{Mode: protocol.ControlManaged}))
	if res.Operation == nil || res.Operation.Status != protocol.OpFailed || res.Operation.Error.Code != protocol.CodeStaleRevision {
		t.Fatalf("stale resume = %+v, want stale_revision", res)
	}
	if res.Current == nil || res.Current.ControlMode != protocol.ControlPaused || res.Current.ControlRevision != 2 {
		t.Errorf("current = %+v, want paused at revision 2", res.Current)
	}
}
