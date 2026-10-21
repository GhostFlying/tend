package app

import (
	"context"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func (h *harness) projects() map[string]store.ProjectRow {
	h.t.Helper()
	out := map[string]store.ProjectRow{}
	if err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		all, err := store.ListProjects(context.Background(), tx.Q())
		for _, p := range all {
			out[p.Project] = p
		}
		return err
	}); err != nil {
		h.t.Fatal(err)
	}
	return out
}

func withProjects() ConfigPayload {
	c := testConfig()
	c.Projects = []ProjectConfig{
		{Name: "video", Title: "Video app", Weight: 3, Repos: []string{"douyin", "template-assembler"}, Template: "douyin-with-lynx"},
		{Name: "tend", Repos: []string{"tend"}},
	}
	return c
}

func TestDefaultProjectHoldsEveryRepo(t *testing.T) {
	h := newHarness(t)
	got := h.projects()
	d, ok := got[DefaultProject]
	if len(got) != 1 || !ok || d.Retired || d.Weight != 1 || strings.Join(d.Repos, ",") != "tend,douyin,template-assembler" {
		t.Fatalf("projects without configuration = %+v", got)
	}
	// With one project, tasks join it without naming it.
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	if p := h.task(res.TaskID).Project; p != DefaultProject {
		t.Errorf("task project = %q, want %s", p, DefaultProject)
	}
}

func TestConfiguredProjects(t *testing.T) {
	h := newHarness(t)
	_, old := h.createTask(owner, createTaskPayload{Title: "before projects", Spec: testSpec})
	h.syncConfig(withProjects())
	got := h.projects()
	if !got[DefaultProject].Retired || got["video"].Weight != 3 || got["video"].Template != "douyin-with-lynx" ||
		got["tend"].Weight != 1 || got["tend"].Title != "tend" {
		t.Fatalf("projects = %+v", got)
	}
	if p := h.task(old.TaskID).Project; p != DefaultProject {
		t.Errorf("existing task moved to %q", p)
	}

	// With several projects the task must name one, and not a retired one.
	for _, project := range []string{"", DefaultProject, "games"} {
		if op, _ := h.createTask(owner, createTaskPayload{Title: "b", Spec: testSpec, Project: project}); op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidRequest {
			t.Errorf("create in project %q: %+v, want invalid_request", project, op)
		}
	}
	// A task without a workspace gets the project's template.
	spec := testSpec
	spec.Workspace = domain.Workspace{}
	op, res := h.createTask(owner, createTaskPayload{Title: "c", Spec: spec, Project: "video"})
	if op.Status != protocol.OpSucceeded {
		t.Fatalf("create in video: %+v", op)
	}
	if ws := h.storedSpec(res.TaskID, 1).Workspace; ws.Template != "douyin-with-lynx" || len(ws.Mounts) != 2 {
		t.Errorf("workspace = %+v, want the video template", ws)
	}
	if p := h.task(res.TaskID).Project; p != "video" {
		t.Errorf("task project = %q, want video", p)
	}
}

func TestMoveTaskToAnotherProject(t *testing.T) {
	h := newHarness(t)
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	h.syncConfig(withProjects())
	move := func(project string, revision int64) protocol.Operation {
		return h.exec(owner, h.env("tasks.update_intent", taskTarget(res.TaskID), updateIntentPayload{Project: &project}, rev(revision)), UpdateIntent{})
	}
	if op := move("games", res.ControlRevision); op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidRequest {
		t.Errorf("move to unknown project: %+v", op)
	}
	if op := move("tend", res.ControlRevision); op.Status != protocol.OpSucceeded {
		t.Fatalf("move to tend: %+v", op)
	}
	row := h.task(res.TaskID)
	if row.Project != "tend" || row.ControlRevision != res.ControlRevision+1 {
		t.Errorf("after move: project %q revision %d", row.Project, row.ControlRevision)
	}
	if ev := h.events("task.project_changed"); len(ev) != 1 || ev[0].Body["project"] != "tend" || ev[0].Body["from"] != DefaultProject {
		t.Errorf("project_changed events = %+v", ev)
	}
	// Naming the current project changes nothing but still checks the
	// revision.
	if op := move("tend", res.ControlRevision); op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeStaleRevision {
		t.Errorf("stale update to the same project: %+v", op)
	}
	// The default project is retired now; moving back is refused.
	if op := move(DefaultProject, row.ControlRevision); op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidRequest {
		t.Errorf("move to retired project: %+v", op)
	}
}

func TestProjectConfigChecks(t *testing.T) {
	h := newHarness(t)
	bad := []struct {
		name   string
		mutate func(*ConfigPayload)
	}{
		{"malformed name", func(c *ConfigPayload) { c.Projects[0].Name = "Video" }},
		{"duplicate", func(c *ConfigPayload) { c.Projects[1].Name = "video" }},
		{"negative weight", func(c *ConfigPayload) { c.Projects[0].Weight = -1 }},
		{"unknown repo", func(c *ConfigPayload) { c.Projects[1].Repos = []string{"chromium"} }},
		{"unknown template", func(c *ConfigPayload) { c.Projects[0].Template = "nope" }},
	}
	for _, tc := range bad {
		c := withProjects()
		tc.mutate(&c)
		if op := h.syncConfig(c); op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidRequest {
			t.Errorf("%s: %+v, want invalid_request", tc.name, op)
		}
	}
}

func TestNormalizeRemote(t *testing.T) {
	same := []string{
		"https://example.com/me/tend.git",
		"https://user@example.com:8443/me/tend/",
		"ssh://git@example.com:22/me/tend.git",
		"git@example.com:me/tend.git",
		"example.com:me/tend",
		"HTTPS://Example.com/Me/Tend",
	}
	for _, r := range same {
		if got := normalizeRemote(r); got != "example.com/me/tend" {
			t.Errorf("normalizeRemote(%q) = %q", r, got)
		}
	}
	for r, want := range map[string]string{
		"ssh://git@[2001:db8::1]/me/tend.git": "2001:db8::1/me/tend",
		"git@[2001:db8::1]:me/tend.git":       "2001:db8::1/me/tend",
		"/srv/git/tend.git":                   "/srv/git/tend",
		"file:///srv/git/tend":                "srv/git/tend",
		"./relative/path:with:it":             "./relative/path:with:it",
	} {
		if got := normalizeRemote(r); got != want {
			t.Errorf("normalizeRemote(%q) = %q, want %q", r, got, want)
		}
	}
}

func TestListTasksFiltersBeforeTheLimit(t *testing.T) {
	h := newHarness(t)
	h.syncConfig(withProjects())
	for _, w := range []string{"a", "b", "c"} {
		h.createTask(owner, createTaskPayload{Title: w, Spec: testSpec, Project: "video", Priority: 9, WorkspaceName: "video-" + w})
	}
	_, res := h.createTask(owner, createTaskPayload{Title: "core", Spec: testSpec, Project: "tend"})
	var got []store.TaskRow
	if err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		var err error
		got, err = store.ListTasks(context.Background(), tx.Q(), "tend", 1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TaskID != res.TaskID {
		t.Errorf("ListTasks(tend, 1) = %+v, want %s", got, res.TaskID)
	}
}
