package runner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/GhostFlying/tend/internal/harness/orca"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestFault_TerminalProviderMutationStillBlocksMaintenance(t *testing.T) {
	for _, pending := range []string{"launch", "send"} {
		for _, identity := range []string{"complete", "incomplete", "different configured harness", "inferred legacy orca"} {
			t.Run(pending+"/"+identity, func(t *testing.T) {
				f := newWorkspaceMaintenanceFixture(t)
				ctx := t.Context()
				as := protocol.Assignment{RunID: "run", TaskID: "task", Workspace: protocol.AssignmentWorkspace{ID: "workspace"}}
				origin := protocol.WorkerOrca
				if identity == "inferred legacy orca" {
					origin = ""
				}
				if err := f.j.AcceptAssignmentFromWorker(ctx, as, "", origin); err != nil {
					t.Fatal(err)
				}
				if _, err := f.j.NextAttempt(ctx, as.RunID); err != nil {
					t.Fatal(err)
				}
				native := map[string]string{"worktree": "workspace", pending: "unconfirmed-request", "marker": "launch-marker"}
				if identity != "incomplete" {
					native["terminal"] = "terminal"
					native["pane"] = "pane"
				}
				raw, _ := json.Marshal(native)
				thread := string(raw)
				if err := f.j.SetThread(ctx, as.RunID, thread); err != nil {
					t.Fatal(err)
				}
				if err := f.j.Done(ctx, as.RunID); err != nil {
					t.Fatal(err)
				}
				remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
					var env protocol.Envelope
					if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					if env.Operation != protocol.OpWorkspaceReconcile {
						t.Error("unexpected resource mutation", env.Operation)
						w.WriteHeader(500)
						return
					}
					result, _ := json.Marshal(protocol.WorkspaceReconcileResult{Workspaces: []protocol.WorkspaceReconcile{{WorkspaceID: "workspace", TaskID: as.TaskID, TaskTerminal: true, RunIDs: []string{as.RunID}}}})
					_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: result}})
				}))
				defer remote.Close()
				var providerCalls atomic.Int64
				client := &orca.Client{Exec: func(context.Context, string, []string) ([]byte, error) {
					providerCalls.Add(1)
					return nil, errors.New("test must not replay or mutate an unresolved provider operation")
				}}
				cfg := f.r.cfg
				cfg.ServerURL = remote.URL
				cfg.HTTPClient = remote.Client()
				cfg.Worker = protocol.WorkerOrca
				cfg.Workers = orca.NewWorkers(orca.WorkerOptions{Client: client, Now: cfg.Now, Log: cfg.Log})
				if identity == "different configured harness" {
					cfg.Worker = protocol.WorkerAppServer
				}
				r := New(cfg)
				if err := r.applyControl(ctx, &protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerMaintenance}, cfg.Now(), true); err != nil {
					t.Fatal(err)
				}
				if err := r.maintainWorkspaces(ctx); err != nil {
					t.Fatal(err)
				}
				view, err := r.runtimeObservation(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if view.Mode == protocol.RunnerReady {
					t.Fatalf("ready despite pending %s: %+v", pending, view)
				}
				run, err := f.j.HistoricalRun(ctx, as.RunID)
				if err != nil {
					t.Fatal(err)
				}
				if run.StoppedConfirmed || run.Thread != thread || providerCalls.Load() != 0 {
					t.Fatalf("unconfirmed provider state changed: stopped=%v calls=%d thread=%s", run.StoppedConfirmed, providerCalls.Load(), run.Thread)
				}
				releases, err := f.j.WorkspaceReleases(ctx)
				if err != nil || len(releases) != 0 {
					t.Fatalf("release intent invented: %+v %v", releases, err)
				}
			})
		}
	}
}

func TestFault_MixedLegacyFailureCannotHideProviderMutation(t *testing.T) {
	for _, pending := range []string{"launch", "send"} {
		for _, legacyFirst := range []bool{true, false} {
			order := "provider-first"
			if legacyFirst {
				order = "legacy-first"
			}
			t.Run(pending+"/"+order, func(t *testing.T) {
				f := newWorkspaceMaintenanceFixture(t)
				ctx := t.Context()
				legacyID, providerID := "b-legacy", "a-provider"
				if legacyFirst {
					legacyID, providerID = "a-legacy", "b-provider"
				}
				for _, id := range []string{legacyID, providerID} {
					as := protocol.Assignment{RunID: id, TaskID: "task", Workspace: protocol.AssignmentWorkspace{ID: "workspace"}}
					origin := protocol.WorkerKind("")
					thread := "00000000-0000-7000-8000-000000000001"
					if id == providerID {
						origin = protocol.WorkerOrca
						state := map[string]string{"worktree": "workspace", "marker": "launch-marker", pending: "unconfirmed-request", "terminal": "terminal", "pane": "pane"}
						body, _ := json.Marshal(state)
						thread = string(body)
					}
					if err := f.j.AcceptAssignmentFromWorker(ctx, as, "", origin); err != nil {
						t.Fatal(err)
					}
					if _, err := f.j.NextAttempt(ctx, id); err != nil {
						t.Fatal(err)
					}
					if err := f.j.SetThread(ctx, id, thread); err != nil {
						t.Fatal(err)
					}
					if err := f.j.Done(ctx, id); err != nil {
						t.Fatal(err)
					}
				}
				ids := []string{providerID, legacyID}
				if legacyFirst {
					ids = []string{legacyID, providerID}
				}
				remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
					var env protocol.Envelope
					_ = json.NewDecoder(q.Body).Decode(&env)
					if env.Operation != protocol.OpWorkspaceReconcile {
						t.Error("unexpected mutation", env.Operation)
						w.WriteHeader(500)
						return
					}
					result, _ := json.Marshal(protocol.WorkspaceReconcileResult{Workspaces: []protocol.WorkspaceReconcile{{WorkspaceID: "workspace", TaskID: "task", TaskTerminal: true, RunIDs: ids}}})
					_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: result}})
				}))
				defer remote.Close()
				var calls atomic.Int64
				cfg := f.r.cfg
				cfg.ServerURL = remote.URL
				cfg.HTTPClient = remote.Client()
				cfg.Worker = protocol.WorkerOrca
				cfg.Workers = orca.NewWorkers(orca.WorkerOptions{Client: &orca.Client{Exec: func(context.Context, string, []string) ([]byte, error) {
					calls.Add(1)
					return nil, errors.New("unexpected provider call")
				}}, Now: cfg.Now, Log: cfg.Log})
				r := New(cfg)
				if err := r.applyControl(ctx, &protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerMaintenance}, cfg.Now(), true); err != nil {
					t.Fatal(err)
				}
				if err := r.maintainWorkspaces(ctx); err != nil {
					t.Fatal(err)
				}
				view, err := r.runtimeObservation(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if view.Mode == protocol.RunnerReady {
					t.Fatalf("first legacy failure hid pending %s in later run", pending)
				}
				for _, id := range ids {
					run, err := f.j.HistoricalRun(ctx, id)
					if err != nil || run.StoppedConfirmed {
						t.Fatalf("proof mutated: %+v %v", run, err)
					}
				}
				if calls.Load() != 0 {
					t.Fatal("provider request was replayed")
				}
			})
		}
	}
}
