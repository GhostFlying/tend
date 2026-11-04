package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// This uses the production RPC handler and unchanged legacy client/report loop.
// A durable accepted fact must never become a successful wire finalization on
// retry; only a new explicit owner cancellation may settle the business run.
func TestRetainedStrictStopWireOwnerCancelPreservesAcceptedHistory(t *testing.T) {
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
		raw, e := json.Marshal(payload)
		if e != nil {
			t.Fatal(e)
		}
		result, e := a.Execute(ctx, p, &protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: op, Target: target, Payload: raw, ExpectedControlRevision: rev}, cmd)
		if e != nil || result.Status != protocol.OpSucceeded {
			t.Fatalf("%s: status=%s err=%v result=%s", op, result.Status, e, result.Result)
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
	claim := execute(hp, protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "worker"}, protocol.ClaimPayload{Worker: protocol.WorkerOrca, WorkspaceProtocol: 1, Runtime: &runtime}, nil, app.Claim{})
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
	j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
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
	attemptCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	var requests []protocol.Envelope
	var statuses []int
	var codes []protocol.Code
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, e := io.ReadAll(request.Body)
		if e != nil {
			t.Error(e)
			return
		}
		request.Body = io.NopCloser(bytes.NewReader(body))
		var got protocol.Envelope
		if e := json.Unmarshal(body, &got); e != nil {
			t.Error(e)
			return
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, request)
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
		if got.Operation == protocol.OpRunsRelease {
			var reply rpcResponse
			_ = json.Unmarshal(rec.Body.Bytes(), &reply)
			mu.Lock()
			requests = append(requests, got)
			statuses = append(statuses, rec.Code)
			if reply.Error != nil {
				codes = append(codes, reply.Error.Code)
			}
			n := len(requests)
			mu.Unlock()
			if n == 3 {
				cancel()
			}
		}
	}))
	defer server.Close()
	ws := &recoveryFinishProbe{}
	r := New(Config{Journal: j, Workspaces: ws, ServerURL: server.URL, HTTPClient: server.Client(), Now: clock.Now, IDs: ids, HostID: "worker", HostToken: "host-secret", Worker: protocol.WorkerOrca, InstanceID: "worker-process", RetryDelay: time.Millisecond, RequestTimeout: time.Second, Log: slog.New(slog.DiscardHandler)})
	if err := r.report(attemptCtx, as); !errors.Is(err, context.Canceled) {
		t.Fatal("report did not remain pending", err)
	}
	mu.Lock()
	for i, got := range requests {
		if !reflect.DeepEqual(got, env) || statuses[i] != http.StatusInternalServerError || codes[i] != protocol.CodeUnknownOutcome {
			t.Fatal("retry changed identity or finalized accepted fact", i, statuses, codes)
		}
	}
	if len(requests) != 3 {
		t.Fatal("missing retry", len(requests))
	}
	mu.Unlock()
	retained, err := j.Run(ctx, as.RunID)
	if err != nil || retained.Phase != journal.PhaseReleasing || !retained.StoppedConfirmed || ws.finishes.Load() != 0 {
		t.Fatal("unknown give-up cleaned original journal/workspace", retained.Phase, err, ws.finishes.Load())
	}
	var accepted store.RequestRecord
	if err := db.Read(ctx, func(tx *store.ReadTx) error {
		now, e := store.GetRun(ctx, tx.Q(), as.RunID)
		if e != nil {
			return e
		}
		if !reflect.DeepEqual(before, now) {
			t.Fatal("physical give-up ended business run")
		}
		pinned, e := store.GetRunExecution(ctx, tx.Q(), as.RunID)
		if e != nil {
			return e
		}
		if !reflect.DeepEqual(pinned, &pin) {
			t.Fatal("pin changed")
		}
		var count int
		if e := tx.Q().QueryRowContext(ctx, `SELECT COUNT(*) FROM maintenance_physical_stops WHERE run_id=?`, as.RunID).Scan(&count); e != nil {
			return e
		}
		if count != 1 {
			t.Fatal("retries duplicated physical fact", count)
		}
		accepted, e = store.GetRequest(ctx, tx.Q(), runCredentialDomain(as.RunID), env.RequestID)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if accepted.Status != protocol.OpAccepted {
		t.Fatal("give-up result not durably pending", accepted.Status)
	}
	if _, err := a.LinkCommittedMaintenance(ctx, protocol.CommittedMaintenanceReceipt{MaintenanceID: p.MaintenanceID, RequestID: p.RequestID, Previous: p.Previous, Successor: p.Successor, Process: process, Generation: 9, Epoch: process.LaunchID, FirstSequence: 1, SecondSequence: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ChangeArchivePreparation(ctx, protocol.ArchiveControlRequest{RequestID: ids.New(protocol.PrefixRequest), ExpectedControlRevision: prep.Revision}, false); err != nil {
		t.Fatal(err)
	}
	execute(owner, "tasks.cancel", protocol.Target{Type: "task", ID: as.TaskID}, struct{}{}, &task.ControlRevision, app.CancelTask{})
	if err := db.Read(ctx, func(tx *store.ReadTx) error {
		now, e := store.GetRun(ctx, tx.Q(), as.RunID)
		if e != nil {
			return e
		}
		if now.ObservedState != protocol.RunFailed {
			t.Fatal("independent cancel did not settle physical stop", now)
		}
		original, e := store.GetRequest(ctx, tx.Q(), runCredentialDomain(as.RunID), env.RequestID)
		if e == nil && !reflect.DeepEqual(accepted, original) {
			t.Fatal("owner cancel rewrote original accepted request")
		}
		return e
	}); err != nil {
		t.Fatal(err)
	}
	// Cancellation alone does not authorize local settlement; the host Hello must
	// independently confirm the original journal is no longer wanted.
	if err := r.report(ctx, as); !isCompletionPending(err) {
		t.Fatal("unconfirmed cancellation settled strict journal", err)
	}
	// Align this runner process with the already admitted host incarnation.
	r.controlLoaded = true
	r.instanceID = runtime.InstanceID
	r.generation = runtime.Generation
	r.sequence = runtime.Sequence
	r.control.Mode = protocol.RunnerNormal
	if err := r.freshHello(ctx); err != nil {
		t.Fatal("authoritative Stop Hello", err)
	}
	r.wg.Wait()
	saved, err := j.HistoricalRun(ctx, as.RunID)
	if err != nil || saved.Phase != journal.PhaseDone || saved.Thread != "original-native-unknown-thread" {
		t.Fatal("authoritative Stop did not settle preserved strict journal", saved.Phase, err)
	}
	stillStrict, err := j.RetainedResume(ctx, as.RunID)
	if err != nil || stillStrict == nil || *stillStrict != authority {
		t.Fatal("Stop erased strict authority", stillStrict, err)
	}
	if err := db.Read(ctx, func(tx *store.ReadTx) error {
		original, e := store.GetRequest(ctx, tx.Q(), runCredentialDomain(as.RunID), env.RequestID)
		if e == nil && !reflect.DeepEqual(accepted, original) {
			t.Fatal("Stop rewrote accepted history")
		}
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if ws.finishes.Load() != 1 {
		t.Fatal("explicit cancellation did not finish workspace", ws.finishes.Load())
	}
}

func TestRetainedStrictStopWireLostArtifactReceipt(t *testing.T) {
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
		raw, e := json.Marshal(payload)
		if e != nil {
			t.Fatal(e)
		}
		result, e := a.Execute(ctx, p, &protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: op, Target: target, Payload: raw, ExpectedControlRevision: rev}, cmd)
		if e != nil || result.Status != protocol.OpSucceeded {
			t.Fatalf("%s: status=%s err=%v result=%s", op, result.Status, e, result.Result)
		}
		return result
	}
	owner := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "cli"}
	execute(protocol.Principal{Kind: protocol.PrincipalSystem}, "config.sync", protocol.Target{Type: "config"}, app.ConfigPayload{Repos: []app.RepoConfig{{Repo: "tend", RemoteURL: "https://example.com/tend.git"}}, Hosts: []app.HostConfig{{HostID: "worker", OS: "linux", Arch: "amd64", Slots: 1, Worker: protocol.WorkerOrca, Repos: []string{"tend"}, TokenHash: runnerapi.HashToken("host-secret"), Nodes: []string{"tag:tend-runner"}}}}, nil, app.SyncConfig{})
	runtime := protocol.RunnerRuntime{InstanceID: "worker-process", Generation: 1, Sequence: 1, Protocol: protocol.CurrentRunnerProtocol, ProtocolMin: protocol.PreviousRunnerProtocol, ProtocolMax: protocol.CurrentRunnerProtocol, Mode: protocol.RunnerNormal}
	hp := protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "worker"}
	execute(hp, protocol.OpRunnerHello, protocol.Target{Type: "host", ID: "worker"}, protocol.HelloPayload{Worker: protocol.WorkerOrca, WorkspaceProtocol: 1, Runtime: &runtime}, nil, app.Hello{})
	execute(owner, "tasks.create", protocol.Target{Type: "task"}, map[string]any{"title": "retained worker", "spec": domain.Spec{Goal: "retain unknown execution", Acceptance: []string{"do not repeat"}, Deliverables: []string{domain.DeliveryReport}, Workspace: domain.Workspace{Mounts: []domain.Mount{{Repo: "tend"}}}}}, nil, app.CreateTask{})
	runtime.Sequence++
	claim := execute(hp, protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "worker"}, protocol.ClaimPayload{Worker: protocol.WorkerOrca, WorkspaceProtocol: 1, Runtime: &runtime}, nil, app.Claim{})
	var claimed struct {
		Assignment *protocol.Assignment `json:"assignment"`
	}
	if err := json.Unmarshal(claim.Result, &claimed); err != nil || claimed.Assignment == nil {
		t.Fatal("no assignment", err)
	}
	as := *claimed.Assignment
	secret := []byte("recovery-wire-secret")
	as.RunToken = runnerapi.RunToken(secret, as.LeaseID)
	j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if err = j.AcceptAssignmentFromWorker(ctx, as, "", protocol.WorkerOrca); err != nil {
		t.Fatal(err)
	}
	if err = j.SetThread(ctx, as.RunID, "original-native-unknown-thread"); err != nil {
		t.Fatal(err)
	}
	authority := protocol.RetainedRunResume{RequestID: "original-strict-authority", Mode: "attach"}
	if err = j.SetRetainedResume(ctx, as.RunID, authority); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(harness.WorkerResult{Summary: "completed report"})
	if err = j.WorkerDone(ctx, as.RunID, body); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "original completed investigation"}})
	env := protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: protocol.OpRunsSubmitArtifact, Target: protocol.Target{Type: "run", ID: as.RunID}, Payload: payload}
	if err = j.Finish(ctx, as.RunID, journal.PhaseSubmitting, journal.Request{RunID: as.RunID, Envelope: env}); err != nil {
		t.Fatal(err)
	}
	if err = j.ConfirmStopped(ctx, as.RunID); err != nil {
		t.Fatal(err)
	}
	handler := runnerapi.New(a, netx.Static{Identity: &netx.Identity{Node: "worker-node", Tags: []string{"tag:tend-runner"}}}, secret, runnerapi.Config{}, slog.New(slog.DiscardHandler)).Handler()
	var mu sync.Mutex
	var submitted []protocol.Envelope
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		raw, e := io.ReadAll(q.Body)
		if e != nil {
			t.Error(e)
			return
		}
		q.Body = io.NopCloser(bytes.NewReader(raw))
		var got protocol.Envelope
		_ = json.Unmarshal(raw, &got)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, q)
		if got.Operation == protocol.OpRunsSubmitArtifact {
			mu.Lock()
			submitted = append(submitted, got)
			first := len(submitted) == 1
			mu.Unlock()
			if first {
				if rec.Code != http.StatusOK {
					t.Errorf("artifact did not commit before receipt loss: %d %s", rec.Code, rec.Body.String())
				}
				panic(http.ErrAbortHandler)
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
	r := New(Config{Journal: j, Worker: protocol.WorkerOrca, Workspaces: ws, HostID: "worker", HostToken: "host-secret", ServerURL: server.URL, HTTPClient: server.Client(), Now: clock.Now, IDs: ids, RetryDelay: time.Millisecond, RequestTimeout: time.Second, Log: slog.New(slog.DiscardHandler)})
	if err = r.report(ctx, as); !isCompletionPending(err) || protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("lost receipt did not reconcile original request through revoked credential", err)
	}
	mu.Lock()
	if len(submitted) != 2 || !reflect.DeepEqual(submitted[0], env) || !reflect.DeepEqual(submitted[1], env) {
		t.Fatal("artifact replay identity changed", submitted)
	}
	mu.Unlock()
	if ws.finishes.Load() != 0 {
		t.Fatal("Forbidden alone finalized the original run")
	}
	if err = j.AppendEvent(ctx, as.RunID, protocol.RunEvent{NativeID: "original-root", Kind: "turn.observed", Body: json.RawMessage(`{"state":"unknown"}`)}); err != nil {
		t.Fatal(err)
	}
	r.controlLoaded = true
	r.instanceID = runtime.InstanceID
	r.generation = runtime.Generation
	r.sequence = runtime.Sequence
	r.control.Mode = protocol.RunnerNormal
	r.runnerProtocol = protocol.CurrentRunnerProtocol
	// The reporting path must schedule this itself: no manual Hello is sent.
	loopCtx, stopLoop := context.WithTimeout(ctx, 3*time.Second)
	loopDone := make(chan struct{})
	go func() { defer close(loopDone); r.reconciler(loopCtx) }()
	defer func() { stopLoop(); <-loopDone; r.wg.Wait() }()
	for {
		held, e := j.Runs(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if len(held) == 0 {
			break
		}
		select {
		case <-loopCtx.Done():
			t.Fatal("strict report did not schedule terminal reconciliation")
		case <-time.After(time.Millisecond):
		}
	}
	r.wg.Wait()
	saved, err := j.HistoricalRun(ctx, as.RunID)
	if err != nil || saved.Phase != journal.PhaseDone || saved.Thread != "original-native-unknown-thread" || ws.finishes.Load() != 1 {
		t.Fatal("terminal Stop failed to finish lost successful receipt", saved.Phase, ws.finishes.Load(), err)
	}
	if err = db.Read(ctx, func(tx *store.ReadTx) error {
		run, e := store.GetRun(ctx, tx.Q(), as.RunID)
		if e != nil {
			return e
		}
		if run.ObservedState != protocol.RunFinished {
			t.Fatal("successful result changed to cancellation/failure", run)
		}
		record, e := store.GetRequest(ctx, tx.Q(), runCredentialDomain(as.RunID), env.RequestID)
		if e == nil && record.Status != protocol.OpSucceeded {
			t.Fatal("successful original receipt changed", record.Status)
		}
		return e
	}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(submitted) != 2 {
		t.Fatal("Stop replayed business submission", len(submitted))
	}
}

func TestRetainedStrictStopLaterRetainRevokesSettlementOnly(t *testing.T) {
	r, as, _ := retainedStrictFixture(t)
	authority := protocol.RetainedRunResume{RequestID: "original-attach", Mode: "attach"}
	if err := r.cfg.Journal.SetRetainedResume(t.Context(), as.RunID, authority); err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.AuthorizeRetainedStop(t.Context(), as.RunID, "original-hello-stop"); err != nil {
		t.Fatal(err)
	}
	if err := r.retainRun(t.Context(), as.RunID); err != nil {
		t.Fatal(err)
	}
	proof, err := r.cfg.Journal.RetainedStop(t.Context(), as.RunID)
	if err != nil || proof != nil {
		t.Fatal("later Retain left stale settlement authorization", proof, err)
	}
	saved, err := r.cfg.Journal.RetainedResume(t.Context(), as.RunID)
	if err != nil || saved == nil || *saved != authority {
		t.Fatal("later Retain erased strict attachment", saved, err)
	}
	before, requests := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
	retainedStrictRegressionServer(t, r, nil, protocol.OpAccepted)
	if err := r.report(t.Context(), as); !isCompletionPending(err) {
		t.Fatal("Retain did not block direct cleanup", err)
	}
	after, pending := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(requests, pending) {
		t.Fatal("revoked Stop still changed original journal")
	}
}

func TestRetainedStrictStopDuringReportingReconcilesAfterExecutorExit(t *testing.T) {
	r, as, _ := retainedStrictFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	if err := r.cfg.Journal.SetFenced(ctx, as.RunID, false); err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.SetPhase(ctx, as.RunID, journal.PhaseReleasing); err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.ConfirmStopped(ctx, as.RunID); err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.SetRetainedResume(ctx, as.RunID, protocol.RetainedRunResume{RequestID: "original-attach", Mode: "attach"}); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		if env.Operation == protocol.OpRunsRelease {
			once.Do(func() { close(entered) })
			select {
			case <-release:
			case <-ctx.Done():
				return
			}
			_ = json.NewEncoder(w).Encode(rpcResponse{Error: protocol.Errorf(protocol.CodeForbidden, "original run credential ended")})
			return
		}
		if env.Operation != protocol.OpRunnerHello {
			t.Errorf("unexpected RPC %s", env.Operation)
			return
		}
		body, _ := json.Marshal(protocol.HelloResult{LeaseTTLMillis: 60000, Runs: []protocol.RunVerdict{{RunID: as.RunID, Verdict: protocol.VerdictStop, Desired: protocol.DesiredStopped}}})
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: body}})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	r.start(ctx, as, r.cfg.Now())
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("original report not in flight")
	}
	if err := r.freshHello(ctx); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	done := make(chan struct{})
	go func() { defer close(done); r.reconciler(ctx) }()
	defer func() { cancel(); <-done; r.wg.Wait() }()
	for {
		held, err := r.cfg.Journal.Runs(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(held) == 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("Stop lost its executor-exit wakeup")
		case <-time.After(time.Millisecond):
		}
	}
	if r.cfg.Workspaces.(*retainedStrictWorkspaceProbe).finishes.Load() != 1 {
		t.Fatal("original responsibility not cleaned once")
	}
}
