package runner

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/netx"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/runnerapi"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestRetainedStrictRetryObservesAuthoritativeStop(t *testing.T) {
	for _, blockEvents := range []bool{false, true} {
		t.Run(map[bool]string{false: "original-accepted-release", true: "event-transport-unknown"}[blockEvents], func(t *testing.T) {
			testRetainedStrictRetryObservesAuthoritativeStop(t, blockEvents)
		})
	}
}

func testRetainedStrictRetryObservesAuthoritativeStop(t *testing.T, blockEvents bool) {
	t.Helper()
	ctx := t.Context()
	clock := testkit.NewClock()
	ids := testkit.NewIDGen(clock, 19)
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "server.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := app.New(db, ids, clock.Now)
	execute := func(p protocol.Principal, op string, target protocol.Target, payload any, rev *int64, cmd app.Command) protocol.Operation {
		t.Helper()
		if op == "tasks.create" && target.ID == "" {
			target.ID = ids.New(protocol.PrefixTask)
		}
		raw, e := json.Marshal(payload)
		if e != nil {
			t.Fatal(e)
		}
		result, e := a.Execute(ctx, p, &protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: op, Target: target, Payload: raw, ExpectedControlRevision: rev}, cmd)
		if e != nil || result.Status != protocol.OpSucceeded {
			t.Fatalf("%s: status=%s err=%v operation_error=%v result=%s", op, result.Status, e, result.Error, result.Result)
		}
		return result
	}
	owner := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "cli"}
	execute(protocol.Principal{Kind: protocol.PrincipalSystem}, "config.sync", protocol.Target{Type: "config"}, app.ConfigPayload{Repos: []app.RepoConfig{{Repo: "tend", RemoteURL: "https://example.com/tend.git"}}, Hosts: []app.HostConfig{{HostID: "worker", OS: "linux", Arch: "amd64", Slots: 1, Worker: protocol.WorkerOrca, Repos: []string{"tend"}, TokenHash: runnerapi.HashToken("host-secret"), Nodes: []string{"tag:tend-runner"}}}}, nil, app.SyncConfig{})
	execute(owner, "tasks.create", protocol.Target{Type: "task"}, map[string]any{"title": "retained worker", "spec": domain.Spec{Goal: "retain unknown execution", Acceptance: []string{"do not repeat"}, Delivery: domain.DeliveryCommit, Workspace: domain.Workspace{Mounts: []domain.Mount{{Repo: "tend"}}}}}, nil, app.CreateTask{})
	runtime := protocol.RunnerRuntime{InstanceID: "worker-process", Generation: 1, Sequence: 1, Protocol: protocol.CurrentRunnerProtocol, ProtocolMin: protocol.PreviousRunnerProtocol, ProtocolMax: protocol.CurrentRunnerProtocol, Mode: protocol.RunnerNormal}
	hp := protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "worker"}
	execute(hp, protocol.OpRunnerHello, protocol.Target{Type: "host", ID: "worker"}, protocol.HelloPayload{Worker: protocol.WorkerOrca, WorkspaceProtocol: 1, Runtime: &runtime}, nil, app.Hello{})
	runtime.Sequence++
	claim := execute(hp, protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "worker"}, protocol.ClaimPayload{RunID: ids.New(protocol.PrefixRun), Worker: protocol.WorkerOrca, WorkspaceProtocol: 1, Runtime: &runtime}, nil, app.Claim{})
	var claimed struct {
		Assignment *protocol.Assignment `json:"assignment"`
	}
	if err := json.Unmarshal(claim.Result, &claimed); err != nil || claimed.Assignment == nil {
		t.Fatal("no assignment", err)
	}
	as := *claimed.Assignment
	secret := []byte("recovery-wire-secret")
	as.RunToken = runnerapi.RunToken(secret, as.LeaseID)
	var before store.RunRow
	var task store.TaskRow
	var action store.ActionRow
	if err := db.Read(ctx, func(tx *store.ReadTx) error {
		var e error
		before, e = store.GetRun(ctx, tx.Q(), as.RunID)
		if e != nil {
			return e
		}
		task, e = store.GetTask(ctx, tx.Q(), as.TaskID)
		if e != nil {
			return e
		}
		action, e = store.GetAction(ctx, tx.Q(), before.ActionID)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	pin := protocol.RunExecutionPin{SnapshotID: "original-pin", AgentRevision: 1, Runtime: protocol.AgentRuntimeReference{RuntimeID: "original-runtime", Revision: 1}}
	if err := db.Write(ctx, func(tx *store.Tx) error { return tx.InsertRunExecution(ctx, as.RunID, pin) }); err != nil {
		t.Fatal(err)
	}
	prep, err := a.ChangeArchivePreparation(ctx, protocol.ArchiveControlRequest{RequestID: ids.New(protocol.PrefixRequest)}, true)
	if err != nil {
		t.Fatal(err)
	}
	p := protocol.CommittedMaintenancePlan{MaintenanceID: "wire-recovery", RequestID: ids.New(protocol.PrefixRequest), LinkRequestID: ids.New(protocol.PrefixRequest), PreparationRevision: prep.Revision, Previous: protocol.UpgradeBinding{InstanceID: "server", ProcessInstanceID: "previous", ArtifactSHA256: strings.Repeat("a", 64)}, Successor: protocol.UpgradeBinding{UpgradeID: "wire-recovery", InstanceID: "server", ProcessInstanceID: "successor", ArtifactSHA256: strings.Repeat("b", 64)}, ReceiptRecoveryStop: &protocol.CommittedReceiptRecoveryStop{Runs: []protocol.CommittedRetainedRun{{TaskID: as.TaskID, Run: protocol.RunView{RunID: as.RunID, Role: as.Role, Host: before.HostID, Generation: before.Generation, SpecVersion: action.TargetSpecVersion, Observed: before.ObservedState}}}}}
	process := protocol.RecoveryIdentity{PID: 123, StartTicks: 456, LaunchID: "successor", SHA256: p.Successor.ArtifactSHA256}
	if _, err := a.RegisterCommittedMaintenance(ctx, p, process); err != nil {
		t.Fatal(err)
	}
	runtime.Sequence++
	runtime.JournalVersion = 12
	runtime.Capabilities = []string{protocol.CapabilityAgentExecution}
	raw, _ := json.Marshal(protocol.HelloPayload{Worker: protocol.WorkerOrca, WorkspaceProtocol: 1, Runtime: &runtime, Held: []protocol.HeldRun{{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunRunning}}})
	if _, err := a.Execute(ctx, hp, &protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: protocol.OpRunnerHello, Target: protocol.Target{Type: "host", ID: "worker"}, Payload: raw}, app.Hello{}); protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
		t.Fatal("protected Hello continued", err)
	}
	journalPath := filepath.Join(t.TempDir(), "journal.db")
	j, err := journal.Open(ctx, journalPath, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if err := j.AcceptAssignmentFromWorker(ctx, as, "", protocol.WorkerOrca); err != nil {
		t.Fatal(err)
	}
	if err := j.SetThread(ctx, as.RunID, "original-native-unknown-thread"); err != nil {
		t.Fatal(err)
	}
	authority := protocol.RetainedRunResume{RequestID: "original-strict-authority", Mode: "attach"}
	if err := j.SetRetainedResume(ctx, as.RunID, authority); err != nil {
		t.Fatal(err)
	}
	result, _ := json.Marshal(harness.WorkerResult{Summary: "same strict worker completed with no changes"})
	if err := j.WorkerDone(ctx, as.RunID, result); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(protocol.ReleasePayload{Reason: "no_changes"})
	env := protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: protocol.OpRunsRelease, Target: protocol.Target{Type: "run", ID: as.RunID}, Payload: raw}
	if err := j.Finish(ctx, as.RunID, journal.PhaseReleasing, journal.Request{RunID: as.RunID, Envelope: env}); err != nil {
		t.Fatal(err)
	}
	if err := j.ConfirmStopped(ctx, as.RunID); err != nil {
		t.Fatal(err)
	}

	handler := runnerapi.New(a, netx.Static{Identity: &netx.Identity{Node: "worker-node", Tags: []string{"tag:tend-runner"}}}, secret, runnerapi.Config{}, slog.New(slog.DiscardHandler)).Handler()
	entered := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	var releaseRequests []protocol.Envelope
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		body, e := io.ReadAll(q.Body)
		if e != nil {
			t.Error(e)
			return
		}
		q.Body = io.NopCloser(bytes.NewReader(body))
		var got protocol.Envelope
		if e := json.Unmarshal(body, &got); e != nil {
			t.Error(e)
			return
		}
		if got.Operation == protocol.OpRunnerEvents && blockEvents {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(rpcResponse{Error: protocol.Errorf(protocol.CodeUnknownOutcome, "event upload outcome is unknown")})
			once.Do(func() { close(entered) })
			return
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, q)
		if got.Operation == protocol.OpRunsRelease {
			mu.Lock()
			releaseRequests = append(releaseRequests, got)
			n := len(releaseRequests)
			mu.Unlock()
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("expected immutable accepted RPC outcome, got %d: %s", rec.Code, rec.Body.String())
			}
			if !blockEvents && n >= 2 {
				once.Do(func() { close(entered) })
			}
		}
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	}))
	defer server.Close()
	ws := &recoveryFinishProbe{}
	r := New(Config{Journal: j, Workspaces: ws, Executions: &observedExecutions{}, ServerURL: server.URL, HTTPClient: server.Client(), Now: clock.Now, IDs: ids, HostID: "worker", HostToken: "host-secret", Worker: protocol.WorkerOrca, InstanceID: runtime.InstanceID, RetryDelay: time.Millisecond, RequestTimeout: time.Second, Log: slog.New(slog.DiscardHandler)})
	// Commit the physical give-up through the real handler before supersession.
	// Its durable accepted operation is returned as unknown/500 on the wire.
	if _, err := r.c.do(ctx, runCredential(as), &env); !isRetryable(err) || protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
		t.Fatal("protected release did not produce a durable unknown outcome", err)
	}
	var accepted maintenancePhysicalStopSnapshot
	if err := db.Read(ctx, func(tx *store.ReadTx) error {
		var e error
		accepted, e = readMaintenancePhysicalStop(ctx, tx.Q(), as.RunID, env.RequestID)
		return e
	}); err != nil || accepted.Operation != env.Operation || accepted.Payload != string(env.Payload) {
		t.Fatal("original accepted fact missing", accepted.Operation, err)
	}
	if blockEvents {
		if err := j.AppendEvent(ctx, as.RunID, protocol.RunEvent{NativeID: "original-unapplied-event", Kind: "turn.observed", Body: json.RawMessage(`{"state":"unknown"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	loopCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer func() { cancel(); r.wg.Wait() }()
	r.start(loopCtx, as, r.cfg.Now())
	select {
	case <-entered:
	case <-loopCtx.Done():
		t.Fatal("strict report did not attempt its original RPC")
	}
	if _, err := a.LinkCommittedMaintenance(ctx, protocol.CommittedMaintenanceReceipt{MaintenanceID: p.MaintenanceID, RequestID: p.RequestID, Previous: p.Previous, Successor: p.Successor, Process: process, Generation: 9, Epoch: process.LaunchID, FirstSequence: 1, SecondSequence: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ChangeArchivePreparation(ctx, protocol.ArchiveControlRequest{RequestID: ids.New(protocol.PrefixRequest), ExpectedControlRevision: prep.Revision}, false); err != nil {
		t.Fatal(err)
	}
	if err := db.Read(ctx, func(tx *store.ReadTx) error { var e error; task, e = store.GetTask(ctx, tx.Q(), as.TaskID); return e }); err != nil {
		t.Fatal(err)
	}
	var changed domain.Spec
	if err := json.Unmarshal(as.Spec, &changed); err != nil {
		t.Fatal(err)
	}
	changed.Goal = "follow-up specification supersedes the original run"
	execute(owner, "tasks.update_intent", protocol.Target{Type: "task", ID: as.TaskID}, map[string]any{"spec": changed}, &task.ControlRevision, app.UpdateIntent{})
	leaseID, _, _, err := a.RunCredential(ctx, as.RunID)
	if err != nil || leaseID != as.LeaseID {
		t.Fatal("supersession unexpectedly revoked original authentication", leaseID, err)
	}
	var superseded store.RunRow
	if err := db.Read(ctx, func(tx *store.ReadTx) error {
		var e error
		superseded, e = store.GetRun(ctx, tx.Q(), as.RunID)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	r.controlLoaded = true
	r.instanceID = runtime.InstanceID
	r.generation = runtime.Generation
	r.sequence = runtime.Sequence
	r.control.Mode = protocol.RunnerNormal
	if err := r.freshHello(loopCtx); err != nil {
		t.Fatal("superseded run did not obtain authoritative Stop", err)
	}
	proof, err := j.RetainedStop(ctx, as.RunID)
	if err != nil || proof == nil {
		t.Fatal("original Stop was not durably recorded", proof, err)
	}
	done := make(chan struct{})
	go func() { defer close(done); r.reconciler(loopCtx) }()
	defer func() { cancel(); <-done }()
	for {
		saved, err := j.HistoricalRun(ctx, as.RunID)
		if err == nil && saved.Phase == journal.PhaseDone {
			break
		}
		select {
		case <-loopCtx.Done():
			t.Fatal("strict transport retry ignored the already durable original Stop")
		case <-time.After(time.Millisecond):
		}
	}
	r.wg.Wait()
	if ws.finishes.Load() != 1 {
		t.Fatal("Stop did not settle responsibility exactly once", ws.finishes.Load())
	}
	if err := db.Read(ctx, func(tx *store.ReadTx) error {
		actual, e := store.GetRun(ctx, tx.Q(), as.RunID)
		if e != nil {
			return e
		}
		if !reflect.DeepEqual(actual, superseded) {
			t.Fatal("local cleanup rewrote the historical server run", actual, superseded)
		}
		original, e := readMaintenancePhysicalStop(ctx, tx.Q(), as.RunID, env.RequestID)
		if e != nil {
			return e
		}
		if !reflect.DeepEqual(original, accepted) {
			t.Fatal("Stop changed the immutable accepted request")
		}
		execution, e := store.GetRunExecution(ctx, tx.Q(), as.RunID)
		if e == nil && !reflect.DeepEqual(execution, &pin) {
			t.Fatal("Stop changed original execution pin", execution)
		}
		return e
	}); err != nil {
		t.Fatal(err)
	}
	saved, err := j.HistoricalRun(ctx, as.RunID)
	if err != nil || saved.Thread != "original-native-unknown-thread" {
		t.Fatal("Stop changed original native identity", saved.Thread, err)
	}
	historyDB, err := sql.Open("sqlite", "file:"+journalPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer historyDB.Close()
	var history string
	if err := historyDB.QueryRowContext(ctx, `SELECT authority_json FROM retained_run_resumes WHERE run_id=?`, as.RunID).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(history, env.RequestID) || blockEvents && !strings.Contains(history, "original-unapplied-event") {
		t.Fatal("Stop lost unapplied original facts", history)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, got := range releaseRequests {
		if !reflect.DeepEqual(got, env) {
			t.Fatal("strict pending retry changed original envelope", got, env)
		}
	}
}
