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
	}
	if server != "" {
		if err := te.saveConfig(config{Server: server}, credentials{ClientID: "cli-0011223344556677", Owner: "owner-token"}); err != nil {
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
		_ = json.NewEncoder(w).Encode(protocol.SessionView{DeploymentID: "deployment-test", Login: "me@example.com", ClientID: req.ClientID, Scope: req.Scope, Token: string(req.Scope) + "-token", ExpiresAt: time.Now()})
	})
	te := newTestEnv(t, "")
	if code := te.main("login"); code != exitUsage {
		t.Errorf("login without a server = %d, want usage", code)
	}
	if code := te.main("login", "--server", f.srv.URL); code != exitOK {
		t.Fatalf("login = %d: %s", code, te.errOut)
	}
	_, first, err := te.loadConfig()
	if err != nil || first.Owner != "owner-token" || !strings.HasPrefix(first.ClientID, "cli-") {
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
