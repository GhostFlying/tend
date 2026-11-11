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
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/netx"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/runnerapi"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

type recoveryFinishProbe struct {
	completionWorkspaces
	finishes atomic.Int64
}

func (w *recoveryFinishProbe) Finish(context.Context, protocol.Assignment) error {
	w.finishes.Add(1)
	return nil
}

// This uses the production RPC handler and unchanged legacy client/report loop.
// A durable accepted fact must never become a successful wire finalization on
// retry; only a new explicit owner cancellation may settle the business run.
func TestReceiptRecoveryWorkerGiveUpWireRetriesUntilOwnerCancel(t *testing.T) {
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
	execute(protocol.Principal{Kind: protocol.PrincipalSystem}, "config.sync", protocol.Target{Type: "config"}, app.ConfigPayload{Repos: []app.RepoConfig{{Repo: "tend", RemoteURL: "https://example.com/tend.git"}}, Hosts: []app.HostConfig{{HostID: "worker", OS: "linux", Arch: "amd64", Slots: 1, Repos: []string{"tend"}, TokenHash: runnerapi.HashToken("host-secret"), Nodes: []string{"tag:tend-runner"}}}}, nil, app.SyncConfig{})
	execute(owner, "tasks.create", protocol.Target{Type: "task"}, map[string]any{"title": "retained worker", "spec": domain.Spec{Goal: "retain unknown execution", Acceptance: []string{"do not repeat"}, Delivery: domain.DeliveryCommit, Workspace: domain.Workspace{Mounts: []domain.Mount{{Repo: "tend"}}}}}, nil, app.CreateTask{})
	runtime := protocol.RunnerRuntime{InstanceID: "worker-process", Generation: 1, Sequence: 1, Protocol: protocol.CurrentRunnerProtocol, ProtocolMin: protocol.PreviousRunnerProtocol, ProtocolMax: protocol.CurrentRunnerProtocol, Mode: protocol.RunnerNormal}
	hp := protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "worker"}
	execute(hp, protocol.OpRunnerHello, protocol.Target{Type: "host", ID: "worker"}, protocol.HelloPayload{WorkspaceProtocol: 1, Runtime: &runtime}, nil, app.Hello{})
	runtime.Sequence++
	claim := execute(hp, protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "worker"}, protocol.ClaimPayload{RunID: ids.New(protocol.PrefixRun), WorkspaceProtocol: 1, Runtime: &runtime}, nil, app.Claim{})
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
	raw, _ := json.Marshal(protocol.HelloPayload{WorkspaceProtocol: 1, Runtime: &runtime, Held: []protocol.HeldRun{{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunRunning}}})
	if _, err := a.Execute(ctx, hp, &protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: protocol.OpRunnerHello, Target: protocol.Target{Type: "host", ID: "worker"}, Payload: raw}, app.Hello{}); protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
		t.Fatal("protected Hello continued", err)
	}
	j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if err := j.AcceptAssignment(ctx, as, ""); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(protocol.ReleasePayload{Reason: "worker_failed: native transport disconnected"})
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
	r := New(Config{Journal: j, Workspaces: ws, ServerURL: server.URL, HTTPClient: server.Client(), Now: clock.Now, IDs: ids, RetryDelay: time.Millisecond, RequestTimeout: time.Second, Log: slog.New(slog.DiscardHandler)})
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
	var accepted maintenancePhysicalStopSnapshot
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
		accepted, e = readMaintenancePhysicalStop(ctx, tx.Q(), as.RunID, env.RequestID)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if accepted.Operation != env.Operation || accepted.Payload != string(env.Payload) {
		t.Fatal("give-up result not durably pending", accepted.Operation)
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
		original, e := readMaintenancePhysicalStop(ctx, tx.Q(), as.RunID, env.RequestID)
		if e == nil && !reflect.DeepEqual(accepted, original) {
			t.Fatal("owner cancel rewrote original accepted request")
		}
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.report(ctx, as); err != nil {
		t.Fatal("explicit cancelled original did not release journal", err)
	}
	if ws.finishes.Load() != 1 {
		t.Fatal("explicit cancellation did not finish workspace", ws.finishes.Load())
	}
}

// Snapshot the original physical-stop business fact, not an RPC result ledger.
type maintenancePhysicalStopSnapshot struct {
	RequestID, MaintenanceID, RunID, LeaseID, Operation, Payload string
	LastSequence, CreatedAt                                      int64
}

func readMaintenancePhysicalStop(ctx context.Context, q store.Querier, run, request string) (maintenancePhysicalStopSnapshot, error) {
	var row maintenancePhysicalStopSnapshot
	err := q.QueryRowContext(ctx, `SELECT request_id,maintenance_id,run_id,lease_id,operation,payload_json,last_sequence,created_at FROM maintenance_physical_stops WHERE run_id=? AND request_id=?`, run, request).Scan(&row.RequestID, &row.MaintenanceID, &row.RunID, &row.LeaseID, &row.Operation, &row.Payload, &row.LastSequence, &row.CreatedAt)
	return row, err
}
