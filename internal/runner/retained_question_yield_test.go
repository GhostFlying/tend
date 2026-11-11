package runner

import (
	"bytes"
	"context"
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
	"github.com/GhostFlying/tend/internal/netx"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/runnerapi"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

type retainedYieldExecutions struct {
	pin      protocol.RunExecutionPin
	resolved ResolvedExecution
}

func (e *retainedYieldExecutions) Refresh(context.Context) error { return nil }
func (e *retainedYieldExecutions) Observe(context.Context) ([]protocol.AgentExecutor, error) {
	return []protocol.AgentExecutor{{Pin: e.pin, Available: true}}, nil
}
func (e *retainedYieldExecutions) Resolve(context.Context, protocol.RunExecutionPin) (ResolvedExecution, error) {
	return e.resolved, nil
}

func TestRetainedStrictQuestionYieldRestartUsesOriginalReceipt(t *testing.T) {
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
	runtime := protocol.RunnerRuntime{InstanceID: "worker-process", Generation: 1, Sequence: 1, Protocol: protocol.CurrentRunnerProtocol, ProtocolMin: protocol.PreviousRunnerProtocol, ProtocolMax: protocol.CurrentRunnerProtocol, Mode: protocol.RunnerNormal, Capabilities: []string{protocol.CapabilityWorkerQuestions, protocol.CapabilityQuestionHandoffs}}
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
	if task.SpecVersion != as.SpecVersion {
		t.Fatal("fixture task and original run spec differ", task.SpecVersion, as.SpecVersion)
	}
	pin := protocol.RunExecutionPin{Identity: protocol.AgentIdentity{ExecutionEnvironmentID: "worker", LocalID: "implementer"}, AgentRevision: 1, Runtime: protocol.AgentRuntimeReference{RuntimeID: "orca", Revision: 1}, InventoryRevision: 1, SnapshotID: "original-pin", PublicSHA256: "sha256:" + strings.Repeat("a", 64), Profile: protocol.ExecutionProfile{Model: "model", Effort: "high"}, Worker: protocol.WorkerOrca, BackendID: "orca"}
	if err := db.Write(ctx, func(tx *store.Tx) error { return tx.InsertRunExecution(ctx, as.RunID, pin) }); err != nil {
		t.Fatal(err)
	}
	as.Execution = &pin
	as.Workspace.BackendID = pin.BackendID
	execute(protocol.Principal{Kind: protocol.PrincipalRun, Subject: as.RunID, Role: as.Role}, protocol.OpRunsReportStarted, runTarget(as), nil, nil, app.ReportRunStarted{})
	before.ObservedState = protocol.RunRunning
	prep, err := a.ChangeArchivePreparation(ctx, protocol.ArchiveControlRequest{RequestID: ids.New(protocol.PrefixRequest)}, true)
	if err != nil {
		t.Fatal(err)
	}
	p := protocol.CommittedMaintenancePlan{MaintenanceID: "wire-recovery", RequestID: ids.New(protocol.PrefixRequest), LinkRequestID: ids.New(protocol.PrefixRequest), PreparationRevision: prep.Revision, Previous: protocol.UpgradeBinding{InstanceID: "server", ProcessInstanceID: "previous", ArtifactSHA256: strings.Repeat("a", 64)}, Successor: protocol.UpgradeBinding{UpgradeID: "wire-recovery", InstanceID: "server", ProcessInstanceID: "successor", ArtifactSHA256: strings.Repeat("b", 64)}, ReceiptRecoveryStop: &protocol.CommittedReceiptRecoveryStop{Runs: []protocol.CommittedRetainedRun{{TaskID: as.TaskID, Run: protocol.RunView{RunID: as.RunID, Role: as.Role, Host: before.HostID, Generation: before.Generation, SpecVersion: action.TargetSpecVersion, Observed: before.ObservedState}}}}}
	process := protocol.RecoveryIdentity{PID: 123, StartTicks: 456, LaunchID: "successor", SHA256: p.Successor.ArtifactSHA256}
	if _, err := a.RegisterCommittedMaintenance(ctx, p, process); err != nil {
		t.Fatal(err)
	}

	proof := protocol.CommittedMaintenanceReceipt{MaintenanceID: p.MaintenanceID, RequestID: p.RequestID, Previous: p.Previous, Successor: p.Successor, Process: process, Generation: 9, Epoch: process.LaunchID, FirstSequence: 1, SecondSequence: 2}
	if _, err := a.LinkCommittedMaintenance(ctx, proof); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ChangeArchivePreparation(ctx, protocol.ArchiveControlRequest{RequestID: ids.New(protocol.PrefixRequest), ExpectedControlRevision: prep.Revision}, false); err != nil {
		t.Fatal(err)
	}
	runtime.Sequence++
	runtime.JournalVersion = journal.CurrentVersion
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityAgentExecution, protocol.CapabilityRetainedReconciliation, protocol.CapabilityStrictRetainedResume)
	runtime.AgentExecutors = []protocol.AgentExecutor{{Pin: pin, Available: true}}
	held := []protocol.HeldRun{{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunInterrupted}}
	execute(hp, protocol.OpRunnerHello, protocol.Target{Type: "host", ID: "worker"}, protocol.HelloPayload{Worker: protocol.WorkerOrca, WorkspaceProtocol: 1, Runtime: &runtime, Held: held}, nil, app.Hello{})
	state, err := a.RetainedRunReconciliation(ctx, as.RunID)
	if err != nil || !state.CanContinue {
		t.Fatal("strict fixture not ready", state.Blockers, err)
	}
	permission := execute(owner, protocol.OpContinueRetainedRun, runTarget(as), protocol.ContinueRetainedRunPayload{RetainedRunIdentity: state.Identity, Protections: state.Protections, ProtectionSetSHA256: state.ProtectionSetSHA256, Reason: "Reconcile only this original worker"}, &state.TaskRevision, app.ContinueRetainedRun{})
	runtime.Sequence++
	activated := execute(hp, protocol.OpRunnerHello, protocol.Target{Type: "host", ID: "worker"}, protocol.HelloPayload{Worker: protocol.WorkerOrca, WorkspaceProtocol: 1, Runtime: &runtime, Held: held}, nil, app.Hello{})
	var verdict protocol.HelloResult
	if err := json.Unmarshal(activated.Result, &verdict); err != nil || len(verdict.Runs) != 1 || verdict.Runs[0].Verdict != protocol.VerdictContinueRetained {
		t.Fatal("strict authority not activated", verdict, err)
	}
	originalAuthority := protocol.RetainedRunResume{RequestID: permission.RequestID, Mode: "attach"}
	questionOp := execute(protocol.Principal{Kind: protocol.PrincipalRun, Subject: as.RunID, Role: as.Role}, protocol.OpRunsAskOwner, runTarget(as), protocol.AskOwnerPayload{QuestionKey: "original-scope", Question: "May the original work continue?"}, nil, app.AskOwner{})
	var question protocol.WorkerQuestion
	if err := json.Unmarshal(questionOp.Result, &question); err != nil {
		t.Fatal(err)
	}
	taskRevision := state.TaskRevision + 1
	execute(owner, protocol.OpAnswerDecisionContinuation, protocol.Target{Type: "decision", ID: question.DecisionID}, protocol.AnswerDecisionPayload{Version: question.Version, Text: "Continue the original handoff", Continuation: &protocol.DecisionContinuationPolicy{Mode: protocol.DecisionContinueNow}}, &taskRevision, app.AnswerDecision{TypedContinuation: true})
	path := filepath.Join(t.TempDir(), "journal.db")
	j, err := journal.Open(ctx, path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.AcceptAssignmentFromWorker(ctx, as, "", protocol.WorkerOrca); err != nil {
		t.Fatal(err)
	}
	if err := j.SetPhase(ctx, as.RunID, journal.PhaseStarted); err != nil {
		t.Fatal(err)
	}
	if err := j.SetThread(ctx, as.RunID, "same-original-native-thread"); err != nil {
		t.Fatal(err)
	}
	if err := j.SetRetainedResume(ctx, as.RunID, originalAuthority); err != nil {
		t.Fatal(err)
	}
	workers := &retainedStrictWorkerProbe{}
	spaces := &retainedStrictWorkspaceProbe{}
	resolver := &retainedYieldExecutions{pin: pin, resolved: ResolvedExecution{Workers: retainedStrictWorkers{workers}, Workspaces: spaces}}
	config := Config{Journal: j, Executions: resolver, Workspaces: spaces, Workers: workers, Worker: protocol.WorkerOrca, HostID: "worker", HostToken: "host-secret", InstanceID: runtime.InstanceID, Now: clock.Now, IDs: ids, RetryDelay: time.Millisecond, RequestTimeout: time.Second, Log: slog.New(slog.DiscardHandler)}
	original := New(config)
	if err := original.loadControl(ctx); err != nil {
		t.Fatal(err)
	}
	if err := original.stopAgent(ctx, as); err != nil {
		t.Fatal(err)
	}
	if err := original.finish(ctx, as, journal.PhaseReleasing, protocol.OpRunsYieldQuestion, protocol.YieldQuestionPayload{DecisionID: question.DecisionID, ThreadID: "same-original-native-thread"}); err != nil {
		t.Fatal(err)
	}
	request, ok, err := j.RequestForRun(ctx, as.RunID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	beforeRestart, err := j.Run(ctx, as.RunID)
	if err != nil || !beforeRestart.StoppedConfirmed || beforeRestart.WorkerDoneAtMillis != 0 || len(beforeRestart.WorkerResult) != 0 {
		t.Fatal("yield invented worker result or lost physical stop", beforeRestart, err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = journal.Open(ctx, path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	handler := runnerapi.New(a, netx.Static{Identity: &netx.Identity{Node: "worker-node", Tags: []string{"tag:tend-runner"}}}, secret, runnerapi.Config{}, slog.New(slog.DiscardHandler)).Handler()
	var mu sync.Mutex
	var submitted []protocol.Envelope
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		body, e := io.ReadAll(q.Body)
		if e != nil {
			t.Error(e)
			return
		}
		q.Body = io.NopCloser(bytes.NewReader(body))
		var env protocol.Envelope
		if e := json.Unmarshal(body, &env); e != nil {
			t.Error(e)
			return
		}
		if env.Operation == protocol.OpRunsYieldQuestion {
			mu.Lock()
			submitted = append(submitted, env)
			mu.Unlock()
		}
		handler.ServeHTTP(w, q)
	}))
	defer server.Close()
	config.Journal, config.InstanceID, config.ServerURL, config.HTTPClient = j, "restarted-worker", server.URL, server.Client()
	restarted := New(config)
	attemptCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Capability negotiation first retains the pin; the second Hello sees its
	// executor observation and returns the original strict authority over real RPC.
	if err := restarted.hello(attemptCtx); err != nil {
		t.Fatal("capability Hello", err)
	}
	if err := restarted.hello(attemptCtx); err != nil {
		t.Fatal("same-authority pending yield was rejected after restart", err)
	}
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		saved, e := j.HistoricalRun(ctx, as.RunID)
		if e == nil && saved.Phase == journal.PhaseDone {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("original yield never settled", e)
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	restarted.wg.Wait()
	mu.Lock()
	sent := append([]protocol.Envelope(nil), submitted...)
	mu.Unlock()
	if len(sent) != 1 || !reflect.DeepEqual(sent[0], request.Envelope) {
		t.Fatal("yield identity or payload changed", sent, request.Envelope)
	}
	if workers.starts.Load() != 0 || workers.releases.Load() != 1 || spaces.prepares.Load() != 0 || spaces.reattaches.Load() != 0 || spaces.collections.Load() != 0 {
		t.Fatal("recovery touched native work", workers.starts.Load(), workers.releases.Load(), spaces.prepares.Load(), spaces.reattaches.Load())
	}
	if err := db.Read(ctx, func(tx *store.ReadTx) error {
		handoff, e := store.GetQuestionHandoff(ctx, tx.Q(), as.RunID)
		if e != nil {
			return e
		}
		lease, e := store.GetLeaseByRun(ctx, tx.Q(), as.RunID)
		if e != nil {
			return e
		}
		var count int
		if e := tx.Q().QueryRowContext(ctx, `SELECT COUNT(*) FROM plan_actions WHERE effect_key=?`, "question_continuation|"+question.DecisionID).Scan(&count); e != nil {
			return e
		}
		if handoff == nil || !handoff.Stopped || handoff.ThreadID != "same-original-native-thread" || lease.Status != store.LeaseReleased || count != 1 {
			t.Fatal("original server handoff did not settle once", handoff, lease, count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
