package app

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

type harness struct {
	t     *testing.T
	app   *App
	store *store.Store
	clock *testkit.Clock
	ids   protocol.IDGen
	// seeded holds the row counts left by the configuration sync, which
	// count excludes.
	seeded map[string]int
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	clock := testkit.NewClock()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "tend.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ids := testkit.NewIDGen(clock, 42)
	h := &harness{t: t, app: New(s, ids, clock.Now), store: s, clock: clock, ids: ids}
	h.syncConfig(testConfig())
	h.seeded = map[string]int{}
	for _, table := range []string{"request_records", "events", "inbox", "outbox", "tasks", "plan_actions", "runs"} {
		h.seeded[table] = h.count(table)
	}
	return h
}

func testConfig() ConfigPayload {
	return ConfigPayload{
		Repos: []RepoConfig{
			{Repo: "tend", RemoteURL: "https://example.com/tend.git"},
			{Repo: "douyin", RemoteURL: "https://example.com/douyin.git", BaseCandidates: []string{"develop"}},
			{Repo: "template-assembler", RemoteURL: "https://example.com/ta.git"},
		},
		Hosts: []HostConfig{{
			HostID: "linux-1", OS: "linux", Arch: "amd64", Repos: []string{"tend", "douyin", "template-assembler"},
			Slots: 2, TokenHash: strings.Repeat("ab", 32), Nodes: []string{"tag:tend-runner"},
		}},
		Templates: []TemplateConfig{{Name: "douyin-with-lynx", Workspace: domain.Workspace{
			Mounts:  []domain.Mount{{Repo: "douyin"}, {Repo: "template-assembler", Path: "lynx"}},
			Profile: "link-lynx",
		}}},
	}
}

func (h *harness) syncConfig(c ConfigPayload) protocol.Operation {
	h.t.Helper()
	return h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, h.env("config.sync", protocol.Target{Type: "config"}, c, nil), SyncConfig{})
}

var (
	owner     = protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner@example.com", Client: "cli-laptop"}
	mainAgent = protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "harness-1", Thread: "th-1"}
)

func runPrincipal(runID string, role protocol.RunRole) protocol.Principal {
	return protocol.Principal{Kind: protocol.PrincipalRun, Subject: runID, Role: role}
}

func (h *harness) env(op string, target protocol.Target, payload any, revision *int64) *protocol.Envelope {
	h.t.Helper()
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			h.t.Fatal(err)
		}
		raw = b
	}
	return &protocol.Envelope{RequestID: h.ids.New(protocol.PrefixRequest), Operation: op, Target: target, ExpectedControlRevision: revision, Payload: raw}
}

func (h *harness) exec(p protocol.Principal, env *protocol.Envelope, cmd Command) protocol.Operation {
	h.t.Helper()
	op, err := h.app.Execute(context.Background(), p, env, cmd)
	if err != nil {
		h.t.Fatalf("Execute(%s) error = %v", env.Operation, err)
	}
	return op
}

func rev(n int64) *int64 { return &n }

var testSpec = domain.Spec{
	Goal: "fix the windows build", Acceptance: []string{"build passes"}, Delivery: domain.DeliveryCommit,
	Workspace: domain.Workspace{Mounts: []domain.Mount{{Repo: "tend"}}},
}

func taskTarget(id string) protocol.Target { return protocol.Target{Type: "task", ID: id} }
func runTarget(id string) protocol.Target  { return protocol.Target{Type: "run", ID: id} }

func (h *harness) createTask(p protocol.Principal, payload createTaskPayload) (protocol.Operation, taskResult) {
	h.t.Helper()
	op := h.exec(p, h.env("tasks.create", protocol.Target{Type: "task"}, payload, nil), CreateTask{})
	var res taskResult
	if op.Status == protocol.OpSucceeded {
		if err := json.Unmarshal(op.Result, &res); err != nil {
			h.t.Fatal(err)
		}
	}
	return op, res
}

func (h *harness) task(id string) store.TaskRow {
	h.t.Helper()
	var row store.TaskRow
	if err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		var err error
		row, err = store.GetTask(context.Background(), tx.Q(), id)
		return err
	}); err != nil {
		h.t.Fatal(err)
	}
	return row
}

// count returns the rows of a table, excluding those of the configuration
// sync every harness starts with.
func (h *harness) count(table string) int {
	h.t.Helper()
	var n int
	if err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&n)
	}); err != nil {
		h.t.Fatal(err)
	}
	return n - h.seeded[table]
}

// startRun stands in for the scheduler (B6): it creates a run for the task's
// newest pending action of the given kind.
func (h *harness) startRun(taskID string, kind protocol.ActionKind) string {
	h.t.Helper()
	var runID string
	err := h.store.Write(context.Background(), func(tx *store.Tx) error {
		actions, err := store.ListActions(context.Background(), tx.Q(), taskID)
		if err != nil {
			return err
		}
		for i := len(actions) - 1; i >= 0; i-- {
			a := actions[i]
			if a.Kind != kind || a.Status != protocol.ActionPending {
				continue
			}
			role := protocol.RoleImplement
			if kind == protocol.ActionAccept {
				role = protocol.RoleAccept
			}
			runID = h.ids.New(protocol.PrefixRun)
			return tx.InsertRun(context.Background(), store.RunRow{
				RunID: runID, TaskID: taskID, ActionID: a.ActionID, Generation: a.Generation, Role: role,
				HostID: "host-a", DesiredState: protocol.DesiredRun, ObservedState: protocol.RunStarting,
			})
		}
		return errors.New("no pending action")
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return runID
}

// toAwaitingAcceptance drives a new task to awaiting_acceptance and returns
// the task ID and the acceptance run ID.
func (h *harness) toAwaitingAcceptance() (string, string) {
	h.t.Helper()
	_, res := h.createTask(mainAgent, createTaskPayload{Title: "windows fix", Spec: testSpec})
	impl := h.startRun(res.TaskID, protocol.ActionImplement)
	if op := h.exec(runPrincipal(impl, protocol.RoleImplement), h.env("runs.report_started", runTarget(impl), nil, nil), ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		h.t.Fatalf("report_started: %+v", op)
	}
	art := testArtifact(h.task(res.TaskID).WorkspaceName)
	if op := h.exec(runPrincipal(impl, protocol.RoleImplement), h.env("runs.submit_artifact", runTarget(impl), art, nil), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		h.t.Fatalf("submit_artifact: %+v", op)
	}
	return res.TaskID, h.startRun(res.TaskID, protocol.ActionAccept)
}

const (
	baseSHA   = "1111111111111111111111111111111111111111"
	commitSHA = "2222222222222222222222222222222222222222"
)

// testArtifact is an artifact of testSpec's single mount.
func testArtifact(workspace string) protocol.ArtifactPayload {
	return protocol.ArtifactPayload{Mounts: []protocol.ArtifactMount{{
		Repo: "tend", Path: "tend", BaseSHA: baseSHA, Branch: domain.TaskBranch(workspace), CommitSHA: commitSHA, Changed: true,
	}}}
}

func (h *harness) submitVerdict(runID string, v protocol.Verdict) protocol.Operation {
	h.t.Helper()
	return h.exec(runPrincipal(runID, protocol.RoleAccept),
		h.env("acceptances.submit", runTarget(runID), protocol.VerdictPayload{Verdict: v, Evidence: json.RawMessage(`{"log":"ok"}`)}, nil),
		SubmitAcceptance{})
}

func TestHappyPathToCompleted(t *testing.T) {
	h := newHarness(t)
	taskID, accept := h.toAwaitingAcceptance()
	if got := h.task(taskID).Lifecycle; got != protocol.TaskAwaitingAcceptance {
		t.Fatalf("stage = %s, want awaiting_acceptance", got)
	}
	if op := h.submitVerdict(accept, protocol.VerdictPassed); op.Status != protocol.OpSucceeded {
		t.Fatalf("submit verdict: %+v", op)
	}
	if got := h.task(taskID).Lifecycle; got != protocol.TaskCompleted {
		t.Errorf("stage = %s, want completed", got)
	}
}

func TestFault_RetrySameRequestID(t *testing.T) {
	h := newHarness(t)
	env := h.env("tasks.create", protocol.Target{Type: "task"}, createTaskPayload{Title: "a", Spec: testSpec}, nil)
	first := h.exec(owner, env, CreateTask{})
	again := *env
	again.TraceID = h.ids.New(protocol.PrefixTrace) // A new transport attempt.
	second := h.exec(owner, &again, CreateTask{})
	if string(first.Result) != string(second.Result) || first.Status != second.Status {
		t.Errorf("retry returned a different operation:\n%+v\n%+v", first, second)
	}
	if n := h.count("tasks"); n != 1 {
		t.Errorf("tasks = %d, want 1", n)
	}
	// Another client of the same owner shares the dedup domain.
	phone := owner
	phone.Client = "cli-phone"
	if third := h.exec(phone, env, CreateTask{}); string(third.Result) != string(first.Result) {
		t.Errorf("retry from another client returned %+v", third)
	}
}

func TestFault_ConcurrentSameRequestID(t *testing.T) {
	h := newHarness(t)
	env := h.env("tasks.create", protocol.Target{Type: "task"}, createTaskPayload{Title: "a", Spec: testSpec}, nil)
	const n = 16
	results := make([]protocol.Operation, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			e := *env
			op, err := h.app.Execute(context.Background(), owner, &e, CreateTask{})
			if err != nil {
				t.Errorf("Execute() error = %v", err)
			}
			results[i] = op
		})
	}
	wg.Wait()
	for i := 1; i < n; i++ {
		if string(results[i].Result) != string(results[0].Result) {
			t.Fatalf("concurrent duplicates diverged: %s vs %s", results[i].Result, results[0].Result)
		}
	}
	if got := h.count("tasks"); got != 1 {
		t.Errorf("tasks = %d, want 1", got)
	}
}

func TestFault_SameIDDifferentPayload(t *testing.T) {
	h := newHarness(t)
	env := h.env("tasks.create", protocol.Target{Type: "task"}, createTaskPayload{Title: "a", Spec: testSpec}, nil)
	first := h.exec(owner, env, CreateTask{})
	other := *env
	other.Payload = json.RawMessage(`{"title":"b","spec":{"goal":"x","acceptance":["y"],"delivery":"commit"}}`)
	_, err := h.app.Execute(context.Background(), owner, &other, CreateTask{})
	if protocol.CodeOf(err) != protocol.CodeConflictPayload {
		t.Fatalf("reused ID with a new payload: err = %v, want conflict_payload", err)
	}
	if again := h.exec(owner, env, CreateTask{}); string(again.Result) != string(first.Result) {
		t.Errorf("original request changed after conflict: %+v", again)
	}
	if n := h.count("tasks"); n != 1 {
		t.Errorf("tasks = %d, want 1", n)
	}
}

// outboxCommand is a test command whose outcome sends two messages.
type outboxCommand struct{}

func (outboxCommand) Operation() string { return "test.fan_out" }
func (outboxCommand) Authorize(context.Context, *Exec, store.Querier) error {
	return nil
}
func (outboxCommand) Apply(context.Context, *Exec, *store.Tx) (Outcome, error) {
	return Outcome{Accepted: true, Outbox: []OutboxMessage{
		{Destination: "runner:host-a", Operation: "executions.start", Payload: map[string]any{"n": 1}},
		{Destination: "runner:host-a", Operation: "executions.start", Payload: map[string]any{"n": 2}},
	}}, nil
}

func TestFault_ChildRequestsDistinct(t *testing.T) {
	h := newHarness(t)
	env := h.env("test.fan_out", protocol.Target{Type: "test"}, map[string]any{}, nil)
	if op := h.exec(owner, env, outboxCommand{}); op.Status != protocol.OpAccepted {
		t.Fatalf("status = %s, want accepted", op.Status)
	}
	children := func() []string {
		var ids []string
		err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
			rows, err := tx.Q().QueryContext(context.Background(), `SELECT request_id, parent_request_id FROM outbox ORDER BY request_id`)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id, parent string
				if err := rows.Scan(&id, &parent); err != nil {
					return err
				}
				if parent != env.RequestID || id == env.RequestID || !protocol.ValidID(protocol.PrefixRequest, id) {
					t.Errorf("child %s with parent %s", id, parent)
				}
				ids = append(ids, id)
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
		return ids
	}
	first := children()
	if len(first) != 2 || first[0] == first[1] {
		t.Fatalf("children = %v, want two distinct IDs", first)
	}
	h.exec(owner, env, outboxCommand{})
	if again := children(); len(again) != 2 || again[0] != first[0] || again[1] != first[1] {
		t.Errorf("replaying the parent changed its children: %v -> %v", first, again)
	}
}

func TestFault_NewCallIDSamePhase(t *testing.T) {
	h := newHarness(t)
	taskID, accept := h.toAwaitingAcceptance()
	h.submitVerdict(accept, protocol.VerdictChangesRequired) // Plans generation 2.
	revision := h.task(taskID).ControlRevision
	ask := func() protocol.Operation {
		return h.exec(mainAgent, h.env("actions.create", taskTarget(taskID), createActionPayload{Kind: protocol.ActionImplement, Generation: 3}, rev(revision)), CreateAction{})
	}
	if op := ask(); op.Status != protocol.OpSucceeded {
		t.Fatalf("first request: %+v", op)
	}
	actionsBefore := h.count("plan_actions")
	// The agent recovers and asks for the same generation with a new call.
	op := ask()
	if op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeDuplicateEffect {
		t.Fatalf("second request: %+v, want duplicate_effect", op)
	}
	var details map[string]any
	if err := json.Unmarshal(op.Error.Details, &details); err != nil || details["action_id"] == "" {
		t.Errorf("duplicate details = %s", op.Error.Details)
	}
	if got := h.count("plan_actions"); got != actionsBefore {
		t.Errorf("actions = %d, want %d", got, actionsBefore)
	}
}

func TestFault_DuplicatePlanCommit(t *testing.T) {
	h := newHarness(t)
	payload := createTaskPayload{Title: "a", Spec: testSpec, SourceMessageID: "msg_1", CommitSeq: 1}
	first, _ := h.createTask(mainAgent, payload)
	if first.Status != protocol.OpSucceeded {
		t.Fatalf("first: %+v", first)
	}
	second, _ := h.createTask(mainAgent, payload)
	if second.Status != protocol.OpFailed || second.Error.Code != protocol.CodeDuplicateEffect {
		t.Fatalf("replayed plan commit: %+v, want duplicate_effect", second)
	}
	var details map[string]any
	if err := json.Unmarshal(second.Error.Details, &details); err != nil || details["original_request_id"] != first.RequestID {
		t.Errorf("details = %s, want original_request_id %s", second.Error.Details, first.RequestID)
	}
	if n := h.count("tasks"); n != 1 {
		t.Errorf("tasks = %d, want 1", n)
	}
}

func TestFault_StaleAcceptance(t *testing.T) {
	h := newHarness(t)
	taskID, accept := h.toAwaitingAcceptance()
	newSpec := testSpec
	newSpec.Constraints = []string{"keep the public API"}
	row := h.task(taskID)
	if op := h.exec(owner, h.env("tasks.update_intent", taskTarget(taskID), updateIntentPayload{Spec: &newSpec}, rev(row.ControlRevision)), UpdateIntent{}); op.Status != protocol.OpSucceeded {
		t.Fatalf("update_intent: %+v", op)
	}
	if op := h.submitVerdict(accept, protocol.VerdictPassed); op.Status != protocol.OpSucceeded {
		t.Fatalf("late verdict should be recorded: %+v", op)
	}
	got := h.task(taskID)
	if got.Lifecycle == protocol.TaskCompleted || got.SpecVersion != 2 {
		t.Errorf("late pass for spec 1 changed the task: %+v", got)
	}
	if n := h.count("acceptances"); n != 1 {
		t.Errorf("acceptances = %d, want the late verdict kept as evidence", n)
	}
}

func TestFault_AcceptanceEnvFailure(t *testing.T) {
	h := newHarness(t)
	taskID, accept := h.toAwaitingAcceptance()
	h.submitVerdict(accept, protocol.VerdictBlocked)
	got := h.task(taskID)
	if got.Lifecycle != protocol.TaskAwaitingAcceptance || got.BlockReason != protocol.BlockAcceptanceEnvUnavailable {
		t.Errorf("task = %+v, want awaiting_acceptance blocked on the environment", got)
	}
	if n := h.count("decisions"); n != 1 {
		t.Errorf("decisions = %d, want 1", n)
	}
}

func TestFault_StaleRevisionRejected(t *testing.T) {
	h := newHarness(t)
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	env := h.env("tasks.update_intent", taskTarget(res.TaskID), updateIntentPayload{Priority: rev(5)}, rev(res.ControlRevision-1))
	first := h.exec(owner, env, UpdateIntent{})
	if first.Status != protocol.OpFailed || first.Error.Code != protocol.CodeStaleRevision {
		t.Fatalf("stale write: %+v", first)
	}
	if second := h.exec(owner, env, UpdateIntent{}); second.Status != first.Status || second.Error.Code != first.Error.Code {
		t.Errorf("retry returned %+v, want the recorded failure", second)
	}
	if got := h.task(res.TaskID); got.Priority != 0 || got.ControlRevision != res.ControlRevision {
		t.Errorf("stale write changed the task: %+v", got)
	}
}

func TestFault_ReplayAfterRevoke(t *testing.T) {
	h := newHarness(t)
	h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	host := protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "linux-1"}
	env := h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, map[string]any{}, nil)
	if op := h.exec(host, env, Claim{}); op.Status != protocol.OpSucceeded {
		t.Fatalf("claim: %+v", op)
	}
	// The host is retired; its old request must not reveal the run any more.
	c := testConfig()
	c.Hosts = nil
	h.syncConfig(c)
	if _, err := h.app.Execute(context.Background(), host, env, Claim{}); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Errorf("replay after revoke: err = %v, want forbidden", err)
	}
}

// flakyCommand writes a row and then fails internally on its first run.
type flakyCommand struct{ calls *int }

func (flakyCommand) Operation() string { return "test.flaky" }
func (flakyCommand) Authorize(context.Context, *Exec, store.Querier) error {
	return nil
}
func (c flakyCommand) Apply(ctx context.Context, _ *Exec, tx *store.Tx) (Outcome, error) {
	*c.calls++
	if _, err := tx.Exec(ctx, `INSERT INTO inbox (source, source_event_id, received_at) VALUES ('flaky', ?, 0)`, *c.calls); err != nil {
		return Outcome{}, err
	}
	if *c.calls == 1 {
		return Outcome{}, errors.New("disk hiccup")
	}
	return Outcome{Result: map[string]any{"ok": true}}, nil
}

func TestFault_InternalErrorRetrySafe(t *testing.T) {
	h := newHarness(t)
	calls := 0
	env := h.env("test.flaky", protocol.Target{Type: "test"}, map[string]any{}, nil)
	if _, err := h.app.Execute(context.Background(), owner, env, flakyCommand{&calls}); err == nil {
		t.Fatal("first attempt succeeded, want internal error")
	}
	if n := h.count("inbox"); n != 0 {
		t.Errorf("rows after internal error = %d, want 0", n)
	}
	if n := h.count("request_records"); n != 0 {
		t.Errorf("request records after internal error = %d, want 0", n)
	}
	if op := h.exec(owner, env, flakyCommand{&calls}); op.Status != protocol.OpSucceeded {
		t.Fatalf("retry: %+v", op)
	}
	if n := h.count("inbox"); n != 1 {
		t.Errorf("rows after retry = %d, want 1", n)
	}
}

// idleCommand writes a row and then reports that it had nothing to do.
type idleCommand struct {
	calls  *int
	events bool
}

func (idleCommand) Operation() string { return "test.idle" }
func (idleCommand) Authorize(context.Context, *Exec, store.Querier) error {
	return nil
}
func (c idleCommand) Apply(ctx context.Context, _ *Exec, tx *store.Tx) (Outcome, error) {
	*c.calls++
	if _, err := tx.Exec(ctx, `INSERT INTO inbox (source, source_event_id, received_at) VALUES ('idle', ?, 0)`, *c.calls); err != nil {
		return Outcome{}, err
	}
	out := Outcome{Result: map[string]any{"work": nil}, NoEffect: true}
	if c.events {
		out.Events = []Event{{ObjectType: "test", ObjectID: "x", Kind: "test.idle"}}
	}
	return out, nil
}

func TestNoEffectIsNotRecorded(t *testing.T) {
	h := newHarness(t)
	calls := 0
	env := h.env("test.idle", protocol.Target{Type: "test"}, map[string]any{}, nil)
	for i := range 2 {
		op := h.exec(owner, env, idleCommand{calls: &calls})
		if op.Status != protocol.OpSucceeded || op.RequestID != env.RequestID || string(op.Result) != `{"work":null}` {
			t.Fatalf("attempt %d: %+v", i, op)
		}
	}
	if calls != 2 {
		t.Errorf("Apply calls = %d, want 2: a no-effect request must run again on retry", calls)
	}
	for _, table := range []string{"inbox", "request_records", "events"} {
		if n := h.count(table); n != 0 {
			t.Errorf("%s rows = %d, want 0", table, n)
		}
	}
}

func TestNoEffectWithEventsIsAnError(t *testing.T) {
	h := newHarness(t)
	calls := 0
	env := h.env("test.idle", protocol.Target{Type: "test"}, map[string]any{}, nil)
	if _, err := h.app.Execute(context.Background(), owner, env, idleCommand{calls: &calls, events: true}); err == nil {
		t.Fatal("Execute succeeded, want an error for a no-effect outcome with events")
	}
	if n := h.count("inbox") + h.count("request_records"); n != 0 {
		t.Errorf("rows = %d, want 0", n)
	}
}

func TestAuthorization(t *testing.T) {
	h := newHarness(t)
	taskID, accept := h.toAwaitingAcceptance()
	tests := []struct {
		name string
		p    protocol.Principal
		env  *protocol.Envelope
		cmd  Command
	}{
		{"runner creates task", protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "host-a"},
			h.env("tasks.create", protocol.Target{Type: "task"}, createTaskPayload{Title: "x", Spec: testSpec}, nil), CreateTask{}},
		{"main agent takes human control", mainAgent,
			h.env("control.set_mode", taskTarget(taskID), setControlModePayload{Mode: protocol.ControlHumanControl}, rev(1)), SetControlMode{}},
		{"implement run submits a verdict", runPrincipal(accept, protocol.RoleImplement),
			h.env("acceptances.submit", runTarget(accept), protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, nil), SubmitAcceptance{}},
		{"run judges another run", runPrincipal("run_other", protocol.RoleAccept),
			h.env("acceptances.submit", runTarget(accept), protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, nil), SubmitAcceptance{}},
		{"main agent submits a verdict", mainAgent,
			h.env("acceptances.submit", runTarget(accept), protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, nil), SubmitAcceptance{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := h.app.Execute(context.Background(), tt.p, tt.env, tt.cmd); protocol.CodeOf(err) != protocol.CodeForbidden {
				t.Errorf("err = %v, want forbidden", err)
			}
		})
	}
	if got := h.task(taskID).Lifecycle; got != protocol.TaskAwaitingAcceptance {
		t.Errorf("forbidden requests changed the task to %s", got)
	}
	if n := h.count("request_records WHERE status = 'failed'"); n != 0 {
		t.Errorf("forbidden requests were recorded: %d", n)
	}
}

func TestEnvelopeRejectedBeforeSideEffects(t *testing.T) {
	h := newHarness(t)
	env := h.env("tasks.create", protocol.Target{Type: "task"}, createTaskPayload{Title: "a", Spec: testSpec}, nil)
	env.RequestID = ""
	if _, err := h.app.Execute(context.Background(), owner, env, CreateTask{}); protocol.CodeOf(err) != protocol.CodeMissingRequestID {
		t.Errorf("err = %v, want missing_request_id", err)
	}
	wrongOp := h.env("tasks.cancel", taskTarget("task_x"), nil, rev(1))
	if _, err := h.app.Execute(context.Background(), owner, wrongOp, CreateTask{}); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Errorf("mismatched operation: err = %v, want invalid_request", err)
	}
	if n := h.count("tasks") + h.count("request_records"); n != 0 {
		t.Errorf("rejected envelopes left %d rows", n)
	}
}

// TestBusinessFailureRollsBackPartialWrites covers a command that writes
// before it fails for a business reason: the failure is recorded, but its
// partial writes must not survive, or a corrected retry would be blocked.
func TestBusinessFailureRollsBackPartialWrites(t *testing.T) {
	h := newHarness(t)
	bad := createTaskPayload{Title: "a", Spec: domain.Spec{Goal: "x", Acceptance: []string{"y"}, Delivery: "release"}, SourceMessageID: "msg_1", CommitSeq: 1}
	if op, _ := h.createTask(mainAgent, bad); op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidRequest {
		t.Fatalf("invalid spec: %+v", op)
	}
	if n := h.count("plan_commits"); n != 0 {
		t.Errorf("plan commits after a failed create = %d, want 0", n)
	}
	good := bad
	good.Spec = testSpec
	if op, _ := h.createTask(mainAgent, good); op.Status != protocol.OpSucceeded {
		t.Errorf("corrected retry of the same planning commit: %+v", op)
	}
}
