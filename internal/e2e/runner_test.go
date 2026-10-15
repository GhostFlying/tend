// Package e2e tests the server and the runner together over HTTP, with
// fake workers and workspaces.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/dispatch"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/netx"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/runnerapi"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

const (
	hostToken = "host-token"
	baseSHA   = "1111111111111111111111111111111111111111"
	commitSHA = "2222222222222222222222222222222222222222"
)

var (
	secret = []byte("server-secret")
	peer   = netx.Identity{Node: "build-box", Tags: []string{"tag:tend-runner"}}
	owner  = protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "me", Client: "c"}
)

// server is tend's ledger and runner endpoint.
type server struct {
	t      *testing.T
	store  *store.Store
	app    *app.App
	ids    protocol.IDGen
	url    string
	signal *dispatch.Signal
}

func newServer(t *testing.T, ttl time.Duration) *server {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "tend.db"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ids := protocol.NewULIDGen(time.Now, nil)
	changes := runnerapi.NewBroadcast()
	sig := dispatch.NewSignal()
	a := app.New(s, ids, time.Now, app.WithLeaseTTL(ttl), app.WithChangeNotifier(changes), app.WithNotifier(sig))
	srv := &server{t: t, store: s, app: a, ids: ids, signal: sig}
	srv.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, "config.sync", protocol.Target{Type: "config"}, app.ConfigPayload{
		Repos: []app.RepoConfig{{Repo: "tend", RemoteURL: "https://example.com/tend.git"}},
		Hosts: []app.HostConfig{{HostID: "linux-1", OS: "linux", Arch: "amd64", Repos: []string{"tend"}, Slots: 2,
			TokenHash: runnerapi.HashToken(hostToken), Nodes: []string{"tag:tend-runner"}}},
	}, app.SyncConfig{})
	api := runnerapi.New(a, netx.Static{Identity: &peer}, secret, changes, runnerapi.Config{ClaimWait: 200 * time.Millisecond}, quiet())
	go api.SweepLeases(ctx, 20*time.Millisecond)
	hs := httptest.NewServer(api.Handler())
	t.Cleanup(hs.Close)
	srv.url = hs.URL
	return srv
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func (s *server) exec(p protocol.Principal, op string, target protocol.Target, payload any, cmd app.Command) protocol.Operation {
	s.t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		s.t.Fatal(err)
	}
	env := &protocol.Envelope{RequestID: s.ids.New(protocol.PrefixRequest), Operation: op, Target: target, Payload: b}
	res, err := s.app.Execute(context.Background(), p, env, cmd)
	if err != nil || res.Status == protocol.OpFailed {
		s.t.Fatalf("%s: %+v, %v", op, res, err)
	}
	return res
}

func (s *server) createTask() string {
	s.t.Helper()
	op := s.exec(owner, "tasks.create", protocol.Target{Type: "task"}, map[string]any{"title": "fix it", "spec": domain.Spec{
		Goal: "fix it", Acceptance: []string{"tests pass"}, Delivery: domain.DeliveryCommit,
		Workspace: domain.Workspace{Mounts: []domain.Mount{{Repo: "tend"}}},
	}}, app.CreateTask{})
	var res struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(op.Result, &res); err != nil {
		s.t.Fatal(err)
	}
	return res.TaskID
}

func (s *server) task(id string) store.TaskRow {
	var row store.TaskRow
	if err := s.store.Read(context.Background(), func(tx *store.ReadTx) error {
		var err error
		row, err = store.GetTask(context.Background(), tx.Q(), id)
		return err
	}); err != nil {
		s.t.Fatal(err)
	}
	return row
}

func (s *server) count(query string, args ...any) int {
	var n int
	if err := s.store.Read(context.Background(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(context.Background(), query, args...).Scan(&n)
	}); err != nil {
		s.t.Fatal(err)
	}
	return n
}

func (s *server) runState(role protocol.RunRole) protocol.RunObserved {
	var st protocol.RunObserved
	if err := s.store.Read(context.Background(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(context.Background(), `SELECT observed_state FROM runs WHERE role = ? ORDER BY created_at DESC LIMIT 1`, role).Scan(&st)
	}); err != nil {
		return ""
	}
	return st
}

// claimDirectly claims over HTTP as the host would, outside any runner.
func (s *server) claimDirectly(env *protocol.Envelope) protocol.Assignment {
	s.t.Helper()
	b, _ := json.Marshal(env)
	req, _ := http.NewRequest(http.MethodPost, s.url+runnerapi.Path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+hostToken)
	req.Header.Set(runnerapi.HeaderHost, "linux-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Operation protocol.Operation `json:"operation"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		s.t.Fatal(err)
	}
	var res struct {
		Assignment *protocol.Assignment `json:"assignment"`
	}
	if err := json.Unmarshal(out.Operation.Result, &res); err != nil || res.Assignment == nil {
		s.t.Fatalf("direct claim: %+v (%v)", out, err)
	}
	return *res.Assignment
}

// workspaces lays out nothing; it reports and collects fixed commits.
type workspaces struct {
	dir       string
	noChanges bool
	// prepareErr fails preparing runs of a role; collectErr fails
	// collecting the artifact.
	prepareErr map[protocol.RunRole]error
	collectErr error
}

func (w *workspaces) Prepare(_ context.Context, as protocol.Assignment) (runner.Prepared, error) {
	if err := w.prepareErr[as.Role]; err != nil {
		return runner.Prepared{}, err
	}
	p := runner.Prepared{Dir: filepath.Join(w.dir, as.Workspace.Name), ScratchDir: filepath.Join(w.dir, "scratch")}
	for _, m := range as.Workspace.Mounts {
		p.Report.Mounts = append(p.Report.Mounts, protocol.ResolvedMount{Path: m.Path, ResolvedBaseSHA: baseSHA, ResolvedFrom: "candidate:main", Branch: as.Workspace.Branch})
	}
	return p, nil
}

func (w *workspaces) Collect(_ context.Context, as protocol.Assignment) (protocol.ArtifactPayload, error) {
	if w.noChanges {
		return protocol.ArtifactPayload{}, runner.ErrNoChanges
	}
	if w.collectErr != nil {
		return protocol.ArtifactPayload{}, w.collectErr
	}
	var a protocol.ArtifactPayload
	for _, m := range as.Workspace.Mounts {
		a.Mounts = append(a.Mounts, protocol.ArtifactMount{Repo: m.Repo, Path: m.Path, BaseSHA: baseSHA, Branch: as.Workspace.Branch, CommitSHA: commitSHA, Changed: true})
	}
	return a, nil
}

func (w *workspaces) Finish(context.Context, protocol.Assignment) error { return nil }

// network can be cut between the runner and tend, or drop requests of
// chosen operations.
type network struct {
	mu   sync.Mutex
	down bool
	// drop counts how many more requests of an operation to fail.
	drop map[string]int
}

// allow stops dropping requests of an operation.
func (n *network) allow(op string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.drop, op)
}

func (n *network) dropNext(op string, times int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.drop == nil {
		n.drop = map[string]int{}
	}
	n.drop[op] += times
}

func (n *network) set(down bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.down = down
}

func (n *network) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	var env struct {
		Operation string `json:"operation"`
	}
	_ = json.Unmarshal(body, &env)
	n.mu.Lock()
	down := n.down
	drop := n.drop[env.Operation] > 0
	if drop {
		n.drop[env.Operation]--
	}
	n.mu.Unlock()
	if down || drop {
		return nil, errors.New("network is down")
	}
	return http.DefaultTransport.RoundTrip(req)
}

// node is one runner process; restart replaces it with a new process on
// the same journal.
type node struct {
	t       *testing.T
	srv     *server
	path    string
	workers *testkit.Workers
	ws      *workspaces
	net     *network
	cancel  context.CancelFunc
	done    chan error
}

func newNode(t *testing.T, srv *server, script func(harness.WorkerSpec) testkit.WorkerScript) *node {
	t.Helper()
	n := &node{
		t: t, srv: srv, path: filepath.Join(t.TempDir(), "journal.db"),
		workers: &testkit.Workers{Script: script}, ws: &workspaces{dir: t.TempDir()}, net: &network{},
	}
	t.Cleanup(n.stop)
	return n
}

func (n *node) journal() *journal.Journal {
	n.t.Helper()
	j, err := journal.Open(context.Background(), n.path, time.Now)
	if err != nil {
		n.t.Fatal(err)
	}
	return j
}

func (n *node) start() {
	n.t.Helper()
	j := n.journal()
	ctx, cancel := context.WithCancel(context.Background())
	n.cancel, n.done = cancel, make(chan error, 1)
	r := runner.New(runner.Config{
		ServerURL: n.srv.url, HostID: "linux-1", HostToken: hostToken, Version: "test",
		HTTPClient: &http.Client{Transport: n.net}, Journal: j, Workers: n.workers, Workspaces: n.ws,
		Now: time.Now, IDs: protocol.NewULIDGen(time.Now, nil), Log: quiet(),
		RetryDelay: 20 * time.Millisecond, EventFlush: 20 * time.Millisecond,
	})
	go func() {
		err := r.Run(ctx)
		j.Close()
		n.done <- err
	}()
}

func (n *node) stop() {
	if n.cancel == nil {
		return
	}
	n.cancel()
	select {
	case <-n.done:
	case <-time.After(10 * time.Second):
		n.t.Error("runner did not stop")
	}
	n.cancel = nil
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting until %s", what)
}

// passing implements with two events and accepts with a passing verdict.
func passing(spec harness.WorkerSpec) testkit.WorkerScript {
	if spec.Role == protocol.RoleAccept {
		return testkit.WorkerScript{Result: harness.WorkerResult{Verdict: &protocol.VerdictPayload{Verdict: protocol.VerdictPassed, Evidence: json.RawMessage(`{"tests":"ok"}`)}}}
	}
	return testkit.WorkerScript{Events: []harness.WorkerEvent{
		{NativeID: "item_1", Kind: "item.started"}, {NativeID: "item_1", Kind: "item.completed", Body: json.RawMessage(`{"text":"done"}`)},
	}}
}

func TestEndToEnd_RunnerCompletesTask(t *testing.T) {
	srv := newServer(t, time.Minute)
	n := newNode(t, srv, passing)
	n.start()
	task := srv.createTask()
	eventually(t, "the task completes", func() bool { return srv.task(task).Lifecycle == protocol.TaskCompleted })

	if got := srv.count(`SELECT COUNT(*) FROM runs`); got != 2 {
		t.Errorf("runs = %d, want an implement and an acceptance run", got)
	}
	eventually(t, "the implement events are uploaded", func() bool { return srv.count(`SELECT COUNT(*) FROM run_logs`) == 2 })
	n.stop()
	if runs, err := n.journal().Runs(context.Background()); err != nil || len(runs) != 0 {
		t.Errorf("journal still holds %+v (%v)", runs, err)
	}
}

func TestFault_ClaimReceiptLost_Runner(t *testing.T) {
	t.Run("journaled claim is resent", func(t *testing.T) {
		srv := newServer(t, time.Minute)
		n := newNode(t, srv, passing)
		task := srv.createTask()
		// The runner journaled a claim and crashed before the answer.
		env := &protocol.Envelope{RequestID: srv.ids.New(protocol.PrefixRequest), Operation: protocol.OpRunnerClaim,
			Target: protocol.Target{Type: "host", ID: "linux-1"}, Payload: json.RawMessage(`{}`)}
		j := n.journal()
		if err := j.PutRequest(context.Background(), journal.Request{Envelope: *env}); err != nil {
			t.Fatal(err)
		}
		j.Close()
		srv.claimDirectly(env)

		n.start()
		eventually(t, "the task completes", func() bool { return srv.task(task).Lifecycle == protocol.TaskCompleted })
		if got := srv.count(`SELECT COUNT(*) FROM runs WHERE role = 'implement'`); got != 1 {
			t.Errorf("implement runs = %d, want 1", got)
		}
	})
	t.Run("unjournaled claim is handed over again", func(t *testing.T) {
		srv := newServer(t, time.Minute)
		n := newNode(t, srv, passing)
		task := srv.createTask()
		srv.claimDirectly(&protocol.Envelope{RequestID: srv.ids.New(protocol.PrefixRequest), Operation: protocol.OpRunnerClaim,
			Target: protocol.Target{Type: "host", ID: "linux-1"}, Payload: json.RawMessage(`{}`)})

		n.start()
		eventually(t, "the task completes", func() bool { return srv.task(task).Lifecycle == protocol.TaskCompleted })
		if got := srv.count(`SELECT COUNT(*) FROM runs WHERE role = 'implement'`); got != 1 {
			t.Errorf("implement runs = %d, want 1", got)
		}
	})
}

func TestFault_HeartbeatLostRunnerAlive_Runner(t *testing.T) {
	srv := newServer(t, 400*time.Millisecond)
	hold := make(chan struct{})
	n := newNode(t, srv, func(spec harness.WorkerSpec) testkit.WorkerScript {
		if spec.Role == protocol.RoleImplement && spec.Attempt == 1 {
			// The first worker would run forever; only fencing stops it.
			return testkit.WorkerScript{Gate: hold}
		}
		return passing(spec)
	})
	n.start()
	task := srv.createTask()
	eventually(t, "the worker starts", func() bool { return len(n.workers.Started()) == 1 })

	n.net.set(true)
	eventually(t, "the runner fences its worker", func() bool { return len(n.workers.Stopped()) == 1 })
	eventually(t, "tend marks the run unknown", func() bool { return srv.runState(protocol.RoleImplement) == protocol.RunUnknown })
	if got := srv.count(`SELECT COUNT(*) FROM runs`); got != 1 {
		t.Fatalf("runs while the runner is unreachable = %d, want 1", got)
	}

	n.net.set(false)
	eventually(t, "the task completes after reconciliation", func() bool { return srv.task(task).Lifecycle == protocol.TaskCompleted })
	started := n.workers.Started()
	if len(started) < 2 || started[1].Role != protocol.RoleImplement || started[1].Attempt != 2 {
		t.Errorf("workers started: %+v, want the implementation resumed as attempt 2", started)
	}
	if got := srv.count(`SELECT COUNT(*) FROM runs WHERE role = 'implement'`); got != 1 {
		t.Errorf("implement runs = %d, want the same run resumed", got)
	}
}

func TestRunnerStopsCancelledRun(t *testing.T) {
	srv := newServer(t, 400*time.Millisecond)
	n := newNode(t, srv, func(harness.WorkerSpec) testkit.WorkerScript { return testkit.WorkerScript{Gate: make(chan struct{})} })
	n.start()
	task := srv.createTask()
	eventually(t, "the worker starts", func() bool { return len(n.workers.Started()) == 1 })
	rev := srv.task(task).ControlRevision
	b, _ := json.Marshal(map[string]any{})
	env := &protocol.Envelope{RequestID: srv.ids.New(protocol.PrefixRequest), Operation: "tasks.cancel",
		Target: protocol.Target{Type: "task", ID: task}, ExpectedControlRevision: &rev, Payload: b}
	if op, err := srv.app.Execute(context.Background(), owner, env, app.CancelTask{}); err != nil || op.Status != protocol.OpSucceeded {
		t.Fatalf("cancel: %+v, %v", op, err)
	}
	eventually(t, "the worker is stopped", func() bool { return len(n.workers.Stopped()) == 1 })
	eventually(t, "the run is released", func() bool { return srv.runState(protocol.RoleImplement) == protocol.RunFailed })
}

func TestRunnerResumesAfterRestart(t *testing.T) {
	srv := newServer(t, time.Minute)
	hold := make(chan struct{})
	n := newNode(t, srv, func(spec harness.WorkerSpec) testkit.WorkerScript {
		if spec.Role == protocol.RoleImplement && spec.Attempt == 1 {
			return testkit.WorkerScript{Gate: hold}
		}
		return passing(spec)
	})
	n.start()
	task := srv.createTask()
	eventually(t, "the worker starts", func() bool { return len(n.workers.Started()) == 1 })
	n.stop()

	n.start()
	eventually(t, "the task completes after the restart", func() bool { return srv.task(task).Lifecycle == protocol.TaskCompleted })
	if got := srv.count(`SELECT COUNT(*) FROM runs WHERE role = 'implement'`); got != 1 {
		t.Errorf("implement runs = %d, want the same run resumed", got)
	}
}

func TestRunnerReleasesRunWithoutChanges(t *testing.T) {
	srv := newServer(t, time.Minute)
	n := newNode(t, srv, passing)
	n.ws.noChanges = true
	n.start()
	task := srv.createTask()
	eventually(t, "the task waits for a decision", func() bool {
		row := srv.task(task)
		return row.Lifecycle == protocol.TaskQueued && row.BlockReason == protocol.BlockWaitingDecision
	})
	if got := srv.count(`SELECT COUNT(*) FROM decisions`); got != 1 {
		t.Errorf("decisions = %d, want 1", got)
	}
}

// waitsForDecision reports whether the task is queued behind an open
// decision, as after a failed implementation run.
func (s *server) waitsForDecision(task string) bool {
	row := s.task(task)
	return row.Lifecycle == protocol.TaskQueued && row.BlockReason == protocol.BlockWaitingDecision
}

func (s *server) released(reason string) int {
	return s.count(`SELECT COUNT(*) FROM events WHERE kind = 'run.released' AND json_extract(body_json, '$.reason') = ?`, reason)
}

// TestFault_WorkerTamperedGit: the run fails as git_tampered and leaves no
// artifact. Detection itself is gitws's TestTamperingIsDetected.
func TestFault_WorkerTamperedGit(t *testing.T) {
	srv := newServer(t, time.Minute)
	n := newNode(t, srv, passing)
	n.ws.collectErr = fmt.Errorf("%w: HEAD moved", runner.ErrTampered)
	n.start()
	task := srv.createTask()
	eventually(t, "the task waits for a decision", func() bool { return srv.waitsForDecision(task) })
	if got := srv.released("git_tampered"); got != 1 {
		t.Errorf("runs released as git_tampered = %d, want 1", got)
	}
	if got := srv.count(`SELECT COUNT(*) FROM artifacts`); got != 0 {
		t.Errorf("artifacts = %d, want none from a tampered workspace", got)
	}
}

// TestFault_FetchFailureNoStaleBase: a failed fetch fails the run as an
// environment problem before any workspace is reported. That gitws does
// not fall back to the stale clone is TestFetchFailureStopsPrepare.
func TestFault_FetchFailureNoStaleBase(t *testing.T) {
	srv := newServer(t, time.Minute)
	n := newNode(t, srv, passing)
	n.ws.prepareErr = map[protocol.RunRole]error{protocol.RoleImplement: fmt.Errorf("%w: remote unreachable", runner.ErrFetchFailed)}
	n.start()
	task := srv.createTask()
	eventually(t, "the task waits for a decision", func() bool { return srv.waitsForDecision(task) })
	if got := srv.released("fetch_failed"); got != 1 {
		t.Errorf("runs released as fetch_failed = %d, want 1", got)
	}
	if got := srv.count(`SELECT COUNT(*) FROM workspaces WHERE status = 'ready'`); got != 0 {
		t.Errorf("ready workspaces = %d, want none reported", got)
	}
}

// TestFault_PrepareFailure: a failing prepare is the environment's fault,
// not the implementation's. An implementation waits for the owner; an
// acceptance blocks the task without a verdict against the artifact.
func TestFault_PrepareFailure(t *testing.T) {
	t.Run("implement", func(t *testing.T) {
		srv := newServer(t, time.Minute)
		n := newNode(t, srv, passing)
		n.ws.prepareErr = map[protocol.RunRole]error{protocol.RoleImplement: errors.New("profile sync: exit status 1")}
		n.start()
		task := srv.createTask()
		eventually(t, "the task waits for a decision", func() bool { return srv.waitsForDecision(task) })
		if got := srv.released("workspace_prepare_failed"); got != 1 {
			t.Errorf("runs released as workspace_prepare_failed = %d, want 1", got)
		}
	})
	t.Run("accept", func(t *testing.T) {
		srv := newServer(t, time.Minute)
		n := newNode(t, srv, passing)
		n.ws.prepareErr = map[protocol.RunRole]error{protocol.RoleAccept: errors.New("profile sync: exit status 1")}
		n.start()
		task := srv.createTask()
		eventually(t, "the task waits on the acceptance environment", func() bool {
			return srv.task(task).BlockReason == protocol.BlockAcceptanceEnvUnavailable
		})
		if got := srv.count(`SELECT COUNT(*) FROM acceptances`); got != 0 {
			t.Errorf("verdicts = %d, want none against the artifact", got)
		}
		if got := srv.task(task).ImplementGeneration; got != 1 {
			t.Errorf("implement generation = %d, want no new implementation", got)
		}
	})
}

func (s *server) setMode(task string, mode protocol.ControlMode) {
	s.t.Helper()
	rev := s.task(task).ControlRevision
	b, _ := json.Marshal(map[string]any{"mode": mode})
	env := &protocol.Envelope{RequestID: s.ids.New(protocol.PrefixRequest), Operation: "control.set_mode",
		Target: protocol.Target{Type: "task", ID: task}, ExpectedControlRevision: &rev, Payload: b}
	if op, err := s.app.Execute(context.Background(), owner, env, app.SetControlMode{}); err != nil || op.Status != protocol.OpSucceeded {
		s.t.Fatalf("set mode: %+v, %v", op, err)
	}
}

// holdFor watches a condition that must stay false for d.
func holdFor(t *testing.T, d time.Duration, what string, bad func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if bad() {
			t.Fatalf("%s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHealthyRunnerKeepsShortLease(t *testing.T) {
	srv := newServer(t, 300*time.Millisecond)
	hold := make(chan struct{})
	n := newNode(t, srv, func(spec harness.WorkerSpec) testkit.WorkerScript {
		if spec.Role == protocol.RoleImplement {
			return testkit.WorkerScript{Gate: hold}
		}
		return passing(spec)
	})
	n.start()
	task := srv.createTask()
	eventually(t, "the worker starts", func() bool { return len(n.workers.Started()) == 1 })
	holdFor(t, 1500*time.Millisecond, "a healthy runner lost its lease or fenced its worker", func() bool {
		return srv.runState(protocol.RoleImplement) == protocol.RunUnknown || len(n.workers.Stopped()) > 0
	})
	close(hold)
	eventually(t, "the task completes", func() bool { return srv.task(task).Lifecycle == protocol.TaskCompleted })
}

func TestPauseStopsWorkerAndResumes(t *testing.T) {
	srv := newServer(t, 400*time.Millisecond)
	n := newNode(t, srv, func(spec harness.WorkerSpec) testkit.WorkerScript {
		if spec.Role == protocol.RoleImplement && spec.Attempt == 1 {
			return testkit.WorkerScript{Gate: make(chan struct{})}
		}
		return passing(spec)
	})
	n.start()
	task := srv.createTask()
	eventually(t, "the worker starts", func() bool { return len(n.workers.Started()) == 1 })

	srv.setMode(task, protocol.ControlPaused)
	eventually(t, "the paused worker is stopped", func() bool { return len(n.workers.Stopped()) == 1 })
	holdFor(t, 500*time.Millisecond, "a worker started while the task is paused", func() bool { return len(n.workers.Started()) > 1 })

	srv.setMode(task, protocol.ControlManaged)
	eventually(t, "the task completes after resuming", func() bool { return srv.task(task).Lifecycle == protocol.TaskCompleted })
	if started := n.workers.Started(); started[1].Role != protocol.RoleImplement || started[1].Attempt != 2 {
		t.Errorf("after resuming: %+v, want implementation attempt 2", started[1])
	}
}

func TestFinalEventsSurviveUploadFailure(t *testing.T) {
	srv := newServer(t, time.Minute)
	n := newNode(t, srv, passing)
	n.net.dropNext(protocol.OpRunnerEvents, 3)
	n.start()
	task := srv.createTask()
	eventually(t, "the task completes", func() bool { return srv.task(task).Lifecycle == protocol.TaskCompleted })
	if got := srv.count(`SELECT COUNT(*) FROM run_logs`); got != 2 {
		t.Errorf("run logs = %d, want both implement events despite failed uploads", got)
	}
}

func TestRestartKeepsShortLease(t *testing.T) {
	srv := newServer(t, 400*time.Millisecond)
	first, second := make(chan struct{}), make(chan struct{})
	n := newNode(t, srv, func(spec harness.WorkerSpec) testkit.WorkerScript {
		switch {
		case spec.Role == protocol.RoleAccept:
			return passing(spec)
		case spec.Attempt == 1:
			return testkit.WorkerScript{Gate: first}
		}
		return testkit.WorkerScript{Gate: second}
	})
	n.start()
	task := srv.createTask()
	eventually(t, "the worker starts", func() bool { return len(n.workers.Started()) == 1 })
	n.stop()
	n.start()
	eventually(t, "the worker resumes", func() bool { return len(n.workers.Started()) == 2 })
	holdFor(t, 1200*time.Millisecond, "the restarted runner let its short lease lapse", func() bool {
		return srv.runState(protocol.RoleImplement) == protocol.RunUnknown || len(n.workers.Stopped()) > 1
	})
	close(second)
	eventually(t, "the task completes", func() bool { return srv.task(task).Lifecycle == protocol.TaskCompleted })
}

func TestRunnerReturnsStartupRejection(t *testing.T) {
	srv := newServer(t, time.Minute)
	n := newNode(t, srv, passing)
	j := n.journal()
	defer j.Close()
	r := runner.New(runner.Config{
		ServerURL: srv.url, HostID: "linux-1", HostToken: "wrong", HTTPClient: &http.Client{Transport: n.net}, Journal: j,
		Workers: n.workers, Workspaces: n.ws, Now: time.Now, IDs: protocol.NewULIDGen(time.Now, nil), Log: quiet(),
		RetryDelay: 20 * time.Millisecond,
	})
	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background()) }()
	select {
	case err := <-done:
		if protocol.CodeOf(err) != protocol.CodeForbidden {
			t.Errorf("Run = %v, want the forbidden rejection", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after tend rejected the runner")
	}
}

func TestLargeEventVolumesArrive(t *testing.T) {
	srv := newServer(t, time.Minute)
	big := json.RawMessage(`{"output":"` + strings.Repeat("x", 200<<10) + `"}`)
	n := newNode(t, srv, func(spec harness.WorkerSpec) testkit.WorkerScript {
		if spec.Role == protocol.RoleAccept {
			return passing(spec)
		}
		var events []harness.WorkerEvent
		for range 30 {
			events = append(events, harness.WorkerEvent{Kind: "item.completed", Body: big})
		}
		// One body too large to keep whole.
		events = append(events, harness.WorkerEvent{Kind: "item.completed", Body: json.RawMessage(`"` + strings.Repeat("y", 300<<10) + `"`)})
		return testkit.WorkerScript{Events: events}
	})
	n.net.dropNext(protocol.OpRunnerEvents, 1000) // hold every upload until the final flush
	n.start()
	task := srv.createTask()
	eventually(t, "the worker finishes", func() bool { return len(n.workers.Started()) == 1 })
	time.Sleep(200 * time.Millisecond)
	n.net.allow(protocol.OpRunnerEvents)
	eventually(t, "the task completes", func() bool { return srv.task(task).Lifecycle == protocol.TaskCompleted })
	if got := srv.count(`SELECT COUNT(*) FROM run_logs`); got != 31 {
		t.Errorf("run logs = %d, want all 31 events", got)
	}
	if got := srv.count(`SELECT COUNT(*) FROM run_logs WHERE body_json LIKE '%"truncated":true%'`); got != 1 {
		t.Errorf("truncated events = %d, want the oversized one", got)
	}
}

func TestRejectedReportReleasesRun(t *testing.T) {
	srv := newServer(t, time.Minute)
	n := newNode(t, srv, func(spec harness.WorkerSpec) testkit.WorkerScript {
		if spec.Role == protocol.RoleAccept {
			return testkit.WorkerScript{Result: harness.WorkerResult{Verdict: &protocol.VerdictPayload{Verdict: "looks fine"}}}
		}
		return passing(spec)
	})
	n.start()
	task := srv.createTask()
	eventually(t, "the task waits on the acceptance environment", func() bool {
		return srv.task(task).BlockReason == protocol.BlockAcceptanceEnvUnavailable
	})
	if st := srv.runState(protocol.RoleAccept); st != protocol.RunFailed {
		t.Errorf("acceptance run = %s, want failed after its report was rejected", st)
	}
}
