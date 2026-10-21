package api

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// configureProjects registers two projects that share one repository.
func (e *env) configureProjects() {
	e.t.Helper()
	e.exec("config.sync", protocol.Target{Type: "config"}, nil, app.ConfigPayload{
		Repos: []app.RepoConfig{
			{Repo: "tend", RemoteURL: "https://example.com/me/tend.git"},
			{Repo: "site", RemoteURL: "git@example.com:me/site.git"},
			{Repo: "shared", RemoteURL: "https://example.com/me/shared.git"},
		},
		Hosts: []app.HostConfig{{HostID: "host-a", OS: "linux", Arch: "amd64", Repos: []string{"tend", "site", "shared"}, Slots: 2,
			TokenHash: strings.Repeat("0", 64), Nodes: []string{"host-a"}}},
		Projects: []app.ProjectConfig{
			{Name: "core", Repos: []string{"tend", "shared"}},
			{Name: "web", Repos: []string{"site", "shared"}},
		},
	}, app.SyncConfig{})
}

// sessionIn returns a bound session working in the repository at remotes.
func (e *env) sessionIn(id string, remotes ...string) *session {
	e.t.Helper()
	s := e.unboundSession(protocol.AgentCodex, id)
	s.remotes = remotes
	s.call("repos_list", map[string]any{}, "")
	return s
}

func TestSessionBindsToItsRepositorysProject(t *testing.T) {
	e := newEnv(t)
	e.configureProjects()
	s := e.sessionIn("s-core", "ssh://git@example.com/me/tend")
	if got := s.start("startup"); !strings.Contains(got, "Project: core, the project of the repository") {
		t.Fatalf("snapshot of a session in tend:\n%s", got)
	}
	// Its tasks join its project without naming one.
	id := created(t, s.call("tasks_create", createArgs("fix the build", "fix-build"), ""))
	if p := e.taskRow(id).Project; p != "core" {
		t.Errorf("task project = %q, want core", p)
	}
	// The session moved to another repository: a full snapshot follows.
	s.remotes = []string{"https://example.com/me/site"}
	if got := s.prompt(); !strings.Contains(got, "kind=full") || !strings.Contains(got, "Project: web") {
		t.Errorf("context after moving to site:\n%s", got)
	}
	// Remotes that say nothing keep the binding.
	s.remotes = nil
	if got := s.prompt(); strings.Contains(got, "kind=full") {
		t.Errorf("context without remotes:\n%s", got)
	}
	// The MCP bridge still reports where the agent started it; the hook
	// knows better.
	s.remotes = []string{"https://example.com/me/tend"}
	id = created(t, s.call("tasks_create", createArgs("new landing page", "landing"), ""))
	if p := e.taskRow(id).Project; p != "web" {
		t.Errorf("task created after the move is in %q, want web", p)
	}
}

func TestOneActiveProjectStillScopes(t *testing.T) {
	e := newEnv(t)
	e.configure()
	old := e.task("left in the default project")
	e.exec("config.sync", protocol.Target{Type: "config"}, nil, app.ConfigPayload{
		Repos:    []app.RepoConfig{{Repo: "tend", RemoteURL: "https://example.com/tend.git"}},
		Projects: []app.ProjectConfig{{Name: "core", Repos: []string{"tend"}}},
	}, app.SyncConfig{})
	s := e.sessionIn("s-1", "https://example.com/tend.git")
	fresh := created(t, s.call("tasks_create", createArgs("new in core", "new-in-core"), ""))
	got := s.start("startup")
	if strings.Contains(got, old) || !strings.Contains(got, "Other projects: default: 1 open tasks") || strings.Contains(got, "Project: ") {
		t.Errorf("snapshot with one active project:\n%s", got)
	}
	// A session bound to no project sees both, each line naming its
	// project.
	all := e.sessionIn("s-2")
	if got := all.start("startup"); !strings.Contains(got, "[default] "+old) || !strings.Contains(got, "[core] "+fresh) {
		t.Errorf("snapshot of an unbound session:\n%s", got)
	}
}

func TestSharedRepositoryLeavesTheChoiceToTheSession(t *testing.T) {
	e := newEnv(t)
	e.configureProjects()
	s := e.sessionIn("s-shared", "https://example.com/me/shared.git")
	if got := s.start("startup"); !strings.Contains(got, "Projects: core, web. This session sees all of them") {
		t.Fatalf("snapshot of a session in a shared repository:\n%s", got)
	}
	if a := s.call("session_project", map[string]any{"project": "games"}, ""); a.OK || a.Error.Code != protocol.CodeInvalidRequest {
		t.Errorf("choosing an unknown project = %+v", a)
	}
	if a := s.call("session_project", map[string]any{"project": "web"}, ""); !a.OK {
		t.Fatalf("session_project = %+v", a)
	}
	if got := s.prompt(); !strings.Contains(got, "kind=full") || !strings.Contains(got, "Project: web, as chosen for this session") {
		t.Errorf("context after choosing web:\n%s", got)
	}
	// The choice outlasts the repository the session is in.
	s.remotes = []string{"https://example.com/me/tend"}
	s.call("repos_list", map[string]any{}, "")
	if got := s.start("resume"); !strings.Contains(got, "Project: web, as chosen") {
		t.Errorf("context after a tool call from tend:\n%s", got)
	}
	// An empty choice is every project.
	s.call("session_project", map[string]any{"project": ""}, "")
	if got := s.prompt(); !strings.Contains(got, "This session sees all of them") {
		t.Errorf("context after choosing every project:\n%s", got)
	}
}

func TestSingleProjectSaysNothingAboutProjects(t *testing.T) {
	e := newEnv(t)
	e.configure()
	s := e.sessionIn("s-1", "https://example.com/tend.git")
	if got := s.start("startup"); strings.Contains(got, "Project") {
		t.Errorf("snapshot with one project mentions projects:\n%s", got)
	}
}

func (e *env) taskRow(id string) store.TaskRow {
	e.t.Helper()
	var row store.TaskRow
	if err := e.store.Read(context.Background(), func(tx *store.ReadTx) error {
		var err error
		row, err = store.GetTask(context.Background(), tx.Q(), id)
		return err
	}); err != nil {
		e.t.Fatal(err)
	}
	return row
}

func TestContextIsScopedToTheSessionsProject(t *testing.T) {
	e := newEnv(t)
	e.configureProjects()
	core := e.sessionIn("s-core", "https://example.com/me/tend")
	web := e.sessionIn("s-web", "git@example.com:me/site.git")
	coreTask := created(t, core.call("tasks_create", createArgs("speed up the ledger", "speed-up"), ""))
	webTask := created(t, web.call("tasks_create", createArgs("redo the landing page", "landing"), ""))

	got := core.start("startup")
	if !strings.Contains(got, "speed up the ledger") || strings.Contains(got, "redo the landing page") ||
		!strings.Contains(got, "Other projects: web: 1 open tasks, 0 open decisions.") {
		t.Fatalf("snapshot of the core session:\n%s", got)
	}
	// A session in a shared repository sees both, each line naming its
	// project.
	all := e.sessionIn("s-all", "https://example.com/me/shared")
	if got := all.start("startup"); !strings.Contains(got, `[core] `+coreTask) || !strings.Contains(got, `[web] `+webTask) {
		t.Errorf("snapshot of a session over every project:\n%s", got)
	}

	// Of the web project, the core session hears only what needs
	// attention.
	rev := e.taskRow(webTask).ControlRevision
	e.exec("tasks.update_intent", protocol.Target{Type: "task", ID: webTask}, &rev, map[string]any{"rating": "p1"}, app.UpdateIntent{})
	if got := core.prompt(); got != "" {
		t.Errorf("the core session heard of a web change:\n%s", got)
	}
	e.failRun(webTask)
	got = core.prompt()
	if !strings.Contains(got, "Other projects, needs attention") || !strings.Contains(got, "[web] ") || !strings.Contains(got, "run.failed") {
		t.Errorf("the core session missed a failure in web:\n%s", got)
	}
	if strings.Contains(got, "now: ") {
		t.Errorf("the core session got the state of a web task:\n%s", got)
	}

	// A task moved out of core is still news to the core session.
	rev = e.taskRow(coreTask).ControlRevision
	e.exec("tasks.update_intent", protocol.Target{Type: "task", ID: coreTask}, &rev, map[string]any{"project": "web"}, app.UpdateIntent{})
	if got := core.prompt(); !strings.Contains(got, "task.project_changed") {
		t.Errorf("the core session missed a task leaving core:\n%s", got)
	}
	rev = e.taskRow(coreTask).ControlRevision
	e.exec("tasks.update_intent", protocol.Target{Type: "task", ID: coreTask}, &rev, map[string]any{"project": "core"}, app.UpdateIntent{})
	core.prompt()

	// tasks_list lists the session's project unless asked for every one.
	list := func(args map[string]any) []string {
		var res struct {
			Tasks []protocol.TaskSummary `json:"tasks"`
		}
		a := core.call("tasks_list", args, "")
		if err := json.Unmarshal(a.Result, &res); err != nil || !a.OK {
			t.Fatalf("tasks_list = %+v: %v", a, err)
		}
		var ids []string
		for _, s := range res.Tasks {
			ids = append(ids, s.TaskID)
		}
		slices.Sort(ids)
		return ids
	}
	if got := list(map[string]any{}); !slices.Equal(got, []string{coreTask}) {
		t.Errorf("tasks_list = %v, want only %s", got, coreTask)
	}
	want := []string{coreTask, webTask}
	slices.Sort(want)
	if got := list(map[string]any{"project": "*"}); !slices.Equal(got, want) {
		t.Errorf("tasks_list of every project = %v", got)
	}
}

func TestHookUnbindingOutlastsBridgeHints(t *testing.T) {
	e := newEnv(t)
	e.configureProjects()
	s := e.sessionIn("s-1", "https://example.com/me/tend")
	s.start("startup")
	// The session moves to the shared repository: no single project.
	s.remotes = []string{"https://example.com/me/shared"}
	s.prompt()
	// The bridge still reports tend, where the agent started it.
	s.remotes = []string{"https://example.com/me/tend"}
	if a := s.call("tasks_create", createArgs("which project?", "which"), ""); a.OK || a.Error.Code != protocol.CodeInvalidRequest {
		t.Errorf("tasks_create without a project after leaving core = %+v", a)
	}
}

func TestRetiredProjectWidensTheScope(t *testing.T) {
	e := newEnv(t)
	e.configureProjects()
	core := e.sessionIn("s-core", "https://example.com/me/tend")
	web := e.sessionIn("s-web", "https://example.com/me/site")
	webTask := created(t, web.call("tasks_create", createArgs("redo the landing page", "landing"), ""))
	if a := core.call("session_project", map[string]any{"project": "core"}, ""); !a.OK {
		t.Fatalf("session_project = %+v", a)
	}
	core.start("startup")
	// core is dropped from the configuration while the session runs.
	e.exec("config.sync", protocol.Target{Type: "config"}, nil, app.ConfigPayload{
		Repos: []app.RepoConfig{
			{Repo: "tend", RemoteURL: "https://example.com/me/tend.git"},
			{Repo: "site", RemoteURL: "git@example.com:me/site.git"},
			{Repo: "shared", RemoteURL: "https://example.com/me/shared.git"},
		},
		Projects: []app.ProjectConfig{{Name: "web", Repos: []string{"site", "shared"}}, {Name: "ops", Repos: []string{"shared"}}},
	}, app.SyncConfig{})
	if got := core.prompt(); !strings.Contains(got, "kind=full") || !strings.Contains(got, webTask) {
		t.Errorf("context after the session's project was retired:\n%s", got)
	}
	a := core.call("tasks_list", map[string]any{}, "")
	if !a.OK || !strings.Contains(string(a.Result), webTask) {
		t.Errorf("tasks_list after the session's project was retired = %+v", a)
	}
}

func TestSnapshotShowsProjectsAndQueue(t *testing.T) {
	e := newEnv(t)
	e.configureProjects()
	create := func(title, project string, r protocol.Rating) string {
		var res struct {
			TaskID string `json:"task_id"`
		}
		_ = json.Unmarshal(e.exec("tasks.create", protocol.Target{Type: "task"}, nil, map[string]any{
			"title": title, "spec": json.RawMessage(`{"goal": "g", "acceptance": ["a"], "delivery": "commit", "workspace": {"mounts": [{"repo": "tend"}]}}`),
			"project": project, "rating": r, "workspace_name": title,
		}, app.CreateTask{}), &res)
		return res.TaskID
	}
	low := create("low", "core", protocol.RatingP3)
	high := create("high", "web", protocol.RatingP1)
	paused := create("paused", "core", protocol.RatingP0)
	rev := int64(1)
	e.exec("control.set_mode", protocol.Target{Type: "task", ID: paused}, &rev, protocol.SetControlModePayload{Mode: protocol.ControlPaused}, app.SetControlMode{})

	var s protocol.Snapshot
	if st := e.do("GET", "/api/v1/snapshot", e.login(protocol.ScopeOwner), nil, &s); st != 200 {
		t.Fatalf("snapshot = %d", st)
	}
	var order []string
	for _, q := range s.Queue {
		order = append(order, q.TaskID)
	}
	// The paused task waits for the owner, not for a host.
	if want := []string{high, low}; !slices.Equal(order, want) || s.Queue[0].Score != 3 || s.Queue[0].Project != "web" || s.Queue[0].Title != "high" {
		t.Errorf("queue = %+v, want %v", s.Queue, want)
	}
	got := map[string]protocol.ProjectView{}
	for _, p := range s.Projects {
		got[p.Project] = p
	}
	if len(got) != 2 || got["core"].Queued != 1 || got["web"].Queued != 1 || got["core"].Weight != 1 {
		t.Errorf("projects = %+v", s.Projects)
	}

	// A project dropped from the configuration stays in view while it has
	// work, and decisions name their project.
	e.decision(high)
	e.exec("config.sync", protocol.Target{Type: "config"}, nil, app.ConfigPayload{
		Repos:    []app.RepoConfig{{Repo: "tend", RemoteURL: "https://example.com/me/tend.git"}},
		Projects: []app.ProjectConfig{{Name: "core", Repos: []string{"tend"}}},
	}, app.SyncConfig{})
	s = protocol.Snapshot{}
	e.do("GET", "/api/v1/snapshot", e.login(protocol.ScopeOwner), nil, &s)
	if i := slices.IndexFunc(s.Projects, func(p protocol.ProjectView) bool { return p.Project == "web" }); i < 0 || !s.Projects[i].Retired {
		t.Errorf("projects after retiring web = %+v", s.Projects)
	}
	if len(s.Decisions) != 1 || s.Decisions[0].Project != "web" {
		t.Errorf("decisions = %+v", s.Decisions)
	}

	// One project's view keeps the places in the whole queue.
	s = protocol.Snapshot{}
	e.do("GET", "/api/v1/snapshot?project=core", e.login(protocol.ScopeOwner), nil, &s)
	if len(s.Tasks) != 2 || len(s.Queue) != 1 || s.Queue[0].TaskID != low || s.Queue[0].Place != 2 || len(s.Decisions) != 0 ||
		len(s.Projects) != 1 || s.Projects[0].Project != "core" {
		t.Errorf("core's snapshot = %+v", s)
	}
	if s.Projects[0].OpenTasks != 2 {
		t.Errorf("core's open tasks = %d, want 2", s.Projects[0].OpenTasks)
	}
	// A retired project with nothing left is still there when asked for.
	s = protocol.Snapshot{}
	e.do("GET", "/api/v1/snapshot?project=default", e.login(protocol.ScopeOwner), nil, &s)
	if len(s.Projects) != 1 || s.Projects[0].Project != "default" || !s.Projects[0].Retired {
		t.Errorf("the default project's snapshot = %+v", s.Projects)
	}
	if st := e.do("GET", "/api/v1/snapshot?project=nope", e.login(protocol.ScopeOwner), nil, nil); st != 404 {
		t.Errorf("snapshot of an unknown project = %d, want 404", st)
	}
}
