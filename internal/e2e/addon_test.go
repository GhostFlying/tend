package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/api"
	"github.com/GhostFlying/tend/internal/cli"
	"github.com/GhostFlying/tend/internal/contextbuilder"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/netx"
	"github.com/GhostFlying/tend/internal/protocol"
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
}

func newAddonAgent(t *testing.T, configDir, agent, session string) *addonAgent {
	a := &addonAgent{t: t, agent: agent, session: session, out: &bytes.Buffer{}, vars: map[string]string{}}
	a.env = &cli.Env{
		Stdin: strings.NewReader(""), Stdout: a.out, Stderr: &bytes.Buffer{},
		Getenv: func(k string) string { return a.vars[k] }, Home: t.TempDir(), ConfigDir: configDir,
		HTTP: &http.Client{Timeout: 10 * time.Second}, IDs: protocol.NewULIDGen(time.Now, nil), Executable: "tend",
		Run: func(context.Context, string, ...string) error { return nil },
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
	in, _ := json.Marshal(map[string]string{"session_id": a.session, "source": source, "prompt": prompt})
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
		if got := a.hook("session-start", "startup", ""); !strings.Contains(got, "kind=full]") {
			t.Fatalf("%s session start:\n%s", a.agent, got)
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

	// The session that created the task is not told about it again; the
	// other session is.
	if got := claude.hook("user-prompt-submit", "", "anything new?"); strings.Contains(got, task) {
		t.Errorf("claude hears of its own task:\n%s", got)
	}
	if got := codex.hook("user-prompt-submit", "", "anything new?"); !strings.Contains(got, task) {
		t.Errorf("codex does not hear of the new task:\n%s", got)
	}

	newNode(t, srv, passing).start()
	eventually(t, "the task completes", func() bool {
		return srv.count(`SELECT COUNT(*) FROM tasks WHERE task_id = ? AND lifecycle = 'completed'`, task) == 1
	})
	for _, a := range []*addonAgent{claude, codex} {
		if got := a.hook("user-prompt-submit", "", "how is it going?"); !strings.Contains(got, task+" task.completed") {
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
	if !strings.Contains(got, "tend carried it out (cancel), nothing left to do") || !strings.Contains(got, task+" task.cancelled") {
		t.Errorf("the agent does not hear what tend did:\n%s", got)
	}
}
