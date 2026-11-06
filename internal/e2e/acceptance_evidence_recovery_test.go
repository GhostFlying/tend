package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/traex"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner"
	"github.com/GhostFlying/tend/internal/runner/gitws"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

const acceptanceRecoveryHelper = "TEND_TEST_E2E_ACCEPTANCE_RECOVERY"

func TestMain(m *testing.M) {
	if path := os.Getenv(acceptanceRecoveryHelper); path != "" && len(os.Args) == 2 && os.Args[1] == "app-server" {
		if err := serveAcceptanceRecoveryProcess(path); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// This fixture is an actual owned app-server root process. EOF from the real
// Session.Close exits it; it makes no model calls and proves no descendant scope.
func serveAcceptanceRecoveryProcess(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := decoder.Decode(&request); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if request.Method != "" {
			if _, err := f.WriteString(request.Method + "\n"); err != nil {
				return err
			}
		}
		if len(request.ID) == 0 {
			continue
		}
		if request.Method == "" {
			if err := encoder.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "evidence-thread", "turn": map[string]string{"id": "evidence-turn", "status": "completed"}}}); err != nil {
				return err
			}
			continue
		}
		result := map[string]any{}
		switch request.Method {
		case "thread/start":
			result["thread"] = map[string]string{"id": "evidence-thread"}
		case "turn/start":
			result["turn"] = map[string]string{"id": "evidence-turn"}
		}
		if err := encoder.Encode(map[string]any{"id": request.ID, "result": result}); err != nil {
			return err
		}
		if request.Method == "turn/start" {
			arguments := map[string]any{
				"verdict": "blocked", "evidence": map[string]string{"check": "original quality gate"},
				"files": []map[string]string{{"path": os.Getenv("TEND_TEST_E2E_EVIDENCE_PATH"), "name": "check.log"}},
			}
			if err := encoder.Encode(map[string]any{"id": "evidence-call", "method": "item/tool/call", "params": map[string]any{"threadId": "evidence-thread", "turnId": "evidence-turn", "callId": "evidence-call", "tool": harness.VerdictTool, "arguments": arguments}}); err != nil {
				return err
			}
		}
	}
}

type acceptanceRecoveryWorkers struct {
	mu         sync.Mutex
	inner      *traex.Workers
	bin, log   string
	original   []byte
	starts     int
	acceptSpec harness.WorkerSpec
}

func (w *acceptanceRecoveryWorkers) Start(ctx context.Context, spec harness.WorkerSpec) (harness.Worker, error) {
	if spec.Role != protocol.RoleAccept {
		workers := &testkit.Workers{Script: func(harness.WorkerSpec) testkit.WorkerScript {
			return testkit.WorkerScript{Result: harness.WorkerResult{Result: &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Independent report for acceptance"}}}}
		}}
		return workers.Start(ctx, spec)
	}
	path := filepath.Join(spec.ScratchDir, "check.log")
	if err := os.WriteFile(path, w.original, 0o600); err != nil {
		return nil, err
	}
	w.mu.Lock()
	w.starts++
	w.acceptSpec = spec
	w.inner = traex.NewWorkers(traex.WorkerOptions{Session: traex.Options{Bin: w.bin, Env: []string{acceptanceRecoveryHelper + "=" + w.log, "TEND_TEST_E2E_EVIDENCE_PATH=" + path}}})
	inner := w.inner
	w.mu.Unlock()
	return inner.Start(ctx, spec)
}

func (w *acceptanceRecoveryWorkers) ReconcileAcceptanceVerdict(ctx context.Context, spec harness.WorkerSpec) (harness.WorkerResult, error) {
	w.mu.Lock()
	inner := w.inner
	w.mu.Unlock()
	return inner.ReconcileAcceptanceVerdict(ctx, spec)
}

func (w *acceptanceRecoveryWorkers) snapshot() (harness.WorkerSpec, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.acceptSpec, w.starts
}

// Only file-upload and final-verdict replies are controlled. Other actual App
// HTTP traffic, including events and lease renewal, remains available.
type acceptanceRecoveryNetwork struct {
	mu              sync.Mutex
	uploads         []string
	bodies          [][]byte
	allowUploads    bool
	allowVerdict    bool
	verdictAttempts int
}

type acceptanceRecoveryWorkspaceCalls struct {
	mu                   sync.Mutex
	prepares, reattaches int
}

type acceptanceRecoveryLocalWorkspaces struct {
	*gitws.Workspaces
	calls *acceptanceRecoveryWorkspaceCalls
}

func (w acceptanceRecoveryLocalWorkspaces) Prepare(ctx context.Context, as protocol.Assignment) (runner.Prepared, error) {
	w.calls.mu.Lock()
	w.calls.prepares++
	w.calls.mu.Unlock()
	return w.Workspaces.Prepare(ctx, as)
}

func (w acceptanceRecoveryLocalWorkspaces) ReattachAcceptanceVerdict(ctx context.Context, as protocol.Assignment) (runner.Prepared, error) {
	w.calls.mu.Lock()
	w.calls.reattaches++
	w.calls.mu.Unlock()
	return w.Workspaces.ReattachAcceptanceVerdict(ctx, as)
}

func (n *acceptanceRecoveryNetwork) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method == http.MethodPost && request.URL.Path == "/runner/v1/files" {
		body, err := io.ReadAll(request.Body)
		_ = request.Body.Close()
		if err != nil {
			return nil, err
		}
		request.Body = io.NopCloser(bytes.NewReader(body))
		n.mu.Lock()
		n.uploads = append(n.uploads, request.URL.Query().Get("request_id"))
		n.bodies = append(n.bodies, append([]byte(nil), body...))
		first, allowed := len(n.uploads) == 1, n.allowUploads
		n.mu.Unlock()
		if !first && !allowed {
			return nil, errors.New("original upload outcome is not available yet")
		}
		response, err := http.DefaultTransport.RoundTrip(request)
		if err != nil || !first {
			return response, err
		}
		_, err = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("original upload committed but its response was lost")
	}
	if request.Method == http.MethodPost && request.URL.Path == "/runner/v1/rpc" {
		body, err := io.ReadAll(request.Body)
		_ = request.Body.Close()
		if err != nil {
			return nil, err
		}
		request.Body = io.NopCloser(bytes.NewReader(body))
		var envelope protocol.Envelope
		if err := json.Unmarshal(body, &envelope); err != nil {
			return nil, err
		}
		if envelope.Operation == protocol.OpAcceptancesSubmit {
			n.mu.Lock()
			n.verdictAttempts++
			allowed := n.allowVerdict
			n.mu.Unlock()
			if !allowed {
				return nil, errors.New("hold original final verdict for journal observation")
			}
		}
	}
	return http.DefaultTransport.RoundTrip(request)
}

func TestAcceptanceEvidenceRecoversThroughLocalGitwsAndOwnedClose(t *testing.T) {
	srv := newServer(t, time.Minute)
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	original := []byte("original failed full check\n\x00\xff\n")
	workers := &acceptanceRecoveryWorkers{bin: bin, log: filepath.Join(t.TempDir(), "native-methods"), original: original}
	network := &acceptanceRecoveryNetwork{}
	workspaceCalls := &acceptanceRecoveryWorkspaceCalls{}
	home, journalPath := t.TempDir(), filepath.Join(t.TempDir(), "journal.db")
	var cancel context.CancelFunc
	var done chan error
	var activeJournal *journal.Journal
	stop := func() {
		t.Helper()
		if cancel == nil {
			return
		}
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("runner stopped: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("runner component did not stop")
		}
		cancel = nil
	}
	t.Cleanup(stop)
	start := func() {
		t.Helper()
		j, err := journal.Open(t.Context(), journalPath, srv.now)
		if err != nil {
			t.Fatal(err)
		}
		activeJournal = j
		ctx, stopRunner := context.WithCancel(context.Background())
		cancel, done = stopRunner, make(chan error, 1)
		workspaces := acceptanceRecoveryLocalWorkspaces{Workspaces: gitws.New(gitws.Config{Home: home, Log: quiet(), Now: srv.now}), calls: workspaceCalls}
		r := runner.New(runner.Config{ServerURL: srv.url, HostID: "linux-1", HostToken: hostToken, Version: "test", HTTPClient: &http.Client{Transport: network}, Journal: j, Worker: protocol.WorkerAppServer, Workers: workers, Workspaces: workspaces, Now: srv.now, IDs: protocol.NewULIDGen(srv.now, nil), Log: quiet(), RetryDelay: 20 * time.Millisecond, EventFlush: 20 * time.Millisecond})
		finished := done
		go func() {
			err := r.Run(ctx)
			err = errors.Join(err, j.Close())
			finished <- err
		}()
	}
	start()
	eventually(t, "runner evidence capability registered", func() bool {
		return srv.count(`SELECT COUNT(*) FROM host_runtime WHERE runtime_json LIKE '%acceptance-evidence-files-v1%'`) == 1
	})
	op := srv.exec(owner, "tasks.create", protocol.Target{Type: "task"}, map[string]any{"title": "recover original acceptance files", "spec": domain.Spec{Goal: "verify original evidence", Acceptance: []string{"independent full check"}, Deliverables: []string{domain.DeliveryReport}, Workspace: domain.Workspace{Mode: domain.WorkspaceTemporary}}}, app.CreateTask{})
	var created struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(op.Result, &created); err != nil || created.TaskID == "" {
		t.Fatal("create original task", err)
	}
	var originalRun journal.Run
	var originalSpec harness.WorkerSpec
	var originalClose []byte
	eventually(t, "original owned Close and unknown upload persisted", func() bool {
		spec, starts := workers.snapshot()
		if starts != 1 || spec.StateDir == "" {
			return false
		}
		body, err := os.ReadFile(filepath.Join(spec.StateDir, "owned-session-close.json"))
		if err != nil {
			return false
		}
		run, err := activeJournal.Run(t.Context(), spec.RunID)
		if err != nil || run.Phase != journal.PhaseStarted || run.StoppedConfirmed || run.Attempts != 1 || run.Thread != "evidence-thread" {
			return false
		}
		originalRun, originalSpec, originalClose = run, spec, body
		return true
	})
	stop()
	workspaceCalls.mu.Lock()
	preparesBefore := workspaceCalls.prepares
	workspaceCalls.mu.Unlock()
	nativeBefore, err := os.ReadFile(workers.log)
	if err != nil || bytes.Count(nativeBefore, []byte("thread/start\n")) != 1 || bytes.Count(nativeBefore, []byte("turn/start\n")) != 1 {
		t.Fatalf("original owned process execution: %q, %v", nativeBefore, err)
	}
	if srv.count(`SELECT COUNT(*) FROM acceptances WHERE task_id=?`, created.TaskID) != 0 {
		t.Fatal("unknown file upload prematurely finalized the verdict")
	}
	// The selected source is ephemeral. Recovery must use the sealed original
	// bytes and may not prepare or recreate this local workspace.
	if err := os.Remove(filepath.Join(originalSpec.ScratchDir, "check.log")); err != nil {
		t.Fatal(err)
	}
	stateBefore, err := os.ReadFile(filepath.Join(originalSpec.Dir, ".tend", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	workers.mu.Lock()
	workers.bin = filepath.Join(home, "forbidden-new-app-server")
	workers.inner = traex.NewWorkers(traex.WorkerOptions{Session: traex.Options{Bin: workers.bin}})
	workers.mu.Unlock()
	network.mu.Lock()
	network.allowUploads = true
	network.mu.Unlock()
	start()
	eventually(t, "original verdict reached submission after result-only recovery", func() bool {
		network.mu.Lock()
		defer network.mu.Unlock()
		return network.verdictAttempts > 0
	})
	run, err := activeJournal.Run(t.Context(), originalSpec.RunID)
	if err != nil || run.Attempts != 1 || !run.StoppedConfirmed || len(run.WorkerResult) == 0 || !reflect.DeepEqual(run.Assignment, originalRun.Assignment) {
		t.Fatalf("recovery changed the original attempt or assignment: %+v, %v", run, err)
	}
	var recovered harness.WorkerResult
	if err := json.Unmarshal(run.WorkerResult, &recovered); err != nil || recovered.Verdict == nil || recovered.Verdict.Verdict != protocol.VerdictBlocked || len(recovered.Verdict.Files) != 1 {
		t.Fatalf("missing original recovered typed verdict: %+v, %v", recovered, err)
	}
	_, starts := workers.snapshot()
	nativeAfter, err := os.ReadFile(workers.log)
	if err != nil || starts != 1 || !bytes.Equal(nativeAfter, nativeBefore) {
		t.Fatalf("recovery launched a new worker/thread/turn: starts=%d, %v", starts, err)
	}
	workspaceCalls.mu.Lock()
	preparesAfter, reattaches := workspaceCalls.prepares, workspaceCalls.reattaches
	workspaceCalls.mu.Unlock()
	if preparesAfter != preparesBefore || reattaches == 0 {
		t.Fatalf("recovery prepared a workspace or skipped actual read-only reattachment: prepares=%d -> %d, reattaches=%d", preparesBefore, preparesAfter, reattaches)
	}
	closeAfter, err := os.ReadFile(filepath.Join(originalSpec.StateDir, "owned-session-close.json"))
	if err != nil || !bytes.Equal(closeAfter, originalClose) {
		t.Fatal("recovery rewrote original owned Close proof", err)
	}
	stateAfter, err := os.ReadFile(filepath.Join(originalSpec.Dir, ".tend", "state.json"))
	if err != nil || !bytes.Equal(stateAfter, stateBefore) {
		t.Fatal("read-only local reattachment changed workspace identity", err)
	}
	if _, err := os.Stat(filepath.Join(originalSpec.ScratchDir, "check.log")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("recovery recreated the missing selected source", err)
	}
	network.mu.Lock()
	if len(network.uploads) < 2 {
		t.Error("original upload was not reconciled")
	}
	for i, id := range network.uploads {
		if id == "" || id != network.uploads[0] || !bytes.Equal(network.bodies[i], original) {
			t.Errorf("upload %d changed the original request identity or bytes", i)
		}
	}
	network.allowVerdict = true
	network.mu.Unlock()
	eventually(t, "original blocked verdict ACK and real scratch cleanup", func() bool {
		if srv.count(`SELECT COUNT(*) FROM acceptances WHERE task_id=? AND verdict='blocked'`, created.TaskID) != 1 {
			return false
		}
		runs, err := activeJournal.Runs(t.Context())
		return err == nil && len(runs) == 0
	})
	if _, err := os.Stat(originalSpec.ScratchDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("real Gitws.Finish did not remove scratch", err)
	}
	file := recovered.Verdict.Files[0]
	row, body, err := srv.app.OpenDeliverableFile(t.Context(), owner, file.FileID)
	if err != nil {
		t.Fatal("owner lost original file after cleanup", err)
	}
	got, readErr := io.ReadAll(body)
	_ = body.Close()
	if readErr != nil || row.ArtifactFile != file || !bytes.Equal(got, original) {
		t.Fatal("durable evidence changed after original ACK and cleanup", readErr)
	}
	stop()
}
