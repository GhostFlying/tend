package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

// fakeServer records requests and answers them with handlers by path.
type fakeServer struct {
	t        *testing.T
	mu       sync.Mutex
	requests []recorded
	handlers map[string]func(w http.ResponseWriter, r *http.Request, body []byte)
	srv      *httptest.Server
}

type recorded struct {
	method, path, auth string
	body               []byte
}

func newFakeServer(t *testing.T) *fakeServer {
	f := &fakeServer{t: t, handlers: map[string]func(http.ResponseWriter, *http.Request, []byte){}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, recorded{r.Method, r.URL.Path, r.Header.Get("Authorization"), body})
		h := f.handlers[r.Method+" "+r.URL.Path]
		f.mu.Unlock()
		if h == nil {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(protocol.APIResponse{Error: protocol.Errorf(protocol.CodeNotFound, "no route")})
			return
		}
		h(w, r, body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) on(route string, h func(w http.ResponseWriter, r *http.Request, body []byte)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[route] = h
}

// answer registers a handler that always answers v.
func (f *fakeServer) answer(route string, v any) {
	f.on(route, func(w http.ResponseWriter, _ *http.Request, _ []byte) { _ = json.NewEncoder(w).Encode(v) })
}

func (f *fakeServer) seen(route string) []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []recorded
	for _, r := range f.requests {
		if r.method+" "+r.path == route {
			out = append(out, r)
		}
	}
	return out
}

type testEnv struct {
	*Env
	out, errOut *bytes.Buffer
	vars        map[string]string
	runs        [][]string
	mcpAdded    bool
}

func newTestEnv(t *testing.T, server string) *testEnv {
	t.Helper()
	te := &testEnv{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, vars: map[string]string{}}
	dir := t.TempDir()
	te.Env = &Env{
		Stdin: strings.NewReader(""), Stdout: te.out, Stderr: te.errOut,
		Getenv: func(k string) string { return te.vars[k] },
		Home:   filepath.Join(dir, "home"), ConfigDir: filepath.Join(dir, "config"),
		HTTP: &http.Client{Timeout: 10 * time.Second}, IDs: protocol.NewULIDGen(time.Now, nil),
		Executable: "/opt/tend/bin/tend",
		LookPath:   func(string) (string, error) { return "", os.ErrNotExist },
		Run: func(_ context.Context, name string, args ...string) error {
			te.runs = append(te.runs, append([]string{name}, args...))
			switch {
			case len(args) > 1 && args[1] == "get" && !te.mcpAdded:
				return os.ErrNotExist
			case len(args) > 1 && args[1] == "add":
				te.mcpAdded = true
			}
			return nil
		},
	}
	if server != "" {
		if err := te.saveConfig(config{Server: server}, credentials{ClientID: "cli-0011223344556677", Owner: "owner-token", Agent: "agent-token"}); err != nil {
			t.Fatal(err)
		}
	}
	return te
}

func (te *testEnv) main(args ...string) int { return Main(context.Background(), te.Env, args) }

func TestLoginKeepsTheClientID(t *testing.T) {
	f := newFakeServer(t)
	f.on("POST /api/v1/session", func(w http.ResponseWriter, _ *http.Request, body []byte) {
		var req protocol.SessionRequest
		_ = json.Unmarshal(body, &req)
		_ = json.NewEncoder(w).Encode(protocol.SessionView{Login: "me@example.com", ClientID: req.ClientID, Scope: req.Scope, Token: string(req.Scope) + "-token", ExpiresAt: time.Now()})
	})
	te := newTestEnv(t, "")
	if code := te.main("login"); code != exitUsage {
		t.Errorf("login without a server = %d, want usage", code)
	}
	if code := te.main("login", "--server", f.srv.URL); code != exitOK {
		t.Fatalf("login = %d: %s", code, te.errOut)
	}
	_, first, err := te.loadConfig()
	if err != nil || first.Owner != "owner-token" || first.Agent != "agent-token" || !strings.HasPrefix(first.ClientID, "cli-") {
		t.Fatalf("credentials = %+v, %v", first, err)
	}
	if st, err := os.Stat(filepath.Join(te.ConfigDir, "credentials.json")); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("credentials file: %v, %v", st.Mode(), err)
	}
	// Logging in again, to the same server, keeps the installation's ID.
	if code := te.main("login"); code != exitOK {
		t.Fatalf("second login = %d: %s", code, te.errOut)
	}
	if _, again, _ := te.loadConfig(); again.ClientID != first.ClientID {
		t.Errorf("client ID changed from %s to %s", first.ClientID, again.ClientID)
	}
}

func TestHookForwardsAndAnswers(t *testing.T) {
	f := newFakeServer(t)
	f.answer("POST /api/v1/agent/hook", protocol.HookResponse{Context: "[tend context packet p]"})
	te := newTestEnv(t, f.srv.URL)
	te.Stdin = strings.NewReader(`{"session_id":"s-1","hook_event_name":"SessionStart","source":"compact","cwd":"/x"}`)
	if code := te.main("hook", "codex", "session-start"); code != exitOK {
		t.Fatalf("hook = %d", code)
	}
	var out struct {
		Specific struct {
			Event   string `json:"hookEventName"`
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(te.out.Bytes(), &out); err != nil || out.Specific.Event != "SessionStart" || out.Specific.Context != "[tend context packet p]" {
		t.Errorf("hook output %q: %v", te.out, err)
	}
	reqs := f.seen("POST /api/v1/agent/hook")
	var req protocol.HookRequest
	if len(reqs) != 1 || json.Unmarshal(reqs[0].body, &req) != nil || reqs[0].auth != "Bearer agent-token" ||
		req.Agent != protocol.AgentCodex || req.Event != protocol.HookSessionStart || req.SessionID != "s-1" || req.Source != "compact" || req.RequestID == "" {
		t.Errorf("hook requests = %+v", reqs)
	}
	// Nothing to add: no output at all.
	f.answer("POST /api/v1/agent/hook", protocol.HookResponse{})
	te.out.Reset()
	te.Stdin = strings.NewReader(`{"session_id":"s-1","prompt":"hi"}`)
	if code := te.main("hook", "codex", "user-prompt-submit"); code != exitOK || te.out.Len() != 0 {
		t.Errorf("empty answer = %d %q", code, te.out)
	}
}

func TestFault_HookServerDown(t *testing.T) {
	// A server that never answers in time, and one that is not there.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer slow.Close()
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	for name, url := range map[string]string{"slow": slow.URL, "gone": gone.URL} {
		te := newTestEnv(t, url)
		te.vars["TEND_HOOK_TIMEOUT"] = "500ms"
		te.Stdin = strings.NewReader(`{"session_id":"s-1","prompt":"hi"}`)
		start := time.Now()
		code := te.main("hook", "claude", "user-prompt-submit")
		if took := time.Since(start); took > 2*time.Second {
			t.Errorf("%s: the hook took %v", name, took)
		}
		if code != exitOK || !strings.Contains(te.out.String(), "tend is unreachable") {
			t.Errorf("%s: hook = %d %q", name, code, te.out)
		}
	}
	// Whatever goes wrong, the hook never blocks the user's input.
	te := newTestEnv(t, "")
	for _, args := range [][]string{{"hook"}, {"hook", "cursor", "session-start"}, {"hook", "claude", "stop"}, {"hook", "claude", "session-start"}} {
		te.Stdin = strings.NewReader(`{"session_id":"s-1"}`)
		if code := te.main(args...); code != exitOK {
			t.Errorf("%v = %d, want 0", args, code)
		}
	}
}

func TestHookStalledInput(t *testing.T) {
	f := newFakeServer(t)
	te := newTestEnv(t, f.srv.URL)
	te.vars["TEND_HOOK_TIMEOUT"] = "200ms"
	r, w := io.Pipe()
	defer w.Close()
	go w.Write([]byte(`{"session_id":"s-1",`)) // and nothing more
	te.Stdin = r
	start := time.Now()
	if code := te.main("hook", "codex", "user-prompt-submit"); code != exitOK || time.Since(start) > 2*time.Second {
		t.Errorf("hook with stalled input = %d after %v", code, time.Since(start))
	}
	if n := len(f.seen("POST /api/v1/agent/hook")); !strings.Contains(te.out.String(), "tend is unreachable") || n != 0 {
		t.Errorf("hook with stalled input printed %q after %d requests", te.out, n)
	}
}

func TestMCPStopsOnCancel(t *testing.T) {
	te := newTestEnv(t, "")
	r, w := io.Pipe()
	defer w.Close()
	te.Stdin = r
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int)
	go func() { done <- Main(ctx, te.Env, []string{"mcp", "--agent", "claude"}) }()
	cancel()
	select {
	case code := <-done:
		if code != exitOK {
			t.Errorf("mcp after cancel = %d: %s", code, te.errOut)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("mcp kept waiting for stdin after cancel")
	}
}

func TestHookOffForWorkers(t *testing.T) {
	f := newFakeServer(t)
	te := newTestEnv(t, f.srv.URL)
	te.vars[AddonEnv] = "off"
	te.Stdin = strings.NewReader(`{"session_id":"s-1"}`)
	if code := te.main("hook", "traex", "session-start"); code != exitOK || te.out.Len() != 0 || len(f.seen("POST /api/v1/agent/hook")) != 0 {
		t.Errorf("hook with the add-on off = %d %q, %d requests", code, te.out, len(f.requests))
	}
}

func TestAddonOffInWorkerWorktrees(t *testing.T) {
	f := newFakeServer(t)
	gitDir := t.TempDir()
	worker := func() *testEnv {
		te := newTestEnv(t, f.srv.URL)
		te.Getwd = func() (string, error) { return "/work/tend", nil }
		te.Output = func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == "git" && slices.Equal(args, []string{"-C", "/work/tend", "rev-parse", "--absolute-git-dir"}) {
				return []byte(gitDir + "\n"), nil
			}
			return nil, os.ErrNotExist
		}
		return te
	}
	if te := worker(); te.addonOff(context.Background()) {
		t.Fatal("add-on off in a worktree without the marker")
	}
	if err := os.WriteFile(filepath.Join(gitDir, protocol.WorkerMarker), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	te := worker()
	te.Stdin = strings.NewReader(`{"session_id":"s-1"}`)
	if code := te.main("hook", "codex", "session-start"); code != exitOK || te.out.Len() != 0 {
		t.Errorf("hook in a worker's worktree = %d %q", code, te.out)
	}
	got := mcpSession(t, worker(), "codex", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if list := result[struct {
		Tools []mcpTool `json:"tools"`
	}](t, got["1"]); len(list.Tools) != 0 {
		t.Errorf("tools in a worker's worktree = %+v", list.Tools)
	}
	if len(f.requests) != 0 {
		t.Errorf("the add-on asked tend %d times in a worker's worktree", len(f.requests))
	}
}

// mcpSession runs the bridge over a list of messages and returns its
// answers by ID.
func mcpSession(t *testing.T, te *testEnv, agent string, msgs ...any) map[string]rpcMessage {
	t.Helper()
	var in bytes.Buffer
	for _, m := range msgs {
		b, _ := json.Marshal(m)
		in.Write(append(b, '\n'))
	}
	te.Stdin = &in
	if code := te.main("mcp", "--agent", agent); code != exitOK {
		t.Fatalf("mcp = %d: %s", code, te.errOut)
	}
	out := map[string]rpcMessage{}
	dec := json.NewDecoder(te.out)
	for dec.More() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *rpcError       `json:"error"`
		}
		if err := dec.Decode(&m); err != nil {
			t.Fatal(err)
		}
		out[string(m.ID)] = rpcMessage{ID: m.ID, Result: m.Result, Error: m.Error}
	}
	return out
}

type callResult struct {
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

func result[T any](t *testing.T, m rpcMessage) T {
	t.Helper()
	var v T
	raw, _ := m.Result.(json.RawMessage)
	if m.Error != nil || json.Unmarshal(raw, &v) != nil {
		t.Fatalf("answer %s: %+v", m.ID, m)
	}
	return v
}

func TestMCPBridge(t *testing.T) {
	f := newFakeServer(t)
	f.answer("GET /api/v1/agent/tools", []protocol.ToolSpec{{Name: "tasks_list", Description: "List tasks.", InputSchema: json.RawMessage(`{"type":"object"}`)}})
	attempts := 0
	f.on("POST /api/v1/agent/tools/tasks_create", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		attempts++
		if attempts == 1 {
			// The answer is lost on the way back.
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(protocol.ToolResponse{Success: true, Content: `{"ok":true}`})
	})
	f.on("POST /api/v1/agent/tools/tasks_read", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(protocol.APIResponse{Error: protocol.Errorf(protocol.CodeForbidden, "agent tokens only")})
	})
	te := newTestEnv(t, f.srv.URL)
	meta := map[string]any{"x-codex-turn-metadata": map[string]any{"session_id": "sess-9", "turn_id": "t1"}, "sessionId": "sess-9"}
	got := mcpSession(t, te, "codex",
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-03-26"}},
		map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"},
		map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{"name": "tasks_create", "arguments": map[string]any{"title": "x"}, "_meta": meta}},
		map[string]any{"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": map[string]any{"name": "tasks_read", "arguments": map[string]any{}}},
		map[string]any{"jsonrpc": "2.0", "id": 5, "method": "resources/list"},
	)
	if init := result[map[string]any](t, got["1"]); init["protocolVersion"] != "2025-03-26" || !strings.Contains(init["instructions"].(string), "tend_context") {
		t.Errorf("initialize = %v", init)
	}
	list := result[struct {
		Tools []mcpTool `json:"tools"`
	}](t, got["2"])
	if len(list.Tools) != 1 || list.Tools[0].Name != "tasks_list" || string(list.Tools[0].InputSchema) != `{"type":"object"}` {
		t.Errorf("tools/list = %+v", list)
	}
	// The call was resent after the lost answer, under the same request.
	if call := result[callResult](t, got["3"]); call.IsError || call.Content[0].Text != `{"ok":true}` {
		t.Errorf("tasks_create = %+v", call)
	}
	sent := f.seen("POST /api/v1/agent/tools/tasks_create")
	var a, b protocol.ToolRequest
	_ = json.Unmarshal(sent[0].body, &a)
	_ = json.Unmarshal(sent[len(sent)-1].body, &b)
	if len(sent) != 2 || a.RequestID == "" || a.RequestID != b.RequestID || a.SessionID != "sess-9" || a.Agent != protocol.AgentCodex {
		t.Errorf("tasks_create requests: %d, first %+v, last %+v", len(sent), a, b)
	}
	// A refusal goes back to the model as a failed tool result.
	if call := result[callResult](t, got["4"]); !call.IsError || !strings.Contains(call.Content[0].Text, "forbidden") {
		t.Errorf("refused call = %+v", call)
	}
	if got["5"].Error == nil || got["5"].Error.Code != -32601 {
		t.Errorf("unknown method = %+v", got["5"])
	}
}

func TestFault_MCPStartsWhileTendDown(t *testing.T) {
	f := newFakeServer(t)
	f.answer("GET /api/v1/agent/tools", []protocol.ToolSpec{{Name: "tasks_list", Description: "List tasks.", InputSchema: json.RawMessage(`{"type":"object"}`)}})
	te := newTestEnv(t, f.srv.URL)
	list := []any{
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"},
	}
	type tools struct {
		Tools []mcpTool `json:"tools"`
	}
	// Never reached: no tools to fall back on.
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	down := newTestEnv(t, gone.URL)
	if got := mcpSession(t, down, "claude", list...); got["2"].Error == nil {
		t.Errorf("tools/list without tend or a cache = %+v", got["2"])
	}

	// Once tend has answered, a later session started while tend is down
	// still gets the tools; calls fail until tend is back.
	if got := result[tools](t, mcpSession(t, te, "claude", list...)["2"]); len(got.Tools) != 1 {
		t.Fatalf("tools/list = %+v", got)
	}
	f.srv.Close()
	te.out.Reset()
	got := mcpSession(t, te, "claude", append(list,
		map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{"name": "tasks_list", "arguments": map[string]any{}}})...)
	if l := result[tools](t, got["2"]); len(l.Tools) != 1 || l.Tools[0].Name != "tasks_list" {
		t.Errorf("tools/list while tend is down = %+v", l)
	}
	if call := result[callResult](t, got["3"]); !call.IsError || !strings.Contains(call.Content[0].Text, "unreachable") {
		t.Errorf("call while tend is down = %+v", call)
	}
	// A tend that accepts but does not answer gets the cache in time.
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(20 * time.Second):
		}
	}))
	defer hung.Close()
	if err := te.saveConfig(config{Server: hung.URL}, credentials{ClientID: "cli-0011223344556677", Agent: "agent-token"}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(te.ConfigDir, "tools.json"), toolsCache{Server: hung.URL, Tools: []protocol.ToolSpec{{Name: "tasks_list"}}}, 0o600); err != nil {
		t.Fatal(err)
	}
	te.out.Reset()
	start := time.Now()
	if l := result[tools](t, mcpSession(t, te, "claude", list...)["2"]); len(l.Tools) != 1 || time.Since(start) > listTimeout+3*time.Second {
		t.Errorf("tools/list from a hung tend = %+v after %v", l, time.Since(start))
	}

	// The cache belongs to the server it came from.
	te2 := newTestEnv(t, gone.URL)
	te2.ConfigDir = te.ConfigDir
	if err := te2.saveConfig(config{Server: gone.URL + "/other"}, credentials{ClientID: "cli-0011223344556677", Agent: "agent-token"}); err != nil {
		t.Fatal(err)
	}
	if got := mcpSession(t, te2, "claude", list...); got["2"].Error == nil {
		t.Errorf("tools/list for another server used the cache: %+v", got["2"])
	}
}

func TestMCPSessionOfClaudeAndWorkers(t *testing.T) {
	f := newFakeServer(t)
	f.answer("GET /api/v1/agent/tools", []protocol.ToolSpec{{Name: "tasks_list", InputSchema: json.RawMessage(`{}`)}})
	f.answer("POST /api/v1/agent/tools/tasks_list", protocol.ToolResponse{Success: true, Content: "{}"})
	te := newTestEnv(t, f.srv.URL)
	te.vars["CLAUDE_CODE_SESSION_ID"] = "claude-sess"
	mcpSession(t, te, "claude", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "tasks_list"}})
	var req protocol.ToolRequest
	if sent := f.seen("POST /api/v1/agent/tools/tasks_list"); len(sent) != 1 || json.Unmarshal(sent[0].body, &req) != nil || req.SessionID != "claude-sess" || string(req.Arguments) != "{}" {
		t.Errorf("claude call = %+v", sent)
	}
	// A worker's bridge shows no tools and calls nothing.
	worker := newTestEnv(t, f.srv.URL)
	worker.vars[AddonEnv] = "off"
	got := mcpSession(t, worker, "traex",
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "tasks_list"}})
	list := result[struct {
		Tools []mcpTool `json:"tools"`
	}](t, got["1"])
	if len(list.Tools) != 0 || !result[callResult](t, got["2"]).IsError {
		t.Errorf("worker bridge = %+v", got)
	}
	if n := len(f.seen("GET /api/v1/agent/tools")); n != 0 {
		t.Errorf("the worker's bridge asked tend for tools %d times", n)
	}
}

func TestPauseUsesTheRevisionItRead(t *testing.T) {
	f := newFakeServer(t)
	f.answer("GET /api/v1/tasks/task_1", protocol.TaskDetail{TaskSummary: protocol.TaskSummary{TaskID: "task_1", ControlRevision: 4}})
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, body []byte) {
		var env protocol.Envelope
		_ = json.Unmarshal(body, &env)
		failed := protocol.Operation{RequestID: env.RequestID, Status: protocol.OpFailed, Error: protocol.Errorf(protocol.CodeStaleRevision, "stale")}
		_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &failed, Current: &protocol.TaskSummary{TaskID: "task_1", ControlMode: protocol.ControlPaused, ControlRevision: 5}})
	})
	te := newTestEnv(t, f.srv.URL)
	if code := te.main("pause", "task_1"); code != exitError {
		t.Errorf("stale pause = %d, want 1", code)
	}
	var env protocol.Envelope
	ops := f.seen("POST /api/v1/ops")
	if len(ops) != 1 || json.Unmarshal(ops[0].body, &env) != nil || env.Operation != "control.set_mode" || env.ExpectedControlRevision == nil ||
		*env.ExpectedControlRevision != 4 || ops[0].auth != "Bearer owner-token" {
		t.Errorf("pause sent %+v (%d writes; a stale write is not retried)", env, len(ops))
	}
	if !strings.Contains(te.out.String(), "control paused at revision 5") {
		t.Errorf("the current state was not shown: %q", te.out)
	}
	// Scripts see the same failure.
	te = newTestEnv(t, f.srv.URL)
	var res protocol.APIResponse
	if code := te.main("pause", "task_1", "--json"); code != exitError || json.Unmarshal(te.out.Bytes(), &res) != nil || res.Current == nil {
		t.Errorf("stale pause --json = %d %q, want 1 and the answer", code, te.out)
	}
}

func TestTakeoverTakesHumanControl(t *testing.T) {
	f := newFakeServer(t)
	f.answer("GET /api/v1/tasks/task_1", protocol.TaskDetail{TaskSummary: protocol.TaskSummary{TaskID: "task_1", ControlRevision: 2}})
	f.answer("POST /api/v1/ops", protocol.APIResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded}})
	te := newTestEnv(t, f.srv.URL)
	if code := te.main("takeover", "task_1"); code != exitOK {
		t.Fatalf("takeover = %d: %s", code, te.errOut)
	}
	var env protocol.Envelope
	var p protocol.SetControlModePayload
	ops := f.seen("POST /api/v1/ops")
	if len(ops) != 1 || json.Unmarshal(ops[0].body, &env) != nil || json.Unmarshal(env.Payload, &p) != nil ||
		env.Operation != "control.set_mode" || p.Mode != protocol.ControlHumanControl || *env.ExpectedControlRevision != 2 {
		t.Errorf("takeover sent %+v", env)
	}
}

func TestStatusGroupsByProject(t *testing.T) {
	f := newFakeServer(t)
	all := protocol.Snapshot{
		Tasks: []protocol.TaskSummary{
			{TaskID: "task_a", Title: "fix login", Project: "app", Lifecycle: protocol.TaskQueued, Rating: protocol.RatingP1, ControlMode: protocol.ControlManaged, BlockReason: protocol.BlockNone},
			{TaskID: "task_b", Title: "tidy docs", Project: "web", Lifecycle: protocol.TaskQueued, ControlMode: protocol.ControlManaged, BlockReason: protocol.BlockNone},
			{TaskID: "task_c", Title: "hotfix", Project: "web", Lifecycle: protocol.TaskQueued, Rating: protocol.RatingP0, ControlMode: protocol.ControlManaged, BlockReason: protocol.BlockNone},
		},
		Decisions: []protocol.DecisionView{{DecisionID: "dec_1", TaskID: "task_b", Project: "web", Version: 1, Status: "open", Question: "which docs?"}},
		Projects: []protocol.ProjectView{
			{Project: "app", Title: "The app", Weight: 2, OpenTasks: 4, Queued: 1},
			{Project: "web", Title: "Web", Weight: 1, OpenTasks: 2, OpenRuns: 1, Queued: 2},
		},
		Queue: []protocol.QueueEntry{
			{Place: 1, ActionID: "act_c", TaskID: "task_c", Title: "hotfix", Project: "web", Kind: protocol.ActionImplement, Rating: protocol.RatingP0, WaitedMillis: 30_000, Score: 2},
			{Place: 2, ActionID: "act_a", TaskID: "task_a", Title: "fix login", Project: "app", Kind: protocol.ActionImplement, Rating: protocol.RatingP1, WaitedMillis: 7_500_000, Score: 6.21},
			{Place: 3, ActionID: "act_b", TaskID: "task_b", Title: "tidy docs", Project: "web", Kind: protocol.ActionAccept, WaitedMillis: 60_000, Score: 2,
				Unplaceable: "host's worker does not support workspaces with more than one mount"},
		},
		Hosts: []protocol.HostView{{HostID: "box", OS: "linux", Arch: "amd64", Slots: 2, Worker: protocol.WorkerOrca, OpenRuns: 1}},
	}
	f.on("GET /api/v1/snapshot", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		s := all
		if p := r.URL.Query().Get("project"); p != "" {
			// The server scopes; the places stay those of the whole queue.
			s.Tasks, s.Projects, s.Queue = all.Tasks[1:], all.Projects[1:], []protocol.QueueEntry{all.Queue[0], all.Queue[2]}
		}
		_ = json.NewEncoder(w).Encode(s)
	})
	te := newTestEnv(t, f.srv.URL)
	if code := te.main("status"); code != exitOK {
		t.Fatalf("status = %d: %s", code, te.errOut)
	}
	// Columns are aligned with spaces; compare words.
	words := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	out := words(te.out.String())
	for _, want := range []string{
		`app "The app": weight 2, 0 running, 1 queued`, `web "Web": weight 1, 1 running, 2 queued`,
		"#1 task_c hotfix implement p0 waited <1m first", "#2 task_a fix login implement p1 waited 2h05m score 6.2",
		"#3 task_b tidy docs accept unrated waited 1m score 2.0", "dec_1 v1 about task_b",
		"(3 more unfinished tasks; see tend status --project app)",
		"no host can take it: host's worker does not support workspaces with more than one mount",
		"box (linux/amd64, orca): 1 of 2 slots busy",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	te = newTestEnv(t, f.srv.URL)
	if code := te.main("status", "--project", "web"); code != exitOK {
		t.Fatalf("status --project = %d: %s", code, te.errOut)
	}
	if out := words(te.out.String()); strings.Contains(out, "task_a") || !strings.Contains(out, "#3 task_b") {
		t.Errorf("status --project web:\n%s", out)
	}
	// An older server answers without projects.
	f.answer("GET /api/v1/snapshot", protocol.Snapshot{Tasks: all.Tasks})
	te = newTestEnv(t, f.srv.URL)
	if code := te.main("status", "--project", "web"); code != exitError {
		t.Errorf("status --project against an older server = %d, want %d", code, exitError)
	}
	te = newTestEnv(t, f.srv.URL)
	if code := te.main("status"); code != exitOK || !strings.Contains(te.out.String(), "task_a") {
		t.Errorf("status against an older server = %d:\n%s", code, te.out)
	}
}

func TestStatusShowsRunsOfFinishedTasks(t *testing.T) {
	f := newFakeServer(t)
	f.answer("GET /api/v1/snapshot", protocol.Snapshot{Projects: []protocol.ProjectView{{Project: "default", Title: "Default", Weight: 1, OpenRuns: 1}}})
	te := newTestEnv(t, f.srv.URL)
	if code := te.main("status"); code != exitOK || !strings.Contains(te.out.String(), "1 running") {
		t.Errorf("status with a run left = %d:\n%s", code, te.out)
	}
	f.answer("GET /api/v1/snapshot", protocol.Snapshot{Projects: []protocol.ProjectView{{Project: "default", Title: "Default", Weight: 1}}})
	te = newTestEnv(t, f.srv.URL)
	if code := te.main("status"); code != exitOK || strings.TrimSpace(te.out.String()) != "No unfinished tasks." {
		t.Errorf("idle status = %d:\n%s", code, te.out)
	}
}

func TestRateSendsTheRating(t *testing.T) {
	f := newFakeServer(t)
	f.answer("GET /api/v1/tasks/task_1", protocol.TaskDetail{TaskSummary: protocol.TaskSummary{TaskID: "task_1", ControlRevision: 4}})
	f.answer("POST /api/v1/ops", protocol.APIResponse{Operation: &protocol.Operation{RequestID: "req_1", Status: protocol.OpSucceeded}})
	te := newTestEnv(t, f.srv.URL)
	if code := te.main("rate", "task_1", "high"); code != exitUsage {
		t.Errorf("rate high = %d, want %d", code, exitUsage)
	}
	if code := te.main("rate", "task_1", "p0"); code != exitOK {
		t.Fatalf("rate p0 = %d: %s", code, te.errOut)
	}
	var env protocol.Envelope
	var payload struct {
		Rating protocol.Rating `json:"rating"`
	}
	ops := f.seen("POST /api/v1/ops")
	if len(ops) != 1 || json.Unmarshal(ops[0].body, &env) != nil || json.Unmarshal(env.Payload, &payload) != nil ||
		env.Operation != "tasks.rate" || env.ExpectedControlRevision == nil || *env.ExpectedControlRevision != 4 || payload.Rating != protocol.RatingP0 {
		t.Errorf("rate sent %+v (%d writes)", env, len(ops))
	}
}

func TestDecideAnswersTheOpenVersion(t *testing.T) {
	f := newFakeServer(t)
	f.answer("GET /api/v1/snapshot", protocol.Snapshot{Decisions: []protocol.DecisionView{{DecisionID: "dec_1", Version: 3, Status: "open"}}})
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, body []byte) {
		var env protocol.Envelope
		_ = json.Unmarshal(body, &env)
		result, _ := json.Marshal(protocol.AnswerResult{DecisionID: "dec_1", Status: "answered", Version: 4,
			Effect: &protocol.EffectResult{Effect: protocol.EffectImplement, Reason: "the task changed after the question was asked"}})
		ok := protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: result}
		_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &ok})
	})
	te := newTestEnv(t, f.srv.URL)
	if code := te.main("decide", "dec_1", "retry", "--note", "once more"); code != exitOK {
		t.Fatalf("decide = %d: %s", code, te.errOut)
	}
	if want := "tend did not carry it out (implement): the task changed after the question was asked\n"; !strings.HasSuffix(te.out.String(), want) {
		t.Errorf("decide printed %q, want it to end with %q", te.out, want)
	}
	var env protocol.Envelope
	var p protocol.AnswerDecisionPayload
	_ = json.Unmarshal(f.seen("POST /api/v1/ops")[0].body, &env)
	_ = json.Unmarshal(env.Payload, &p)
	if env.Operation != "decisions.respond" || env.Target.ID != "dec_1" || p.Version != 3 || p.Option != "retry" || p.Text != "once more" {
		t.Errorf("decide sent %+v %+v", env, p)
	}
	if code := te.main("decide", "dec_2", "x"); code != exitError {
		t.Errorf("decide on a decision that is not open = %d", code)
	}
}

func TestSetupClaude(t *testing.T) {
	te := newTestEnv(t, "")
	settings := filepath.Join(te.Home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	original := "{\n  \"model\": \"opus\",\n  \"permissions\": {\n    \"allow\": [\"Bash(ls:*)\"]\n  },\n  \"hooks\": {\n    \"Stop\": [{\"hooks\": [{\"type\": \"command\", \"command\": \"[ -x /bin/say ] && say done < /dev/null\"}]}]\n  }\n}\n"
	if err := os.WriteFile(settings, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := te.main("setup", "claude"); code != exitOK {
		t.Fatalf("setup = %d: %s", code, te.errOut)
	}
	if b, _ := os.ReadFile(settings); string(b) != original || len(te.runs) != 1 || strings.Join(te.runs[0], " ") != "claude mcp get tend" {
		t.Fatalf("setup without --write changed something: %s, ran %v", b, te.runs)
	}
	if !strings.Contains(te.out.String(), "+ ") || !strings.Contains(te.out.String(), "claude mcp add --scope user tend -- /opt/tend/bin/tend mcp --agent claude") {
		t.Errorf("setup output:\n%s", te.out)
	}
	// The owner's shell command keeps &, < and > as written.
	if strings.Contains(te.out.String(), `\u00`) {
		t.Errorf("setup escapes the owner's own hook:\n%s", te.out)
	}
	if code := te.main("setup", "claude", "--write"); code != exitOK {
		t.Fatalf("setup --write = %d: %s", code, te.errOut)
	}
	b, _ := os.ReadFile(settings)
	root, err := parseOrdered(b)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(root.keys, ",") != "model,permissions,hooks" {
		t.Errorf("keys reordered: %v", root.keys)
	}
	hooks, _ := root.child("hooks")
	if strings.Join(hooks.keys, ",") != "Stop,SessionStart,UserPromptSubmit" || !strings.Contains(string(b), `"/opt/tend/bin/tend hook claude user-prompt-submit"`) ||
		!strings.Contains(string(b), `"mcp__tend__*"`) || !strings.Contains(string(b), `"[ -x /bin/say ] && say done < /dev/null"`) {
		t.Errorf("settings after setup:\n%s", b)
	}
	if len(te.runs) != 3 || strings.Join(te.runs[2], " ") != "claude mcp add --scope user tend -- /opt/tend/bin/tend mcp --agent claude" {
		t.Errorf("ran %v", te.runs)
	}
	// Running it again changes nothing.
	te.out.Reset()
	if code := te.main("setup", "claude", "--write"); code != exitOK || !strings.Contains(te.out.String(), "already has tend's configuration") {
		t.Errorf("second setup = %d:\n%s", code, te.out)
	}
	if again, _ := os.ReadFile(settings); string(again) != string(b) {
		t.Errorf("second setup rewrote the settings:\n%s", again)
	}
	te.out.Reset()
	if code := te.main("setup", "claude"); code != exitOK || !strings.Contains(te.out.String(), "already set up") || strings.Contains(te.out.String(), "--write") {
		t.Errorf("setup once set up = %d:\n%s", code, te.out)
	}
}

func TestSetupCodexAndTraex(t *testing.T) {
	te := newTestEnv(t, "")
	te.vars["TRAE_HOME"] = filepath.Join(te.Home, "trae-home")
	if code := te.main("setup", "codex", "--write"); code != exitOK {
		t.Fatalf("setup codex = %d: %s", code, te.errOut)
	}
	cfg, _ := os.ReadFile(filepath.Join(te.Home, ".codex", "config.toml"))
	hooks, _ := os.ReadFile(filepath.Join(te.Home, ".codex", "hooks.json"))
	for _, want := range []string{`[mcp_servers.tend]`, `args = ["mcp", "--agent", "codex"]`, `env_vars = ["TEND_ADDON", "TEND_CONFIG_DIR"]`, `default_tools_approval_mode = "approve"`} {
		if !strings.Contains(string(cfg), want) {
			t.Errorf("config.toml lacks %s:\n%s", want, cfg)
		}
	}
	var hf struct {
		Hooks map[string][]struct {
			Hooks []map[string]any `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(hooks, &hf); err != nil || len(hf.Hooks["SessionStart"]) != 1 || hf.Hooks["UserPromptSubmit"][0].Hooks[0]["additionalContextLimit"] != float64(0) ||
		hf.Hooks["SessionStart"][0].Hooks[0]["command"] != "/opt/tend/bin/tend hook codex session-start" {
		t.Errorf("hooks.json:\n%s", hooks)
	}
	// An existing tend server section is left alone.
	path := filepath.Join(te.vars["TRAE_HOME"], "traecli.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("model = \"x\"\n\n[mcp_servers.tend]\ncommand = \"tend\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := te.main("setup", "traex", "--write"); code != exitOK {
		t.Fatalf("setup traex = %d: %s", code, te.errOut)
	}
	// traex reads hooks only from traecli.toml.
	want := "[[hooks.UserPromptSubmit]]\n[[hooks.UserPromptSubmit.hooks]]\ntype = \"command\"\ncommand = \"/opt/tend/bin/tend hook traex user-prompt-submit\"\ntimeout = 5\nadditionalContextLimit = 0\n"
	traecli, _ := os.ReadFile(path)
	if strings.Count(string(traecli), "[mcp_servers.tend]") != 1 || !strings.Contains(string(traecli), want) || !strings.Contains(string(traecli), "tend hook traex session-start") {
		t.Errorf("traecli.toml:\n%s", traecli)
	}
	if _, err := os.Stat(filepath.Join(te.vars["TRAE_HOME"], "hooks.json")); err == nil {
		t.Error("setup wrote a hooks.json traex does not read")
	}
	te.out.Reset()
	if code := te.main("setup", "traex", "--write"); code != exitOK || !strings.Contains(te.out.String(), "already has tend's configuration") {
		t.Errorf("second setup traex = %d:\n%s", code, te.out)
	}
	if again, _ := os.ReadFile(path); string(again) != string(traecli) {
		t.Errorf("second setup rewrote traecli.toml:\n%s", again)
	}
	te.out.Reset()
	if code := te.main("setup", "traex"); code != exitOK || !strings.Contains(te.out.String(), "already set up") {
		t.Errorf("setup traex once set up = %d:\n%s", code, te.out)
	}

	// Hooks defined as a plain array cannot take appended tables: the
	// owner merges them by hand.
	conflict := "[hooks]\nSessionStart = []\n"
	if err := os.WriteFile(path, []byte(conflict), 0o600); err != nil {
		t.Fatal(err)
	}
	te.out.Reset()
	if code := te.main("setup", "traex", "--write"); code != exitOK || !strings.Contains(te.out.String(), "add tend's by hand") {
		t.Errorf("setup traex with array hooks = %d:\n%s", code, te.out)
	}
	if b, _ := os.ReadFile(path); strings.Contains(string(b), "[[hooks.") || !strings.Contains(string(b), "[mcp_servers.tend]") {
		t.Errorf("traecli.toml with array hooks:\n%s", b)
	}
}

func TestHookConflict(t *testing.T) {
	for config, want := range map[string]bool{
		"":                             false,
		"[features]\nhooks = true\n":   false,
		"[[hooks.Stop]]\nhooks = []\n": false,
		"[[hooks.SessionStart]]\n[[hooks.SessionStart.hooks]]\ncommand = \"x\"\n": false,
		"[hooks.state.\"a:b\"]\ntrusted_hash = \"h\"\n":                           false,
		"[hooks]\nSessionStart = []\n":                                            true,
		"[hooks]\n'UserPromptSubmit' = []\n":                                      true,
		"[ hooks ]\n\"SessionStart\" = []\n":                                      true,
		"hooks.SessionStart = []\n":                                               true,
		"hooks = { SessionStart = [] }\n":                                         true,
		"[hooks.SessionStart]\nx = 1\n":                                           true,
	} {
		if got := hookConflict(config); got != want {
			t.Errorf("hookConflict(%q) = %v, want %v", config, got, want)
		}
	}
}

func TestSetupTraexQuotedPath(t *testing.T) {
	te := newTestEnv(t, "")
	te.Executable = `/opt/te"nd/bin/tend`
	te.vars["TRAE_HOME"] = filepath.Join(te.Home, "trae-home")
	for range 2 {
		if code := te.main("setup", "traex", "--write"); code != exitOK {
			t.Fatalf("setup traex = %d: %s", code, te.errOut)
		}
	}
	b, _ := os.ReadFile(filepath.Join(te.vars["TRAE_HOME"], "traecli.toml"))
	if n := strings.Count(string(b), "[[hooks.SessionStart]]"); n != 1 {
		t.Errorf("%d SessionStart hooks after two runs:\n%s", n, b)
	}
}

func TestWriteJSONConcurrently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tools.json")
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for range 20 {
				if err := writeJSON(path, map[string]int{"writer": i}, 0o600); err != nil {
					t.Error(err)
				}
				var v map[string]int
				if err := readJSON(path, &v); err != nil {
					t.Errorf("read while writing: %v", err)
				}
			}
		})
	}
	wg.Wait()
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("file after writes: %v, %v", st, err)
	}
	if left, _ := filepath.Glob(path + ".*.tmp"); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

func TestOrderedRoundTrip(t *testing.T) {
	in := "{\n  \"b\": 1.50,\n  \"a\": [\n    true,\n    null,\n    \"x\"\n  ],\n  \"c\": {}\n}\n"
	o, err := parseOrdered([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if out := formatOrdered(o); out != in {
		t.Errorf("round trip:\n%s\nwant:\n%s", out, in)
	}
}

func TestLogLines(t *testing.T) {
	at := time.Date(2026, 10, 16, 7, 34, 40, 0, time.UTC)
	for body, want := range map[string]string{
		`{"type":"agentMessage","text":"Done.\nAll checks pass."}`:                                                                                                                           "Done.\n    All checks pass.",
		`{"type":"userMessage","content":[{"type":"text","text":"Implement this task.\n\nGoal: x"}]}`:                                                                                        "brief: Implement this task.",
		`{"type":"commandExecution","command":"python3 -m unittest","exitCode":0,"status":"completed"}`:                                                                                      "$ python3 -m unittest → exit 0",
		`{"type":"fileChange","changes":[{"path":"/h/runner/workspaces/add-sub/calc/calc.py","kind":{"type":"update"}},{"path":"/h/runner/scratch/add-sub/plan.md","kind":{"type":"add"}}]}`: "changed calc/calc.py (update), scratch:plan.md (add)",
		`{"type":"dynamicToolCall","tool":"submit_verdict","arguments":{}}`:                                                                                                                  "tool submit_verdict",
		`{"type":"mcpToolCall","server":"docs","tool":"search"}`:                                                                                                                             "tool docs/search",
		`{"type":"fileChange","status":"failed","changes":[{"path":"/h/runner/workspaces/add-sub/calc/calc.py","kind":{"type":"update"}}]}`:                                                  "change failed: calc/calc.py (update)",
		`{"type":"commandExecution","command":"` + strings.Repeat("x", 3000) + `","exitCode":1}`:                                                                                             "$ " + strings.Repeat("x", 2000) + "… → exit 1",
		`{"type":"commandExecution","command":"make check","exitCode":0,"status":"backgrounded"}`:                                                                                            "$ make check → backgrounded",
		`{"type":"commandExecution","command":"make check","exitCode":2,"status":"failed"}`:                                                                                                  "$ make check → exit 2",
		`{"type":"somethingNew"}`: "item somethingNew",
	} {
		line, ok := logLine(protocol.RunLogView{Seq: 3, Kind: "item.completed", Body: json.RawMessage(body), CreatedAt: at})
		if _, rest, _ := strings.Cut(line, " #3 "); !ok || rest != want {
			t.Errorf("logLine(%s) = %q, %v; want %q", body, line, ok, want)
		}
	}
	if _, ok := logLine(protocol.RunLogView{Kind: "item.completed", Body: json.RawMessage(`{"type":"reasoning","content":["x"]}`)}); ok {
		t.Error("reasoning gets a line")
	}
	if line, _ := logLine(protocol.RunLogView{Kind: "item.started", Body: json.RawMessage(`{"type":"commandExecution","command":"make"}`)}); !strings.HasSuffix(line, "$ make …") {
		t.Errorf("started command = %q", line)
	}
	if line, _ := logLine(protocol.RunLogView{Kind: "run.running", Body: json.RawMessage(`{}`)}); !strings.HasSuffix(line, " run.running") {
		t.Errorf("run event = %q", line)
	}
	if got := clip("ab€cd", 4); got != "ab…" {
		t.Errorf("clip in the middle of a rune = %q", got)
	}
}

func TestTaskShowsArtifactsAndIssues(t *testing.T) {
	f := newFakeServer(t)
	f.answer("GET /api/v1/tasks/task_1", protocol.TaskDetail{
		TaskSummary: protocol.TaskSummary{TaskID: "task_1", Title: "fix"},
		Artifacts: []protocol.ArtifactView{{ArtifactID: "art_1", RunID: "run_1", Ref: json.RawMessage(
			`{"mounts":[{"repo":"calc","path":"calc","base_sha":"2a5c718c56374b145d","branch":"tend/fix","commit_sha":"5f6ea1e143cd58d82c","changed":true},` +
				`{"repo":"lib","path":"lib","base_sha":"9999999999999999","changed":false},` +
				`{"repo":"web","path":"web","base_sha":"7777777777777777","branch":"","commit_sha":"7777777777777777","snapshot_sha":"6666666666666666","changed":true},` +
				`{"repo":"doc","path":"doc","base_sha":"1111111111111111","branch":"tend/fix","commit_sha":"8888888888888888","changed":false},` +
				`{"repo":"cfg","path":"cfg","base_sha":"5555555555555555","branch":"tend/fix","commit_sha":"5555555555555555","snapshot_sha":"4444444444444444","changed":false}]}`)}},
		Acceptances: []protocol.AcceptanceView{
			{ArtifactID: "art_1", SpecVersion: 1, Verdict: protocol.VerdictChangesRequired, Issues: json.RawMessage(`["tests fail",{"check":2}]`)},
			{ArtifactID: "art_1", SpecVersion: 1, Verdict: protocol.VerdictPassed, Issues: json.RawMessage(`[]`)},
		},
		Decisions: []protocol.DecisionView{{DecisionID: "dec_1", Version: 1, Status: "open", Question: "Run failed. How should this task proceed?",
			Options: json.RawMessage(`["retry the implementation","revise the spec"]`),
			Effects: map[string]protocol.DecisionEffect{"retry the implementation": protocol.EffectImplement}}},
	})
	te := newTestEnv(t, f.srv.URL)
	if code := te.main("task", "task_1"); code != exitOK {
		t.Fatalf("task = %d: %s", code, te.errOut)
	}
	for _, want := range []string{
		"  art_1 from run_1\n    calc: tend/fix at 5f6ea1e143cd (base 2a5c718c5637)\n    lib: unchanged in this run, at 999999999999\n    web: detached HEAD at 777777777777 (base 777777777777, uncommitted changes in snapshot 666666666666)\n    doc: unchanged in this run, at 888888888888\n    cfg: unchanged in this run, at 555555555555, uncommitted changes in snapshot 444444444444\n",
		"verdict on art_1 (spec v1): changes_required\n  - tests fail\n  - {\"check\":2}\n",
		"verdict on art_1 (spec v1): passed\n",
		"    options: retry the implementation (tend: implement) | revise the spec\n",
	} {
		if !strings.Contains(te.out.String(), want) {
			t.Errorf("task output lacks %q:\n%s", want, te.out)
		}
	}
}

func TestTailnetHostsSkipTheProxy(t *testing.T) {
	for host, want := range map[string]bool{
		"tend.example-tailnet.ts.net": true, "TEND.Example.ts.net.": true, "tend": true,
		"100.120.122.6": true, "fd7a:115c:a1e0::1": true,
		"github.com": false, "10.0.0.1": false, "8.8.8.8": false, "2001:db8::1": false, "": false,
	} {
		if got := onTailnet(host); got != want {
			t.Errorf("onTailnet(%q) = %v, want %v", host, got, want)
		}
	}
	req, _ := http.NewRequest("GET", "https://tend.example-tailnet.ts.net/api/v1/session", nil)
	if u, err := Transport().Proxy(req); u != nil || err != nil {
		t.Errorf("proxy for a tailnet server = %v, %v", u, err)
	}
}

func TestDiffShowsOldLinesFirst(t *testing.T) {
	if got, want := diff("a\nb\nc\n", "a\nx\nc\n"), "  a\n- b\n+ x\n  c\n  \n"; got != want {
		t.Errorf("diff = %q, want %q", got, want)
	}
}

func TestLogsOfATask(t *testing.T) {
	f := newFakeServer(t)
	f.answer("GET /api/v1/tasks/task_1", protocol.TaskDetail{
		TaskSummary: protocol.TaskSummary{TaskID: "task_1", Title: "fix"},
		Runs: []protocol.RunView{
			{RunID: "run_1", Role: protocol.RoleImplement, Generation: 1, Host: "h", Observed: protocol.RunFinished},
			{RunID: "run_2", Role: protocol.RoleAccept, Generation: 1, Host: "h", Observed: protocol.RunFinished},
		},
	})
	for _, run := range []string{"run_1", "run_2"} {
		f.on("GET /api/v1/runs/"+run+"/logs", func(w http.ResponseWriter, r *http.Request, _ []byte) {
			page := protocol.RunLogPage{Observed: protocol.RunFinished, Next: 1}
			if r.URL.Query().Get("after") == "0" {
				page.Logs = []protocol.RunLogView{{Seq: 1, Kind: "item.completed", Body: json.RawMessage(`{"type":"agentMessage","text":"said in ` + run + `"}`)}}
			}
			_ = json.NewEncoder(w).Encode(page)
		})
	}
	te := newTestEnv(t, f.srv.URL)
	if code := te.main("logs", "task_1"); code != exitOK {
		t.Fatalf("logs = %d: %s", code, te.errOut)
	}
	out := te.out.String()
	// JSON entries say which run they belong to.
	te.out.Reset()
	if code := te.main("logs", "--json", "task_1"); code != exitOK {
		t.Fatalf("logs --json = %d: %s", code, te.errOut)
	}
	var runs []string
	for _, line := range strings.Split(strings.TrimSpace(te.out.String()), "\n") {
		var entry struct {
			RunID string `json:"run_id"`
			Seq   int64  `json:"seq"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil || entry.Seq != 1 {
			t.Fatalf("logs --json line %q: %v", line, err)
		}
		runs = append(runs, entry.RunID)
	}
	if strings.Join(runs, ",") != "run_1,run_2" {
		t.Errorf("logs --json runs = %v", runs)
	}
	first, second := strings.Index(out, "== run_1 implement gen 1 on h: finished\n"), strings.Index(out, "== run_2 accept gen 1 on h: finished\n")
	if first < 0 || second < first || !strings.Contains(out[first:second], "said in run_1") || !strings.Contains(out[second:], "said in run_2") {
		t.Errorf("logs of a task:\n%s", out)
	}
}

func TestLogsOfATaskWithoutRuns(t *testing.T) {
	f := newFakeServer(t)
	f.answer("GET /api/v1/tasks/task_1", protocol.TaskDetail{TaskSummary: protocol.TaskSummary{TaskID: "task_1"}})
	te := newTestEnv(t, f.srv.URL)
	if code := te.main("logs", "--json", "task_1"); code != exitOK || te.out.Len() != 0 {
		t.Errorf("logs --json of a task without runs = %d, %q; want no records", code, te.out)
	}
}

func TestHookSendsTheRepositoryRemotes(t *testing.T) {
	f := newFakeServer(t)
	f.answer("POST /api/v1/agent/hook", protocol.HookResponse{})
	te := newTestEnv(t, f.srv.URL)
	var dirs []string
	te.Output = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "git" || len(args) < 3 || args[0] != "-C" || args[2] != "remote" {
			return nil, os.ErrNotExist
		}
		dirs = append(dirs, args[1])
		if args[1] == "/outside" {
			return nil, errors.New("not a git repository")
		}
		return []byte("origin\tgit@example.com:me/tend.git (fetch)\norigin\tgit@example.com:me/tend.git (push)\n" +
			"mirror\thttps://bot:s3cret@example.com/me/tend.git (fetch)\nbackup\t/mnt/backup/tend.git (fetch)\n"), nil
	}
	te.Getwd = func() (string, error) { return "/work/tend", nil }
	hook := func(input string) protocol.HookRequest {
		t.Helper()
		te.Stdin = strings.NewReader(input)
		if code := te.main("hook", "claude", "user-prompt-submit"); code != exitOK {
			t.Fatalf("hook = %d", code)
		}
		reqs := f.seen("POST /api/v1/agent/hook")
		var req protocol.HookRequest
		if err := json.Unmarshal(reqs[len(reqs)-1].body, &req); err != nil {
			t.Fatal(err)
		}
		if body := string(reqs[len(reqs)-1].body); strings.Contains(body, "/work/") || strings.Contains(body, "/mnt/") || strings.Contains(body, "s3cret") {
			t.Errorf("hook request carries a local path or a credential: %s", body)
		}
		return req
	}
	want := []string{"git@example.com:me/tend.git", "https://example.com/me/tend.git"}
	if req := hook(`{"session_id":"s-1","prompt":"hi","cwd":"/work/other"}`); !slices.Equal(req.Remotes, want) {
		t.Errorf("remotes = %q, want %q", req.Remotes, want)
	}
	// Without a cwd in the input, the hook's own working directory counts.
	hook(`{"session_id":"s-1","prompt":"hi"}`)
	if !slices.Equal(dirs, []string{"/work/other", "/work/tend"}) {
		t.Errorf("git ran in %q", dirs)
	}
	if req := hook(`{"session_id":"s-1","prompt":"hi","cwd":"/outside"}`); len(req.Remotes) != 0 {
		t.Errorf("remotes outside a repository = %q", req.Remotes)
	}
}

func TestOnlyNetworkRemotesLeave(t *testing.T) {
	for url, want := range map[string]bool{
		"https://example.com/me/tend.git": true,
		"ssh://git@example.com/me/tend":   true,
		"git@example.com:me/tend.git":     true,
		"example.com:me/tend":             true,
		"/srv/git/tend.git":               false,
		"../tend":                         false,
		"~/src/tend":                      false,
		"file:///srv/git/tend":            false,
		"FILE:///srv/git/tend":            false,
		`C:\src\tend`:                     false,
		"C:/src/tend":                     false,
		"./dir:with:colons":               false,
	} {
		if got := networkRemote(url); got != want {
			t.Errorf("networkRemote(%q) = %v, want %v", url, got, want)
		}
	}
}
