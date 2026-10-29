package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness/orca"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

// TestFault_UndeliveredLaunchFinalizesAfterRestart drives the real Orca worker
// through runner Start, durable failure intent, failed cleanup, and recovery.
func TestFault_UndeliveredLaunchFinalizesAfterRestart(t *testing.T) {
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
		t.Run(string(role), func(t *testing.T) {
			ctx := t.Context()
			clock := testkit.NewClock()
			path := filepath.Join(t.TempDir(), "journal.db")
			j, err := journal.Open(ctx, path, clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { j.Close() }()
			as := protocol.Assignment{RunID: "run_delivery", TaskID: "task_delivery", Role: role, Desired: protocol.DesiredRun, Spec: json.RawMessage(`{"deliverables":["report"]}`)}
			if err := j.AcceptAssignmentFromWorker(ctx, as, "", protocol.WorkerOrca); err != nil {
				t.Fatal(err)
			}
			if err := j.SetPhase(ctx, as.RunID, journal.PhaseStarted); err != nil {
				t.Fatal(err)
			}
			sockets, err := os.MkdirTemp("", "tld-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(sockets) })
			alive, failClose := true, true
			launches, sends, closes := 0, 0, 0
			client := &orca.Client{Exec: func(_ context.Context, _ string, args []string) ([]byte, error) {
				result := map[string]any{}
				switch args[0] + " " + args[1] {
				case "agent launch":
					launches++
					result = map[string]any{"worktreeId": "folder:owned", "outcome": map[string]any{"kind": "terminal", "handle": "owned-terminal", "paneKey": "tab:leaf"}, "prompt": map[string]any{"outcome": "not-delivered"}}
				case "terminal list":
					terms := []map[string]any{}
					if alive {
						terms = append(terms, map[string]any{"handle": "owned-terminal", "worktreeId": "folder:owned", "tabId": "tab", "leafId": "leaf", "connected": true})
					}
					result = map[string]any{"terminals": terms}
				case "worktree ps":
					result = map[string]any{"worktrees": []any{map[string]any{"worktreeId": "folder:owned", "agents": []any{}}}}
				case "terminal close":
					closes++
					if failClose {
						return nil, errors.New("temporary provider close failure")
					}
					alive = false
				case "terminal send":
					sends++
					t.Error("undelivered launch must not be resent")
				default:
					t.Fatalf("unexpected provider operation: %v", args)
				}
				b, err := json.Marshal(map[string]any{"ok": true, "result": result})
				return b, err
			}}
			opts := orca.WorkerOptions{Client: client, Agent: protocol.AgentTraex, IDs: testkit.NewIDGen(clock, 1), Now: clock.Now, SocketDir: sockets}
			var sent []protocol.Envelope
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				var env protocol.Envelope
				if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
					t.Error(err)
				}
				sent = append(sent, env)
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded}})
			}))
			defer server.Close()
			cfg := Config{Journal: j, Now: clock.Now, IDs: testkit.NewIDGen(clock, 2), Worker: protocol.WorkerOrca, Workers: orca.NewWorkers(opts), Workspaces: prelaunchSpaces{dir: t.TempDir(), state: t.TempDir()}, ServerURL: server.URL, HTTPClient: server.Client(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), RetryDelay: time.Millisecond}
			r := New(cfg)
			if err := r.work(ctx, ctx, as, &active{as: as, desired: protocol.DesiredRun}); err == nil || !strings.Contains(err.Error(), "was not delivered") {
				t.Fatalf("prompt failure not immediate: %v", err)
			}
			saved, err := j.Run(ctx, as.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(saved.Thread, `"launch_prompt_outcome":"not-delivered"`) || saved.StoppedConfirmed || saved.Attempts != 1 {
				t.Fatalf("negative receipt lost or fabricated stop: %+v", saved)
			}
			events, err := j.PendingEvents(ctx, as.RunID, 100)
			if err != nil {
				t.Fatal(err)
			}
			for _, ev := range events {
				if ev.Kind == "turn.started" {
					t.Fatal("a missing prompt was announced as a turn")
				}
			}
			if err := r.finish(ctx, as, journal.PhaseReleasing, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "worker_start_failed"}); err != nil {
				t.Fatal(err)
			}
			request, found, err := j.RequestForRun(ctx, as.RunID)
			if err != nil || !found {
				t.Fatalf("failure intent=%v %v", found, err)
			}
			if err := r.stopCompletedAgent(ctx, as, false); err == nil {
				t.Fatal("failed close fabricated stop proof")
			}
			saved, err = j.Run(ctx, as.RunID)
			if err != nil || saved.StoppedConfirmed || saved.Phase != journal.PhaseReleasing || !alive {
				t.Fatalf("failure state changed: %+v %v", saved, err)
			}
			beforeRestart := append([]protocol.Envelope(nil), sent...)
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			j, err = journal.Open(ctx, path, clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Journal = j
			cfg.Workers = orca.NewWorkers(opts)
			r = New(cfg)
			failClose = false
			if err := r.execute(ctx, ctx, as.RunID, &active{as: as, desired: protocol.DesiredRun}); err != nil {
				t.Fatal(err)
			}
			if alive || launches != 1 || sends != 0 || closes != 2 {
				t.Fatalf("recovery external calls: alive=%v launch=%d send=%d close=%d", alive, launches, sends, closes)
			}
			// Startup navigation facts may already have been flushed before
			// the failed launch returns. Recovery must not replay that upload.
			if len(beforeRestart) != 1 || beforeRestart[0].Operation != protocol.OpRunnerEvents || len(sent) != len(beforeRestart)+1 || !reflect.DeepEqual(sent[:len(beforeRestart)], beforeRestart) {
				t.Fatalf("recovery rewrote or replayed startup observations: before=%+v after=%+v", beforeRestart, sent)
			}
			var observed protocol.EventsPayload
			if err := json.Unmarshal(beforeRestart[0].Payload, &observed); err != nil || len(observed.Events) != 1 || observed.Events[0].Kind != protocol.EventNativeIdentity || observed.Events[0].Seq != 1 || beforeRestart[0].Target.ID != as.RunID {
				t.Fatalf("unexpected recorded startup observation: %+v (%v)", observed, err)
			}
			release := sent[len(beforeRestart)]
			if release.Operation != protocol.OpRunsRelease || release.RequestID != request.Envelope.RequestID {
				t.Fatalf("recovery changed failure request: %+v", release)
			}
			beforeHash, err := protocol.PayloadHash(&request.Envelope)
			if err != nil {
				t.Fatal(err)
			}
			afterHash, err := protocol.PayloadHash(&release)
			if err != nil || beforeHash != afterHash {
				t.Fatalf("recovery changed failure payload: %s != %s (%v)", beforeHash, afterHash, err)
			}
			historical, err := j.HistoricalRun(ctx, as.RunID)
			if err != nil || historical.Phase != journal.PhaseDone || !historical.StoppedConfirmed {
				t.Fatalf("not safely finalized: %+v %v", historical, err)
			}
		})
	}
}

func TestStartupUnknownCloseRetainsRunAndResourceAfterRestart(t *testing.T) {
	ctx := t.Context()
	clock := testkit.NewClock()
	path := filepath.Join(t.TempDir(), "journal.db")
	j, err := journal.Open(ctx, path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { j.Close() }()
	as := protocol.Assignment{RunID: "run_startup_unknown", TaskID: "task_startup_unknown", Role: protocol.RoleImplement, Desired: protocol.DesiredRun}
	if err := j.AcceptAssignmentFromWorker(ctx, as, "", protocol.WorkerOrca); err != nil {
		t.Fatal(err)
	}
	if err := j.SetPhase(ctx, as.RunID, journal.PhaseStarted); err != nil {
		t.Fatal(err)
	}
	thread := `{"worktree":"folder:owned","terminal":"owned-terminal","pane":"tab:leaf","launch_prompt_outcome":"not-delivered","startup":{"attempts":1,"phase":"closing","terminal":"owned-terminal","close_issued":true,"stopped":false}}`
	if err := j.SetThread(ctx, as.RunID, thread); err != nil {
		t.Fatal(err)
	}
	nativeCalls := 0
	cfg := Config{Journal: j, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), Worker: protocol.WorkerOrca, Workers: orca.NewWorkers(orca.WorkerOptions{Client: &orca.Client{Exec: func(context.Context, string, []string) ([]byte, error) {
		nativeCalls++
		return nil, errors.New("must reconcile original close")
	}}, Now: clock.Now}), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	r := New(cfg)
	if err := r.finish(ctx, as, journal.PhaseReleasing, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "worker_start_failed"}); err != nil {
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
	if err := r.stopCompletedAgent(ctx, as, false); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("stop=%v", err)
	}
	saved, err := j.Run(ctx, as.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.StoppedConfirmed || saved.Phase != journal.PhaseReleasing || nativeCalls != 0 {
		t.Fatalf("run prematurely released: %+v nativeCalls=%d", saved, nativeCalls)
	}
	request, found, err := j.RequestForRun(ctx, as.RunID)
	if err != nil || !found || request.Envelope.Operation != protocol.OpRunsRelease {
		t.Fatalf("original final intent lost: %+v %v", request, err)
	}
}
