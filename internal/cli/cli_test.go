package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	if init := result[map[string]any](t, got["1"]); init["protocolVersion"] != "2025-03-26" {
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

func TestDecideAnswersTheOpenVersion(t *testing.T) {
	f := newFakeServer(t)
	f.answer("GET /api/v1/snapshot", protocol.Snapshot{Decisions: []protocol.DecisionView{{DecisionID: "dec_1", Version: 3, Status: "open"}}})
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, body []byte) {
		var env protocol.Envelope
		_ = json.Unmarshal(body, &env)
		ok := protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded}
		_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &ok})
	})
	te := newTestEnv(t, f.srv.URL)
	if code := te.main("decide", "dec_1", "retry", "--note", "once more"); code != exitOK {
		t.Fatalf("decide = %d: %s", code, te.errOut)
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
	original := "{\n  \"model\": \"opus\",\n  \"permissions\": {\n    \"allow\": [\"Bash(ls:*)\"]\n  },\n  \"hooks\": {\n    \"Stop\": [{\"hooks\": [{\"type\": \"command\", \"command\": \"say done\"}]}]\n  }\n}\n"
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
		!strings.Contains(string(b), `"mcp__tend__*"`) || !strings.Contains(string(b), `"say done"`) {
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
