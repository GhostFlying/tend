package runner

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/traex"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

const acceptanceRunnerServer = "TEND_TEST_RUNNER_ACCEPTANCE_SERVER"

func TestMain(m *testing.M) {
	if path := os.Getenv(acceptanceRunnerServer); path != "" {
		if err := serveAcceptanceRunner(path); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// The native fixture owns a real root process and closes it through the actual
// traex Session.Close contract. It never invokes a model or remote provider.
func serveAcceptanceRunner(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	dec, enc := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := dec.Decode(&req); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if req.Method != "" {
			if _, err := f.WriteString(req.Method + "\n"); err != nil {
				return err
			}
		}
		if len(req.ID) == 0 {
			continue
		}
		if req.Method == "" {
			if err := enc.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "original-thread", "turn": map[string]string{"id": "original-turn", "status": "completed"}}}); err != nil {
				return err
			}
			continue
		}
		result := map[string]any{}
		if req.Method == "thread/start" {
			result["thread"] = map[string]string{"id": "original-thread"}
		}
		if req.Method == "turn/start" {
			result["turn"] = map[string]string{"id": "original-turn"}
		}
		if err := enc.Encode(map[string]any{"id": req.ID, "result": result}); err != nil {
			return err
		}
		if req.Method == "turn/start" {
			if err := enc.Encode(map[string]any{"id": "original-call", "method": "item/tool/call", "params": map[string]any{"threadId": "original-thread", "turnId": "original-turn", "callId": "original-call", "tool": harness.VerdictTool, "arguments": map[string]any{"verdict": "blocked", "evidence": map[string]string{"check": "failed"}, "files": []map[string]string{{"path": "check.log", "name": "check.log"}}}}}); err != nil {
				return err
			}
		}
	}
}

type acceptanceReconcileWorkers struct {
	inner  *traex.Workers
	starts int
}

func (w *acceptanceReconcileWorkers) Start(ctx context.Context, spec harness.WorkerSpec) (harness.Worker, error) {
	w.starts++
	return w.inner.Start(ctx, spec)
}

func (w *acceptanceReconcileWorkers) ReconcileAcceptanceVerdict(ctx context.Context, spec harness.WorkerSpec) (harness.WorkerResult, error) {
	return w.inner.ReconcileAcceptanceVerdict(ctx, spec)
}

type acceptanceReconcileSpaces struct {
	completionWorkspaces
	p                    Prepared
	prepares, reattaches int
	err                  error
}

func (w *acceptanceReconcileSpaces) Prepare(context.Context, protocol.Assignment) (Prepared, error) {
	w.prepares++
	return w.p, nil
}

func (w *acceptanceReconcileSpaces) ReattachAcceptanceVerdict(context.Context, protocol.Assignment) (Prepared, error) {
	w.reattaches++
	return w.p, w.err
}

func TestAcceptanceEvidenceRunnerRecoversOwnedCloseBeforeNativeGate(t *testing.T) {
	for _, scenario := range []string{"confirmed", "prior stop", "result checkpoint", "upload unknown", "missing close", "changed thread", "missing workspace", "no reattach", "retained authority", "no candidate"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
			defer cancel()
			confirmed := false
			var requestIDs []string
			e, cfg := acceptanceEvidenceFixture(t, func(w http.ResponseWriter, q *http.Request) {
				if q.URL.Query().Get("name") == "" {
					var env protocol.Envelope
					_ = json.NewDecoder(q.Body).Decode(&env)
					_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded}})
					return
				}
				requestIDs = append(requestIDs, q.URL.Query().Get("request_id"))
				body, _ := io.ReadAll(q.Body)
				if string(body) != "original evidence bytes" {
					t.Error("reconciliation replaced the original selected bytes")
				}
				if !confirmed {
					_, _ = w.Write([]byte("lost original upload receipt"))
					return
				}
				replyEvidence(w, "check.log", body)
			})
			if err := os.WriteFile(filepath.Join(e.prepared.Dir, "check.log"), []byte("original evidence bytes"), 0o600); err != nil {
				t.Fatal(err)
			}
			clock := testkit.NewClock()
			path := filepath.Join(t.TempDir(), "journal.db")
			j, err := journal.Open(ctx, path, clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = j.Close() })
			as := e.as
			as.Desired = protocol.DesiredRun
			as.Spec = json.RawMessage(`{"deliverables":["report"]}`)
			if err := j.AcceptAssignmentFromWorker(ctx, as, "", protocol.WorkerAppServer); err != nil {
				t.Fatal(err)
			}
			if err := j.SetPhase(ctx, as.RunID, journal.PhaseStarted); err != nil {
				t.Fatal(err)
			}
			bin, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			nativeLog := filepath.Join(t.TempDir(), "native-methods")
			workers := &acceptanceReconcileWorkers{inner: traex.NewWorkers(traex.WorkerOptions{Session: traex.Options{Bin: bin, Env: []string{acceptanceRunnerServer + "=" + nativeLog}}})}
			spaces := &acceptanceReconcileSpaces{p: e.prepared}
			cfg.Journal, cfg.Worker, cfg.Workers, cfg.Workspaces = j, protocol.WorkerAppServer, workers, spaces
			cfg.Log = slog.New(slog.DiscardHandler)
			r := New(cfg)
			a := &active{as: as, desired: protocol.DesiredRun}
			if err := r.work(ctx, ctx, as, a); !isCompletionPending(err) {
				t.Fatalf("original unknown upload lost its pending state: %v", err)
			}
			saved, err := j.Run(ctx, as.RunID)
			if err != nil || saved.Attempts != 1 || saved.StoppedConfirmed || saved.Phase != journal.PhaseStarted || workers.starts != 1 || spaces.prepares != 1 || len(requestIDs) == 0 {
				t.Fatalf("original pending facts: %+v starts=%d prepare=%d requests=%v err=%v", saved, workers.starts, spaces.prepares, requestIDs, err)
			}
			closePath := filepath.Join(e.prepared.StateDir, "owned-session-close.json")
			originalClose, err := os.ReadFile(closePath)
			if err != nil {
				t.Fatalf("actual owned root Close did not persist its receipt: %v", err)
			}
			originalNative, err := os.ReadFile(nativeLog)
			if err != nil || bytes.Count(originalNative, []byte("thread/start\n")) != 1 || bytes.Count(originalNative, []byte("turn/start\n")) != 1 {
				t.Fatal("native original did not execute exactly one thread and turn", string(originalNative), err)
			}
			if err := os.WriteFile(filepath.Join(e.prepared.Dir, "check.log"), []byte("changed scratch bytes"), 0o600); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "prior stop":
				if err := j.ConfirmStopped(ctx, as.RunID); err != nil {
					t.Fatal(err)
				}
			case "retained authority":
				if err := j.SetRetainedResume(ctx, as.RunID, protocol.RetainedRunResume{RequestID: "independent-retained-authority", Mode: "attach"}); err != nil {
					t.Fatal(err)
				}
			case "no candidate":
				if err := os.Remove(filepath.Join(e.prepared.StateDir, "pending-verdict.json")); err != nil {
					t.Fatal(err)
				}
			case "missing close":
				if err := os.Remove(closePath); err != nil {
					t.Fatal(err)
				}
			case "changed thread":
				if err := j.SetThread(ctx, as.RunID, "foreign-thread"); err != nil {
					t.Fatal(err)
				}
			case "missing workspace":
				spaces.err = os.ErrNotExist
			case "no reattach":
				cfg.Workspaces = completionWorkspaces{}
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			j, err = journal.Open(ctx, path, clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Journal = j
			confirmed = scenario != "upload unknown"
			beforeUploads := len(requestIDs)
			if scenario == "result checkpoint" {
				db, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				_, err = db.ExecContext(ctx, `CREATE TRIGGER fail_close BEFORE UPDATE OF stopped_confirmed ON runs WHEN NEW.stopped_confirmed=1 BEGIN SELECT RAISE(ABORT,'injected closure checkpoint'); END`)
				_ = db.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			// Any accidental native Start would fail instead of recovering data.
			workers.inner = traex.NewWorkers(traex.WorkerOptions{Session: traex.Options{Bin: filepath.Join(t.TempDir(), "forbidden-native")}})
			err = New(cfg).work(ctx, ctx, as, a)
			saved, readErr := j.Run(ctx, as.RunID)
			if readErr != nil || saved.Attempts != 1 || workers.starts != 1 || spaces.prepares != 1 {
				t.Fatalf("receipt recovery launched or prepared again: %+v starts=%d prepare=%d err=%v", saved, workers.starts, spaces.prepares, readErr)
			}
			if scenario == "result checkpoint" {
				if !isCompletionPending(err) || saved.Phase != journal.PhaseWorkerDone || saved.StoppedConfirmed || len(saved.WorkerResult) == 0 {
					t.Fatalf("closure checkpoint lost the committed original result: %+v err=%v", saved, err)
				}
				if err := j.Close(); err != nil {
					t.Fatal(err)
				}
				db, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				_, err = db.ExecContext(ctx, `DROP TRIGGER fail_close`)
				_ = db.Close()
				if err != nil {
					t.Fatal(err)
				}
				j, err = journal.Open(ctx, path, clock.Now)
				if err != nil {
					t.Fatal(err)
				}
				cfg.Journal = j
				if err := New(cfg).execute(ctx, ctx, as.RunID, a); err != nil {
					t.Fatal("result checkpoint did not complete after reopening", err)
				}
				if _, err := j.Run(ctx, as.RunID); !errors.Is(err, journal.ErrNotFound) || workers.starts != 1 || spaces.prepares != 1 {
					t.Fatal("checkpoint restarted native work or failed to submit the original result", err)
				}
			} else if scenario == "confirmed" || scenario == "prior stop" {
				var result harness.WorkerResult
				if err != nil || !saved.StoppedConfirmed || saved.Phase != journal.PhaseWorkerDone || json.Unmarshal(saved.WorkerResult, &result) != nil || result.Verdict == nil || len(result.Verdict.Files) != 1 || result.Verdict.Files[0].SHA256 != evidenceDigest([]byte("original evidence bytes")) {
					t.Fatalf("confirmed original result was not recovered: %+v err=%v", saved, err)
				}
			} else if !isCompletionPending(err) || saved.StoppedConfirmed || saved.Phase != journal.PhaseStarted || len(saved.WorkerResult) != 0 {
				t.Fatalf("uncertainty became completed work: %+v err=%v", saved, err)
			}
			if scenario != "confirmed" && scenario != "prior stop" && scenario != "result checkpoint" && scenario != "upload unknown" && len(requestIDs) != beforeUploads {
				t.Fatal("missing original close/workspace scope retried an upload")
			}
			for _, id := range requestIDs {
				if id == "" || id != requestIDs[0] {
					t.Fatal("recovery changed the original upload request identity", requestIDs)
				}
			}
			if after, _ := os.ReadFile(nativeLog); !bytes.Equal(after, originalNative) {
				t.Fatal("receipt recovery executed another native method")
			}
			if scenario != "missing close" {
				if after, _ := os.ReadFile(closePath); !bytes.Equal(after, originalClose) {
					t.Fatal("recovery rewrote the historical owned-close receipt")
				}
			}
		})
	}
}
