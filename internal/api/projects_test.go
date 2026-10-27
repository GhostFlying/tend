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
