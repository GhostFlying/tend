package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/traex"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

type continuationStartupSpaces struct {
	completionWorkspaces
	root, dir, state string
	selected         string
	recoveryReads    int
}

func (s *continuationStartupSpaces) Prepare(context.Context, protocol.Assignment) (Prepared, error) {
	return Prepared{Dir: s.dir, StateDir: s.state}, nil
}

func (s *continuationStartupSpaces) RunStateDirectory(run string) (string, error) {
	s.selected = run
	return filepath.Join(s.root, run), nil
}

func (s *continuationStartupSpaces) ReattachRecovery(context.Context, protocol.Assignment) (Prepared, error) {
	s.recoveryReads++
	return Prepared{Dir: s.dir, StateDir: s.state}, nil
}

func TestFault_ContinuationPrelaunchReleasesSameRunAfterJournalRestart(t *testing.T) {
	for _, kind := range []string{"external_wait", "question", "pause"} {
		t.Run(kind, func(t *testing.T) {
			ctx := t.Context()
			clock := testkit.NewClock()
			root := t.TempDir()
			spaces := &continuationStartupSpaces{root: root, dir: t.TempDir(), state: filepath.Join(root, "current")}
			as := protocol.Assignment{RunID: "current", TaskID: "review", Role: protocol.RoleImplement, Desired: protocol.DesiredRun, Spec: json.RawMessage(`{"deliverables":["report"]}`),
				SessionContinuation:      &protocol.SessionContinuation{RunID: "history", ThreadID: "history-thread"},
				ExternalWaitContinuation: &protocol.ExternalWait{RunID: "wait", ThreadID: "wait-thread"}}
			want := "wait"
			if kind == "question" || kind == "pause" {
				as.QuestionContinuation = &protocol.QuestionContinuation{RunID: "question", ThreadID: "question-thread"}
				want = "question"
			}
			if kind == "pause" {
				as.PauseContinuation = &protocol.PauseContinuation{RunID: "pause", ThreadID: "pause-thread"}
				want = "pause"
			}
			for _, previous := range []string{"history", "wait", "question", "pause"} {
				dir := filepath.Join(root, previous)
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(map[string]any{"execution": "run:" + previous, "binding": nil})
				if err := os.WriteFile(filepath.Join(dir, "codex-environment.json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(t.TempDir(), "journal.db")
			j, err := journal.Open(ctx, path, clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = j.Close() })
			if err := j.AcceptAssignmentFromWorker(ctx, as, "", protocol.WorkerAppServer); err != nil {
				t.Fatal(err)
			}
			if err := j.SetPhase(ctx, as.RunID, journal.PhaseStarted); err != nil {
				t.Fatal(err)
			}
			var sent []protocol.Envelope
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var env protocol.Envelope
				if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
					t.Error(err)
				}
				sent = append(sent, env)
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded}})
			}))
			defer server.Close()
			workers := traex.NewWorkers(traex.WorkerOptions{Session: traex.Options{Bin: filepath.Join(t.TempDir(), "native-must-not-exist")}})
			cfg := Config{Journal: j, Worker: protocol.WorkerAppServer, Workers: workers, Workspaces: spaces, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), Log: slog.New(slog.DiscardHandler), ServerURL: server.URL, HTTPClient: server.Client(), RequestTimeout: time.Second, RetryDelay: time.Millisecond}
			r := New(cfg)
			err = r.work(ctx, ctx, as, &active{as: as, desired: protocol.DesiredRun})
			if !errors.Is(err, harness.ErrWorkerNotStarted) || !strings.Contains(err.Error(), "start app-server") {
				t.Fatalf("matching predecessor did not reach the explicit prelaunch failure: %v", err)
			}
			saved, err := j.Run(ctx, as.RunID)
			if err != nil || spaces.selected != want || saved.Thread != want+"-thread" || saved.Attempts != 1 || !saved.StoppedConfirmed || spaces.recoveryReads != 0 {
				t.Fatalf("continuation lost its current no-launch proof: run=%+v selected=%s recovery=%d err=%v", saved, spaces.selected, spaces.recoveryReads, err)
			}
			if _, err := os.Stat(filepath.Join(spaces.state, "owned-appserver-process.json")); !os.IsNotExist(err) {
				t.Fatalf("a rejected launch acquired native ownership: %v", err)
			}
			if err := r.finish(ctx, as, journal.PhaseReleasing, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "worker_start_failed"}); err != nil {
				t.Fatal(err)
			}
			pending, _, err := j.RequestForRun(ctx, as.RunID)
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
			if err := New(cfg).execute(ctx, ctx, as.RunID, &active{as: as, desired: protocol.DesiredRun}); err != nil {
				t.Fatal(err)
			}
			if len(sent) != 1 || sent[0].Operation != protocol.OpRunsRelease || sent[0].RequestID != pending.Envelope.RequestID || sent[0].Target.ID != as.RunID || spaces.recoveryReads != 0 {
				t.Fatalf("release changed identity or repeated native work: sent=%+v recovery=%d", sent, spaces.recoveryReads)
			}
		})
	}
}

type continuationStartupFailure struct {
	startErr, stopErr error
}

func (w continuationStartupFailure) Start(context.Context, harness.WorkerSpec) (harness.Worker, error) {
	return nil, w.startErr
}
func (w continuationStartupFailure) SupportsOwnedWorkerStop() bool { return true }
func (w continuationStartupFailure) StopOwnedWorker(context.Context, harness.WorkerSpec) error {
	return w.stopErr
}

func TestFault_StartupFailurePreservesOriginalAndClosureErrors(t *testing.T) {
	clock := testkit.NewClock()
	j, err := journal.Open(t.Context(), filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	as := protocol.Assignment{RunID: "unknown-start", TaskID: "review", Role: protocol.RoleImplement, Desired: protocol.DesiredRun}
	if err := j.AcceptAssignmentFromWorker(t.Context(), as, "", protocol.WorkerAppServer); err != nil {
		t.Fatal(err)
	}
	if err := j.SetPhase(t.Context(), as.RunID, journal.PhaseStarted); err != nil {
		t.Fatal(err)
	}
	startErr := errors.New("original startup outcome is unknown")
	stopErr := errors.New("original ownership cannot be confirmed")
	spaces := &continuationStartupSpaces{dir: t.TempDir(), state: t.TempDir()}
	r := New(Config{Journal: j, Worker: protocol.WorkerAppServer, Workers: continuationStartupFailure{startErr: startErr, stopErr: stopErr}, Workspaces: spaces, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), Log: slog.New(slog.DiscardHandler)})
	err = r.work(t.Context(), t.Context(), as, &active{as: as, desired: protocol.DesiredRun})
	if !isCompletionPending(err) || !errors.Is(err, startErr) || !errors.Is(err, stopErr) {
		t.Fatalf("a secondary closure error masked the startup error: %v", err)
	}
	saved, err := j.Run(t.Context(), as.RunID)
	if err != nil || saved.StoppedConfirmed || saved.Attempts != 1 || spaces.recoveryReads != 1 {
		t.Fatalf("uncertainty was changed into stop proof: %+v reads=%d err=%v", saved, spaces.recoveryReads, err)
	}
}

type continuationExternalStartup struct {
	alive            atomic.Bool
	starts, releases int
	closeErr         error
}

func (w *continuationExternalStartup) Start(context.Context, harness.WorkerSpec) (harness.Worker, error) {
	w.starts++
	w.alive.Store(true)
	return nil, fmt.Errorf("%w: inner native preflight rejected after outer executor started", harness.ErrWorkerNotStarted)
}

func (w *continuationExternalStartup) Release(context.Context, string, string) error {
	w.releases++
	if w.closeErr != nil {
		return w.closeErr
	}
	w.alive.Store(false)
	return nil
}

func TestFault_InheritedPrelaunchWaitsForExternalExecutorClose(t *testing.T) {
	ctx := t.Context()
	clock := testkit.NewClock()
	j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	as := protocol.Assignment{RunID: "external-current", TaskID: "review", Role: protocol.RoleImplement, Desired: protocol.DesiredRun,
		SessionContinuation:      &protocol.SessionContinuation{RunID: "history", ThreadID: "history-thread"},
		ExternalWaitContinuation: &protocol.ExternalWait{RunID: "wait", ThreadID: "wait-thread"}}
	if err := j.AcceptAssignmentFromWorker(ctx, as, "", protocol.WorkerAppServer); err != nil {
		t.Fatal(err)
	}
	if err := j.SetPhase(ctx, as.RunID, journal.PhaseStarted); err != nil {
		t.Fatal(err)
	}
	outer := &continuationExternalStartup{closeErr: errors.New("outer executor closure unconfirmed")}
	sent := make(chan protocol.Envelope, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
			t.Error(err)
		}
		if outer.alive.Load() {
			t.Error("Run was released before the external executor closed")
		}
		sent <- env
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded}})
	}))
	defer server.Close()
	spaces := &continuationStartupSpaces{root: t.TempDir(), dir: t.TempDir(), state: t.TempDir()}
	r := New(Config{Journal: j, Worker: protocol.WorkerAppServer, Workers: outer, Workspaces: spaces, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), Log: slog.New(slog.DiscardHandler), ServerURL: server.URL, HTTPClient: server.Client(), RequestTimeout: time.Second, RetryDelay: time.Millisecond})
	a := &active{as: as, desired: protocol.DesiredRun}
	if err := r.work(ctx, ctx, as, a); !errors.Is(err, harness.ErrWorkerNotStarted) {
		t.Fatal(err)
	}
	if err := r.finish(ctx, as, journal.PhaseReleasing, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "worker_start_failed"}); err != nil {
		t.Fatal(err)
	}
	pending, _, err := j.RequestForRun(ctx, as.RunID)
	if err != nil {
		t.Fatal(err)
	}
	err = r.awaitCompletedStop(ctx, ctx, as, a)
	saved, readErr := j.Run(ctx, as.RunID)
	if !errors.Is(err, outer.closeErr) || readErr != nil || saved.StoppedConfirmed || !outer.alive.Load() || outer.releases != 1 || len(sent) != 0 {
		t.Fatalf("inner no-launch proof bypassed outer cleanup: stop=%t alive=%t releases=%d sent=%d err=%v read=%v", saved.StoppedConfirmed, outer.alive.Load(), outer.releases, len(sent), err, readErr)
	}
	outer.closeErr = nil
	if err := r.execute(ctx, ctx, as.RunID, a); err != nil {
		t.Fatal(err)
	}
	if outer.alive.Load() || outer.starts != 1 || outer.releases != 2 || len(sent) != 1 {
		t.Fatalf("outer cleanup was not reconciled exactly once: alive=%t starts=%d releases=%d sent=%d", outer.alive.Load(), outer.starts, outer.releases, len(sent))
	}
	if env := <-sent; env.Operation != protocol.OpRunsRelease || env.RequestID != pending.Envelope.RequestID || env.Target.ID != as.RunID {
		t.Fatalf("external close recovery changed the original release: %+v", env)
	}
}
