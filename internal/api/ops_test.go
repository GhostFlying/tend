package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

var system = protocol.Principal{Kind: protocol.PrincipalSystem}

// exec runs a command directly, for setting up the ledger.
func (e *env) exec(op string, target protocol.Target, revision *int64, payload any, cmd app.Command) json.RawMessage {
	e.t.Helper()
	b, _ := json.Marshal(payload)
	res, err := e.app.Execute(context.Background(), system, &protocol.Envelope{
		RequestID: e.ids.New(protocol.PrefixRequest), Operation: op, Target: target, ExpectedControlRevision: revision, Payload: b,
	}, cmd)
	if err != nil || res.Status == protocol.OpFailed {
		e.t.Fatalf("%s: %+v, %v", op, res, err)
	}
	return res.Result
}

// configure registers a repository and a host serving it.
func (e *env) configure() {
	e.t.Helper()
	e.exec("config.sync", protocol.Target{Type: "config"}, nil, app.ConfigPayload{
		Repos: []app.RepoConfig{{Repo: "tend", RemoteURL: "https://example.com/tend.git"}},
		Hosts: []app.HostConfig{{HostID: "host-a", OS: "linux", Arch: "amd64", Repos: []string{"tend"}, Slots: 2, TokenHash: strings.Repeat("0", 64), Nodes: []string{"host-a"}}},
	}, app.SyncConfig{})
}

// task creates a task and returns its ID.
func (e *env) task(title string) string {
	e.t.Helper()
	var res struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(e.exec("tasks.create", protocol.Target{Type: "task"}, nil, map[string]any{"title": title, "spec": domain.Spec{
		Goal: "fix the build", Acceptance: []string{"build passes"}, Delivery: domain.DeliveryCommit,
		Workspace: domain.Workspace{Mounts: []domain.Mount{{Repo: "tend"}}},
	}}, app.CreateTask{}), &res)
	return res.TaskID
}

// decision opens a decision about a task and returns its ID.
func (e *env) decision(taskID string) string {
	e.t.Helper()
	var opened struct {
		DecisionID string `json:"decision_id"`
	}
	_ = json.Unmarshal(e.exec("decisions.open", protocol.Target{Type: "decision"}, nil,
		map[string]any{"task_id": taskID, "question": "which?", "options": []string{"a", "b"}}, app.OpenDecision{}), &opened)
	return opened.DecisionID
}

// op posts an envelope with a token.
func (e *env) op(tok string, env protocol.Envelope) (int, protocol.APIResponse) {
	e.t.Helper()
	var res protocol.APIResponse
	st := e.do("POST", "/api/v1/ops", tok, env, &res)
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
	tok := e.login(protocol.ScopeOwner)
	e.configure()
	taskID := e.task("fix it")
	rev := int64(1)
	env := e.envelope("control.set_mode", protocol.Target{Type: "task", ID: taskID}, &rev, protocol.SetControlModePayload{Mode: protocol.ControlPaused})
	st, first := e.op(tok, env)
	if st != http.StatusOK || first.Operation == nil || first.Operation.Status != protocol.OpSucceeded {
		t.Fatalf("pause = %d %+v", st, first)
	}
	// The CLI resends after a lost response.
	st, again := e.op(tok, env)
	if st != http.StatusOK || again.Operation == nil || again.Operation.Status != protocol.OpSucceeded || string(again.Operation.Result) != string(first.Operation.Result) {
		t.Errorf("resend = %d %+v, want the original result %s", st, again, first.Operation.Result)
	}
	if n := e.count(`SELECT control_revision FROM tasks WHERE task_id = ?`, taskID); n != 2 {
		t.Errorf("control revision = %d, want 2", n)
	}
	// Reusing the request ID for something else is a conflict.
	env.Payload = json.RawMessage(`{"mode":"human_control"}`)
	if st, res := e.op(tok, env); st != http.StatusConflict || res.Error == nil || res.Error.Code != protocol.CodeConflictPayload {
		t.Errorf("same request ID, other payload = %d %+v", st, res)
	}
	// The request can be looked up afterwards.
	var rv protocol.RequestView
	if st := e.do("GET", "/api/v1/requests/"+env.RequestID, tok, nil, &rv); st != http.StatusOK || rv.Status != protocol.OpSucceeded || rv.Operation != "control.set_mode" {
		t.Errorf("request = %d %+v", st, rv)
	}
	if st := e.do("GET", "/api/v1/requests/req_missing", tok, nil, nil); st != http.StatusNotFound {
		t.Errorf("unknown request = %d, want 404", st)
	}
}

func TestOwnerOpsWhitelist(t *testing.T) {
	e := newEnv(t)
	tok := e.login(protocol.ScopeOwner)
	for _, op := range []string{"tasks.create", "tasks.update_intent", "decisions.open", "decisions.resolve_from_message",
		"conversation.submit_message", "runs.release", "config.sync"} {
		env := e.envelope(op, protocol.Target{Type: "task"}, nil, map[string]any{})
		if st, res := e.op(tok, env); st != http.StatusBadRequest || res.Error == nil || res.Error.Code != protocol.CodeInvalidRequest {
			t.Errorf("%s = %d %+v, want invalid_request", op, st, res)
		}
	}
}

func TestFault_AgentScopeOwnerOp(t *testing.T) {
	e := newEnv(t)
	e.configure()
	taskID := e.task("fix it")
	tok := e.login(protocol.ScopeAgent)
	rev := int64(1)
	st, res := e.op(tok, e.envelope("tasks.cancel", protocol.Target{Type: "task", ID: taskID}, &rev, map[string]any{}))
	if st != http.StatusForbidden || res.Error == nil || res.Error.Code != protocol.CodeForbidden {
		t.Errorf("cancel with an agent token = %d %+v, want forbidden", st, res)
	}
	for _, path := range []string{"/api/v1/snapshot", "/api/v1/tasks/" + taskID, "/api/v1/tasks?finished=1"} {
		if st := e.do("GET", path, tok, nil, nil); st != http.StatusForbidden {
			t.Errorf("GET %s with an agent token = %d, want 403", path, st)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM tasks WHERE lifecycle = 'cancelled'`); n != 0 {
		t.Errorf("cancelled tasks = %d, want 0", n)
	}
}

func TestFault_ConcurrentDecisionAnswers(t *testing.T) {
	e := newEnv(t)
	e.configure()
	decisionID := e.decision(e.task("fix it"))
	target := protocol.Target{Type: "decision", ID: decisionID}
	clients := []struct {
		tok    string
		option string
	}{{e.loginAs("cli-0123456789", protocol.ScopeOwner), "a"}, {e.loginAs("laptop-0123456789", protocol.ScopeOwner), "b"}}
	results := make([]protocol.APIResponse, len(clients))
	var wg sync.WaitGroup
	for i, c := range clients {
		wg.Go(func() {
			_, results[i] = e.op(c.tok, e.envelope("decisions.respond", target, nil, protocol.AnswerDecisionPayload{Version: 1, Option: c.option}))
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
	// The winner sends the same answer again under a new request.
	w := clients[winner]
	_, res := e.op(w.tok, e.envelope("decisions.respond", target, nil, protocol.AnswerDecisionPayload{Version: 1, Option: w.option}))
	if res.Operation == nil || res.Operation.Status != protocol.OpSucceeded || string(res.Operation.Result) != string(results[winner].Operation.Result) {
		t.Errorf("repeated answer = %+v, want the recorded result", res)
	}
}

func TestFault_StaleControlFromCLI(t *testing.T) {
	e := newEnv(t)
	tok := e.login(protocol.ScopeOwner)
	e.configure()
	target := protocol.Target{Type: "task", ID: e.task("fix it")}
	rev := int64(1)
	if _, res := e.op(tok, e.envelope("control.set_mode", target, &rev, protocol.SetControlModePayload{Mode: protocol.ControlPaused})); res.Operation == nil || res.Operation.Status != protocol.OpSucceeded {
		t.Fatalf("pause = %+v", res)
	}
	// Another client, still showing revision 1, resumes.
	_, res := e.op(tok, e.envelope("control.set_mode", target, &rev, protocol.SetControlModePayload{Mode: protocol.ControlManaged}))
	if res.Operation == nil || res.Operation.Status != protocol.OpFailed || res.Operation.Error.Code != protocol.CodeStaleRevision {
		t.Fatalf("stale resume = %+v, want stale_revision", res)
	}
	if res.Current == nil || res.Current.ControlMode != protocol.ControlPaused || res.Current.ControlRevision != 2 {
		t.Errorf("current = %+v, want paused at revision 2", res.Current)
	}
}

func TestSnapshotAndTask(t *testing.T) {
	e := newEnv(t)
	tok := e.login(protocol.ScopeOwner)
	e.configure()
	open := e.task("open")
	done := e.task("done")
	rev := int64(1)
	e.exec("tasks.cancel", protocol.Target{Type: "task", ID: done}, &rev, map[string]any{}, app.CancelTask{})
	decisionID := e.decision(open)

	var snap protocol.Snapshot
	if st := e.do("GET", "/api/v1/snapshot", tok, nil, &snap); st != http.StatusOK {
		t.Fatalf("snapshot = %d", st)
	}
	if want := int64(e.count(`SELECT MAX(event_seq) FROM events`)); snap.EventSeq != want {
		t.Errorf("event_seq = %d, want %d", snap.EventSeq, want)
	}
	if len(snap.Tasks) != 1 || snap.Tasks[0].TaskID != open {
		t.Errorf("tasks = %+v, want only %s", snap.Tasks, open)
	}
	if len(snap.Decisions) != 1 || snap.Decisions[0].DecisionID != decisionID || snap.Decisions[0].TaskID != open {
		t.Errorf("decisions = %+v, want %s about %s", snap.Decisions, decisionID, open)
	}
	if len(snap.Hosts) != 1 || snap.Hosts[0].HostID != "host-a" || snap.Hosts[0].Slots != 2 || snap.Hosts[0].OpenRuns != 0 {
		t.Errorf("hosts = %+v", snap.Hosts)
	}

	var d protocol.TaskDetail
	if st := e.do("GET", "/api/v1/tasks/"+open, tok, nil, &d); st != http.StatusOK || d.TaskID != open || len(d.Decisions) != 1 || d.WorkspaceState != nil {
		t.Errorf("task = %d %+v", st, d)
	}
	// Once a runner has laid out the workspace, the detail shows how.
	if err := e.store.Write(context.Background(), func(tx *store.Tx) error {
		now := e.clock.Now().UnixMilli()
		if err := tx.InsertWorkspace(context.Background(), store.WorkspaceRow{
			WorkspaceID: "ws_1", TaskID: open, Name: d.Workspace, HostID: "host-a", Status: store.WorkspaceReady,
		}, now); err != nil {
			return err
		}
		return tx.InsertMountFact(context.Background(), "ws_1", store.MountFact{
			Path: "tend", Repo: "tend", BaseJSON: `{"branch":"main"}`, ResolvedBaseSHA: "abc123", ResolvedFrom: "origin/main", Branch: "tend/" + d.Workspace,
		}, now)
	}); err != nil {
		t.Fatal(err)
	}
	d = protocol.TaskDetail{}
	e.do("GET", "/api/v1/tasks/"+open, tok, nil, &d)
	if ws := d.WorkspaceState; ws == nil || ws.Host != "host-a" || len(ws.Mounts) != 1 || ws.Mounts[0].ResolvedBaseSHA != "abc123" {
		t.Errorf("workspace state = %+v", ws)
	}
	var res protocol.APIResponse
	if st := e.do("GET", "/api/v1/tasks/task_missing", tok, nil, &res); st != http.StatusNotFound || res.Error == nil || res.Error.Code != protocol.CodeNotFound {
		t.Errorf("unknown task = %d %+v", st, res)
	}
	var page protocol.TaskPage
	if st := e.do("GET", "/api/v1/tasks", tok, nil, &page); st != http.StatusOK || len(page.Tasks) != 1 || page.Tasks[0].TaskID != open || page.Next != "" {
		t.Errorf("unfinished tasks = %d %+v", st, page)
	}
}

func TestFinishedTaskPages(t *testing.T) {
	e := newEnv(t)
	tok := e.login(protocol.ScopeOwner)
	e.configure()
	e.task("still open")
	want := map[string]bool{}
	for i := range taskPageSize + 3 {
		id := e.task(fmt.Sprintf("task %d", i))
		rev := int64(1)
		e.exec("tasks.cancel", protocol.Target{Type: "task", ID: id}, &rev, map[string]any{}, app.CancelTask{})
		want[id] = true
		// Several tasks finish in the same millisecond.
		if i%2 == 0 {
			e.clock.Advance(time.Millisecond)
		}
	}
	seen := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 2 {
			t.Fatal("too many pages")
		}
		var page protocol.TaskPage
		if st := e.do("GET", "/api/v1/tasks?finished=1&before="+cursor, tok, nil, &page); st != http.StatusOK {
			t.Fatalf("page %d = %d", pages, st)
		}
		for _, s := range page.Tasks {
			if !want[s.TaskID] || seen[s.TaskID] {
				t.Errorf("page %d lists %s again or unexpectedly", pages, s.TaskID)
			}
			seen[s.TaskID] = true
		}
		if page.Next == "" {
			break
		}
		cursor = page.Next
	}
	if len(seen) != len(want) {
		t.Errorf("listed %d finished tasks, want %d", len(seen), len(want))
	}
	if st := e.do("GET", "/api/v1/tasks?finished=1&before=nonsense", tok, nil, nil); st != http.StatusBadRequest {
		t.Errorf("bad cursor = %d, want 400", st)
	}
}

// startRun records a run of the task's pending implement action.
func (e *env) startRun(taskID string) string {
	e.t.Helper()
	runID := e.ids.New(protocol.PrefixRun)
	err := e.store.Write(context.Background(), func(tx *store.Tx) error {
		actions, err := store.ListActions(context.Background(), tx.Q(), taskID)
		if err != nil {
			return err
		}
		a := actions[len(actions)-1]
		return tx.InsertRun(context.Background(), store.RunRow{
			RunID: runID, TaskID: taskID, ActionID: a.ActionID, Generation: a.Generation, Role: protocol.RoleImplement,
			HostID: "host-a", DesiredState: protocol.DesiredRun, ObservedState: protocol.RunStarting,
		})
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return runID
}

func (e *env) runLogs(tok, runID string, after int64, wait int) (int, protocol.RunLogPage) {
	e.t.Helper()
	var page protocol.RunLogPage
	st := e.do("GET", fmt.Sprintf("/api/v1/runs/%s/logs?after=%d&wait=%d", runID, after, wait), tok, nil, &page)
	return st, page
}

func TestRunLogs(t *testing.T) {
	e := newEnv(t)
	tok := e.login(protocol.ScopeOwner)
	e.configure()
	runID := e.startRun(e.task("fix it"))
	p := protocol.Principal{Kind: protocol.PrincipalRun, Subject: runID, Role: protocol.RoleImplement}
	record := func(seqs ...int64) {
		t.Helper()
		var events []protocol.RunEvent
		for _, s := range seqs {
			events = append(events, protocol.RunEvent{Seq: s, Kind: "item.completed", Body: json.RawMessage(fmt.Sprintf(`{"n":%d}`, s))})
		}
		if _, err := e.app.RecordRunEvents(context.Background(), p, runID, events); err != nil {
			t.Fatal(err)
		}
	}

	if st, page := e.runLogs(tok, runID, 0, 0); st != http.StatusOK || len(page.Logs) != 0 || page.Next != 0 || page.Observed != protocol.RunStarting {
		t.Errorf("empty logs = %d %+v", st, page)
	}
	record(1, 2)
	st, page := e.runLogs(tok, runID, 0, 0)
	if st != http.StatusOK || len(page.Logs) != 2 || page.Logs[1].Seq != 2 || string(page.Logs[1].Body) != `{"n":2}` || page.Next != 2 {
		t.Fatalf("logs = %d %+v", st, page)
	}

	// A waiting read returns as soon as a log arrives.
	type result struct {
		st   int
		page protocol.RunLogPage
	}
	done := make(chan result, 1)
	go func() {
		st, page := e.runLogs(tok, runID, 2, 20)
		done <- result{st, page}
	}()
	time.Sleep(50 * time.Millisecond)
	record(3)
	select {
	case r := <-done:
		if r.st != http.StatusOK || len(r.page.Logs) != 1 || r.page.Logs[0].Seq != 3 || r.page.Next != 3 {
			t.Errorf("waited logs = %d %+v", r.st, r.page)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("waiting read did not return after a log arrived")
	}

	setObserved := func(o protocol.RunObserved) {
		t.Helper()
		if err := e.store.Write(context.Background(), func(tx *store.Tx) error {
			return tx.UpdateRunObserved(context.Background(), runID, o, e.clock.Now().UnixMilli())
		}); err != nil {
			t.Fatal(err)
		}
	}
	// An interrupted run may resume, so the read still waits.
	setObserved(protocol.RunInterrupted)
	start := time.Now()
	if st, page := e.runLogs(tok, runID, 3, 1); st != http.StatusOK || len(page.Logs) != 0 || page.Observed != protocol.RunInterrupted {
		t.Errorf("logs of an interrupted run = %d %+v", st, page)
	}
	if time.Since(start) < 900*time.Millisecond {
		t.Error("read of an interrupted run did not wait")
	}

	// A waiting read returns when the run ends, with its new state.
	go func() {
		st, page := e.runLogs(tok, runID, 3, 20)
		done <- result{st, page}
	}()
	time.Sleep(50 * time.Millisecond)
	setObserved(protocol.RunFinished)
	e.app.Changes().Notify()
	select {
	case r := <-done:
		if r.st != http.StatusOK || len(r.page.Logs) != 0 || r.page.Next != 3 || r.page.Observed != protocol.RunFinished {
			t.Errorf("logs when the run ended = %d %+v", r.st, r.page)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("waiting read did not return when the run ended")
	}
	// Nothing more comes from an ended run, so the read does not wait.
	start = time.Now()
	if st, page := e.runLogs(tok, runID, 3, 20); st != http.StatusOK || len(page.Logs) != 0 || page.Observed != protocol.RunFinished {
		t.Errorf("logs of an ended run = %d %+v", st, page)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("read of an ended run waited")
	}
	if st, _ := e.runLogs(tok, "run_missing", 0, 0); st != http.StatusNotFound {
		t.Errorf("unknown run = %d, want 404", st)
	}
	if st := e.do("GET", "/api/v1/runs/"+runID+"/logs?after=-1", tok, nil, nil); st != http.StatusBadRequest {
		t.Errorf("negative after = %d, want 400", st)
	}
}

func TestFault_HostedMainThreadDisabled(t *testing.T) {
	e := newEnv(t)
	tok := e.login(protocol.ScopeOwner)
	say := func() (int, protocol.APIResponse) {
		return e.op(tok, e.envelope("conversation.submit_message", protocol.Target{Type: "conversation"}, nil, protocol.SubmitMessagePayload{Body: "hello"}))
	}
	st, res := say()
	if st != http.StatusBadRequest || res.Error == nil || res.Error.Code != protocol.CodeInvalidRequest || !strings.Contains(res.Error.Message, "hosted main agent") {
		t.Errorf("message without the hosted main agent = %d %+v", st, res.Error)
	}
	for _, table := range []string{"request_records", "conversation_messages", "outbox"} {
		if n := e.count(`SELECT COUNT(*) FROM ` + table); n != 0 {
			t.Errorf("%s has %d rows after a refused message", table, n)
		}
	}

	e.server.cfg.HostedMainAgent = true
	if st, res := say(); st != http.StatusOK || res.Operation == nil || res.Operation.Status != protocol.OpAccepted {
		t.Fatalf("message with the hosted main agent = %d %+v", st, res)
	}
	if n := e.count(`SELECT COUNT(*) FROM outbox WHERE destination = ?`, app.MainThreadDestination); n != 1 {
		t.Errorf("%d messages queued for the main thread, want 1", n)
	}
}
