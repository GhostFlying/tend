package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/orca"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

type prelaunchSpaces struct {
	completionWorkspaces
	dir, state string
}

func (w prelaunchSpaces) Prepare(context.Context, protocol.Assignment) (Prepared, error) {
	return Prepared{Dir: w.dir, StateDir: w.state, ProviderID: "folder:owned"}, nil
}

func TestOrcaBridgeConflictProofSurvivesRestartAndReleasesWithoutProvider(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewClock()
	path := filepath.Join(t.TempDir(), "journal.db")
	j, err := journal.Open(ctx, path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	dir := t.TempDir()
	config := filepath.Join(dir, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(config), 0700); err != nil {
		t.Fatal(err)
	}
	const ownerConfig = "# owner's existing config\n"
	if err := os.WriteFile(config, []byte(ownerConfig), 0600); err != nil {
		t.Fatal(err)
	}
	as := protocol.Assignment{RunID: "run_prelaunch", TaskID: "task_prelaunch", Role: protocol.RoleImplement, Desired: protocol.DesiredRun, Spec: json.RawMessage(`{"deliverables":["report"]}`)}
	if err := j.AcceptAssignmentFromWorker(ctx, as, "", protocol.WorkerOrca); err != nil {
		t.Fatal(err)
	}
	if err := j.SetPhase(ctx, as.RunID, journal.PhaseStarted); err != nil {
		t.Fatal(err)
	}
	sockets, err := os.MkdirTemp("", "tnp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockets) })
	calls := 0
	workers := orca.NewWorkers(orca.WorkerOptions{Agent: protocol.AgentCodex, IDs: testkit.NewIDGen(clock, 1), Now: clock.Now, SocketDir: sockets, Client: &orca.Client{Exec: func(context.Context, string, []string) ([]byte, error) {
		calls++
		return nil, errors.New("provider must not be called")
	}}})
	var sent []protocol.Envelope
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		_ = json.NewDecoder(q.Body).Decode(&env)
		sent = append(sent, env)
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded}})
	}))
	defer srv.Close()
	spaces := prelaunchSpaces{dir: dir, state: t.TempDir()}
	cfg := Config{Journal: j, Now: clock.Now, IDs: testkit.NewIDGen(clock, 2), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ServerURL: srv.URL, HTTPClient: srv.Client(), Worker: protocol.WorkerOrca, Workers: workers, Workspaces: spaces, RetryDelay: time.Millisecond}
	r := New(cfg)
	if err := r.work(ctx, ctx, as, &active{as: as, desired: protocol.DesiredRun}); !errors.Is(err, harness.ErrWorkerNotStarted) || !strings.Contains(err.Error(), "already exists and is not tend's") {
		t.Fatalf("bridge conflict lost explicit prelaunch proof: %v", err)
	}
	saved, err := j.Run(ctx, as.RunID)
	if err != nil || saved.Attempts != 1 || saved.Thread != "" || !saved.StoppedConfirmed || calls != 0 {
		t.Fatalf("prelaunch error not safely recorded: %+v calls=%d err=%v", saved, calls, err)
	}
	if err := r.finish(ctx, as, journal.PhaseReleasing, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "worker_start_failed"}); err != nil {
		t.Fatal(err)
	}
	before, _, err := j.RequestForRun(ctx, as.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = journal.Open(ctx, path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Journal = j
	r = New(cfg)
	if err := r.execute(ctx, ctx, as.RunID, &active{as: as, desired: protocol.DesiredRun}); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || len(sent) != 1 || sent[0].Operation != protocol.OpRunsRelease || sent[0].RequestID != before.Envelope.RequestID {
		t.Fatalf("restart launched or changed release: calls=%d sent=%+v", calls, sent)
	}
	actual, err := os.ReadFile(config)
	if err != nil || string(actual) != ownerConfig {
		t.Fatalf("owner configuration changed: %q %v", actual, err)
	}
}

type notStartedWorkers struct {
	cancel context.CancelFunc
	err    error
}

func (w notStartedWorkers) Start(context.Context, harness.WorkerSpec) (harness.Worker, error) {
	if w.cancel != nil {
		w.cancel()
	}
	return nil, w.err
}

func TestStartProofRequiresFreshAttemptAndPersistsDuringShutdown(t *testing.T) {
	for _, tc := range []struct {
		name, thread                  string
		attempts                      int
		explicit, shutdown, wantProof bool
	}{
		{name: "explicit fresh prelaunch", explicit: true, wantProof: true},
		{name: "explicit prelaunch while shutting down", explicit: true, shutdown: true, wantProof: true},
		{name: "ordinary unknown launch"},
		{name: "existing thread cannot gain fresh proof", thread: `{"marker":"owned_launch","worktree":"wt"}`, explicit: true},
		{name: "earlier attempted run cannot gain fresh proof", attempts: 1, explicit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := context.Background()
			ctx, cancel := context.WithCancel(base)
			defer cancel()
			clock := testkit.NewClock()
			j, err := journal.Open(base, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			as := protocol.Assignment{RunID: "run_start_proof", TaskID: "task", Desired: protocol.DesiredRun}
			if err := j.AcceptAssignmentFromWorker(base, as, "", protocol.WorkerOrca); err != nil {
				t.Fatal(err)
			}
			if err := j.SetPhase(base, as.RunID, journal.PhaseStarted); err != nil {
				t.Fatal(err)
			}
			if tc.thread != "" {
				if err := j.SetThread(base, as.RunID, tc.thread); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < tc.attempts; i++ {
				if _, err := j.NextAttempt(base, as.RunID); err != nil {
					t.Fatal(err)
				}
			}
			startErr := errors.New("start outcome unknown")
			if tc.explicit {
				startErr = fmt.Errorf("%w: rejected before launch", harness.ErrWorkerNotStarted)
			}
			workers := notStartedWorkers{err: startErr}
			if tc.shutdown {
				workers.cancel = cancel
			}
			r := New(Config{Journal: j, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Worker: protocol.WorkerOrca, Workers: workers, Workspaces: completionWorkspaces{}})
			if err := r.work(ctx, ctx, as, &active{as: as, desired: protocol.DesiredRun}); err == nil {
				t.Fatal("expected Start failure")
			}
			saved, err := j.Run(base, as.RunID)
			if err != nil || saved.StoppedConfirmed != tc.wantProof {
				t.Fatalf("wrong proof: %+v err=%v want=%t", saved, err, tc.wantProof)
			}
		})
	}
}
