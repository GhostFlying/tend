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

func TestExternalWaitPhysicalStopPrecedesSlotRelease(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "stopped", true: "unknown-stop"}[unknown], func(t *testing.T) {
			clock := testkit.NewClock()
			j, err := journal.Open(t.Context(), filepath.Join(t.TempDir(), "journal.db"), clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			as := protocol.Assignment{RunID: "original", TaskID: "task", Role: protocol.RoleImplement, Desired: protocol.DesiredRun, ExternalWaits: true}
			if err := j.AcceptAssignmentFromWorker(t.Context(), as, "", protocol.WorkerAppServer); err != nil {
				t.Fatal(err)
			}
			if err := j.SetPhase(t.Context(), as.RunID, journal.PhaseStarted); err != nil {
				t.Fatal(err)
			}
			waiting := &protocol.ExternalWait{WaitID: "original-wait", RunID: as.RunID, TaskID: as.TaskID, Status: "stopping", WaitExternalPayload: protocol.WaitExternalPayload{WaitKey: "ci", Kind: "checks", TargetID: "original-pr", Reason: "Checks are running", Recovery: "Read the existing checks"}}
			workers := &pauseWorkers{started: make(chan struct{}), unknown: unknown}
			var reports atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var env protocol.Envelope
				if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
					t.Error(err)
					return
				}
				if env.Operation == protocol.OpRunsYieldExternalWait {
					reports.Add(1)
					saved, err := j.Run(req.Context(), as.RunID)
					if err != nil || !saved.StoppedConfirmed || workers.running.Load() {
						t.Error("yield lacks actual stop proof", err)
					}
					var p protocol.YieldExternalWaitPayload
					if json.Unmarshal(env.Payload, &p) != nil || p.WaitID != waiting.WaitID || p.ThreadID != "retained-thread" {
						t.Error("lost wait identity", p)
					}
				}
				if env.Operation == protocol.OpRunsRelease || env.Operation == protocol.OpRunsSubmitArtifact {
					t.Error("waiting became completion or failure")
				}
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: json.RawMessage(`{}`)}})
			}))
			defer server.Close()
			r := New(Config{Journal: j, Worker: protocol.WorkerAppServer, Now: clock.Now, IDs: testkit.NewIDGen(clock, 31), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ServerURL: server.URL, HTTPClient: server.Client(), Workers: workers, Workspaces: prelaunchSpaces{dir: t.TempDir(), state: t.TempDir()}, RetryDelay: time.Millisecond})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			a := &active{as: as, desired: protocol.DesiredRun, resume: make(chan struct{}, 1)}
			done := make(chan struct{})
			go func() {
				defer close(done)
				select {
				case <-workers.started:
				case <-ctx.Done():
					return
				}
				if err := j.RecordExternalWait(ctx, as.RunID, waiting); err != nil {
					t.Error(err)
					return
				}
				r.applyDesired(ctx, a, protocol.DesiredPaused, clock.Now())
			}()
			err = r.execute(ctx, ctx, as.RunID, a)
			<-done
			if unknown {
				if err == nil || reports.Load() != 0 {
					t.Fatal("unknown stop released slot", err, reports.Load())
				}
				if _, err := j.Run(t.Context(), as.RunID); err != nil {
					t.Fatal("lost original unknown Run", err)
				}
			} else {
				if err != nil || reports.Load() != 1 {
					t.Fatal(err, reports.Load())
				}
				if _, err := j.Run(t.Context(), as.RunID); !errors.Is(err, journal.ErrNotFound) {
					t.Fatal("slot remained held", err)
				}
			}
			if workers.starts.Load() != 1 {
				t.Fatal("wait restarted model")
			}
		})
	}
}

func TestExternalWaitRecoveryDoesNotStartAnotherModel(t *testing.T) {
	clock := testkit.NewClock()
	j, err := journal.Open(t.Context(), filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	as := protocol.Assignment{RunID: "original", TaskID: "task", Role: protocol.RoleImplement, Desired: protocol.DesiredRun, ExternalWaits: true}
	if err := j.AcceptAssignmentFromWorker(t.Context(), as, "", protocol.WorkerAppServer); err != nil {
		t.Fatal(err)
	}
	var original string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var env protocol.Envelope
		_ = json.NewDecoder(req.Body).Decode(&env)
		if original == "" {
			original = env.RequestID
		} else if env.RequestID != original {
			t.Error("recovery allocated new request")
		}
		raw, _ := json.Marshal(protocol.ExternalWait{WaitID: env.RequestID, RunID: as.RunID, TaskID: as.TaskID, Status: "stopping"})
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: raw}})
	}))
	defer server.Close()
	r := New(Config{Journal: j, Worker: protocol.WorkerAppServer, Now: clock.Now, IDs: testkit.NewIDGen(clock, 32), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ServerURL: server.URL, HTTPClient: server.Client()})
	p := Prepared{StateDir: t.TempDir()}
	payload := protocol.WaitExternalPayload{WaitKey: "original", Kind: "task", TargetID: "dependency", Reason: "Wait", Recovery: "Continue"}
	env, err := r.persistExternalWaitIntent(p.StateDir, as, payload)
	if err != nil {
		t.Fatal(err)
	}
	original = env.RequestID
	v, err := r.recoverExternalWait(t.Context(), as, p)
	if err != nil || v == nil || v.WaitID != original {
		t.Fatal(v, err)
	}
	if v, err := r.recoverExternalWait(t.Context(), as, p); err != nil || v == nil || v.WaitID != original {
		t.Fatal(v, err)
	}
	if _, err := r.persistExternalWaitIntent(p.StateDir, as, protocol.WaitExternalPayload{WaitKey: payload.WaitKey, Kind: "task", TargetID: "different", Reason: "Wait", Recovery: "Continue"}); err == nil {
		t.Fatal("mutated original wait intent")
	}
}

func TestExternalWaitLostNativeCloseReplyReconcilesOriginalReceipt(t *testing.T) {
	clock := testkit.NewClock()
	j, err := journal.Open(t.Context(), filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	as := protocol.Assignment{RunID: "original-owner", TaskID: "original-task", Role: protocol.RoleIssueManager, Desired: protocol.DesiredRun, ExternalWaits: true, Manager: &protocol.IssueManagerAssignment{}}
	if err := j.AcceptAssignmentFromWorker(t.Context(), as, "", protocol.WorkerAppServer); err != nil {
		t.Fatal(err)
	}
	command := protocol.ManagerNativeRequest{ID: "original-close", Method: "close", Payload: json.RawMessage(`{}`)}
	if _, _, err := j.ManagerOperation(t.Context(), as.RunID, command); err != nil {
		t.Fatal(err)
	}
	reply := protocol.ManagerNativeReply{ID: command.ID, Result: json.RawMessage(`null`)}
	if err := j.CompleteManagerOperation(t.Context(), reply); err != nil {
		t.Fatal(err)
	}
	closePayload, _ := json.Marshal(reply)
	closeEnvelope := protocol.Envelope{RequestID: "original-close-receipt", Operation: protocol.OpManagerNativeReply, Target: runTarget(as), Payload: closePayload}
	if err := j.PutRequest(t.Context(), journal.Request{RunID: as.RunID, Envelope: closeEnvelope}); err != nil {
		t.Fatal(err)
	}
	if err := j.RecordExternalWait(t.Context(), as.RunID, &protocol.ExternalWait{WaitID: "original-wait", RunID: as.RunID, TaskID: as.TaskID}); err != nil {
		t.Fatal(err)
	}
	if err := j.ConfirmStopped(t.Context(), as.RunID); err != nil {
		t.Fatal(err)
	}
	var replayed atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		if env.Operation == protocol.OpRunsYieldExternalWait {
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(rpcResponse{Error: protocol.Errorf(protocol.CodeForbidden, "the original native close already released this lease")})
			return
		}
		if env.Operation != closeEnvelope.Operation || env.RequestID != closeEnvelope.RequestID || string(env.Payload) != string(closeEnvelope.Payload) {
			t.Error("recovery changed the original native receipt", env)
		}
		replayed.Add(1)
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: closePayload}})
	}))
	defer server.Close()
	r := New(Config{Journal: j, Worker: protocol.WorkerAppServer, Now: clock.Now, IDs: testkit.NewIDGen(clock, 33), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ServerURL: server.URL, HTTPClient: server.Client()})
	if err := r.finishExternalWait(t.Context(), as); err != nil {
		t.Fatal(err)
	}
	if replayed.Load() != 1 {
		t.Fatal("original close was not reconciled", replayed.Load())
	}
	if _, err := j.Run(t.Context(), as.RunID); !errors.Is(err, journal.ErrNotFound) {
		t.Fatal("reconciled closed process retained its execution slot", err)
	}
}

var _ harness.Workers = (*pauseWorkers)(nil)
