package runner

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

func TestLegacyResourceMaintenanceCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name    string
		change  func(*Runner, *protocol.Assignment, *protocol.WorkspaceReconcile)
		warning bool
	}{
		{name: "terminal legacy receipt", warning: true},
		{name: "terminal fact absent", change: func(_ *Runner, _ *protocol.Assignment, p *protocol.WorkspaceReconcile) { p.TaskTerminal = false }},
		{name: "missing historical run", change: func(_ *Runner, _ *protocol.Assignment, p *protocol.WorkspaceReconcile) {
			p.RunIDs = append(p.RunIDs, "missing")
		}},
		{name: "recorded orca origin", change: func(_ *Runner, a *protocol.Assignment, _ *protocol.WorkspaceReconcile) {
			a.Workspace.ProviderID = "folder:known"
		}},
		{name: "new resource receipt", change: func(_ *Runner, a *protocol.Assignment, _ *protocol.WorkspaceReconcile) { a.Workspace.ID = "workspace" }},
		{name: "nonlegacy protocol", change: func(_ *Runner, a *protocol.Assignment, _ *protocol.WorkspaceReconcile) { a.Protocol = 1 }},
		{name: "task mismatch", change: func(_ *Runner, a *protocol.Assignment, _ *protocol.WorkspaceReconcile) { a.TaskID = "other" }},
		{name: "held"}, {name: "fenced"}, {name: "unfinished"}, {name: "thread"}, {name: "origin"}, {name: "pending operation"}, {name: "pending release"}, {name: "active executor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := controlRunner(t)
			ctx := t.Context()
			a := protocol.Assignment{RunID: "legacy", TaskID: "task"}
			p := protocol.WorkspaceReconcile{WorkspaceID: "workspace", TaskID: "task", TaskTerminal: true, RunIDs: []string{"legacy"}}
			if tc.change != nil {
				tc.change(r, &a, &p)
			}
			if err := r.cfg.Journal.AcceptAssignment(ctx, a, ""); err != nil {
				t.Fatal(err)
			}
			if _, err := r.cfg.Journal.NextAttempt(ctx, a.RunID); err != nil {
				t.Fatal(err)
			}
			if err := r.cfg.Journal.Done(ctx, a.RunID); err != nil {
				t.Fatal(err)
			}
			var err error
			switch tc.name {
			case "held":
				err = r.cfg.Journal.SetHeld(ctx, a.RunID, true)
			case "fenced":
				err = r.cfg.Journal.SetFenced(ctx, a.RunID, true)
			case "unfinished":
				err = r.cfg.Journal.SetPhase(ctx, a.RunID, journal.PhaseStarted)
			case "thread":
				err = r.cfg.Journal.SetThread(ctx, a.RunID, `{"terminal":"known"}`)
			case "origin":
				a.RunID = "current"
				err = r.cfg.Journal.AcceptAssignmentFromWorker(ctx, a, "", protocol.WorkerOrca)
				if err == nil {
					err = r.cfg.Journal.Done(ctx, a.RunID)
				}
				p.RunIDs = []string{a.RunID}
			case "pending operation":
				err = r.cfg.Journal.PutRequest(ctx, journal.Request{Envelope: protocol.Envelope{RequestID: "pending", Operation: protocol.OpRunnerClaim}})
			case "pending release":
				err = r.cfg.Journal.QueueWorkspaceRelease(ctx, protocol.WorkspaceRelease{WorkspaceID: p.WorkspaceID, TaskID: p.TaskID})
			case "active executor":
				r.active[a.RunID] = &active{as: a}
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := r.legacyResourceWarning(ctx, p)
			if err != nil || got != tc.warning {
				t.Fatalf("warning=%v want=%v err=%v", got, tc.warning, err)
			}
			run, err := r.cfg.Journal.HistoricalRun(ctx, "legacy")
			if err != nil {
				t.Fatal(err)
			}
			if run.StoppedConfirmed {
				t.Fatal("compatibility fabricated process-stop proof")
			}
		})
	}
}

func TestLegacyQuarantineAllowsDrainedMaintenanceWithoutRelease(t *testing.T) {
	f := newWorkspaceMaintenanceFixture(t)
	ctx := t.Context()
	a := protocol.Assignment{RunID: "legacy", TaskID: "task"}
	if err := f.j.AcceptAssignment(ctx, a, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.j.NextAttempt(ctx, a.RunID); err != nil {
		t.Fatal(err)
	}
	if err := f.j.Done(ctx, a.RunID); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		if env.Operation != protocol.OpWorkspaceReconcile {
			t.Errorf("resource mutated: %s", env.Operation)
			w.WriteHeader(500)
			return
		}
		result, _ := json.Marshal(protocol.WorkspaceReconcileResult{Workspaces: []protocol.WorkspaceReconcile{{WorkspaceID: "workspace", TaskID: "task", TaskTerminal: true, RunIDs: []string{a.RunID}}}})
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: result}})
	}))
	defer server.Close()
	cfg := f.r.cfg
	cfg.ServerURL = server.URL
	cfg.HTTPClient = server.Client()
	r := New(cfg)
	if err := r.applyControl(ctx, &protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerMaintenance}, cfg.Now(), true); err != nil {
		t.Fatal(err)
	}
	state, err := r.reconcileWorkspaceProcesses(ctx)
	if err != nil || !state.blocked["workspace"] || state.maintenanceBlocked {
		t.Fatalf("quarantine lost or promoted: %+v %v", state, err)
	}
	if err := r.maintainWorkspaces(ctx); err != nil {
		t.Fatal(err)
	}
	view, err := r.runtimeObservation(ctx)
	if err != nil || view.Mode != protocol.RunnerReady {
		t.Fatalf("maintenance not ready: %+v %v", view, err)
	}
	run, err := f.j.HistoricalRun(ctx, a.RunID)
	if err != nil || run.StoppedConfirmed {
		t.Fatalf("stop proof changed: %+v %v", run, err)
	}
	releases, err := f.j.WorkspaceReleases(ctx)
	if err != nil || len(releases) != 0 {
		t.Fatalf("release queued: %+v %v", releases, err)
	}
}
