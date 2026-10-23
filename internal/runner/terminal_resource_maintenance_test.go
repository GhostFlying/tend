package runner

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

func TestTerminalResourceStopUncertaintyDoesNotOwnRunnerMaintenance(t *testing.T) {
	for _, kind := range []string{"failed legacy UUID", "explicit appserver", "explicit external provider", "external stop unconfirmed", "successful native proof", "live task", "missing journal", "task mismatch", "workspace mismatch", "held", "fenced", "unfinished", "active executor", "pending final", "pending release", "journal unavailable"} {
		t.Run(kind, func(t *testing.T) {
			f := newWorkspaceMaintenanceFixture(t)
			ctx := t.Context()
			as := protocol.Assignment{RunID: "run", TaskID: "task"}
			resource := protocol.WorkspaceReconcile{WorkspaceID: "workspace", TaskID: "task", TaskTerminal: true, RunIDs: []string{"run"}}
			origin := protocol.WorkerKind("")
			switch kind {
			case "explicit appserver":
				origin = protocol.WorkerAppServer
				as.Protocol = 1
				as.Workspace.ID = resource.WorkspaceID
			case "explicit external provider":
				origin = protocol.WorkerAppServer
				as.Workspace.ID = resource.WorkspaceID
				as.Workspace.ProviderID = "retained-native-provider"
			case "external stop unconfirmed":
				origin = protocol.WorkerOrca
				as.Protocol = 1
				as.Workspace.ID = resource.WorkspaceID
			case "successful native proof":
				resource.FinishedRunIDs = []string{as.RunID}
			case "live task":
				resource.TaskTerminal = false
			case "missing journal":
				resource.RunIDs = append(resource.RunIDs, "missing")
			case "task mismatch":
				as.TaskID = "other"
			case "workspace mismatch":
				as.Workspace.ID = "other"
			}
			if err := f.j.AcceptAssignmentFromWorker(ctx, as, "", origin); err != nil {
				t.Fatal(err)
			}
			if _, err := f.j.NextAttempt(ctx, as.RunID); err != nil {
				t.Fatal(err)
			}
			thread := "00000000-0000-7000-8000-000000000001"
			if kind == "external stop unconfirmed" {
				thread = `{"terminal":"retained"}`
			}
			if err := f.j.SetThread(ctx, as.RunID, thread); err != nil {
				t.Fatal(err)
			}
			if err := f.j.Done(ctx, as.RunID); err != nil {
				t.Fatal(err)
			}
			var setupErr error
			switch kind {
			case "held":
				setupErr = f.j.SetHeld(ctx, as.RunID, true)
			case "fenced":
				setupErr = f.j.SetFenced(ctx, as.RunID, true)
			case "unfinished":
				setupErr = f.j.SetPhase(ctx, as.RunID, journal.PhaseStarted)
			case "pending final":
				setupErr = f.j.PutRequest(ctx, journal.Request{Envelope: protocol.Envelope{RequestID: "final", Operation: protocol.OpRunsSubmitArtifact}, RunID: as.RunID})
			case "pending release":
				setupErr = f.j.QueueWorkspaceRelease(ctx, protocol.WorkspaceRelease{WorkspaceID: resource.WorkspaceID, TaskID: resource.TaskID})
			}
			if setupErr != nil {
				t.Fatal(setupErr)
			}
			var releases atomic.Int64
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				var env protocol.Envelope
				if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				var result any
				switch env.Operation {
				case protocol.OpWorkspaceReconcile:
					result = protocol.WorkspaceReconcileResult{Workspaces: []protocol.WorkspaceReconcile{resource}}
				case protocol.OpWorkspaceRelease:
					releases.Add(1)
					result = protocol.WorkspaceReleaseResult{Released: true}
				default:
					t.Errorf("unexpected resource mutation: %s", env.Operation)
					w.WriteHeader(500)
					return
				}
				raw, _ := json.Marshal(result)
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: raw}})
			}))
			defer remote.Close()
			cfg := f.r.cfg
			cfg.ServerURL = remote.URL
			cfg.HTTPClient = remote.Client()
			cfg.Workers = &historicalWorkers{fail: as.RunID}
			cfg.Worker = protocol.WorkerOrca
			r := New(cfg)
			if err := r.applyControl(ctx, &protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerMaintenance}, cfg.Now(), true); err != nil {
				t.Fatal(err)
			}
			if kind == "active executor" {
				r.active[as.RunID] = &active{as: as}
			}
			if kind == "journal unavailable" {
				_ = f.j.Close()
			}
			err := r.maintainWorkspaces(ctx)
			if kind == "journal unavailable" {
				if err == nil {
					t.Fatal("journal IO failure treated as resource warning")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			view, err := r.runtimeObservation(ctx)
			if err != nil {
				t.Fatal(err)
			}
			shouldReady := kind == "failed legacy UUID" || kind == "explicit appserver" || kind == "explicit external provider" || kind == "external stop unconfirmed" || kind == "successful native proof"
			if (view.Mode == protocol.RunnerReady) != shouldReady {
				t.Fatalf("mode=%s wantReady=%v blockers=%v", view.Mode, shouldReady, view.Blockers)
			}
			run, err := f.j.HistoricalRun(ctx, as.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "successful native proof" {
				if !run.StoppedConfirmed || releases.Load() != 1 {
					t.Fatal("provable resource was quarantined instead of released")
				}
			} else {
				if run.StoppedConfirmed || releases.Load() != 0 {
					t.Fatalf("uncertain resource mutated: stopped=%v releases=%d", run.StoppedConfirmed, releases.Load())
				}
				queued, err := f.j.WorkspaceReleases(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if kind != "pending release" && len(queued) != 0 {
					t.Fatal("quarantine invented release intent", queued)
				}
			}
		})
	}
}
