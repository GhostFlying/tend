//go:build integration

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/bridge"
	"github.com/GhostFlying/tend/internal/harness/orca"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner"
	"github.com/GhostFlying/tend/internal/runner/gitws"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/runnerapi"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

// TestIntegration_OrcaTemporaryRunner exercises an isolated server and runner,
// including immutable file upload and independent acceptance download. It uses
// Orca's configured Traex launcher (which may wrap Traex in a gateway), without
// changing that configuration. Set TEND_ORCA_TEMPORARY_RUNNER=1 explicitly;
// TEND_ORCA_BIN and TEND_ORCA_EFFORT select the provider/effort. The model
// defaults to TEND_TRAEX_MODEL (GPT-5.6-Sol); TEND_ORCA_MODEL overrides it.
// An explicitly empty TEND_ORCA_MODEL uses the host launcher configuration.
// The test covers only the host running it, not every configured Orca host.
// No production ledger, repository, or runner configuration is used.
func TestIntegration_OrcaTemporaryRunner(t *testing.T) {
	if os.Getenv("TEND_ORCA_TEMPORARY_RUNNER") != "1" {
		t.Skip("explicit real-worker opt-in required")
	}
	temporaryRunnerSmoke(t, true)
}

// This fixture check performs the same real HTTP, journal, upload, download and
// cleanup path without launching a model or contacting Orca.
func TestIntegrationFixture_TemporaryRunner(t *testing.T) { temporaryRunnerSmoke(t, false) }

type smokeWorkspaces struct {
	*gitws.Workspaces
	t         *testing.T
	mu        sync.Mutex
	resources map[string]protocol.WorkspaceCleanup
}

func (w *smokeWorkspaces) Prepare(ctx context.Context, as protocol.Assignment) (runner.Prepared, error) {
	p, err := w.Workspaces.Prepare(ctx, as)
	if err != nil {
		w.t.Logf("isolated workspace preparation failed for run %s: %v", as.RunID, err)
	}
	if err == nil {
		w.mu.Lock()
		// Acceptance reopens the same resource and may omit its provider report.
		if _, exists := w.resources[as.Workspace.ID]; !exists {
			w.resources[as.Workspace.ID] = protocol.WorkspaceCleanup{WorkspaceID: as.Workspace.ID, Kind: "directory", Path: p.Dir, ProviderID: p.Report.ProviderID, ProviderInstanceID: p.Report.ProviderInstanceID}
		}
		w.mu.Unlock()
	}
	return p, err
}

// Embedding preserves Orca's historical/finalizing release interfaces.
type smokeOrcaWorkers struct {
	*orca.Workers
	t         *testing.T
	mu        sync.Mutex
	nonce     string
	downloads int
	runs      map[string]smokeRunEvidence
}

type smokeRunEvidence struct {
	Role      protocol.RunRole `json:"role"`
	Terminal  string           `json:"terminal,omitempty"`
	Pane      string           `json:"pane,omitempty"`
	SessionID string           `json:"session_id,omitempty"`
}

func (w *smokeOrcaWorkers) Start(ctx context.Context, spec harness.WorkerSpec) (harness.Worker, error) {
	var deliveredID string
	if spec.Role == protocol.RoleAccept {
		var err error
		deliveredID, err = smokeDeliveredFile(spec.Artifact)
		if err != nil {
			return nil, err
		}
	}
	onThread := spec.OnThread
	spec.OnThread = func(thread string) error {
		var evidence smokeRunEvidence
		if err := json.Unmarshal([]byte(thread), &evidence); err != nil {
			return err
		}
		evidence.Role = spec.Role
		w.mu.Lock()
		w.runs[spec.RunID] = evidence
		w.mu.Unlock()
		return onThread(thread)
	}
	download := spec.DownloadFile
	spec.DownloadFile = func(ctx context.Context, id string) (string, error) {
		if spec.Role == protocol.RoleAccept && id != deliveredID {
			return "", fmt.Errorf("download must reference the single delivered nonce file")
		}
		path, err := download(ctx, id)
		if err != nil {
			return "", err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		if string(body) != w.nonce {
			return "", fmt.Errorf("downloaded smoke bytes differ")
		}
		if spec.Role == protocol.RoleAccept {
			w.mu.Lock()
			w.downloads++
			w.mu.Unlock()
		}
		return path, nil
	}
	worker, err := w.Workers.Start(ctx, spec)
	if err != nil {
		w.t.Logf("isolated worker start failed for run %s: %v", spec.RunID, smokeStartError(err))
	}
	return worker, err
}

func temporaryRunnerSmoke(t *testing.T, real bool) {
	t.Helper()
	// Unlike t.TempDir, this root survives uncertain terminal cleanup.
	root, err := os.MkdirTemp("", "tend-it-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("isolated smoke root: %s", root)
	runnerStarted := false
	t.Cleanup(func() {
		// Setup failures occur before any provider resource can be created.
		if !runnerStarted {
			if err := os.RemoveAll(root); err != nil {
				t.Error(err)
			}
		}
	})
	srv := openServer(t, time.Minute, filepath.Join(root, "server.db"))
	srv.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, "config.sync", protocol.Target{Type: "config"}, app.ConfigPayload{Hosts: []app.HostConfig{{HostID: "linux-1", OS: "linux", Arch: "amd64", Slots: 1, TokenHash: runnerapi.HashToken(hostToken), Nodes: []string{"tag:tend-runner"}, Worker: protocol.WorkerOrca}}}, app.SyncConfig{})
	home := filepath.Join(root, "runner")
	sockets := filepath.Join(root, "s")
	if err := os.MkdirAll(sockets, 0o700); err != nil {
		t.Fatal(err)
	}
	// Probe the actual bridge even in the fake-worker fixture, so socket setup
	// errors cannot be hidden by a worker that does not use the Unix transport.
	probe, err := bridge.Listen(filepath.Join(sockets, "probe.sock"), "smoke-probe", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := gitws.Config{Home: home, ServerURL: srv.url}
	var workers harness.Workers
	var observed *smokeOrcaWorkers
	var spaces *gitws.Workspaces
	nonce := srv.ids.New(protocol.PrefixRequest) + "\n"
	if real {
		oc := &orca.Client{Bin: os.Getenv("TEND_ORCA_BIN")}
		if err := oc.CheckWorkspaceProtocol(t.Context()); err != nil {
			t.Fatal(err)
		}
		bin := filepath.Join(root, "tend-runner")
		if out, err := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "github.com/GhostFlying/tend/cmd/tend-runner").CombinedOutput(); err != nil {
			t.Fatalf("build bridge: %v\n%s", err, out)
		}
		model, explicitModel := os.LookupEnv("TEND_ORCA_MODEL")
		if !explicitModel {
			model = traexModel()
		}
		observed = &smokeOrcaWorkers{t: t, nonce: nonce, runs: map[string]smokeRunEvidence{}, Workers: orca.NewWorkers(orca.WorkerOptions{Client: oc, Agent: protocol.AgentTraex, Model: model, Effort: os.Getenv("TEND_ORCA_EFFORT"), BridgeCommand: bin, BridgeArgs: []string{"worker-mcp"}, SocketDir: sockets, IDs: protocol.NewULIDGen(time.Now, nil), Now: time.Now})}
		workers = observed
		spaces = gitws.NewOrca(cfg, oc)
	} else {
		spaces = gitws.New(cfg)
		workers = &testkit.Workers{Script: func(spec harness.WorkerSpec) testkit.WorkerScript {
			result, err := smokeFileResult(t.Context(), spec, nonce)
			return testkit.WorkerScript{Result: result, Err: err}
		}}
	}
	ws := &smokeWorkspaces{Workspaces: spaces, t: t, resources: map[string]protocol.WorkspaceCleanup{}}
	j, err := journal.Open(t.Context(), filepath.Join(root, "journal.db"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := runner.New(runner.Config{ServerURL: srv.url, HostID: "linux-1", HostToken: hostToken, Version: "integration", Journal: j, Worker: protocol.WorkerOrca, Workers: workers, Workspaces: ws, Now: time.Now, IDs: protocol.NewULIDGen(time.Now, nil), Log: quiet(), RetryDelay: 100 * time.Millisecond, HeartbeatInterval: time.Second, EventFlush: 100 * time.Millisecond})
	done := make(chan error, 1)
	runnerStarted = true
	go func() { done <- r.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Errorf("runner did not stop; preserve %s", root)
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		runs, err := j.Runs(cleanupCtx)
		clean := err == nil
		if err != nil {
			t.Errorf("read cleanup journal: %v", err)
		}
		for _, run := range runs {
			if run.Held {
				clean = false
				continue
			}
			if run.Thread != "" {
				if err := workers.(harness.Releaser).Release(cleanupCtx, run.Assignment.RunID, run.Thread); err != nil {
					clean = false
					t.Errorf("release test run %s: %v", run.Assignment.RunID, err)
				}
			}
		}
		if clean {
			for _, resource := range ws.resources {
				if err := spaces.Cleanup(cleanupCtx, resource); err != nil {
					clean = false
					t.Errorf("cleanup test workspace %s: %v", resource.WorkspaceID, err)
				}
			}
			// A failed Prepare may register a folder before returning its report.
			// Keep the ownership files in that case; never delete unrecorded resources.
			entries, err := os.ReadDir(filepath.Join(home, "temporary-workspaces"))
			if err != nil && !os.IsNotExist(err) {
				clean = false
			}
			if len(entries) != 0 {
				clean = false
			}
		}
		_ = j.Close()
		srv.shutdown()
		if clean {
			if err := os.RemoveAll(root); err != nil {
				t.Error(err)
			} else {
				t.Log("isolated folders, terminals, journal and scratch removed")
			}
		} else {
			t.Errorf("cleanup incomplete; preserve isolated evidence at %s", root)
		}
	}()
	smokeWait(t, 30*time.Second, "runner capability registration", func() bool {
		err := srv.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var err error
			_, err = store.GetHostRuntime(t.Context(), tx.Q(), "linux-1")
			return err
		})
		return err == nil
	})
	goal := fmt.Sprintf("Create exactly one deliverable file named nonce.txt with exact bytes %q. Upload it with upload_file and submit_result with the returned file metadata. Do not make a Git commit or change anything outside this task workspace.", nonce)
	spec := domain.Spec{Goal: goal, Acceptance: []string{"Exactly one delivered file named nonce.txt", fmt.Sprintf("The acceptance worker must call download_file with the supplied file_id and independently compare its downloaded bytes to %q, including the final newline. Do not substitute reading the implementation directory. Include the file_id and byte comparison result in verdict evidence.", nonce)}, Deliverables: []string{domain.DeliveryFiles}, Workspace: domain.Workspace{Mode: "temporary"}}
	op := srv.exec(owner, "tasks.create", protocol.Target{Type: "task"}, map[string]any{"title": "isolated temporary runner smoke", "spec": spec}, app.CreateTask{})
	var created struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(op.Result, &created); err != nil || created.TaskID == "" {
		t.Fatalf("create isolated task: %v", err)
	}
	deadline := 20 * time.Minute
	if !real {
		deadline = 30 * time.Second
	}
	smokeWait(t, deadline, "independently accepted task", func() bool {
		task := srv.task(created.TaskID)
		if task.Lifecycle == protocol.TaskCancelled || task.Lifecycle == protocol.TaskChangesRequired || (task.BlockReason != protocol.BlockNone && task.BlockReason != protocol.BlockWaitingResource) {
			var question string
			_ = srv.store.Read(t.Context(), func(tx *store.ReadTx) error {
				return tx.Q().QueryRowContext(t.Context(), "SELECT question FROM decisions WHERE task_id=? AND status='open' LIMIT 1", created.TaskID).Scan(&question)
			})
			t.Logf("isolated task decision: %s", question)
			t.Fatalf("isolated task %s stopped progressing: lifecycle=%s block_reason=%s", task.TaskID, task.Lifecycle, task.BlockReason)
		}
		return task.Lifecycle == protocol.TaskCompleted
	})
	smokeWait(t, 30*time.Second, "all run releases acknowledged", func() bool { runs, err := j.Runs(t.Context()); return err == nil && len(runs) == 0 })
	if count := srv.count(`SELECT COUNT(*) FROM runs`); count != 2 {
		t.Errorf("runs = %d, want exactly implementation and acceptance", count)
	}
	if observed != nil {
		observed.mu.Lock()
		evidence, err := json.Marshal(struct {
			TaskID    string                      `json:"task_id"`
			Downloads int                         `json:"acceptance_downloads"`
			Runs      map[string]smokeRunEvidence `json:"runs"`
		}{created.TaskID, observed.downloads, observed.runs})
		downloads := observed.downloads
		observed.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		if downloads == 0 {
			t.Fatal("acceptance passed without a verified download_file call")
		}
		t.Logf("smoke evidence: %s", evidence)
	}
	t.Logf("isolated task %s completed with independent file acceptance and acknowledged run releases", created.TaskID)
}

func smokeWait(t *testing.T, timeout time.Duration, what string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func smokeFileResult(ctx context.Context, spec harness.WorkerSpec, nonce string) (harness.WorkerResult, error) {
	if spec.Role == protocol.RoleImplement {
		path := filepath.Join(spec.Dir, "nonce.txt")
		if err := os.WriteFile(path, []byte(nonce), 0o600); err != nil {
			return harness.WorkerResult{}, err
		}
		f, err := spec.UploadFile(ctx, path, "nonce.txt")
		return harness.WorkerResult{Result: &protocol.ArtifactPayload{Files: []protocol.ArtifactFile{f}}}, err
	}
	fileID, err := smokeDeliveredFile(spec.Artifact)
	if err != nil {
		return harness.WorkerResult{}, err
	}
	path, err := spec.DownloadFile(ctx, fileID)
	if err != nil {
		return harness.WorkerResult{}, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return harness.WorkerResult{}, err
	}
	if string(body) != nonce {
		return harness.WorkerResult{}, fmt.Errorf("downloaded nonce differs")
	}
	return harness.WorkerResult{Verdict: &protocol.VerdictPayload{Verdict: protocol.VerdictPassed, Evidence: json.RawMessage(`{"downloaded_bytes":"exact match"}`)}}, nil
}

func smokeDeliveredFile(raw json.RawMessage) (string, error) {
	var artifact protocol.ArtifactPayload
	if err := json.Unmarshal(raw, &artifact); err != nil {
		return "", err
	}
	if len(artifact.Files) != 1 || artifact.Files[0].Name != "nonce.txt" || artifact.Files[0].FileID == "" {
		return "", fmt.Errorf("expected exactly one named nonce.txt delivery with a file ID")
	}
	return artifact.Files[0].FileID, nil
}

func TestIntegrationFixture_SmokeDeliveryShape(t *testing.T) {
	for _, raw := range []string{`{}`, `{"files":[]}`, `{"files":[{"file_id":"f1","name":"other.txt"}]}`, `{"files":[{"name":"nonce.txt"}]}`, `{"files":[{"file_id":"f1","name":"nonce.txt"},{"file_id":"f2","name":"nonce.txt"}]}`} {
		if _, err := smokeDeliveredFile(json.RawMessage(raw)); err == nil {
			t.Errorf("accepted invalid delivery: %s", raw)
		}
	}
	if id, err := smokeDeliveredFile(json.RawMessage(`{"files":[{"file_id":"f1","name":"nonce.txt"}]}`)); err != nil || id != "f1" {
		t.Fatalf("valid delivery = %q, %v", id, err)
	}
}

// Native errors can carry provider payloads. Keep only typed startup details.
func smokeStartError(err error) string {
	if provider, ok := errors.AsType[*orca.Error](err); ok {
		return "orca_code=" + provider.Code
	}
	if socket, ok := errors.AsType[*net.OpError](err); ok {
		if errno, ok := errors.AsType[syscall.Errno](socket); ok {
			return fmt.Sprintf("socket_operation=%s network=%s errno=%d", socket.Op, socket.Net, errno)
		}
		return fmt.Sprintf("socket_operation=%s network=%s", socket.Op, socket.Net)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	if errors.Is(err, context.Canceled) {
		return "context_cancelled"
	}
	return fmt.Sprintf("type=%T worker_not_started=%t", err, errors.Is(err, harness.ErrWorkerNotStarted))
}
