package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/api"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/cli"
	"github.com/GhostFlying/tend/internal/contextbuilder"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/netx"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runnerapi"
	"github.com/GhostFlying/tend/internal/tools"
)

const ownerLogin = "me@example.com"

// startAPI serves the owner and agent API to the owner's devices.
func (s *server) startAPI() string {
	cb := contextbuilder.New(s.app, s.store, s.ids, contextbuilder.Config{})
	laptop := netx.Static{Identity: &netx.Identity{Node: "laptop", User: ownerLogin}}
	h := api.New(s.app, s.store, tools.New(s.app, s.store, cb, quiet()), cb, laptop, secret, api.Config{Owners: []string{ownerLogin}}, quiet())
	hs := httptest.NewServer(h.Handler())
	s.t.Cleanup(hs.Close)
	return hs.URL
}

// addonAgent stands in for an agent the owner works in: it runs the tend
// hook and MCP commands the way the agent's configuration would.
type addonAgent struct {
	t       *testing.T
	agent   string
	session string
	env     *cli.Env
	out     *bytes.Buffer
	vars    map[string]string
	// cwd is where the agent works; remotes are the git remotes git
	// reports there.
	cwd     string
	remotes []string
}

func newAddonAgent(t *testing.T, configDir, agent, session string) *addonAgent {
	a := &addonAgent{t: t, agent: agent, session: session, out: &bytes.Buffer{}, vars: map[string]string{}}
	a.env = &cli.Env{
		Stdin: strings.NewReader(""), Stdout: a.out, Stderr: &bytes.Buffer{},
		Getenv: func(k string) string { return a.vars[k] }, Home: t.TempDir(), ConfigDir: configDir,
		HTTP: &http.Client{Timeout: 10 * time.Second}, IDs: protocol.NewULIDGen(time.Now, nil), Executable: "tend",
		Run:   func(context.Context, string, ...string) error { return nil },
		Getwd: func() (string, error) { return a.cwd, nil },
		Output: func(_ context.Context, name string, args ...string) ([]byte, error) {
			var out strings.Builder
			if name == "git" && len(args) > 1 && args[1] == a.cwd {
				for i, r := range a.remotes {
					fmt.Fprintf(&out, "r%d\t%s (fetch)\n", i, r)
				}
			}
			return []byte(out.String()), nil
		},
	}
	if agent == string(protocol.AgentClaude) {
		a.vars["CLAUDE_CODE_SESSION_ID"] = session
	}
	return a
}

func (a *addonAgent) run(stdin string, args ...string) string {
	a.t.Helper()
	a.out.Reset()
	a.env.Stdin = strings.NewReader(stdin)
	if code := cli.Main(context.Background(), a.env, args); code != 0 {
		a.t.Fatalf("tend %v = %d: %s", args, code, a.env.Stderr)
	}
	return a.out.String()
}

// hook runs a hook and returns the context it adds.
func (a *addonAgent) hook(event, source, prompt string) string {
	a.t.Helper()
	in, _ := json.Marshal(map[string]string{"session_id": a.session, "source": source, "prompt": prompt, "cwd": a.cwd})
	out := a.run(string(in), "hook", a.agent, event)
	if out == "" {
		return ""
	}
	var v struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		a.t.Fatalf("hook output %q: %v", out, err)
	}
	return v.HookSpecificOutput.AdditionalContext
}

// call has the model call a tend tool through the MCP bridge and returns
// the tool's text.
func (a *addonAgent) call(tool string, args any) string {
	a.t.Helper()
	params := map[string]any{"name": tool, "arguments": args}
	if a.agent != string(protocol.AgentClaude) {
		params["_meta"] = map[string]any{"x-codex-turn-metadata": map[string]any{"session_id": a.session, "turn_id": "turn-1"}}
	}
	var in bytes.Buffer
	for _, m := range []map[string]any{
		{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-06-18"}},
		{"jsonrpc": "2.0", "method": "notifications/initialized"},
		{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": params},
	} {
		b, _ := json.Marshal(m)
		in.Write(append(b, '\n'))
	}
	dec := json.NewDecoder(strings.NewReader(a.run(in.String(), "mcp", "--agent", a.agent)))
	for dec.More() {
		var m struct {
			ID     int `json:"id"`
			Result struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		if err := dec.Decode(&m); err != nil {
			a.t.Fatal(err)
		}
		if m.ID == 2 {
			if m.Result.IsError || len(m.Result.Content) != 1 {
				a.t.Fatalf("%s = %+v", tool, m.Result)
			}
			return m.Result.Content[0].Text
		}
	}
	a.t.Fatalf("%s: no answer", tool)
	return ""
}

// TestEndToEnd_AgentAddon drives the add-on loop through the tend CLI: two
// agent sessions see the ledger, one creates a task through MCP, a runner
// implements and accepts it, and both sessions hear that it completed.
func TestEndToEnd_AgentAddon(t *testing.T) {
	srv := newServer(t, time.Minute)
	url := srv.startAPI()
	configDir := filepath.Join(t.TempDir(), "tend")
	claude := newAddonAgent(t, configDir, string(protocol.AgentClaude), "sess-claude")
	claude.run("", "login", "--server", url)
	codex := newAddonAgent(t, configDir, string(protocol.AgentCodex), "sess-codex")

	for _, a := range []*addonAgent{claude, codex} {
		// Until a session uses tend, its hooks get nothing (D54).
		if got := a.hook("session-start", "startup", ""); got != "" {
			t.Fatalf("%s session start before using tend:\n%s", a.agent, got)
		}
		if got := a.call("tend_context", map[string]any{}); !strings.Contains(got, "[tend context snapshot") {
			t.Fatalf("%s tend_context:\n%s", a.agent, got)
		}
	}

	var created struct {
		Result struct {
			Outcome struct {
				TaskID string `json:"task_id"`
			} `json:"outcome"`
		} `json:"result"`
	}
	text := claude.call("tasks_create", map[string]any{
		"title": "fix the build", "workspace_name": "fix-build", "reason": "the owner asked for it",
		"spec": domain.Spec{Goal: "fix the build", Acceptance: []string{"build passes"}, Delivery: domain.DeliveryCommit,
			Workspace: domain.Workspace{Mounts: []domain.Mount{{Repo: "tend"}}}},
	})
	if err := json.Unmarshal([]byte(text), &created); err != nil || created.Result.Outcome.TaskID == "" {
		t.Fatalf("tasks_create = %s", text)
	}
	task := created.Result.Outcome.TaskID

	// The first prompt after a session started using tend gets the whole
	// ledger, the new task included.
	for _, a := range []*addonAgent{claude, codex} {
		if got := a.hook("user-prompt-submit", "", "anything new?"); !strings.Contains(got, "kind=full]") || !strings.Contains(got, task) {
			t.Errorf("%s first prompt:\n%s", a.agent, got)
		}
	}

	newNode(t, srv, passing).start()
	eventually(t, "the task completes", func() bool {
		return srv.count(`SELECT COUNT(*) FROM tasks WHERE task_id = ? AND lifecycle = 'completed'`, task) == 1
	})
	for _, a := range []*addonAgent{claude, codex} {
		if got := a.hook("user-prompt-submit", "", "how is it going?"); !strings.Contains(got, task+` "fix the build" task.completed`) || !strings.Contains(got, "(by tend)") {
			t.Errorf("%s does not hear that the task completed:\n%s", a.agent, got)
		}
		// Nothing new since.
		if got := a.hook("user-prompt-submit", "", "and now?"); got != "" {
			t.Errorf("%s is told again:\n%s", a.agent, got)
		}
	}

	// The owner sees the same from the command line.
	owner := newAddonAgent(t, configDir, "", "")
	var d protocol.TaskDetail
	if err := json.Unmarshal([]byte(owner.run("", "task", task, "--json")), &d); err != nil || d.Lifecycle != "completed" {
		t.Errorf("tend task = %+v, %v", d, err)
	}
}

// TestEndToEnd_OwnerAnswersDecision has the owner answer a decision tend
// opened from the command line: tend carries out the chosen option, and
// the agent hears on its next prompt that nothing is left to do.
func TestEndToEnd_OwnerAnswersDecision(t *testing.T) {
	srv := newServer(t, time.Minute)
	url := srv.startAPI()
	configDir := filepath.Join(t.TempDir(), "tend")
	claude := newAddonAgent(t, configDir, string(protocol.AgentClaude), "sess-claude")
	claude.run("", "login", "--server", url)
	claude.call("tend_context", map[string]any{})
	claude.hook("session-start", "startup", "")

	n := newNode(t, srv, passing)
	n.ws.noChanges = true
	n.start()
	task := srv.createTask()
	eventually(t, "the task waits for a decision", func() bool { return srv.waitsForDecision(task) })
	if got := claude.hook("user-prompt-submit", "", "anything new?"); !strings.Contains(got, "cancel the task (tend carries it out)") {
		t.Errorf("the agent does not hear which options tend carries out:\n%s", got)
	}

	owner := newAddonAgent(t, configDir, "", "")
	var s protocol.Snapshot
	if err := json.Unmarshal([]byte(owner.run("", "status", "--json")), &s); err != nil || len(s.Decisions) != 1 {
		t.Fatalf("tend status = %+v, %v", s, err)
	}
	if out := owner.run("", "decide", s.Decisions[0].DecisionID, "cancel the task"); !strings.Contains(out, "tend carried it out: cancel") {
		t.Errorf("tend decide printed:\n%s", out)
	}
	if got := srv.task(task).Lifecycle; got != protocol.TaskCancelled {
		t.Errorf("task = %s, want cancelled", got)
	}
	got := claude.hook("user-prompt-submit", "", "and now?")
	if !strings.Contains(got, "answered by owner: ") || !strings.Contains(got, "tend carried it out (cancel), nothing left to do") ||
		!strings.Contains(got, task+` "fix it" task.cancelled (by owner)`) {
		t.Errorf("the agent does not hear what tend did:\n%s", got)
	}
}

// TestEndToEnd_ProjectsScopeContext has two sessions work in the
// repositories of two projects: each sees its own project's tasks, and
// hears from the other only what needs attention (D59).
func TestEndToEnd_ProjectsScopeContext(t *testing.T) {
	srv := newServer(t, time.Minute)
	srv.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, "config.sync", protocol.Target{Type: "config"}, app.ConfigPayload{
		Repos: []app.RepoConfig{
			{Repo: "tend", RemoteURL: "https://example.com/tend.git"},
			{Repo: "site", RemoteURL: "https://example.com/site.git"},
		},
		Hosts: []app.HostConfig{{HostID: "linux-1", OS: "linux", Arch: "amd64", Repos: []string{"tend", "site"}, Slots: 2,
			TokenHash: runnerapi.HashToken(hostToken), Nodes: []string{"tag:tend-runner"}}},
		Projects: []app.ProjectConfig{{Name: "core", Repos: []string{"tend"}}, {Name: "web", Repos: []string{"site"}}},
	}, app.SyncConfig{})
	url := srv.startAPI()
	configDir := filepath.Join(t.TempDir(), "tend")
	core := newAddonAgent(t, configDir, string(protocol.AgentClaude), "sess-core")
	core.run("", "login", "--server", url)
	core.cwd, core.remotes = "/work/tend", []string{"git@example.com:tend.git"}
	web := newAddonAgent(t, configDir, string(protocol.AgentCodex), "sess-web")
	web.cwd, web.remotes = "/work/site", []string{"https://example.com/site"}

	create := func(a *addonAgent, title, workspace, repo string) string {
		t.Helper()
		var created struct {
			Result struct {
				Outcome struct {
					TaskID string `json:"task_id"`
				} `json:"outcome"`
			} `json:"result"`
		}
		text := a.call("tasks_create", map[string]any{
			"title": title, "workspace_name": workspace, "reason": "the owner asked for it",
			"spec": domain.Spec{Goal: title, Acceptance: []string{"done"}, Delivery: domain.DeliveryCommit,
				Workspace: domain.Workspace{Mounts: []domain.Mount{{Repo: repo}}}},
		})
		if err := json.Unmarshal([]byte(text), &created); err != nil || created.Result.Outcome.TaskID == "" {
			t.Fatalf("tasks_create = %s", text)
		}
		return created.Result.Outcome.TaskID
	}
	coreTask := create(core, "speed up the ledger", "speed-up", "tend")
	webTask := create(web, "redo the landing page", "landing", "site")
	if p := srv.count(`SELECT COUNT(*) FROM tasks WHERE task_id = ? AND project = 'core'`, coreTask); p != 1 {
		t.Errorf("the core session's task is not in core")
	}
	if p := srv.count(`SELECT COUNT(*) FROM tasks WHERE task_id = ? AND project = 'web'`, webTask); p != 1 {
		t.Errorf("the web session's task is not in web")
	}

	got := core.hook("user-prompt-submit", "", "what is open?")
	if !strings.Contains(got, "Project: core") || !strings.Contains(got, coreTask) || strings.Contains(got, webTask) {
		t.Fatalf("the core session's first context:\n%s", got)
	}
	if got := web.hook("user-prompt-submit", "", "what is open?"); !strings.Contains(got, "Project: web") || !strings.Contains(got, webTask) || strings.Contains(got, coreTask) {
		t.Fatalf("the web session's first context:\n%s", got)
	}

	// A question about the web task reaches the core session too.
	web.call("decisions_open", map[string]any{"task_id": webTask, "question": "Keep the old hero image?", "options": []string{"keep", "replace"}})
	if got := core.hook("user-prompt-submit", "", "anything new?"); !strings.Contains(got, "Other projects, needs attention") || !strings.Contains(got, "[web]") {
		t.Errorf("the core session missed the web decision:\n%s", got)
	}
}
