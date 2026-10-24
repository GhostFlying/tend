package runnerapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
)

func httpQuestionRun(t *testing.T) (*env, *protocol.Assignment) {
	t.Helper()
	e := newEnv(t, 10*time.Millisecond)
	rt := &protocol.RunnerRuntime{InstanceID: "questions", Generation: 1, Sequence: 1, Protocol: 1, ProtocolMin: 0, ProtocolMax: 1, Mode: protocol.RunnerNormal, Capabilities: []string{protocol.CapabilityWorkerQuestions}}
	e.mustOK(asHost(), e.envelope(protocol.OpRunnerHello, "", protocol.HelloPayload{Runtime: rt, WorkspaceProtocol: 1}))
	e.createTask()
	as := e.claim(e.envelope(protocol.OpRunnerClaim, "", protocol.ClaimPayload{Runtime: rt, WorkspaceProtocol: 1}))
	if as == nil || !as.WorkerQuestions {
		t.Fatalf("assignment: %+v", as)
	}
	e.mustOK(asRun(*as), e.envelope(protocol.OpRunsReportStarted, as.RunID, nil))
	return e, as
}
func questionGET(ctx context.Context, e *env, c credential, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.server.URL+"/runner/v1/questions"+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set(c.header, c.subject)
	req.Header.Set("Authorization", "Bearer "+c.token)
	return http.DefaultClient.Do(req)
}

func TestWorkerQuestionHTTPWaitAndRepeatedAnswerRead(t *testing.T) {
	e, as := httpQuestionRun(t)
	payload := protocol.AskOwnerPayload{QuestionKey: "source", Question: "Which source should be used?", Evidence: "Two supported formats found.", Options: []string{"old", "new"}}
	env := e.envelope(protocol.OpRunsAskOwner, as.RunID, payload)
	first := e.mustOK(asRun(*as), env)
	replayed := e.mustOK(asRun(*as), env)
	if string(first) != string(replayed) {
		t.Fatal("lost create receipt changed question")
	}
	var q protocol.WorkerQuestion
	if err := json.Unmarshal(first, &q); err != nil {
		t.Fatal(err)
	}
	type result struct {
		question protocol.WorkerQuestion
		err      error
		status   int
	}
	finished := make(chan result, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		res, err := questionGET(ctx, e, asRun(*as), "/"+q.DecisionID+"?wait=25")
		if err != nil {
			finished <- result{err: err}
			return
		}
		defer res.Body.Close()
		var got protocol.WorkerQuestion
		err = json.NewDecoder(res.Body).Decode(&got)
		finished <- result{question: got, err: err, status: res.StatusCode}
	}()
	select {
	case got := <-finished:
		t.Fatalf("open question wait returned before answer: %+v", got)
	case <-time.After(30 * time.Millisecond):
	}
	answer := e.envelope("decisions.respond", q.DecisionID, protocol.AnswerDecisionPayload{Version: 1, Option: "new", Text: "Use the revised format."})
	answer.Target.Type = "decision"
	p := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "web"}
	if op, err := e.app.Execute(t.Context(), p, answer, app.AnswerDecision{}); err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
	select {
	case got := <-finished:
		if got.err != nil || got.status != http.StatusOK || got.question.Status != "answered" || len(got.question.Answer) == 0 {
			t.Fatalf("long-poll answer: %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("committed owner answer did not wake HTTP reader")
	}
	for range 2 {
		res, err := questionGET(t.Context(), e, asRun(*as), "/"+q.DecisionID+"?wait=25")
		if err != nil {
			t.Fatal(err)
		}
		var got protocol.WorkerQuestion
		err = json.NewDecoder(res.Body).Decode(&got)
		res.Body.Close()
		if err != nil || got.Status != "answered" {
			t.Fatalf("answer was consumed: %+v %v", got, err)
		}
	}
	res, err := questionGET(t.Context(), e, asRun(*as), "")
	if err != nil {
		t.Fatal(err)
	}
	var questions []protocol.WorkerQuestion
	err = json.NewDecoder(res.Body).Decode(&questions)
	res.Body.Close()
	if err != nil || len(questions) != 1 || questions[0].QuestionKey != "source" || questions[0].Status != "answered" {
		t.Fatalf("recovery list: %+v %v", questions, err)
	}
}

func TestWorkerQuestionHTTPScopeAndCancelledWait(t *testing.T) {
	e, as := httpQuestionRun(t)
	var q protocol.WorkerQuestion
	if err := json.Unmarshal(e.mustOK(asRun(*as), e.envelope(protocol.OpRunsAskOwner, as.RunID, protocol.AskOwnerPayload{QuestionKey: "scope", Question: "Which scope?"})), &q); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		c      credential
		path   string
		status int
	}{
		{asHost(), "/" + q.DecisionID, http.StatusForbidden},
		{asRun(*as), "/dec_missing", http.StatusNotFound},
		{asRun(*as), "/" + q.DecisionID + "?wait=26", http.StatusBadRequest},
	} {
		res, err := questionGET(t.Context(), e, tc.c, tc.path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != tc.status {
			t.Fatalf("read %s: %d want %d", tc.path, res.StatusCode, tc.status)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		res, err := questionGET(ctx, e, asRun(*as), "/"+q.DecisionID+"?wait=25")
		if res != nil {
			res.Body.Close()
		}
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled wait succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled HTTP wait remained blocked")
	}
	// A disconnected reader does not withdraw the durable question.
	res, err := questionGET(t.Context(), e, asRun(*as), "/"+q.DecisionID)
	if err != nil {
		t.Fatal(err)
	}
	var got protocol.WorkerQuestion
	err = json.NewDecoder(res.Body).Decode(&got)
	res.Body.Close()
	if err != nil || got.Status != "open" {
		t.Fatal(got, err)
	}
}
