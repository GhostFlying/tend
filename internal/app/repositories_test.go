package app

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestOnlineRepositorySurvivesBootstrapAndHonorsRevision(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	payload := protocol.RegisterRepositoryPayload{RemoteURL: "git@example.com:team/new.git", BaseCandidates: []string{"release"}}
	env := h.env("repos.register", protocol.Target{Type: "repo", ID: "new"}, payload, rev(0))
	first := h.exec(owner, env, RegisterRepository{})
	if first.Status != protocol.OpSucceeded {
		t.Fatal(first)
	}
	if again := h.exec(owner, env, RegisterRepository{}); string(again.Result) != string(first.Result) {
		t.Fatal("request replay changed", again)
	}
	if op := h.exec(owner, h.env("repos.register", env.Target, payload, rev(0)), RegisterRepository{}); op.Error == nil || op.Error.Code != protocol.CodeStaleRevision {
		t.Fatal("stale registration accepted", op)
	}
	h.syncConfig(testConfig())
	config := testConfig()
	config.Repos = append(config.Repos, RepoConfig{Repo: "new", RemoteURL: "https://wrong.example/new.git", Retired: true})
	h.syncConfig(config)
	if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
		row, err := store.GetRepo(ctx, tx.Q(), "new")
		if err != nil {
			return err
		}
		if row.Retired || row.RemoteURL != payload.RemoteURL || !slices.Equal(row.BaseCandidates, payload.BaseCandidates) {
			t.Fatalf("bootstrap replaced online catalog: %+v", row)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	payload.Retired = true
	if op := h.exec(owner, h.env("repos.register", env.Target, payload, rev(1)), RegisterRepository{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.syncConfig(testConfig())
	payload.RemoteURL = "https://other.example/new.git"
	if op := h.exec(owner, h.env("repos.register", env.Target, payload, rev(2)), RegisterRepository{}); op.Error == nil || op.Error.Code != protocol.CodeInvalidRequest {
		t.Fatal("changed repository identity", op)
	}
	if op, err := h.app.Execute(ctx, mainAgent, h.env("repos.register", env.Target, payload, rev(2)), RegisterRepository{}); err == nil && op.Status != protocol.OpFailed {
		t.Fatal("agent registered owner resource")
	}
}

func TestRunnerRepositoryObservationEnablesNewClaimsWithoutRestart(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	runtime := testRuntime("same-process")
	h.versionedHello("linux-1", runtime)
	payload := protocol.RegisterRepositoryPayload{RemoteURL: "git@example.com:team/new.git"}
	if op := h.exec(owner, h.env("repos.register", protocol.Target{Type: "repo", ID: "new"}, payload, rev(0)), RegisterRepository{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	getHost := func() store.HostRow {
		t.Helper()
		var host store.HostRow
		if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
			var err error
			host, err = store.GetHost(ctx, tx.Q(), "linux-1")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return host
	}
	if slices.Contains(getHost().Repos, "new") {
		t.Fatal("registration alone admitted an unprepared runner")
	}
	runtime.Repositories = []protocol.RunnerRepository{{Repo: "new", RemoteIdentity: protocol.RepositoryRemoteKey("https://wrong.example/new.git")}}
	runtime.Sequence++
	if _, err := h.app.RuntimeHeartbeat(ctx, runner("linux-1"), protocol.HeartbeatPayload{Runtime: runtime}); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(getHost().Repos, "new") {
		t.Fatal("mismatched remote admitted")
	}
	runtime.Repositories[0].RemoteIdentity = protocol.RepositoryRemoteKey(payload.RemoteURL)
	runtime.Sequence++
	if _, err := h.app.RuntimeHeartbeat(ctx, runner("linux-1"), protocol.HeartbeatPayload{Runtime: runtime}); err != nil {
		t.Fatal(err)
	}
	host := getHost()
	if !slices.Contains(host.Repos, "new") || slices.Contains(host.ConfiguredRepos, "new") {
		t.Fatal("dynamic and configured repositories lost their boundary", host)
	}
	// Configuration replay compares bootstrap facts, not the dynamic union.
	h.syncConfig(testConfig())
	if !slices.Contains(getHost().Repos, "new") {
		t.Fatal("bootstrap erased dynamic admission")
	}
	spec := testSpec
	spec.Workspace = domain.Workspace{Mounts: []domain.Mount{{Repo: "new"}}}
	op, created := h.createTask(owner, createTaskPayload{Title: "online repository", Spec: spec})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	claim := h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Worker: h.worker("linux-1"), WorkspaceProtocol: 1, Runtime: runtime}, nil)
	assignment, ok := h.claimWith("linux-1", claim)
	if !ok || assignment.TaskID != created.TaskID || assignment.Workspace.Mounts[0].RemoteURL != payload.RemoteURL {
		t.Fatal("new work not assigned", assignment)
	}
	// A new incarnation that has lost its local binding must not keep admission.
	next := testRuntime("replacement-process")
	h.versionedHello("linux-1", next)
	if slices.Contains(getHost().Repos, "new") {
		t.Fatal("lost binding survived new runner observation")
	}
	runtime.Sequence++
	if _, err := h.app.RuntimeHeartbeat(ctx, runner("linux-1"), protocol.HeartbeatPayload{Runtime: runtime}); err == nil {
		t.Fatal("retired process resurrected its binding")
	}
	var recorded protocol.RepositoryRegistration
	if err := json.Unmarshal(h.exec(owner, h.env("repos.register", protocol.Target{Type: "repo", ID: "new"}, payload, rev(1)), RegisterRepository{}).Result, &recorded); err != nil {
		t.Fatal(err)
	}
	if recorded.ControlRevision != 2 {
		t.Fatal(recorded)
	}
}

func TestOnlineRetirementPreservesBootstrapTemplateReferences(t *testing.T) {
	h := newHarness(t)
	payload := protocol.RegisterRepositoryPayload{RemoteURL: "https://example.com/douyin.git", Retired: true}
	op := h.exec(owner, h.env("repos.register", protocol.Target{Type: "repo", ID: "douyin"}, payload, rev(0)), RegisterRepository{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.syncConfig(testConfig()); op.Status != protocol.OpSucceeded {
		t.Fatal("unchanged bootstrap rejected", op)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		row, err := store.GetRepo(t.Context(), tx.Q(), "douyin")
		if err != nil {
			return err
		}
		if !row.Retired {
			t.Fatal("bootstrap revived retired repository")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	spec := testSpec
	spec.Workspace = domain.Workspace{Template: "douyin-with-lynx"}
	if op, _ := h.createTask(owner, createTaskPayload{Title: "retired template", Spec: spec}); op.Status != protocol.OpFailed {
		t.Fatal("retired repository accepted through template", op)
	}
}
