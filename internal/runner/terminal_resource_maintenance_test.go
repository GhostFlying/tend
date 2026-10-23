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
			shouldReady := kind == "failed legacy UUID" || kind == "explicit appserver" || kind == "successful native proof"
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

func TestTerminalQuarantineChecksEveryUnconfirmedIdentity(t *testing.T) {
	for _, kind := range []string{"another static UUID", "already stopped provider", "unknown identity", "provider identity conflict"} {
		t.Run(kind, func(t *testing.T) {
			r := controlRunner(t)
			ctx := t.Context()
			for _, id := range []string{"first", "second"} {
				as := protocol.Assignment{RunID: id, TaskID: "task"}
				origin := protocol.WorkerKind("")
				thread := "00000000-0000-7000-8000-000000000001"
				if id == "second" {
					switch kind {
					case "already stopped provider":
						origin = protocol.WorkerOrca
						thread = `{"terminal":"closed"}`
					case "unknown identity":
						thread = "unknown-native-identity"
					case "provider identity conflict":
						as.Workspace.ProviderID = "external-provider"
					}
				}
				if err := r.cfg.Journal.AcceptAssignmentFromWorker(ctx, as, "", origin); err != nil {
					t.Fatal(err)
				}
				if _, err := r.cfg.Journal.NextAttempt(ctx, id); err != nil {
					t.Fatal(err)
				}
				if err := r.cfg.Journal.SetThread(ctx, id, thread); err != nil {
					t.Fatal(err)
				}
				if err := r.cfg.Journal.Done(ctx, id); err != nil {
					t.Fatal(err)
				}
				if id == "second" && kind == "already stopped provider" {
					if err := r.cfg.Journal.ConfirmStopped(ctx, id); err != nil {
						t.Fatal(err)
					}
				}
			}
			got, err := r.terminalResourceQuarantine(ctx, protocol.WorkspaceReconcile{WorkspaceID: "workspace", TaskID: "task", TaskTerminal: true, RunIDs: []string{"first", "second"}})
			want := kind == "another static UUID" || kind == "already stopped provider"
			if err != nil || got != want {
				t.Fatalf("quarantine=%v want=%v err=%v", got, want, err)
			}
			first, err := r.cfg.Journal.HistoricalRun(ctx, "first")
			if err != nil || first.StoppedConfirmed {
				t.Fatalf("classifier wrote stop proof: %+v %v", first, err)
			}
		})
	}
}

func TestNeverStartedRunDoesNotBlockLegacyQuarantine(t *testing.T) {
	for _, legacyFirst := range []bool{true, false} {
		name := "unstarted-first"
		if legacyFirst {
			name = "legacy-first"
		}
		t.Run(name, func(t *testing.T) {
			f := newWorkspaceMaintenanceFixture(t)
			ctx := t.Context()
			ids := []string{"unstarted", "legacy"}
			if legacyFirst {
				ids = []string{"legacy", "unstarted"}
			}
			for _, id := range ids {
				as := protocol.Assignment{RunID: id, TaskID: "task", Workspace: protocol.AssignmentWorkspace{ID: "workspace"}}
				if err := f.j.AcceptAssignment(ctx, as, ""); err != nil {
					t.Fatal(err)
				}
				if id == "legacy" {
					if _, err := f.j.NextAttempt(ctx, id); err != nil {
						t.Fatal(err)
					}
					if err := f.j.SetThread(ctx, id, "00000000-0000-7000-8000-000000000001"); err != nil {
						t.Fatal(err)
					}
				}
				if err := f.j.Done(ctx, id); err != nil {
					t.Fatal(err)
				}
			}
			resource := protocol.WorkspaceReconcile{WorkspaceID: "workspace", TaskID: "task", TaskTerminal: true, RunIDs: ids}
			// Classification itself is read-only, including the never-started receipt.
			r := f.r
			eligible, err := r.terminalResourceQuarantine(ctx, resource)
			if err != nil || !eligible {
				t.Fatalf("never-started receipt blocked quarantine: %v %v", eligible, err)
			}
			untouched, err := f.j.HistoricalRun(ctx, "unstarted")
			if err != nil || untouched.StoppedConfirmed {
				t.Fatalf("classifier wrote stop proof: %+v %v", untouched, err)
			}
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				var env protocol.Envelope
				_ = json.NewDecoder(q.Body).Decode(&env)
				if env.Operation != protocol.OpWorkspaceReconcile {
					t.Error("unexpected resource mutation", env.Operation)
					w.WriteHeader(500)
					return
				}
				result, _ := json.Marshal(protocol.WorkspaceReconcileResult{Workspaces: []protocol.WorkspaceReconcile{resource}})
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: result}})
			}))
			defer remote.Close()
			cfg := r.cfg
			cfg.ServerURL = remote.URL
			cfg.HTTPClient = remote.Client()
			r = New(cfg)
			if err := r.applyControl(ctx, &protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerMaintenance}, cfg.Now(), true); err != nil {
				t.Fatal(err)
			}
			if err := r.maintainWorkspaces(ctx); err != nil {
				t.Fatal(err)
			}
			view, err := r.runtimeObservation(ctx)
			if err != nil || view.Mode != protocol.RunnerReady {
				t.Fatalf("not Ready: %+v %v", view, err)
			}
			legacy, err := f.j.HistoricalRun(ctx, "legacy")
			if err != nil || legacy.StoppedConfirmed {
				t.Fatalf("missing legacy proof fabricated: %+v %v", legacy, err)
			}
			// The existing normal reconcile path can prove never-started when it visits
			// that run before the legacy failure; the fallback adds no proof itself.
			unstarted, err := f.j.HistoricalRun(ctx, "unstarted")
			if err != nil || unstarted.StoppedConfirmed == legacyFirst {
				t.Fatalf("unexpected existing reconcile behavior: %+v %v", unstarted, err)
			}
			releases, err := f.j.WorkspaceReleases(ctx)
			if err != nil || len(releases) != 0 {
				t.Fatalf("release invented: %+v %v", releases, err)
			}
		})
	}
}
