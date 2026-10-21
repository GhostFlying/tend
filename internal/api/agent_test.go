package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// session is one agent session the owner works in.
type session struct {
	e     *env
	tok   string
	agent protocol.AgentKind
	id    string
	// remotes are those of the repository the session works in.
	remotes []string
}

// session returns an agent session that has used tend, so its hooks get
// context (D54).
func (e *env) session(agent protocol.AgentKind, id string) *session {
	e.t.Helper()
	s := e.unboundSession(agent, id)
	s.call("repos_list", map[string]any{}, "")
	return s
}

// unboundSession returns an agent session that has not called a tend tool.
func (e *env) unboundSession(agent protocol.AgentKind, id string) *session {
	e.t.Helper()
	return &session{e: e, tok: e.login(protocol.ScopeAgent), agent: agent, id: id}
}

// hook sends a hook and returns the context, failing on an error.
func (s *session) hook(event protocol.HookEvent, source string) string {
	s.e.t.Helper()
	var res protocol.HookResponse
	req := protocol.HookRequest{RequestID: s.e.ids.New(protocol.PrefixRequest), Agent: s.agent, Event: event, SessionID: s.id, Source: source, Prompt: "hello", Remotes: s.remotes}
	if st := s.e.do("POST", "/api/v1/agent/hook", s.tok, req, &res); st != http.StatusOK {
		s.e.t.Fatalf("%s hook of %s = %d", event, s.id, st)
	}
	return res.Context
}

func (s *session) start(source string) string {
	s.e.t.Helper()
	return s.hook(protocol.HookSessionStart, source)
}
func (s *session) prompt() string { s.e.t.Helper(); return s.hook(protocol.HookUserPromptSubmit, "") }

// toolAnswer is a tool's answer as the model reads it.
type toolAnswer struct {
	OK      bool            `json:"ok"`
	Result  json.RawMessage `json:"result"`
	Error   *protocol.Error `json:"error"`
	Current json.RawMessage `json:"current"`
}

// call runs a tool as the session under requestID, a fresh one if empty.
func (s *session) call(tool string, args any, requestID string) toolAnswer {
	s.e.t.Helper()
	if requestID == "" {
		requestID = s.e.ids.New(protocol.PrefixRequest)
	}
	raw, _ := json.Marshal(args)
	var res protocol.ToolResponse
	req := protocol.ToolRequest{RequestID: requestID, Agent: s.agent, SessionID: s.id, Arguments: raw, Remotes: s.remotes}
	if st := s.e.do("POST", "/api/v1/agent/tools/"+tool, s.tok, req, &res); st != http.StatusOK {
		s.e.t.Fatalf("%s = %d", tool, st)
	}
	var a toolAnswer
	if err := json.Unmarshal([]byte(res.Content), &a); err != nil || a.OK != res.Success {
		s.e.t.Fatalf("%s answered %+v: %v", tool, res, err)
	}
	return a
}

func createArgs(title, workspace string) map[string]any {
	return map[string]any{"title": title, "workspace_name": workspace, "reason": "the owner asked for it", "spec": domain.Spec{
		Goal: title, Acceptance: []string{"works"}, Delivery: domain.DeliveryCommit, Workspace: domain.Workspace{Mounts: []domain.Mount{{Repo: "tend"}}},
	}}
}

// created returns the task ID a successful tasks_create made.
func created(t *testing.T, a toolAnswer) string {
	t.Helper()
	var res struct {
		Outcome struct {
			TaskID string `json:"task_id"`
		} `json:"outcome"`
	}
	if !a.OK || json.Unmarshal(a.Result, &res) != nil || res.Outcome.TaskID == "" {
		t.Fatalf("tasks_create = %+v", a)
	}
	return res.Outcome.TaskID
}

// failRun makes the task's current implementation run fail, which opens a
// decision.
func (e *env) failRun(taskID string) string {
	e.t.Helper()
	runID := e.startRun(taskID)
	b, _ := json.Marshal(protocol.ReleasePayload{Reason: "the build broke"})
	res, err := e.app.Execute(context.Background(), protocol.Principal{Kind: protocol.PrincipalRun, Subject: runID, Role: protocol.RoleImplement},
		&protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: protocol.OpRunsRelease, Target: protocol.Target{Type: "run", ID: runID}, Payload: b},
		app.Release{})
	if err != nil || res.Status != protocol.OpSucceeded {
		e.t.Fatalf("release: %+v, %v", res, err)
	}
	return runID
}

func TestAgentEndpointsNeedAnAgentToken(t *testing.T) {
	e := newEnv(t)
	owner := e.login(protocol.ScopeOwner)
	agent := e.login(protocol.ScopeAgent)
	hook := protocol.HookRequest{RequestID: e.ids.New(protocol.PrefixRequest), Agent: protocol.AgentClaude, Event: protocol.HookSessionStart, SessionID: "s1"}
	if st := e.do("POST", "/api/v1/agent/hook", owner, hook, nil); st != http.StatusForbidden {
		t.Errorf("hook with an owner token = %d, want 403", st)
	}
	if st := e.do("GET", "/api/v1/agent/tools", owner, nil, nil); st != http.StatusForbidden {
		t.Errorf("tool list with an owner token = %d, want 403", st)
	}
	for name, req := range map[string]protocol.HookRequest{
		"unknown agent": {RequestID: hook.RequestID, Agent: "cursor", Event: protocol.HookSessionStart, SessionID: "s1"},
		"no session":    {RequestID: hook.RequestID, Agent: protocol.AgentClaude, Event: protocol.HookSessionStart},
		"unknown event": {RequestID: hook.RequestID, Agent: protocol.AgentClaude, Event: "stop", SessionID: "s1"},
		"no request ID": {Agent: protocol.AgentClaude, Event: protocol.HookSessionStart, SessionID: "s1"},
	} {
		if st := e.do("POST", "/api/v1/agent/hook", agent, req, nil); st != http.StatusBadRequest {
			t.Errorf("hook with %s = %d, want 400", name, st)
		}
	}
	var specs []protocol.ToolSpec
	if st := e.do("GET", "/api/v1/agent/tools", agent, nil, &specs); st != http.StatusOK || len(specs) == 0 {
		t.Fatalf("tool list = %d %v", st, specs)
	}
	for _, s := range specs {
		if strings.Contains(string(s.InputSchema), "commit_seq") {
			t.Errorf("%s asks for commit_seq", s.Name)
		}
		var schema struct {
			Required []string `json:"required"`
		}
		_ = json.Unmarshal(s.InputSchema, &schema)
		if s.Name == "tasks_create" && !slices.Contains(schema.Required, "workspace_name") {
			t.Errorf("tasks_create does not require workspace_name: %s", s.InputSchema)
		}
	}
}

func TestHookSnapshotAndRetry(t *testing.T) {
	e := newEnv(t)
	e.configure()
	s := e.session(protocol.AgentClaude, "sess-a")
	// An empty ledger still tells the agent that tend exists.
	first := s.start("startup")
	if !strings.Contains(first, "kind=full") || !strings.Contains(first, "There are no unfinished tasks.") || !strings.Contains(first, "About tend") {
		t.Errorf("snapshot of an empty ledger:\n%s", first)
	}
	if n := e.count(`SELECT COUNT(*) FROM agent_sessions WHERE agent = 'claude' AND session_id = 'sess-a' AND client_id = ?`, clientID); n != 1 {
		t.Errorf("agent sessions = %d, want 1", n)
	}
	// The hook command resends after a lost response: the same answer, and
	// the watermark does not move twice.
	e.task("fix windows")
	req := protocol.HookRequest{RequestID: e.ids.New(protocol.PrefixRequest), Agent: s.agent, Event: protocol.HookUserPromptSubmit, SessionID: s.id}
	var a, b protocol.HookResponse
	e.do("POST", "/api/v1/agent/hook", s.tok, req, &a)
	e.do("POST", "/api/v1/agent/hook", s.tok, req, &b)
	if !strings.Contains(a.Context, "task.created") || a.Context != b.Context {
		t.Errorf("first answer:\n%s\nresent:\n%s", a.Context, b.Context)
	}
	if got := s.prompt(); got != "" {
		t.Errorf("next prompt with nothing new:\n%s", got)
	}
}

// TestFault_UnboundSessionIgnored: an agent's other sessions, such as a
// background session that consolidates memories, run the same hooks. tend
// gives them nothing and records nothing until the session calls a tend
// tool (D54).
func TestFault_UnboundSessionIgnored(t *testing.T) {
	e := newEnv(t)
	e.configure()
	e.task("fix windows")
	s := e.unboundSession(protocol.AgentCodex, "memories")
	if got := s.start("startup") + s.prompt(); got != "" {
		t.Errorf("an unbound session got context:\n%s", got)
	}
	for _, q := range []string{`SELECT COUNT(*) FROM agent_sessions`, `SELECT COUNT(*) FROM thread_baselines`,
		`SELECT COUNT(*) FROM context_packets`, `SELECT COUNT(*) FROM request_records WHERE operation = 'agent.hook'`} {
		if n := e.count(q); n != 0 {
			t.Errorf("%s = %d for an unbound session", q, n)
		}
	}
	// Its first tool call binds it; the next hook has no baseline yet and
	// gets the whole ledger.
	s.call("tasks_list", map[string]any{}, "")
	if got := s.prompt(); !strings.Contains(got, "kind=full") || !strings.Contains(got, "fix windows") {
		t.Errorf("first hook after binding:\n%s", got)
	}
}

// TestFault_FirstUseKeepsResults: a new session uses tend, creates a task
// and the task ends before the session's next prompt. That prompt gets a
// full snapshot, which lists only unfinished tasks; the result is told
// with it rather than lost.
func TestFault_FirstUseKeepsResults(t *testing.T) {
	e := newEnv(t)
	e.configure()
	owner := e.login(protocol.ScopeOwner)
	s := e.unboundSession(protocol.AgentClaude, "sess-a")
	s.start("startup")
	s.call("tend_context", map[string]any{}, "")
	id := created(t, s.call("tasks_create", createArgs("fix windows", "fix-windows"), ""))
	decision := e.decision(e.task("fix linux"))
	e.op(owner, e.envelope("decisions.respond", protocol.Target{Type: "decision", ID: decision}, nil, protocol.AnswerDecisionPayload{Version: 1, Option: "a"}))
	rev := int64(1)
	e.op(owner, e.envelope("tasks.cancel", protocol.Target{Type: "task", ID: id}, &rev, map[string]any{}))
	got := s.prompt()
	unseen := sectionOf(got, "Since your last tend context")
	if !strings.Contains(got, "kind=full") || !strings.Contains(unseen, id+` "fix windows" task.cancelled`) || !strings.Contains(unseen, "answered by owner: "+decision) {
		t.Errorf("first prompt after the task ended and the decision was answered:\n%s", got)
	}
	// Questions opened meanwhile are all told, whatever the budget.
	if attention := sectionOf(got, "Since your last tend context, needs attention"); !strings.Contains(attention, "opened: "+decision) {
		t.Errorf("the new decision is not among what needs attention:\n%s", got)
	}
	// Told once: a later full snapshot does not repeat them.
	if got := s.start("compact"); strings.Contains(got, "task.cancelled") || strings.Contains(got, "answered by") {
		t.Errorf("told again:\n%s", got)
	}
}

func TestFault_ToolRetrySameRequest(t *testing.T) {
	e := newEnv(t)
	e.configure()
	s := e.session(protocol.AgentCodex, "sess-a")
	requestID := e.ids.New(protocol.PrefixRequest)
	first := s.call("tasks_create", createArgs("fix windows", "fix-windows"), requestID)
	again := s.call("tasks_create", createArgs("fix windows", "fix-windows"), requestID)
	if created(t, first) != created(t, again) || string(first.Result) != string(again.Result) {
		t.Errorf("resent call = %s, want the original %s", again.Result, first.Result)
	}
	if n := e.count(`SELECT COUNT(*) FROM tasks`); n != 1 {
		t.Errorf("tasks = %d, want 1", n)
	}
}

func TestFault_ToolRecallSameWorkspace(t *testing.T) {
	e := newEnv(t)
	e.configure()
	s := e.session(protocol.AgentCodex, "sess-a")
	id := created(t, s.call("tasks_create", createArgs("fix windows", "fix-windows"), ""))
	// The model calls again, under a new request.
	a := s.call("tasks_create", createArgs("fix windows", "fix-windows"), "")
	var current protocol.TaskSummary
	if a.OK || a.Error == nil || a.Error.Code != protocol.CodeInvalidRequest || json.Unmarshal(a.Current, &current) != nil || current.TaskID != id {
		t.Errorf("repeated creation = %+v, want the existing task %s", a, id)
	}
	if n := e.count(`SELECT COUNT(*) FROM tasks`); n != 1 {
		t.Errorf("tasks = %d, want 1", n)
	}
	// Without a workspace name there would be nothing to catch it.
	args := createArgs("fix linux", "")
	delete(args, "workspace_name")
	if a := s.call("tasks_create", args, ""); a.OK || a.Error.Code != protocol.CodeInvalidRequest {
		t.Errorf("creation without a workspace name = %+v", a)
	}
}

func TestFault_ToolRecallStaleAction(t *testing.T) {
	e := newEnv(t)
	e.configure()
	s := e.session(protocol.AgentTraex, "sess-a")
	id := created(t, s.call("tasks_create", createArgs("fix windows", "fix-windows"), ""))
	e.failRun(id)
	var task protocol.TaskSummary
	e.do("GET", "/api/v1/tasks/"+id, e.login(protocol.ScopeOwner), nil, &task)
	retry := map[string]any{"task_id": id, "expected_control_revision": task.ControlRevision, "kind": "implement", "generation": task.NextImplementGeneration}
	first := s.call("actions_create", retry, "")
	if !first.OK {
		t.Fatalf("retry = %+v", first)
	}
	// The model calls again with what it read before: the same generation
	// and the old revision. Nothing new is dispatched.
	if again := s.call("actions_create", retry, ""); again.OK || again.Error.Code != protocol.CodeDuplicateEffect {
		t.Errorf("repeated retry = %+v, want duplicate_effect", again)
	}
	if n := e.count(`SELECT COUNT(*) FROM plan_actions WHERE task_id = ? AND kind = 'implement' AND generation = ?`, id, task.NextImplementGeneration); n != 1 {
		t.Errorf("implement actions of generation %d = %d, want 1", task.NextImplementGeneration, n)
	}
	// A further generation needs the current revision.
	retry["generation"] = task.NextImplementGeneration + 1
	if a := s.call("actions_create", retry, ""); a.OK || a.Error.Code != protocol.CodeStaleRevision || a.Current == nil {
		t.Errorf("next generation at the old revision = %+v, want stale_revision with the current state", a)
	}
}

func TestFault_ToolWithoutSession(t *testing.T) {
	e := newEnv(t)
	e.configure()
	s := e.session(protocol.AgentCodex, "")
	requestID := e.ids.New(protocol.PrefixRequest)
	created(t, s.call("tasks_create", createArgs("fix windows", "fix-windows"), requestID))
	var principal, reason string
	if err := e.store.Read(context.Background(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(context.Background(), `SELECT principal_json, reason FROM request_records WHERE request_id = ?`, requestID).Scan(&principal, &reason)
	}); err != nil {
		t.Fatal(err)
	}
	var p protocol.Principal
	_ = json.Unmarshal([]byte(principal), &p)
	if p.Kind != protocol.PrincipalMainAgent || p.Subject != clientID || p.Agent != protocol.AgentCodex || p.Thread != "" || reason != "the owner asked for it" {
		t.Errorf("recorded caller %s with reason %q", principal, reason)
	}
	// A hook needs its session.
	req := protocol.HookRequest{RequestID: e.ids.New(protocol.PrefixRequest), Agent: s.agent, Event: protocol.HookUserPromptSubmit}
	if st := e.do("POST", "/api/v1/agent/hook", s.tok, req, nil); st != http.StatusBadRequest {
		t.Errorf("hook without a session = %d, want 400", st)
	}
}

func TestFault_OwnWritesFiltered(t *testing.T) {
	e := newEnv(t)
	e.configure()
	a := e.session(protocol.AgentClaude, "sess-a")
	b := e.session(protocol.AgentClaude, "sess-b")
	a.start("startup")
	b.start("startup")
	id := created(t, a.call("tasks_create", createArgs("fix windows", "fix-windows"), ""))
	if got := a.prompt(); got != "" {
		t.Errorf("A hears of its own task:\n%s", got)
	}
	if got := b.prompt(); !strings.Contains(got, id+` "fix windows" task.created`) {
		t.Errorf("B does not hear of A's task:\n%s", got)
	}
	// What follows from A's task comes from tend, not from A.
	e.failRun(id)
	for _, s := range []*session{a, b} {
		got := s.prompt()
		if attention := sectionOf(got, "Needs attention"); !strings.Contains(attention, "run.failed") || !strings.Contains(attention, "The implementation run failed") {
			t.Errorf("%s does not hear of the failure and its decision:\n%s", s.id, got)
		}
	}
	// A call without a session cannot be told apart: both hear of it.
	nameless := &session{e: e, tok: a.tok, agent: a.agent}
	other := created(t, nameless.call("tasks_create", createArgs("fix linux", "fix-linux"), ""))
	for _, s := range []*session{a, b} {
		if got := s.prompt(); !strings.Contains(got, other+` "fix linux" task.created`) {
			t.Errorf("%s does not hear of the sessionless task:\n%s", s.id, got)
		}
	}
	// The full snapshot shows the ledger as it is, own writes included.
	if got := a.start("resume"); !strings.Contains(got, id) || !strings.Contains(got, other) {
		t.Errorf("A's snapshot:\n%s", got)
	}
}

func TestFault_ProgressNotInjected(t *testing.T) {
	e := newEnv(t)
	e.configure()
	s := e.session(protocol.AgentCodex, "sess-a")
	id := e.task("fix windows")
	s.start("startup")
	// A run starts and reports progress and logs.
	runID := e.startRun(id)
	run := protocol.Principal{Kind: protocol.PrincipalRun, Subject: runID, Role: protocol.RoleImplement}
	e.exec2(run, protocol.OpRunsReportStarted, protocol.Target{Type: "run", ID: runID}, nil, app.ReportRunStarted{})
	if _, err := e.app.RecordRunEvents(context.Background(), run, runID, []protocol.RunEvent{
		{Seq: 1, Kind: protocol.EventRunRunning}, {Seq: 2, Kind: "item.completed"}, {Seq: 3, Kind: protocol.EventRunWaiting},
	}); err != nil {
		t.Fatal(err)
	}
	if got := s.prompt(); got != "" {
		t.Errorf("progress was injected:\n%s", got)
	}
	// Many questions at once go beyond the budget but are all told.
	var ids []string
	for range 40 {
		ids = append(ids, e.decision(id))
	}
	e.exec("decisions.open", protocol.Target{Type: "decision"}, nil, map[string]any{"task_id": id, "question": strings.Repeat("Which way? ", 30), "options": []string{"a", "b"}}, app.OpenDecision{})
	got := s.prompt()
	if len(got) <= 4000 {
		t.Fatalf("the questions fit the budget after all (%d characters)", len(got))
	}
	for _, d := range ids {
		if !strings.Contains(got, d) {
			t.Errorf("decision %s was left out", d)
		}
	}
}

func TestFault_CompactRebuildsBaseline(t *testing.T) {
	e := newEnv(t)
	e.configure()
	owner := e.login(protocol.ScopeOwner)
	s := e.session(protocol.AgentTraex, "sess-a")
	id := e.task("fix windows")
	s.start("startup")
	rev := int64(1)
	e.op(owner, e.envelope("control.set_mode", protocol.Target{Type: "task", ID: id}, &rev, protocol.SetControlModePayload{Mode: protocol.ControlPaused}))
	if got := s.prompt(); !strings.Contains(got, "task.control_changed") {
		t.Fatalf("pause not told:\n%s", got)
	}
	compacted := s.start("compact")
	if !strings.Contains(compacted, "kind=full") || !strings.Contains(compacted, "This replaces all earlier tend context") ||
		!strings.Contains(compacted, "epoch=2") || !strings.Contains(compacted, "control paused") {
		t.Errorf("snapshot after compaction:\n%s", compacted)
	}
	rev = 2
	e.op(owner, e.envelope("control.set_mode", protocol.Target{Type: "task", ID: id}, &rev, protocol.SetControlModePayload{Mode: protocol.ControlManaged}))
	got := s.prompt()
	if strings.Count(got, "task.control_changed") != 1 || !strings.Contains(got, "epoch=2") {
		t.Errorf("delta after compaction holds more than the resume:\n%s", got)
	}
}

func TestFault_DomainDecisionVisible(t *testing.T) {
	e := newEnv(t)
	e.configure()
	s := e.session(protocol.AgentClaude, "sess-a")
	id := e.task("fix windows")
	s.start("startup")
	e.failRun(id)
	var decisionID string
	if err := e.store.Read(context.Background(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(context.Background(), `SELECT decision_id FROM decisions WHERE task_id = ?`, id).Scan(&decisionID)
	}); err != nil {
		t.Fatal(err)
	}
	if got := sectionOf(s.prompt(), "Needs attention"); !strings.Contains(got, "opened: "+decisionID) {
		t.Errorf("delta does not show the decision %s:\n%s", decisionID, got)
	}
	if got := sectionOf(s.start("clear"), "Decisions"); !strings.Contains(got, decisionID) {
		t.Errorf("snapshot does not show the decision %s:\n%s", decisionID, got)
	}
	tc := s.call("tend_context", map[string]any{}, "")
	var text string
	if !tc.OK || json.Unmarshal(tc.Result, &text) != nil || !strings.Contains(text, decisionID) || !strings.Contains(text, "[tend context snapshot") {
		t.Errorf("tend_context = %+v", tc)
	}
}

func sectionOf(text, title string) string {
	_, rest, ok := strings.Cut(text, "## "+title+"\n")
	if !ok {
		return ""
	}
	body, _, _ := strings.Cut(rest, "\n## ")
	return body
}

// exec2 runs a command as p, for setting up the ledger.
func (e *env) exec2(p protocol.Principal, op string, target protocol.Target, payload any, cmd app.Command) {
	e.t.Helper()
	var b json.RawMessage
	if payload != nil {
		b, _ = json.Marshal(payload)
	}
	res, err := e.app.Execute(context.Background(), p, &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: op, Target: target, Payload: b}, cmd)
	if err != nil || res.Status == protocol.OpFailed {
		e.t.Fatalf("%s: %+v, %v", op, res, err)
	}
}
