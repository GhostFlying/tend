package app

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func (h *harness) storedSpec(taskID string, version int64) domain.Spec {
	h.t.Helper()
	var spec domain.Spec
	if err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		body, err := store.GetSpec(context.Background(), tx.Q(), taskID, version)
		if err != nil {
			return err
		}
		return json.Unmarshal([]byte(body), &spec)
	}); err != nil {
		h.t.Fatal(err)
	}
	return spec
}

func TestCreateTaskExpandsTemplateAndNormalizes(t *testing.T) {
	h := newHarness(t)
	spec := testSpec
	spec.Workspace = domain.Workspace{Template: "douyin-with-lynx"}
	op, res := h.createTask(owner, createTaskPayload{Title: "integrate lynx", Spec: spec, WorkspaceName: "feature_a"})
	if op.Status != protocol.OpSucceeded {
		t.Fatalf("create: %+v", op)
	}
	got := h.storedSpec(res.TaskID, 1).Workspace
	want := domain.Workspace{
		Template: "douyin-with-lynx",
		Mounts:   []domain.Mount{{Repo: "douyin", Path: "douyin"}, {Repo: "template-assembler", Path: "lynx"}},
		Profile:  "link-lynx",
	}
	if got.Template != want.Template || !got.Equal(want) {
		t.Errorf("stored workspace = %+v, want %+v", got, want)
	}
	if name := h.task(res.TaskID).WorkspaceName; name != "feature_a" {
		t.Errorf("workspace name = %q, want feature_a", name)
	}
}

func TestCreateTaskWorkspaceChecks(t *testing.T) {
	h := newHarness(t)
	withMounts := func(m ...domain.Mount) domain.Spec {
		s := testSpec
		s.Workspace = domain.Workspace{Mounts: m}
		return s
	}
	templateAndMounts := testSpec
	templateAndMounts.Workspace.Template = "douyin-with-lynx"
	missingTemplate := testSpec
	missingTemplate.Workspace = domain.Workspace{Template: "nope"}

	if op, _ := h.createTask(owner, createTaskPayload{Title: "first", Spec: testSpec, WorkspaceName: "taken"}); op.Status != protocol.OpSucceeded {
		t.Fatalf("seed task: %+v", op)
	}
	cases := []struct {
		name string
		p    createTaskPayload
		msg  string
	}{
		{"unregistered repo", createTaskPayload{Title: "a", Spec: withMounts(domain.Mount{Repo: "chromium"})}, "not registered"},
		{"template and mounts", createTaskPayload{Title: "a", Spec: templateAndMounts}, "not both"},
		{"unknown template", createTaskPayload{Title: "a", Spec: missingTemplate}, "does not exist"},
		{"name taken", createTaskPayload{Title: "a", Spec: testSpec, WorkspaceName: "taken"}, "already used"},
		{"bad name", createTaskPayload{Title: "a", Spec: testSpec, WorkspaceName: "Feature A"}, "single lowercase"},
		{"name not a branch", createTaskPayload{Title: "a", Spec: testSpec, WorkspaceName: "fix.lock"}, "valid in a git branch"},
		{"name with dots", createTaskPayload{Title: "a", Spec: testSpec, WorkspaceName: "fix..build"}, "valid in a git branch"},
		{"no mounts", createTaskPayload{Title: "a", Spec: withMounts()}, "at least one mount"},
	}
	for _, tc := range cases {
		op, _ := h.createTask(owner, tc.p)
		if op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidRequest || !strings.Contains(op.Error.Message, tc.msg) {
			t.Errorf("%s: %+v, want invalid_request mentioning %q", tc.name, op.Error, tc.msg)
		}
	}
}

func TestDefaultWorkspaceName(t *testing.T) {
	h := newHarness(t)
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	name := h.task(res.TaskID).WorkspaceName
	if !strings.HasPrefix(name, "task-") || !domain.NamePattern.MatchString(name) {
		t.Errorf("default workspace name = %q", name)
	}
}

func TestRetiredRepoKeepsExistingTasks(t *testing.T) {
	h := newHarness(t)
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	c := testConfig()
	c.Repos[0].Retired = true // "tend", still served by linux-1
	if op := h.syncConfig(c); op.Status != protocol.OpSucceeded {
		t.Fatalf("sync with a retired repo still served by a host: %+v", op.Error)
	}
	var host store.HostRow
	if err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		var err error
		host, err = store.GetHost(context.Background(), tx.Q(), "linux-1")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(host.Repos, "tend") {
		t.Errorf("host repos = %v, want tend kept for existing tasks", host.Repos)
	}

	if op, _ := h.createTask(owner, createTaskPayload{Title: "b", Spec: testSpec}); op.Status != protocol.OpFailed || !strings.Contains(op.Error.Message, "retired") {
		t.Errorf("new task on retired repo: %+v", op)
	}
	// The existing task can still change its spec while keeping the repo.
	changed := testSpec
	changed.Constraints = []string{"keep the API"}
	op := h.exec(owner, h.env("tasks.update_intent", taskTarget(res.TaskID), updateIntentPayload{Spec: &changed}, rev(res.ControlRevision)), UpdateIntent{})
	if op.Status != protocol.OpSucceeded {
		t.Errorf("update with retired but already mounted repo: %+v", op.Error)
	}
}

func TestConfigSync(t *testing.T) {
	h := newHarness(t)
	c := testConfig()
	c.Hosts = append(c.Hosts, HostConfig{HostID: "mac-1", OS: "darwin", Arch: "arm64", Repos: []string{"douyin"}, Slots: 1,
		TokenHash: strings.Repeat("cd", 32), Nodes: []string{"tag:tend-runner"}})
	h.syncConfig(c)
	c.Hosts = c.Hosts[1:]
	h.syncConfig(c)

	var linux, mac store.HostRow
	if err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		var err error
		if linux, err = store.GetHost(context.Background(), tx.Q(), "linux-1"); err != nil {
			return err
		}
		mac, err = store.GetHost(context.Background(), tx.Q(), "mac-1")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if linux.Status != store.HostRetired || mac.Status != store.HostActive || mac.OS != "darwin" {
		t.Errorf("hosts after resync: linux=%s mac=%s/%s", linux.Status, mac.Status, mac.OS)
	}

	bad := []struct {
		name   string
		mutate func(*ConfigPayload)
	}{
		{"host serves unknown repo", func(c *ConfigPayload) { c.Hosts[0].Repos = []string{"chromium"} }},
		{"bad token hash", func(c *ConfigPayload) { c.Hosts[0].TokenHash = "secret" }},
		{"unsupported os", func(c *ConfigPayload) { c.Hosts[0].OS = "plan9" }},
		{"template on unknown repo", func(c *ConfigPayload) {
			c.Templates[0].Workspace.Mounts = append(c.Templates[0].Workspace.Mounts, domain.Mount{Repo: "chromium"})
		}},
		{"duplicate repo", func(c *ConfigPayload) { c.Repos = append(c.Repos, c.Repos[0]) }},
		{"bad base candidate", func(c *ConfigPayload) { c.Repos[0].BaseCandidates = []string{"-x"} }},
		{"template on retired repo", func(c *ConfigPayload) { c.Repos[1].Retired = true }},
	}
	for _, tc := range bad {
		c := testConfig()
		tc.mutate(&c)
		if op := h.syncConfig(c); op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidRequest {
			t.Errorf("%s: %+v, want invalid_request", tc.name, op)
		}
	}
	envl := h.env("config.sync", protocol.Target{Type: "config"}, testConfig(), nil)
	if _, err := h.app.Execute(context.Background(), owner, envl, SyncConfig{}); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Errorf("owner config.sync: err = %v, want forbidden", err)
	}
}

func TestPinnedWorkspaceRejectsMountChanges(t *testing.T) {
	h := newHarness(t)
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	// Stand in for the claim that creates the workspace (B7).
	if err := h.store.Write(context.Background(), func(tx *store.Tx) error {
		return tx.InsertWorkspace(context.Background(), store.WorkspaceRow{WorkspaceID: "wsp_1", TaskID: res.TaskID, Name: h.task(res.TaskID).WorkspaceName, HostID: "linux-1", Status: store.WorkspaceAssigned}, 0)
	}); err != nil {
		t.Fatal(err)
	}
	if !h.task(res.TaskID).WorkspaceAssigned {
		t.Fatal("WorkspaceAssigned not derived from the workspace row")
	}
	moved := testSpec
	moved.Workspace = domain.Workspace{Mounts: []domain.Mount{{Repo: "douyin"}}}
	op := h.exec(owner, h.env("tasks.update_intent", taskTarget(res.TaskID), updateIntentPayload{Spec: &moved}, rev(res.ControlRevision)), UpdateIntent{})
	if op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidRequest {
		t.Errorf("replace mount after assignment: %+v", op)
	}
	added := testSpec
	added.Workspace = domain.Workspace{Mounts: []domain.Mount{{Repo: "tend"}, {Repo: "douyin"}}}
	op = h.exec(owner, h.env("tasks.update_intent", taskTarget(res.TaskID), updateIntentPayload{Spec: &added}, rev(res.ControlRevision)), UpdateIntent{})
	if op.Status != protocol.OpSucceeded {
		t.Errorf("add mount after assignment: %+v", op.Error)
	}
}
