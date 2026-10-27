//go:build integration

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/contextbuilder"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/orca"
	"github.com/GhostFlying/tend/internal/harness/traex"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner"
	"github.com/GhostFlying/tend/internal/runner/gitws"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/runnerapi"
	"github.com/GhostFlying/tend/internal/tools"
)

type messageTestWorkspaces struct {
	*gitws.Workspaces
	mu        sync.Mutex
	prepared  map[string]runner.Prepared
	resources map[string]protocol.WorkspaceCleanup
}

func (w *messageTestWorkspaces) Prepare(ctx context.Context, as protocol.Assignment) (runner.Prepared, error) {
	p, err := w.Workspaces.Prepare(ctx, as)
	if err != nil {
		return p, err
	}
	if as.Role == protocol.RoleImplement {
		gate := `import json, os, time
from pathlib import Path
root = Path(__file__).parent
pid = os.getpid()
stat = Path(f'/proc/{pid}/stat')
(root / 'executing.json').write_text(json.dumps({'pid': pid, 'start_ticks': stat.read_text().split()[21] if stat.exists() else None}))
deadline = time.monotonic() + 240
while not (root / 'release').exists():
    if time.monotonic() > deadline:
        raise RuntimeError('isolated gate timed out')
    time.sleep(0.1)
print('isolated execution gate completed')
`
		if err := os.WriteFile(filepath.Join(p.ScratchDir, "gate.py"), []byte(gate), 0o600); err != nil {
			return p, err
		}
	}
	w.mu.Lock()
	w.prepared[as.RunID] = p
	if _, exists := w.resources[as.Workspace.ID]; !exists {
		w.resources[as.Workspace.ID] = protocol.WorkspaceCleanup{WorkspaceID: as.Workspace.ID, Kind: "directory", Path: p.Dir, ProviderID: p.Report.ProviderID, ProviderInstanceID: p.Report.ProviderInstanceID}
	}
	w.mu.Unlock()
	return p, nil
}

type messageTestWorkers struct {
	harness.Workers
	t       *testing.T
	mu      sync.Mutex
	starts  map[string]int
	threads map[string][]string
	results map[string]harness.WorkerResult
}

func (w *messageTestWorkers) Start(ctx context.Context, spec harness.WorkerSpec) (harness.Worker, error) {
	w.mu.Lock()
	w.starts[spec.RunID]++
	w.mu.Unlock()
	saved := spec.OnThread
	spec.OnThread = func(thread string) error {
		w.mu.Lock()
		w.threads[spec.RunID] = append(w.threads[spec.RunID], thread)
		w.mu.Unlock()
		return saved(thread)
	}
	inner, err := w.Workers.Start(ctx, spec)
	if err != nil {
		w.t.Logf("isolated worker start failure: %s", smokeStartError(err))
		return nil, err
	}
	return &messageTestWorker{Worker: inner, parent: w, run: spec.RunID}, nil
}

type messageTestWorker struct {
	harness.Worker
	parent *messageTestWorkers
	run    string
}

// Preserve Orca's release/ownership guards through observation. App-server
// observation deliberately has no external Releaser so its close proof remains
// the real per-run session shutdown.
type messageTestOrcaWorkers struct {
	*orca.Workers
	observed *messageTestWorkers
}

func (w *messageTestOrcaWorkers) Start(ctx context.Context, spec harness.WorkerSpec) (harness.Worker, error) {
	return w.observed.Start(ctx, spec)
}

func (w *messageTestWorker) Wait() (harness.WorkerResult, error) {
	result, err := w.Worker.Wait()
	w.parent.mu.Lock()
	w.parent.results[w.run] = result
	w.parent.mu.Unlock()
	return result, err
}
func (w *messageTestWorker) MessageTarget() (string, string) {
	return w.Worker.(harness.LiveMessenger).MessageTarget()
}
func (w *messageTestWorker) SendMessage(ctx context.Context, text string) (protocol.RunMessageStatus, string) {
	return w.Worker.(harness.LiveMessenger).SendMessage(ctx, text)
}

// TestIntegration_RunMessages uses a fresh ledger, runner journal, workspace and
// real original agent. It never sends input to an existing production worker.
// Orca receives via its run-private MCP bridge; appserver uses turn/steer.
func TestIntegration_RunMessages(t *testing.T) {
	if os.Getenv("TEND_RUN_MESSAGES_REAL") != "1" {
		t.Skip("set TEND_RUN_MESSAGES_REAL=1 for isolated real executors")
	}
	for _, kind := range []protocol.WorkerKind{protocol.WorkerAppServer, protocol.WorkerOrca} {
		t.Run(string(kind), func(t *testing.T) { realRunMessage(t, kind) })
	}
}

func realRunMessage(t *testing.T, kind protocol.WorkerKind) {
	root, err := os.MkdirTemp("", "tend-message-it-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("isolated evidence root: %s", root)
	srv := openServer(t, time.Minute, filepath.Join(root, "server.db"))
	srv.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, "config.sync", protocol.Target{Type: "config"}, app.ConfigPayload{Hosts: []app.HostConfig{{HostID: "linux-1", OS: "linux", Arch: "amd64", Slots: 1, TokenHash: runnerapi.HashToken(hostToken), Nodes: []string{"tag:tend-runner"}, Worker: kind}}}, app.SyncConfig{})
	var native harness.Workers
	var spaces *gitws.Workspaces
	var oc *orca.Client
	if kind == protocol.WorkerOrca {
		oc = &orca.Client{Bin: os.Getenv("TEND_ORCA_BIN")}
		if err := oc.CheckWorkspaceProtocol(t.Context()); err != nil {
			t.Fatalf("isolated Orca connection: %s", smokeStartError(err))
		}
		bin := filepath.Join(root, "tend-runner")
		if out, err := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "github.com/GhostFlying/tend/cmd/tend-runner").CombinedOutput(); err != nil {
			t.Fatalf("build worker bridge: %v %s", err, out)
		}
		sockets := filepath.Join(root, "s")
		if err := os.MkdirAll(sockets, 0o700); err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS == "linux" {
			// Keep socket files inside the task scratch while presenting a short
			// pathname within Unix sun_path limits to the bridge subprocesses.
			directory, err := os.Open(sockets)
			if err != nil {
				t.Fatal(err)
			}
			defer directory.Close()
			sockets = fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), directory.Fd())
		}
		model, explicit := os.LookupEnv("TEND_ORCA_MODEL")
		if !explicit {
			model = traexModel()
		}
		native = orca.NewWorkers(orca.WorkerOptions{Client: oc, Agent: protocol.AgentTraex, Model: model, Effort: os.Getenv("TEND_ORCA_EFFORT"), BridgeCommand: bin, BridgeArgs: []string{"worker-mcp"}, SocketDir: sockets, IDs: protocol.NewULIDGen(time.Now, nil), Now: time.Now, Poll: 100 * time.Millisecond, WaitChunk: time.Second})
		spaces = gitws.NewOrca(gitws.Config{Home: filepath.Join(root, "runner"), ServerURL: srv.url}, oc)
	} else {
		native = traex.NewWorkers(traex.WorkerOptions{Session: traex.Options{Bin: os.Getenv("TEND_TRAEX_BIN"), Version: "message-integration"}, Model: traexModel()})
		spaces = gitws.New(gitws.Config{Home: filepath.Join(root, "runner"), ServerURL: srv.url})
	}
	ws := &messageTestWorkspaces{Workspaces: spaces, prepared: map[string]runner.Prepared{}, resources: map[string]protocol.WorkspaceCleanup{}}
	workers := &messageTestWorkers{Workers: native, t: t, starts: map[string]int{}, threads: map[string][]string{}, results: map[string]harness.WorkerResult{}}
	var driven harness.Workers = workers
	if external, ok := native.(*orca.Workers); ok {
		driven = &messageTestOrcaWorkers{Workers: external, observed: workers}
	}
	j, err := journal.Open(t.Context(), filepath.Join(root, "journal.db"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := runner.New(runner.Config{ServerURL: srv.url, HostID: "linux-1", HostToken: hostToken, Journal: j, Worker: kind, Workers: driven, Workspaces: ws, Now: time.Now, IDs: protocol.NewULIDGen(time.Now, nil), Log: quiet(), HeartbeatInterval: time.Second, RetryDelay: 100 * time.Millisecond, EventFlush: 100 * time.Millisecond})
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Error("isolated runner did not stop")
			return
		}
		cleanup, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		clean := true
		if releaser, ok := native.(harness.Releaser); ok {
			workers.mu.Lock()
			var ids []string
			for id := range workers.starts {
				ids = append(ids, id)
			}
			workers.mu.Unlock()
			for _, id := range ids {
				run, err := j.HistoricalRun(cleanup, id)
				if err != nil {
					clean = false
					t.Error(err)
					continue
				}
				if run.Thread != "" {
					if err := releaser.Release(cleanup, run.Assignment.RunID, run.Thread); err != nil {
						clean = false
						t.Errorf("isolated terminal cleanup: %s", smokeStartError(err))
					}
				}
			}
		}
		if clean {
			ws.mu.Lock()
			resources := make([]protocol.WorkspaceCleanup, 0, len(ws.resources))
			for _, resource := range ws.resources {
				resources = append(resources, resource)
			}
			ws.mu.Unlock()
			for _, resource := range resources {
				if err := spaces.Cleanup(cleanup, resource); err != nil {
					clean = false
					t.Errorf("isolated workspace cleanup failed: %s", smokeStartError(err))
				}
			}
		}
		if clean {
			t.Log("isolated terminals and workspace provider resources removed; evidence retained")
		}
		j.Close()
		srv.shutdown()
		// Keep the isolated evidence, so a failed run never deletes an uncertain
		// provider resource. The local report states its path and cleanup result.
	}()
	smokeWait(t, 30*time.Second, "message capability", func() bool { return srv.count(`SELECT COUNT(*) FROM host_runtime WHERE host_id='linux-1'`) > 0 })
	goal := "This is an isolated supplemental-message check. Run python3 on gate.py in your task scratch directory in the foreground, allowing up to 240 seconds. The test operator will release its file gate; do not create or edit release yourself. While the command executes, continue waiting for it without interrupting it. After the gate completes, call receive_messages if available, acknowledge any received message_id with ack_message, and include any supplemental codeword in your report and final response. Do not search other directories or contact anyone. Submit exactly one report with submit_result; no Git changes."
	spec := domain.Spec{Goal: goal, Acceptance: []string{"The report records the received supplemental codeword and original execution completed"}, Deliverables: []string{domain.DeliveryReport}, Workspace: domain.Workspace{Mode: "temporary"}}
	created := srv.exec(owner, "tasks.create", protocol.Target{Type: "task"}, map[string]any{"title": "isolated run message check", "spec": spec}, app.CreateTask{})
	var task struct {
		TaskID string `json:"task_id"`
	}
	if json.Unmarshal(created.Result, &task) != nil {
		t.Fatal("task result")
	}
	var as protocol.Assignment
	var prepared runner.Prepared
	smokeWait(t, 5*time.Minute, "actual command executing", func() bool {
		if state := srv.task(task.TaskID); state.BlockReason == protocol.BlockWaitingDecision || state.Lifecycle == protocol.TaskCancelled {
			t.Fatalf("isolated execution cannot start: lifecycle=%s block=%s", state.Lifecycle, state.BlockReason)
		}
		runs, err := j.Runs(t.Context())
		if err != nil {
			return false
		}
		for _, run := range runs {
			if run.Assignment.Role == protocol.RoleImplement {
				ws.mu.Lock()
				p, ok := ws.prepared[run.Assignment.RunID]
				ws.mu.Unlock()
				if ok {
					if _, err := os.Stat(filepath.Join(p.ScratchDir, "executing.json")); err == nil {
						as = run.Assignment
						prepared = p
						return true
					}
				}
			}
		}
		return false
	})
	before, err := j.Run(t.Context(), as.RunID)
	if err != nil {
		t.Fatal(err)
	}
	executionProof, err := os.ReadFile(filepath.Join(prepared.ScratchDir, "executing.json"))
	if err != nil {
		t.Fatal(err)
	}
	var execution struct {
		PID        int    `json:"pid"`
		StartTicks string `json:"start_ticks"`
	}
	if json.Unmarshal(executionProof, &execution) != nil || execution.PID == 0 {
		t.Fatal("execution identity missing")
	}
	if runtime.GOOS == "linux" {
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", execution.PID))
		if err != nil {
			t.Fatal("original command is not executing at send time")
		}
		if fields := strings.Fields(string(stat)); len(fields) < 22 || fields[21] != execution.StartTicks {
			t.Fatal("original command incarnation changed")
		}
	}
	var agentsBefore []orca.Agent
	if oc != nil {
		agentsBefore, err = oc.Agents(t.Context(), prepared.ProviderID)
		if err != nil {
			t.Fatal(smokeStartError(err))
		}
	}
	ownerInput := srv.exec(owner, "conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{Body: "Provide supplemental context within the approved worker contract."}, app.SubmitMessage{})
	var source struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(ownerInput.Result, &source); err != nil || source.MessageID == "" {
		t.Fatal(ownerInput, err)
	}
	marker := "SUPPLEMENT_" + srv.ids.New(protocol.PrefixRequest)
	service := tools.New(srv.app, srv.store, contextbuilder.New(srv.app, srv.store, srv.ids, contextbuilder.Config{}), quiet())
	args, _ := json.Marshal(map[string]any{"run_id": as.RunID, "task_id": as.TaskID, "spec_version": as.SpecVersion, "generation": as.Generation, "expected_control_revision": srv.task(as.TaskID).ControlRevision, "commit_seq": 1, "text": "The supplemental codeword is " + marker + ". Include this exact codeword in your completed report and final response."})
	principal := protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "isolated-control", Thread: "isolated-control-session"}
	call := tools.Call{Principal: principal, SourceMessageID: source.MessageID, RequestID: srv.ids.New(protocol.PrefixRequest), Tool: "runs_send_message", Arguments: args}
	res := service.Handle(t.Context(), call)
	if !res.Success {
		t.Fatal(res.Text)
	}
	var result struct {
		Result struct {
			Outcome protocol.RunMessage `json:"outcome"`
		} `json:"result"`
	}
	if json.Unmarshal([]byte(res.Text), &result) != nil || result.Result.Outcome.MessageID == "" {
		t.Fatal(res.Text)
	}
	message := result.Result.Outcome
	if again := service.Handle(t.Context(), call); again.Text != res.Text {
		t.Fatal("public request retry changed message identity")
	}
	if kind == protocol.WorkerAppServer {
		smokeWait(t, 30*time.Second, "native same-turn acceptance", func() bool {
			page, _ := srv.app.RunMessages(t.Context(), principal, as.RunID, 0, 20, false)
			if len(page.Messages) > 0 {
				message = page.Messages[0]
			}
			return message.Status == protocol.RunMessageAccepted
		})
	}
	if err := os.WriteFile(filepath.Join(prepared.ScratchDir, "release"), []byte("isolated operator release\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	smokeWait(t, 5*time.Minute, "same worker response contains supplement", func() bool {
		workers.mu.Lock()
		defer workers.mu.Unlock()
		return strings.Contains(workers.results[as.RunID].Summary, marker)
	})
	page, err := srv.app.RunMessages(t.Context(), principal, as.RunID, 0, 20, false)
	if err != nil || len(page.Messages) != 1 {
		t.Fatal(page, err)
	}
	message = page.Messages[0]
	if message.Status != protocol.RunMessageAccepted {
		t.Fatalf("delivery lacks receiver evidence: %+v", message)
	}
	workers.mu.Lock()
	starts := workers.starts[as.RunID]
	threads := append([]string(nil), workers.threads[as.RunID]...)
	reply := workers.results[as.RunID].Summary
	workers.mu.Unlock()
	if starts != 1 || before.Attempts != 1 || srv.count(`SELECT COUNT(*) FROM runs WHERE task_id=? AND role='implement'`, task.TaskID) != 1 {
		t.Fatal("supplement restarted original execution")
	}
	if kind == protocol.WorkerAppServer && (len(threads) != 1 || message.ThreadID != before.Thread || message.TurnID == "") {
		t.Fatal("native thread or turn changed")
	}
	if oc != nil {
		// Hooks may update fields, but terminal and provider session must stay
		// the original ones through execution and the supplemental response.
		var original struct {
			Terminal  string `json:"terminal"`
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal([]byte(before.Thread), &original)
		for _, thread := range threads {
			var next struct {
				Terminal  string `json:"terminal"`
				SessionID string `json:"session_id"`
			}
			_ = json.Unmarshal([]byte(thread), &next)
			if (next.Terminal != "" && next.Terminal != original.Terminal) || (next.SessionID != "" && original.SessionID != "" && next.SessionID != original.SessionID) {
				t.Fatal("Orca terminal/session changed")
			}
		}
		if len(agentsBefore) != 1 || agentsBefore[0].ProviderSession == nil {
			t.Fatal("original native session evidence missing")
		}
	}
	evidence, _ := json.MarshalIndent(map[string]any{"command_at_send": json.RawMessage(executionProof), "executor": kind, "task_id": as.TaskID, "run_id": as.RunID, "spec_version": as.SpecVersion, "generation": as.Generation, "worker_starts": starts, "original_thread": before.Thread, "thread_history": threads, "native_agents_before": agentsBefore, "message": message, "same_worker_response": reply, "codeword": marker}, "", "  ")
	if err := os.WriteFile(filepath.Join(root, "evidence.json"), evidence, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("continuity evidence: message=%s status=%s starts=%d file=%s", message.MessageID, message.Status, starts, filepath.Join(root, "evidence.json"))
	if !strings.Contains(string(evidence), marker) {
		t.Fatal(fmt.Errorf("missing codeword evidence"))
	}
	smokeWait(t, 5*time.Minute, "isolated independent acceptance", func() bool { return srv.task(as.TaskID).Lifecycle == protocol.TaskCompleted })
	smokeWait(t, 30*time.Second, "original run releases acknowledged", func() bool { runs, err := j.Runs(t.Context()); return err == nil && len(runs) == 0 })
}
