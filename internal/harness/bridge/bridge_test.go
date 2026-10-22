package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

var verdictTool = harness.ToolSpec{Name: "submit_verdict", Description: "deliver the verdict", InputSchema: json.RawMessage(`{"type":"object"}`)}

// socketPath is short enough for a unix socket on every platform.
func socketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "tb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s")
}

type calls struct {
	mu  sync.Mutex
	got []harness.ToolCall
}

func (c *calls) handle(_ context.Context, call harness.ToolCall) harness.ToolResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, call)
	return harness.ToolResult{Success: true, Text: "verdict recorded"}
}

func listen(t *testing.T, token string) (*Server, string, *calls) {
	t.Helper()
	path := socketPath(t)
	c := &calls{}
	s, err := Listen(path, token, []harness.ToolSpec{verdictTool}, c.handle)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path, c
}

func writeState(t *testing.T, st State) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), protocol.WorkerMarker)
	b, _ := json.Marshal(st)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// session runs ServeMCP over msgs and returns its answers by ID.
func session(t *testing.T, state string, msgs ...string) map[string]rpcMessage {
	t.Helper()
	var out bytes.Buffer
	if err := ServeMCP(context.Background(), state, "run_1", strings.NewReader(strings.Join(msgs, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	got := map[string]rpcMessage{}
	for line := range strings.Lines(out.String()) {
		var m struct {
			rpcMessage
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatal(err)
		}
		m.rpcMessage.Result = m.Result
		got[string(m.ID)] = m.rpcMessage
	}
	return got
}

func decode[T any](t *testing.T, m rpcMessage) T {
	t.Helper()
	var v T
	if m.Error != nil {
		t.Fatalf("error %+v", m.Error)
	}
	if err := json.Unmarshal(m.Result.(json.RawMessage), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestBridgeServesTheRunsTools(t *testing.T) {
	_, sock, c := listen(t, "tok")
	state := writeState(t, State{Run: "run_1", Socket: sock, Token: "tok"})
	got := session(t, state,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"submit_verdict","arguments":{"verdict":"pass"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"tasks_list"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"ping"}`,
	)
	if v := decode[struct {
		ProtocolVersion string `json:"protocolVersion"`
	}](t, got["1"]); v.ProtocolVersion != "2025-03-26" {
		t.Errorf("initialize = %+v", v)
	}
	if l := decode[struct {
		Tools []mcpTool `json:"tools"`
	}](t, got["2"]); len(l.Tools) != 1 || l.Tools[0].Name != "submit_verdict" || string(l.Tools[0].InputSchema) != `{"type":"object"}` {
		t.Errorf("tools/list = %+v", l)
	}
	if r := decode[callResult](t, got["3"]); r.IsError || r.Content[0].Text != "verdict recorded" {
		t.Errorf("submit_verdict = %+v", r)
	}
	if r := decode[callResult](t, got["4"]); !r.IsError {
		t.Errorf("unknown tool = %+v, want an error", r)
	}
	if _, ok := got["5"]; !ok || len(got) != 5 {
		t.Errorf("answers = %v", got)
	}
	if len(c.got) != 1 || c.got[0].Tool != "submit_verdict" || string(c.got[0].Arguments) != `{"verdict":"pass"}` {
		t.Errorf("handled = %+v", c.got)
	}
}

func TestBridgeRefusesAnotherRunsToken(t *testing.T) {
	_, sock, c := listen(t, "tok")
	got := session(t, writeState(t, State{Run: "run_1", Socket: sock, Token: "other"}),
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"submit_verdict","arguments":{}}}`)
	if got["1"].Error == nil {
		t.Errorf("tools/list with a wrong token = %+v", got["1"])
	}
	if r := decode[callResult](t, got["2"]); !r.IsError {
		t.Errorf("call with a wrong token = %+v", r)
	}
	if len(c.got) != 0 {
		t.Errorf("handled %d calls with a wrong token", len(c.got))
	}
}

func TestBridgeAfterTheRunEnded(t *testing.T) {
	s, sock, _ := listen(t, "tok")
	if fi, err := os.Stat(sock); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket = %v, %v; want mode 0600", fi, err)
	}
	state := writeState(t, State{Run: "run_1", Socket: sock, Token: "tok"})
	s.Close()
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Errorf("socket left behind: %v", err)
	}
	if r := decode[callResult](t, session(t, state, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"submit_verdict"}}`)["1"]); !r.IsError {
		t.Errorf("call after close = %+v", r)
	}
	os.Remove(state)
	if got := session(t, state, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); got["1"].Error == nil || !strings.Contains(got["1"].Error.Message, "no run") {
		t.Errorf("tools/list without state = %+v", got["1"])
	}
}

func TestBridgeKeepsAnAgentToItsRun(t *testing.T) {
	_, sock, _ := listen(t, "tok")
	_, later, c := listen(t, "later")
	state := writeState(t, State{Run: "run_1", Socket: sock, Token: "tok"})
	// A later run in the same worktree writes its own state while the
	// first run's agent keeps its MCP server.
	b, _ := json.Marshal(State{Run: "run_2", Socket: later, Token: "later"})
	if err := os.WriteFile(state, b, 0o600); err != nil {
		t.Fatal(err)
	}
	got := session(t, state,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"submit_verdict","arguments":{"verdict":"passed"}}}`)
	if got["1"].Error == nil || !strings.Contains(got["1"].Error.Message, "another run") {
		t.Errorf("tools/list = %+v, want refused", got["1"])
	}
	if r := decode[callResult](t, got["2"]); !r.IsError || len(c.got) != 0 {
		t.Errorf("call = %+v, handled %d; want refused", r, len(c.got))
	}
}

func TestBridgeCloseDoesNotWaitForIdleClients(t *testing.T) {
	s, sock, _ := listen(t, "tok")
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	done := make(chan struct{})
	go func() {
		s.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close waited for a client that sent nothing")
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// linked makes a repository with one commit and a linked worktree of it.
func linked(t *testing.T) (repo, wt string) {
	t.Helper()
	root := t.TempDir()
	repo, wt = filepath.Join(root, "repo"), filepath.Join(root, "wt")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	runGit(t, repo, "worktree", "add", "-q", "-b", "tend/run", wt)
	return repo, wt
}

func TestInstallConfiguresTheAgent(t *testing.T) {
	ctx := context.Background()
	repo, wt := linked(t)
	st := State{Run: "run_1", Socket: "/run/s", Token: "tok"}
	for _, agent := range []protocol.AgentKind{protocol.AgentCodex, protocol.AgentTraex} {
		// Twice: a retried launch writes again.
		for range 2 {
			if err := Install(ctx, wt, agent, `/opt/tend "runner"`, []string{"worker-mcp"}, st); err != nil {
				t.Fatalf("%s: %v", agent, err)
			}
		}
	}
	state, err := StatePath(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(state, filepath.Join(repo, ".git", "worktrees")) {
		t.Errorf("state at %s, want in the worktree's git directory", state)
	}
	if fi, err := os.Stat(state); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("state = %v, %v; want mode 0600", fi, err)
	}
	b, _ := os.ReadFile(filepath.Join(wt, ".codex", "config.toml"))
	want := configHeader + "\napproval_policy = \"never\"\nsandbox_mode = \"danger-full-access\"\ncheck_for_update_on_startup = false\n" +
		"\n[mcp_servers.tend_worker]\ncommand = \"/opt/tend \\\"runner\\\"\"\nargs = [\"worker-mcp\", \"--state\", \"" + state + "\", \"--run\", \"run_1\"]\n" +
		"default_tools_approval_mode = \"approve\"\n"
	if string(b) != want {
		t.Errorf("config =\n%s\nwant\n%s", b, want)
	}
	if _, err := os.Stat(filepath.Join(wt, ".trae", "traecli.toml")); err != nil {
		t.Error(err)
	}
	// Neither the work nor the owner's checkout sees the files.
	if s := runGit(t, wt, "status", "--porcelain", "--untracked-files=all"); s != "" {
		t.Errorf("worktree status = %q", s)
	}
	exclude, _ := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if n := strings.Count(string(exclude), "/.codex/config.toml\n"); n != 1 {
		t.Errorf("exclude has the codex line %d times:\n%s", n, exclude)
	}

	for _, agent := range []protocol.AgentKind{protocol.AgentCodex, protocol.AgentTraex} {
		if err := Uninstall(ctx, wt, agent); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{state, filepath.Join(wt, ".codex"), filepath.Join(wt, ".trae")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s left behind: %v", p, err)
		}
	}
	// Again, as after a restart that finds nothing.
	if err := Uninstall(ctx, wt, protocol.AgentCodex); err != nil {
		t.Errorf("second uninstall: %v", err)
	}
}

func TestInstallLeavesOthersConfigurationAlone(t *testing.T) {
	ctx := context.Background()
	_, wt := linked(t)
	cfg := filepath.Join(wt, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("model = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Install(ctx, wt, protocol.AgentCodex, "/opt/tend-runner", []string{"worker-mcp"}, State{Run: "run_1", Socket: "/run/s", Token: "tok"})
	if err == nil || !strings.Contains(err.Error(), "not tend's") {
		t.Fatalf("install over the repository's config = %v", err)
	}
	if err := Uninstall(ctx, wt, protocol.AgentCodex); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(cfg); string(b) != "model = \"x\"\n" {
		t.Errorf("config = %q, want it untouched", b)
	}
	if err := Install(ctx, wt, protocol.AgentClaude, "/opt/tend-runner", nil, State{Run: "run_1", Socket: "/run/s", Token: "tok"}); err == nil {
		t.Error("install for claude succeeded")
	}
}

func TestInstallPrivateInOrdinaryDirectory(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	private := t.TempDir()
	st := State{Run: "run_plain", Socket: "/run/s", Token: "secret-run-token"}
	if err := InstallPrivate(ctx, dir, protocol.AgentCodex, "tend-runner", []string{"worker-mcp"}, st, private); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(private, protocol.WorkerMarker)
	b, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), st.Token) {
		t.Fatal("private state missing token")
	}
	cfg, err := os.ReadFile(filepath.Join(dir, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cfg), st.Token) || !strings.Contains(string(cfg), state) {
		t.Fatal("credentials must stay in private state")
	}
	if err := UninstallPrivate(ctx, dir, protocol.AgentCodex, private); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("private state remains: %v", err)
	}
}
