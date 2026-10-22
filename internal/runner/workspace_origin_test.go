package runner

import (
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/traex"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
	"path/filepath"
	"testing"
)

func TestHistoricalWorkspaceOriginSurvivesHarnessChanges(t *testing.T) {
	uuid := "00000000-0000-7000-8000-000000000001"
	orcaThread := `{"worktree":"worktree","terminal":"terminal","pane":"pane"}`
	for _, tc := range []struct {
		name                                     string
		origin, current                          protocol.WorkerKind
		thread                                   string
		finished, held, provider, stopped, allow bool
	}{
		{name: "legacy success appserver", current: protocol.WorkerAppServer, thread: uuid, finished: true, allow: true},
		{name: "legacy success switched to orca", current: protocol.WorkerOrca, thread: uuid, finished: true, allow: true},
		{name: "explicit appserver success switched to orca", origin: protocol.WorkerAppServer, current: protocol.WorkerOrca, thread: "th_native", finished: true, allow: true},
		{name: "failed legacy appserver", current: protocol.WorkerAppServer, thread: uuid},
		{name: "failed legacy appserver switched to orca", current: protocol.WorkerOrca, thread: uuid},
		{name: "failed explicit appserver", origin: protocol.WorkerAppServer, current: protocol.WorkerOrca, thread: "th_native"},
		{name: "legacy success held", current: protocol.WorkerAppServer, thread: uuid, finished: true, held: true},
		{name: "legacy success provider conflict", current: protocol.WorkerAppServer, thread: uuid, finished: true, provider: true},
		{name: "unknown plain legacy", current: protocol.WorkerAppServer, thread: "unknown-format", finished: true},
		{name: "damaged structured legacy", current: protocol.WorkerAppServer, thread: `{"worktree":`, finished: true},
		{name: "successful legacy orca switched to appserver", current: protocol.WorkerAppServer, thread: orcaThread, finished: true},
		{name: "failed legacy orca switched to appserver", current: protocol.WorkerAppServer, thread: orcaThread},
		{name: "explicit orca switched to appserver", origin: protocol.WorkerOrca, current: protocol.WorkerAppServer, thread: orcaThread, finished: true},
		{name: "explicit appserver structured thread conflicts", origin: protocol.WorkerAppServer, current: protocol.WorkerAppServer, thread: orcaThread, finished: true},
		{name: "failed with actual prior stop proof", origin: protocol.WorkerAppServer, current: protocol.WorkerOrca, thread: "th_native", stopped: true, allow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := testkit.NewClock()
			j, err := journal.Open(t.Context(), filepath.Join(t.TempDir(), "journal.db"), clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			as := protocol.Assignment{RunID: "run", TaskID: "task", Workspace: protocol.AssignmentWorkspace{ID: "workspace"}}
			if tc.provider {
				as.Workspace.ProviderID = "folder:external"
			}
			if err := j.AcceptAssignmentFromWorker(t.Context(), as, "", tc.origin); err != nil {
				t.Fatal(err)
			}
			if err := j.SetThread(t.Context(), as.RunID, tc.thread); err != nil {
				t.Fatal(err)
			}
			if err := j.SetHeld(t.Context(), as.RunID, tc.held); err != nil {
				t.Fatal(err)
			}
			if tc.stopped {
				if err := j.ConfirmStopped(t.Context(), as.RunID); err != nil {
					t.Fatal(err)
				}
			}
			if err := j.Done(t.Context(), as.RunID); err != nil {
				t.Fatal(err)
			}
			fake := &historicalWorkers{}
			var workers harness.Workers = fake
			if tc.current == protocol.WorkerAppServer {
				workers = traex.NewWorkers(traex.WorkerOptions{})
			}
			r := New(Config{Journal: j, Workers: workers, Worker: tc.current, Now: clock.Now})
			resource := protocol.WorkspaceReconcile{WorkspaceID: "workspace", TaskID: "task", RunIDs: []string{as.RunID}}
			if tc.finished {
				resource.FinishedRunIDs = []string{as.RunID}
			}
			err = r.reconcileWorkspace(t.Context(), resource)
			if (err == nil) != tc.allow {
				t.Fatalf("allow=%v error=%v", tc.allow, err)
			}
			saved, readErr := j.HistoricalRun(t.Context(), as.RunID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			releases, readErr := j.WorkspaceReleases(t.Context())
			if readErr != nil {
				t.Fatal(readErr)
			}
			if tc.allow {
				if !saved.StoppedConfirmed || len(releases) != 1 {
					t.Fatalf("confirmed origin did not release resource: %+v %v", saved, releases)
				}
			} else if saved.StoppedConfirmed || len(releases) != 0 {
				t.Fatalf("unproven historical process was released: %+v %v", saved, releases)
			}
			if len(fake.stopped) != 0 {
				t.Fatal("app-server session proof dispatched to current external harness")
			}
		})
	}
}

func TestHistoricalAppserverRequiresFinishedFactForEveryRun(t *testing.T) {
	clock := testkit.NewClock()
	j, err := journal.Open(t.Context(), filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	for _, id := range []string{"implementation", "acceptance"} {
		as := protocol.Assignment{RunID: id, TaskID: "task"}
		if err := j.AcceptAssignment(t.Context(), as, ""); err != nil {
			t.Fatal(err)
		}
		if err := j.SetThread(t.Context(), id, "00000000-0000-7000-8000-000000000001"); err != nil {
			t.Fatal(err)
		}
		if err := j.Done(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	r := New(Config{Journal: j, Workers: traex.NewWorkers(traex.WorkerOptions{}), Now: clock.Now})
	resource := protocol.WorkspaceReconcile{WorkspaceID: "workspace", TaskID: "task", RunIDs: []string{"implementation", "acceptance"}, FinishedRunIDs: []string{"implementation", "unrelated"}}
	if err := r.reconcileWorkspace(t.Context(), resource); err == nil {
		t.Fatal("unrelated finished fact substituted for missing acceptance closure")
	}
	releases, err := j.WorkspaceReleases(t.Context())
	if err != nil || len(releases) != 0 {
		t.Fatalf("partial process proof released resource: %v %v", releases, err)
	}
	resource.FinishedRunIDs = append(resource.FinishedRunIDs, "acceptance")
	if err := r.reconcileWorkspace(t.Context(), resource); err != nil {
		t.Fatal(err)
	}
}
