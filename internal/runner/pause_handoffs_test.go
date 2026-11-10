package runner

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type pauseWorkers struct {
	running atomic.Bool
	starts  atomic.Int64
	started chan struct{}
	unknown bool
}

func (w *pauseWorkers) Start(ctx context.Context, s harness.WorkerSpec) (harness.Worker, error) {
	w.starts.Add(1)
	w.running.Store(true)
	if err := s.OnThread("retained-thread"); err != nil {
		return nil, err
	}
	events := make(chan harness.WorkerEvent)
	go func() { <-ctx.Done(); w.running.Store(false); close(events) }()
	close(w.started)
	return handoffWorker{ctx: ctx, events: events}, nil
}
func (w *pauseWorkers) Release(context.Context, string, string) error {
	if w.unknown {
		return errors.New("physical stop could not be verified")
	}
	if w.running.Load() {
		return errors.New("process is still running")
	}
	return nil
}

func TestPauseHandoffPersistsStopBeforeReportAndPreservesWorkspace(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "stopped", true: "unknown-stop"}[unknown], func(t *testing.T) {
			clock := testkit.NewClock()
			j, err := journal.Open(t.Context(), filepath.Join(t.TempDir(), "journal.db"), clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			as := protocol.Assignment{RunID: "original", TaskID: "task", Role: protocol.RoleImplement, Desired: protocol.DesiredRun, PauseHandoffs: true}
			if err := j.AcceptAssignmentFromWorker(t.Context(), as, "", protocol.WorkerAppServer); err != nil {
				t.Fatal(err)
			}
			if err := j.SetPhase(t.Context(), as.RunID, journal.PhaseStarted); err != nil {
				t.Fatal(err)
			}
			p := &protocol.PauseHandoff{RequestID: "original-pause", IssueID: "issue", IssueRevision: 3, ControlRevision: 1}
			workers := &pauseWorkers{started: make(chan struct{}), unknown: unknown}
			var reports atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var env protocol.Envelope
				if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
					t.Error(err)
					return
				}
				if env.Operation == protocol.OpRunsYieldPause {
					reports.Add(1)
					saved, err := j.Run(req.Context(), as.RunID)
					if err != nil || !saved.StoppedConfirmed || workers.running.Load() {
						t.Error("handoff lacks durable stop proof", err)
					}
					var sent protocol.YieldPausePayload
					if err := json.Unmarshal(env.Payload, &sent); err != nil {
						t.Error(err)
					}
					if sent.PauseHandoff != *p || sent.ThreadID != "retained-thread" {
						t.Error("original pause identity changed", sent)
					}
				}
				if env.Operation == protocol.OpRunsRelease || env.Operation == protocol.OpRunsSubmitArtifact {
					t.Error("pause became a failed or completed result", env.Operation)
				}
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: json.RawMessage(`{}`)}})
			}))
			defer server.Close()
			r := New(Config{Journal: j, Worker: protocol.WorkerAppServer, Now: clock.Now, IDs: testkit.NewIDGen(clock, 3), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ServerURL: server.URL, HTTPClient: server.Client(), Workers: workers, Workspaces: prelaunchSpaces{dir: t.TempDir(), state: t.TempDir()}, RetryDelay: time.Millisecond})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			a := &active{as: as, desired: protocol.DesiredRun, resume: make(chan struct{}, 1)}
			stopped := make(chan struct{})
			go func() {
				defer close(stopped)
				select {
				case <-workers.started:
				case <-ctx.Done():
					return
				}
				if err := j.RecordPause(ctx, as.RunID, p); err != nil {
					t.Error(err)
					return
				}
				r.applyDesired(ctx, a, protocol.DesiredPaused, clock.Now())
			}()
			err = r.execute(ctx, ctx, as.RunID, a)
			<-stopped
			if unknown {
				if err == nil || reports.Load() != 0 {
					t.Fatalf("unknown stop released slot: %v reports=%d", err, reports.Load())
				}
				if _, err := j.Run(t.Context(), as.RunID); err != nil {
					t.Fatal("lost retained original", err)
				}
			} else {
				if err != nil || reports.Load() != 1 {
					t.Fatal(err, reports.Load())
				}
				if _, err := j.Run(t.Context(), as.RunID); !errors.Is(err, journal.ErrNotFound) {
					t.Fatal("local slot retained", err)
				}
			}
			if workers.starts.Load() != 1 {
				t.Fatal("pause restarted model", workers.starts.Load())
			}
		})
	}
}

func TestPauseHandoffPreservesAlreadyCompletedResultWithoutRestartingWorker(t *testing.T) {
	clock := testkit.NewClock()
	j, err := journal.Open(t.Context(), filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	as := protocol.Assignment{RunID: "completed", TaskID: "task", Role: protocol.RoleImplement, Desired: protocol.DesiredPaused, PauseHandoffs: true, Spec: json.RawMessage(`{"goal":"Record findings","deliverables":["report"]}`)}
	if err := j.AcceptAssignmentFromWorker(t.Context(), as, "", protocol.WorkerAppServer); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(harness.WorkerResult{Result: &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Already verified findings"}}})
	if err := j.WorkerDone(t.Context(), as.RunID, raw); err != nil {
		t.Fatal(err)
	}
	if err := j.RecordPause(t.Context(), as.RunID, &protocol.PauseHandoff{RequestID: "pause", IssueID: "issue", IssueRevision: 3, ControlRevision: 1}); err != nil {
		t.Fatal(err)
	}
	var results atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		if env.Operation == protocol.OpRunsSubmitArtifact {
			results.Add(1)
		}
		if env.Operation == protocol.OpRunsYieldPause || env.Operation == protocol.OpRunsRelease {
			t.Error("completed result replaced by a pause or failure")
		}
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: json.RawMessage(`{}`)}})
	}))
	defer server.Close()
	workers := &pauseWorkers{started: make(chan struct{})}
	r := New(Config{Journal: j, Worker: protocol.WorkerAppServer, Now: clock.Now, IDs: testkit.NewIDGen(clock, 3), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ServerURL: server.URL, HTTPClient: server.Client(), Workers: workers, Workspaces: prelaunchSpaces{dir: t.TempDir(), state: t.TempDir()}, RetryDelay: time.Millisecond})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := r.execute(ctx, ctx, as.RunID, &active{as: as, desired: protocol.DesiredPaused, resume: make(chan struct{}, 1)}); err != nil {
		t.Fatal(err)
	}
	if results.Load() != 1 || workers.starts.Load() != 0 {
		t.Fatal("known outcome was lost or rerun", results.Load(), workers.starts.Load())
	}
}

func TestPauseHandoffRetainsHumanControlledExecution(t *testing.T) {
	for _, unknown := range []bool{false} {
		t.Run(map[bool]string{false: "stopped", true: "unknown-stop"}[unknown], func(t *testing.T) {
			clock := testkit.NewClock()
			j, err := journal.Open(t.Context(), filepath.Join(t.TempDir(), "journal.db"), clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			as := protocol.Assignment{RunID: "original", TaskID: "task", Role: protocol.RoleImplement, Desired: protocol.DesiredRun, PauseHandoffs: true}
			if err := j.AcceptAssignmentFromWorker(t.Context(), as, "", protocol.WorkerAppServer); err != nil {
				t.Fatal(err)
			}
			if err := j.SetPhase(t.Context(), as.RunID, journal.PhaseStarted); err != nil {
				t.Fatal(err)
			}
			p := &protocol.PauseHandoff{RequestID: "original-pause", IssueID: "issue", IssueRevision: 3, ControlRevision: 1}
			workers := &pauseWorkers{started: make(chan struct{}), unknown: unknown}
			var reports atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var env protocol.Envelope
				if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
					t.Error(err)
					return
				}
				if env.Operation == protocol.OpRunsYieldPause {
					reports.Add(1)
					saved, err := j.Run(req.Context(), as.RunID)
					if err != nil || !saved.StoppedConfirmed || workers.running.Load() {
						t.Error("handoff lacks durable stop proof", err)
					}
					var sent protocol.YieldPausePayload
					if err := json.Unmarshal(env.Payload, &sent); err != nil {
						t.Error(err)
					}
					if sent.PauseHandoff != *p || sent.ThreadID != "retained-thread" {
						t.Error("original pause identity changed", sent)
					}
				}
				if env.Operation == protocol.OpRunsRelease || env.Operation == protocol.OpRunsSubmitArtifact {
					t.Error("pause became a failed or completed result", env.Operation)
				}
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: json.RawMessage(`{}`)}})
			}))
			defer server.Close()
			r := New(Config{Journal: j, Worker: protocol.WorkerAppServer, Now: clock.Now, IDs: testkit.NewIDGen(clock, 3), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ServerURL: server.URL, HTTPClient: server.Client(), Workers: workers, Workspaces: prelaunchSpaces{dir: t.TempDir(), state: t.TempDir()}, RetryDelay: time.Millisecond})
			ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			defer cancel()
			a := &active{as: as, desired: protocol.DesiredRun, resume: make(chan struct{}, 1)}
			stopped := make(chan struct{})
			go func() {
				defer close(stopped)
				select {
				case <-workers.started:
				case <-ctx.Done():
					return
				}
				if err := j.RecordPause(ctx, as.RunID, p); err != nil {
					t.Error(err)
					return
				}
				r.applyDesired(ctx, a, protocol.DesiredHeld, clock.Now())
			}()
			err = r.execute(ctx, ctx, as.RunID, a)
			<-stopped
			if unknown {
				if err == nil || reports.Load() != 0 {
					t.Fatalf("unknown stop released slot: %v reports=%d", err, reports.Load())
				}
				if _, err := j.Run(t.Context(), as.RunID); err != nil {
					t.Fatal("lost retained original", err)
				}
			} else {
				if reports.Load() != 0 {
					t.Fatal(err, reports.Load())
				}
				if saved, err := j.Run(t.Context(), as.RunID); err != nil || !saved.Held {
					t.Fatal("human controlled execution was discarded", saved, err)
				}
			}
			if workers.starts.Load() != 1 {
				t.Fatal("pause restarted model", workers.starts.Load())
			}
		})
	}
}
