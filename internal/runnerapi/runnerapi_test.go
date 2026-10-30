package runnerapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/netx"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

const (
	hostToken = "host-secret-token"
	baseSHA   = "1111111111111111111111111111111111111111"
	commitSHA = "2222222222222222222222222222222222222222"
)

var secret = []byte("server-secret")

// peers answers WhoIs with whatever identity the test set last.
type peers struct {
	mu sync.Mutex
	id *netx.Identity
}

func (p *peers) set(id *netx.Identity) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.id = id
}

func (p *peers) WhoIs(ctx context.Context, addr string) (netx.Identity, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return netx.Static{Identity: p.id}.WhoIs(ctx, addr)
}

var runnerNode = &netx.Identity{Node: "build-box", Tags: []string{"tag:tend-runner"}}

type env struct {
	t      *testing.T
	app    *app.App
	store  *store.Store
	ids    protocol.IDGen
	server *httptest.Server
	peers  *peers
	logs   *syncBuffer
}

// syncBuffer is a bytes.Buffer safe for the server's goroutines and the test.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newEnv(t *testing.T, claimWait time.Duration) *env {
	t.Helper()
	clock := testkit.NewClock()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "tend.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ids := testkit.NewIDGen(clock, 3)
	a := app.New(s, ids, clock.Now)
	e := &env{t: t, app: a, store: s, ids: ids, peers: &peers{id: runnerNode}, logs: &syncBuffer{}}
	e.system("config.sync", app.ConfigPayload{
		Repos: []app.RepoConfig{{Repo: "tend", RemoteURL: "https://example.com/tend.git"}},
		Hosts: []app.HostConfig{{HostID: "linux-1", OS: "linux", Arch: "amd64", Repos: []string{"tend"}, Slots: 1,
			TokenHash: HashToken(hostToken), Nodes: []string{"tag:tend-runner"}}},
	}, app.SyncConfig{})
	srv := New(a, e.peers, secret, Config{ClaimWait: claimWait}, slog.New(slog.NewTextHandler(e.logs, nil)))
	e.server = httptest.NewServer(srv.Handler())
	t.Cleanup(e.server.Close)
	return e
}

func (e *env) system(op string, payload any, cmd app.Command) {
	e.t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		e.t.Fatal(err)
	}
	envl := &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: op, Target: protocol.Target{Type: "x"}, Payload: b}
	got, err := e.app.Execute(context.Background(), protocol.Principal{Kind: protocol.PrincipalSystem}, envl, cmd)
	if err != nil || got.Status != protocol.OpSucceeded {
		e.t.Fatalf("%s: %+v, %v", op, got, err)
	}
}

func (e *env) createTask() {
	e.t.Helper()
	b, _ := json.Marshal(map[string]any{"title": "a", "spec": domain.Spec{
		Goal: "g", Acceptance: []string{"a"}, Delivery: domain.DeliveryCommit,
		Workspace: domain.Workspace{Mounts: []domain.Mount{{Repo: "tend"}}},
	}})
	envl := &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: "tasks.create", Target: protocol.Target{Type: "task"}, Payload: b}
	owner := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "me", Client: "c"}
	if op, err := e.app.Execute(context.Background(), owner, envl, app.CreateTask{}); err != nil || op.Status != protocol.OpSucceeded {
		e.t.Fatalf("create task: %+v, %v", op, err)
	}
}

type credential struct{ header, subject, token string }

func asHost() credential                     { return credential{HeaderHost, "linux-1", hostToken} }
func asRun(a protocol.Assignment) credential { return credential{HeaderRun, a.RunID, a.RunToken} }

// call posts an envelope and returns the HTTP status and decoded response.
func (e *env) call(c credential, envl *protocol.Envelope) (int, response) {
	e.t.Helper()
	b, err := json.Marshal(envl)
	if err != nil {
		e.t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, e.server.URL+Path, bytes.NewReader(b))
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set(c.header, c.subject)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out response
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		e.t.Fatal(err)
	}
	return resp.StatusCode, out
}

func (e *env) envelope(op, targetID string, payload any) *protocol.Envelope {
	b, err := json.Marshal(payload)
	if err != nil {
		e.t.Fatal(err)
	}
	if op == protocol.OpRunnerHello || op == protocol.OpRunnerClaim || op == protocol.OpRunnerHeartbeat {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(b, &fields); err != nil {
			e.t.Fatal(err)
		}
		if fields["runtime"] == nil || string(fields["runtime"]) == "null" {
			if op == protocol.OpRunnerClaim {
				missing := false
				_ = e.store.Read(context.Background(), func(tx *store.ReadTx) error {
					_, err := store.GetHostRuntime(context.Background(), tx.Q(), "linux-1")
					missing = err != nil
					return nil
				})
				if missing {
					e.mustOK(asHost(), e.envelope(protocol.OpRunnerHello, "linux-1", protocol.HelloPayload{WorkspaceProtocol: 1}))
				}
			}
			runtime := protocol.RunnerRuntime{InstanceID: "fixture-runner", Generation: 1, Sequence: 1, Protocol: protocol.CurrentRunnerProtocol, ProtocolMin: protocol.PreviousRunnerProtocol, ProtocolMax: protocol.CurrentRunnerProtocol, Mode: protocol.RunnerNormal}
			_ = e.store.Read(context.Background(), func(tx *store.ReadTx) error {
				old, err := store.GetHostRuntime(context.Background(), tx.Q(), "linux-1")
				if err == nil {
					runtime = old.Runtime
					runtime.Sequence++
				}
				return nil
			})
			fields["runtime"], _ = json.Marshal(runtime)
			b, _ = json.Marshal(fields)
		}
	}

	return &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: op, Target: protocol.Target{Type: "run", ID: targetID}, Payload: b}
}

func (e *env) mustOK(c credential, envl *protocol.Envelope) json.RawMessage {
	e.t.Helper()
	status, res := e.call(c, envl)
	if status != http.StatusOK || res.Operation == nil || res.Operation.Status != protocol.OpSucceeded {
		e.t.Fatalf("%s: status %d, %+v %+v", envl.Operation, status, res.Operation, res.Error)
	}
	return res.Operation.Result
}

func (e *env) claim(envl *protocol.Envelope) *protocol.Assignment {
	e.t.Helper()
	var res struct {
		Assignment *protocol.Assignment `json:"assignment"`
	}
	if err := json.Unmarshal(e.mustOK(asHost(), envl), &res); err != nil {
		e.t.Fatal(err)
	}
	return res.Assignment
}

func TestAuthentication(t *testing.T) {
	e := newEnv(t, 10*time.Millisecond)
	hello := e.envelope(protocol.OpRunnerHello, "", protocol.HelloPayload{})
	cases := []struct {
		name string
		c    credential
		peer *netx.Identity
	}{
		{"wrong host token", credential{HeaderHost, "linux-1", "guess"}, runnerNode},
		{"unknown host", credential{HeaderHost, "mac-1", hostToken}, runnerNode},
		{"host token from another node", asHost(), &netx.Identity{Node: "laptop"}},
		{"not on the tailnet", asHost(), nil},
		{"forged run token", credential{HeaderRun, "run_00000000000000000000000000", "x"}, runnerNode},
	}
	for _, tc := range cases {
		e.peers.set(tc.peer)
		if status, res := e.call(tc.c, hello); status != http.StatusForbidden || res.Error == nil || res.Error.Message != "authentication failed" {
			t.Errorf("%s: status %d, %+v", tc.name, status, res.Error)
		}
	}
	e.peers.set(runnerNode)
	e.mustOK(asHost(), hello)
}

func TestRunLifecycleOverHTTP(t *testing.T) {
	e := newEnv(t, 10*time.Millisecond)
	e.createTask()
	claimEnv := e.envelope(protocol.OpRunnerClaim, "", map[string]any{})
	as := e.claim(claimEnv)
	if as == nil || as.RunToken != RunToken(secret, as.LeaseID) {
		t.Fatalf("assignment = %+v", as)
	}
	// A retried claim gets the same run and the same token.
	if again := e.claim(claimEnv); again == nil || again.RunID != as.RunID || again.RunToken != as.RunToken {
		t.Errorf("retried claim = %+v", again)
	}

	run := asRun(*as)
	e.mustOK(run, e.envelope(protocol.OpRunsReportWorkspace, as.RunID, protocol.WorkspaceReport{Mounts: []protocol.ResolvedMount{
		{Path: "tend", ResolvedBaseSHA: baseSHA, ResolvedFrom: "candidate:main", Branch: as.Workspace.Branch},
	}}))
	e.mustOK(run, e.envelope(protocol.OpRunsReportStarted, as.RunID, nil))
	var ev protocol.EventsResult
	if err := json.Unmarshal(e.mustOK(run, e.envelope(protocol.OpRunnerEvents, as.RunID, protocol.EventsPayload{
		Events: []protocol.RunEvent{{Seq: 1, Kind: "item.completed"}},
	})), &ev); err != nil || ev.Accepted != 1 {
		t.Errorf("events = %+v, %v", ev, err)
	}
	var hb protocol.HeartbeatResult
	if err := json.Unmarshal(e.mustOK(asHost(), e.envelope(protocol.OpRunnerHeartbeat, "", protocol.HeartbeatPayload{
		Runs: []protocol.LeaseRef{{RunID: as.RunID, LeaseID: as.LeaseID}},
	})), &hb); err != nil || len(hb.Runs) != 1 || !hb.Runs[0].Renewed {
		t.Errorf("heartbeat = %+v, %v", hb, err)
	}

	// The run's credential cannot act as the host, nor the host as the run.
	if status, _ := e.call(run, e.envelope(protocol.OpRunnerClaim, "", map[string]any{})); status != http.StatusForbidden {
		t.Errorf("claim with a run credential: %d", status)
	}
	if status, _ := e.call(asHost(), e.envelope(protocol.OpRunsReportStarted, as.RunID, nil)); status != http.StatusForbidden {
		t.Errorf("run operation with the host credential: %d", status)
	}

	e.mustOK(run, e.envelope(protocol.OpRunsSubmitArtifact, as.RunID, protocol.ArtifactPayload{Mounts: []protocol.ArtifactMount{
		{Repo: "tend", Path: "tend", BaseSHA: baseSHA, Branch: as.Workspace.Branch, CommitSHA: commitSHA, Changed: true},
	}}))
	// The lease is released, so the run's token no longer authenticates.
	if status, _ := e.call(run, e.envelope(protocol.OpRunsReportStarted, as.RunID, nil)); status != http.StatusForbidden {
		t.Errorf("run token after the run ended: %d", status)
	}
}

func TestClaimWaitsForWork(t *testing.T) {
	e := newEnv(t, 5*time.Second)
	got := make(chan *protocol.Assignment, 1)
	go func() { got <- e.claim(e.envelope(protocol.OpRunnerClaim, "", map[string]any{})) }()
	time.Sleep(100 * time.Millisecond)
	e.createTask()
	select {
	case as := <-got:
		if as == nil {
			t.Fatal("claim returned without work")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("claim did not wake up when work arrived")
	}
}

func TestClaimTimesOutWithoutWork(t *testing.T) {
	e := newEnv(t, 50*time.Millisecond)
	start := time.Now()
	if as := e.claim(e.envelope(protocol.OpRunnerClaim, "", map[string]any{})); as != nil {
		t.Fatalf("claim found work: %+v", as)
	}
	if d := time.Since(start); d < 50*time.Millisecond {
		t.Errorf("claim returned after %v, before the wait elapsed", d)
	}
}

// TestAbandonedClaimIsNotAnError covers a runner that stops while its claim
// waits for work.
func TestAbandonedClaimIsNotAnError(t *testing.T) {
	e := newEnv(t, 5*time.Second)
	b, err := json.Marshal(e.envelope(protocol.OpRunnerClaim, "", map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.server.URL+Path, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+hostToken)
	req.Header.Set(HeaderHost, "linux-1")
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
		t.Fatal("claim answered before the runner hung up")
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(e.logs.String(), "runner request abandoned") {
		if time.Now().After(deadline) {
			t.Fatalf("abandoned claim not logged:\n%s", e.logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if strings.Contains(e.logs.String(), "level=ERROR") {
		t.Errorf("abandoned claim logged as an error:\n%s", e.logs.String())
	}
}

func TestHelloReassignedCarriesToken(t *testing.T) {
	e := newEnv(t, 10*time.Millisecond)
	e.createTask()
	as := e.claim(e.envelope(protocol.OpRunnerClaim, "", map[string]any{}))
	var res protocol.HelloResult
	if err := json.Unmarshal(e.mustOK(asHost(), e.envelope(protocol.OpRunnerHello, "", protocol.HelloPayload{})), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Reassigned) != 1 || res.Reassigned[0].RunToken != as.RunToken {
		t.Errorf("reassigned = %+v, want run %s with its token", res.Reassigned, as.RunID)
	}
}
