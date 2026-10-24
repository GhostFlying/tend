package tools

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/contextbuilder"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

// hosted is the hosted main agent's principal.
var hosted = protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "harness-1", Thread: "th_1"}

type env struct {
	t     *testing.T
	svc   *Service
	store *store.Store
	calls int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	clock := testkit.NewClock()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "tend.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ids := testkit.NewIDGen(clock, 9)
	a := app.New(s, ids, clock.Now)
	payload, _ := json.Marshal(app.ConfigPayload{
		Repos: []app.RepoConfig{{Repo: "tend", RemoteURL: "https://example.com/tend.git"}},
		Hosts: []app.HostConfig{{HostID: "linux-1", OS: "linux", Arch: "amd64", Repos: []string{"tend"}, Slots: 1,
			TokenHash: strings.Repeat("ab", 32), Nodes: []string{"tag:tend-runner"}}},
		Templates: []app.TemplateConfig{{Name: "tend-only", Workspace: domain.Workspace{Mounts: []domain.Mount{{Repo: "tend"}}}}},
	})
	envl := &protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: "config.sync", Target: protocol.Target{Type: "config"}, Payload: payload}
	if _, err := a.Execute(context.Background(), protocol.Principal{Kind: protocol.PrincipalSystem}, envl, app.SyncConfig{}); err != nil {
		t.Fatal(err)
	}
	return &env{t: t, svc: New(a, s, contextbuilder.New(a, s, ids, contextbuilder.Config{}), slog.New(slog.NewTextHandler(io.Discard, nil))), store: s}
}

// call runs a tool as the main agent handling message msg_1. callID
// defaults to a fresh one.
func (e *env) call(tool string, args any, callID string) answerView {
	e.t.Helper()
	if callID == "" {
		e.calls++
		callID = "call_" + string(rune('a'+e.calls))
	}
	raw, err := json.Marshal(args)
	if err != nil {
		e.t.Fatal(err)
	}
	res := e.svc.Handle(context.Background(), Call{
		Principal: hosted, Native: "th_1/tu_1/" + callID, SourceMessageID: "msg_1", Tool: tool, Arguments: raw,
	})
	var a answerView
	if err := json.Unmarshal([]byte(res.Text), &a); err != nil {
		e.t.Fatalf("tool answer %q: %v", res.Text, err)
	}
	if a.OK != res.Success {
		e.t.Errorf("Success = %v but answer ok = %v", res.Success, a.OK)
	}
	return a
}

type answerView struct {
	OK      bool            `json:"ok"`
	Result  json.RawMessage `json:"result"`
	Error   *protocol.Error `json:"error"`
	Current json.RawMessage `json:"current"`
}

var spec = domain.Spec{Goal: "fix the build", Acceptance: []string{"build passes"}, Delivery: domain.DeliveryCommit,
	Workspace: domain.Workspace{Template: "tend-only"}}

func (e *env) tasks() int {
	var n int
	_ = e.store.Read(context.Background(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM tasks`).Scan(&n)
	})
	return n
}

func TestFault_DuplicateToolCall(t *testing.T) {
	e := newEnv(t)
	args := createArgs{Title: "fix", Spec: spec, CommitSeq: 1}
	if a := e.call("tasks_create", args, ""); !a.OK {
		t.Fatalf("first call: %+v", a.Error)
	}
	// The model calls again with the same arguments under a new call ID.
	a := e.call("tasks_create", args, "")
	if a.OK || a.Error.Code != protocol.CodeDuplicateEffect {
		t.Errorf("second call = %+v, want duplicate_effect", a)
	}
	if n := e.tasks(); n != 1 {
		t.Errorf("tasks = %d, want 1", n)
	}
}

func TestFault_ToolRetrySameCall(t *testing.T) {
	e := newEnv(t)
	args := createArgs{Title: "fix", Spec: spec, CommitSeq: 1}
	first := e.call("tasks_create", args, "call_same")
	again := e.call("tasks_create", args, "call_same")
	if !first.OK || !again.OK || string(first.Result) != string(again.Result) {
		t.Errorf("replay differs:\n%s\n%s", first.Result, again.Result)
	}
	if n := e.tasks(); n != 1 {
		t.Errorf("tasks = %d, want 1", n)
	}
}

func createdTask(t *testing.T, a answerView) (string, int64) {
	t.Helper()
	var r struct {
		Outcome struct {
			TaskID          string `json:"task_id"`
			ControlRevision int64  `json:"control_revision"`
		} `json:"outcome"`
	}
	if err := json.Unmarshal(a.Result, &r); err != nil || r.Outcome.TaskID == "" {
		t.Fatalf("create result %s (%v)", a.Result, err)
	}
	return r.Outcome.TaskID, r.Outcome.ControlRevision
}

func TestCreateAsksForARating(t *testing.T) {
	e := newEnv(t)
	note := func(a answerView) string {
		var r struct {
			Note string `json:"note"`
		}
		if !a.OK || json.Unmarshal(a.Result, &r) != nil {
			t.Fatalf("create = %+v", a)
		}
		return r.Note
	}
	if got := note(e.call("tasks_create", createArgs{Title: "fix", Spec: spec, CommitSeq: 1}, "")); got != unratedNote {
		t.Errorf("unrated create note = %q", got)
	}
	if got := note(e.call("tasks_create", createArgs{Title: "fix", Spec: spec, Rating: protocol.RatingP1, CommitSeq: 2}, "")); got != "" {
		t.Errorf("rated create note = %q, want none", got)
	}
	if a := e.call("tasks_create", createArgs{Title: "fix", Spec: spec, Rating: "urgent", CommitSeq: 3}, ""); a.OK || a.Error.Code != protocol.CodeInvalidRequest {
		t.Errorf("unknown rating = %+v, want invalid_request", a)
	}
}

func TestStaleWriteReturnsCurrentState(t *testing.T) {
	e := newEnv(t)
	id, rev := createdTask(t, e.call("tasks_create", createArgs{Title: "fix", Spec: spec, CommitSeq: 1}, ""))
	if a := e.call("tasks_update_intent", updateArgs{TaskID: id, ExpectedControlRevision: rev, Rating: new(protocol.RatingP1), CommitSeq: 2}, ""); !a.OK {
		t.Fatalf("update: %+v", a.Error)
	}
	a := e.call("tasks_update_intent", updateArgs{TaskID: id, ExpectedControlRevision: rev, Rating: new(protocol.RatingP3), CommitSeq: 3}, "")
	if a.OK || a.Error.Code != protocol.CodeStaleRevision {
		t.Fatalf("stale update = %+v", a)
	}
	var cur protocol.TaskSummary
	if err := json.Unmarshal(a.Current, &cur); err != nil || cur.ControlRevision != rev+1 || cur.Rating != protocol.RatingP1 {
		t.Errorf("current = %s (%v)", a.Current, err)
	}
}

func TestPlanningNeedsAMessage(t *testing.T) {
	e := newEnv(t)
	raw, _ := json.Marshal(createArgs{Title: "fix", Spec: spec, CommitSeq: 1})
	res := e.svc.Handle(context.Background(), Call{Principal: hosted, Native: "th_1/t/c", Tool: "tasks_create", Arguments: raw})
	if res.Success || !strings.Contains(res.Text, "invalid_request") {
		t.Errorf("create outside a message turn = %s", res.Text)
	}
	if a := e.call("tasks_create", map[string]any{"title": "x", "bogus": 1}, ""); a.OK || a.Error.Code != protocol.CodeInvalidRequest {
		t.Errorf("unknown argument accepted: %+v", a)
	}
	if a := e.call("no_such_tool", map[string]any{}, ""); a.OK {
		t.Error("unknown tool answered ok")
	}
}

func TestReadTools(t *testing.T) {
	e := newEnv(t)
	created := e.call("tasks_create", createArgs{Title: "fix", Spec: spec, CommitSeq: 1}, "")
	id, _ := createdTask(t, created)

	var list struct {
		Tasks []protocol.TaskSummary `json:"tasks"`
	}
	if a := e.call("tasks_list", listArgs{}, ""); !a.OK || json.Unmarshal(a.Result, &list) != nil || len(list.Tasks) != 1 || list.Tasks[0].NextImplementGeneration != 2 {
		t.Errorf("tasks_list = %s", a.Result)
	}
	var detail protocol.TaskDetail
	if a := e.call("tasks_read", taskArgs{TaskID: id}, ""); !a.OK || json.Unmarshal(a.Result, &detail) != nil || len(detail.Actions) != 1 || !strings.Contains(string(detail.Spec), `"repo":"tend"`) {
		t.Errorf("tasks_read = %s", a.Result)
	}
	if a := e.call("tasks_read", taskArgs{TaskID: "task_missing"}, ""); a.OK || a.Error.Code != protocol.CodeNotFound {
		t.Errorf("missing task = %+v", a)
	}
	if a := e.call("repos_list", noArgs{}, ""); !a.OK || !strings.Contains(string(a.Result), `"hosts":["linux-1"]`) || !strings.Contains(string(a.Result), "tend-only") ||
		!strings.Contains(string(a.Result), `"worker":"appserver"`) || strings.Contains(string(a.Result), "single_mount") {
		t.Errorf("repos_list = %s", a.Result)
	}
	var r struct {
		RequestID string `json:"request_id"`
	}
	_ = json.Unmarshal(created.Result, &r)
	if a := e.call("requests_read", requestArgs{RequestID: r.RequestID}, ""); !a.OK || !strings.Contains(string(a.Result), `"succeeded"`) {
		t.Errorf("requests_read = %+v %s", a.Error, a.Result)
	}
}

func TestDecisionTools(t *testing.T) {
	e := newEnv(t)
	a := e.call("decisions_open", openArgs{Question: "which?", Options: []string{"first", "second"}}, "")
	var r struct {
		Outcome struct {
			DecisionID string `json:"decision_id"`
		} `json:"outcome"`
	}
	if !a.OK || json.Unmarshal(a.Result, &r) != nil {
		t.Fatalf("open = %+v %s", a.Error, a.Result)
	}
	if a := e.call("decisions_resolve", resolveArgs{DecisionID: r.Outcome.DecisionID, Version: 1, Option: "second"}, ""); !a.OK {
		t.Errorf("resolve = %+v", a.Error)
	}
}

// schemaFields returns the property names and required list of a schema.
func schemaFields(t *testing.T, raw string) ([]string, []string, map[string]json.RawMessage) {
	t.Helper()
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatalf("schema: %v\n%s", err, raw)
	}
	var props []string
	for k := range s.Properties {
		props = append(props, k)
	}
	slices.Sort(props)
	return props, s.Required, s.Properties
}

func tags(v any) []string {
	var out []string
	rt := reflect.TypeOf(v)
	for i := range rt.NumField() {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

func TestSchemasMatchArgs(t *testing.T) {
	args := map[string]any{
		"conversation_read": conversationArgs{},
		"tasks_list":        listArgs{}, "tasks_read": taskArgs{}, "tasks_create": createArgs{}, "tasks_update_intent": updateArgs{},
		"tasks_set_control": controlArgs{}, "tasks_cancel": cancelArgs{}, "actions_create": actionArgs{}, "decisions_open": openArgs{}, "decisions_resolve": resolveArgs{},
		"artifacts_read": artifactArgs{}, "attachments_read": fileArgs{}, "files_read": fileArgs{}, "requests_read": requestArgs{}, "repos_list": noArgs{}, "tend_context": noArgs{}, "session_project": sessionProjectArgs{},
	}
	if len(args) != len(tools) {
		t.Fatalf("%d tools but %d argument types", len(tools), len(args))
	}
	for _, tl := range tools {
		props, required, _ := schemaFields(t, schemas[tl.name])
		want := tags(args[tl.name])
		if !slices.Equal(props, want) {
			t.Errorf("%s: schema properties %v, argument fields %v", tl.name, props, want)
		}
		for _, r := range required {
			if !slices.Contains(want, r) {
				t.Errorf("%s requires unknown %s", tl.name, r)
			}
		}
	}
	// The spec schema mirrors domain.Spec and its parts.
	props, _, nested := schemaFields(t, specSchema)
	if want := tags(domain.Spec{}); !slices.Equal(props, want) {
		t.Errorf("spec schema %v, domain.Spec %v", props, want)
	}
	wsProps, _, wsNested := schemaFields(t, string(nested["workspace"]))
	if want := tags(domain.Workspace{}); !slices.Equal(wsProps, want) {
		t.Errorf("workspace schema %v, domain.Workspace %v", wsProps, want)
	}
	var mounts struct {
		Items json.RawMessage `json:"items"`
	}
	_ = json.Unmarshal(wsNested["mounts"], &mounts)
	if mProps, _, _ := schemaFields(t, string(mounts.Items)); !slices.Equal(mProps, tags(domain.Mount{})) {
		t.Errorf("mount schema %v, domain.Mount %v", mProps, tags(domain.Mount{}))
	}
	if rProps, _, _ := schemaFields(t, string(nested["requirements"])); !slices.Equal(rProps, tags(domain.Requirements{})) {
		t.Errorf("requirements schema %v, domain.Requirements %v", rProps, tags(domain.Requirements{}))
	}
}

func TestHostedMainAgentLacksAddonTools(t *testing.T) {
	for _, spec := range Specs() {
		if spec.Name == "session_project" {
			t.Error("the hosted main agent is given session_project")
		}
	}
	if !slices.ContainsFunc(AddonSpecs(), func(s protocol.ToolSpec) bool { return s.Name == "session_project" }) {
		t.Error("add-on sessions are not given session_project")
	}
	e := newEnv(t)
	if a := e.call("session_project", sessionProjectArgs{Project: "default"}, "c1"); a.OK || a.Error == nil || a.Error.Code != protocol.CodeInvalidRequest {
		t.Errorf("hosted session_project = %+v", a)
	}
}

func TestUpdateWithTemplateSpec(t *testing.T) {
	e := newEnv(t)
	id, rev := createdTask(t, e.call("tasks_create", createArgs{Title: "fix", Spec: spec, CommitSeq: 1}, ""))
	changed := spec
	changed.Constraints = []string{"keep the API"}
	if a := e.call("tasks_update_intent", updateArgs{TaskID: id, ExpectedControlRevision: rev, Spec: &changed, CommitSeq: 2}, ""); !a.OK {
		t.Errorf("update with a template spec: %+v", a.Error)
	}
}

func TestRequestsReadKeepsErrorDetails(t *testing.T) {
	e := newEnv(t)
	args := createArgs{Title: "fix", Spec: spec, CommitSeq: 1}
	e.call("tasks_create", args, "")
	e.call("tasks_create", args, "call_dup")
	var dup string
	_ = e.store.Read(context.Background(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(context.Background(), `SELECT request_id FROM native_requests WHERE native_key LIKE '%call_dup'`).Scan(&dup)
	})
	a := e.call("requests_read", requestArgs{RequestID: dup}, "")
	if !a.OK || !strings.Contains(string(a.Result), "original_request_id") {
		t.Errorf("requests_read of a duplicate = %s", a.Result)
	}
}

func TestControlAndCancel(t *testing.T) {
	e := newEnv(t)
	id, rev := createdTask(t, e.call("tasks_create", createArgs{Title: "fix", Spec: spec, CommitSeq: 1}, ""))
	if a := e.call("tasks_set_control", controlArgs{TaskID: id, ExpectedControlRevision: rev, Mode: protocol.ControlPaused}, ""); !a.OK {
		t.Fatalf("pause: %+v", a.Error)
	}
	// The revision moved on: a write with the old one is stale.
	if a := e.call("tasks_cancel", cancelArgs{TaskID: id, ExpectedControlRevision: rev}, ""); a.OK || a.Error.Code != protocol.CodeStaleRevision {
		t.Fatalf("cancel with a stale revision: %+v", a)
	}
	if a := e.call("tasks_set_control", controlArgs{TaskID: id, ExpectedControlRevision: rev + 1, Mode: protocol.ControlHumanControl}, ""); a.OK || a.Error.Code != protocol.CodeForbidden {
		t.Errorf("take human control: %+v", a)
	}
	if a := e.call("tasks_cancel", cancelArgs{TaskID: id, ExpectedControlRevision: rev + 1, Reason: "the owner changed their mind"}, ""); !a.OK {
		t.Fatalf("cancel: %+v", a.Error)
	}
	var lifecycle string
	if err := e.store.Read(context.Background(), func(tx *store.ReadTx) error {
		row, err := store.GetTask(context.Background(), tx.Q(), id)
		lifecycle = string(row.Lifecycle)
		return err
	}); err != nil || lifecycle != "cancelled" {
		t.Errorf("lifecycle after cancel = %q, %v", lifecycle, err)
	}
}

func TestTaskRelationToolsCreateReadAndReplace(t *testing.T) {
	e := newEnv(t)
	parent, _ := createdTask(t, e.call("tasks_create", createArgs{Title: "original", Spec: spec, CommitSeq: 1}, ""))
	refs := []protocol.TaskRelation{{Kind: protocol.RelationFollowUp, TaskID: parent}}
	child, revision := createdTask(t, e.call("tasks_create", createArgs{Title: "followup", Spec: spec, Relations: refs, CommitSeq: 2}, ""))
	read := e.call("tasks_read", taskArgs{TaskID: parent}, "")
	if !read.OK || !strings.Contains(string(read.Result), child) || !strings.Contains(string(read.Result), "incoming") {
		t.Fatalf("backlink not visible: %+v", read)
	}
	refs = []protocol.TaskRelation{{Kind: protocol.RelationRelated, TaskID: parent}}
	update := e.call("tasks_update_intent", updateArgs{TaskID: child, ExpectedControlRevision: revision, Relations: &refs, CommitSeq: 3}, "")
	if !update.OK {
		t.Fatalf("update relation: %+v", update)
	}
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		rows, err := store.ListTaskRelations(t.Context(), tx.Q(), child)
		if err != nil {
			return err
		}
		if len(rows) != 1 || rows[0].Kind != protocol.RelationRelated || rows[0].TargetTaskID != parent {
			t.Fatalf("updated refs=%+v", rows)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyCreateRequestReplayAfterRelationsUpgrade(t *testing.T) {
	e := newEnv(t)
	a := createArgs{Title: "fix", Spec: spec, CommitSeq: 1}
	raw, _ := json.Marshal(a)
	call := Call{Principal: hosted, Native: "th_1/tu_1/call_prior", SourceMessageID: "msg_1", Tool: "tasks_create", Arguments: raw}
	prior, err := e.svc.execute(t.Context(), call, "tasks.create", protocol.Target{Type: "task"}, nil, a.Reason, map[string]any{
		"title": a.Title, "spec": a.Spec, "rating": a.Rating, "project": a.Project, "workspace_name": a.WorkspaceName,
		"source_message_id": call.SourceMessageID, "commit_seq": a.CommitSeq,
	}, app.CreateTask{})
	if err != nil {
		t.Fatal(err)
	}
	result := e.svc.Handle(t.Context(), call)
	if !result.Success {
		t.Fatalf("legacy retry rejected: %s", result.Text)
	}
	if e.tasks() != 1 {
		t.Fatal("legacy replay created another task")
	}
	var outcome struct {
		Outcome json.RawMessage `json:"outcome"`
	}
	var answer answerView
	if err := json.Unmarshal([]byte(result.Text), &answer); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(answer.Result, &outcome); err != nil {
		t.Fatal(err)
	}
	if string(outcome.Outcome) != string(prior["outcome"].(json.RawMessage)) {
		t.Fatal("legacy replay changed its result")
	}
}
