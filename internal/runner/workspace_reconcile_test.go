package runner

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

type historicalWorkers struct {
	stopped []string
	fail    string
}

func (*historicalWorkers) Start(context.Context, harness.WorkerSpec) (harness.Worker, error) {
	return nil, errors.New("not used")
}
func (w *historicalWorkers) ReleaseHistorical(_ context.Context, id, thread string, _ bool, completedAt int64) error {
	if thread == "" || completedAt <= 0 {
		return errors.New("missing historical identity")
	}
	if id == w.fail {
		return errors.New("manual activity")
	}
	w.stopped = append(w.stopped, id)
	return nil
}

func TestLegacyWorkspaceRequiresEveryHistoricalRunToStop(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewClock()
	j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	for _, id := range []string{"run_impl", "run_accept"} {
		as := protocol.Assignment{RunID: id, TaskID: "task_legacy"}
		if err := j.AcceptAssignmentFromWorker(ctx, as, "", protocol.WorkerOrca); err != nil {
			t.Fatal(err)
		}
		if err := j.SetThread(ctx, id, `{"worktree":"legacy","terminal":"terminal","pane":"pane"}`); err != nil {
			t.Fatal(err)
		}
		if err := j.Done(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	workers := &historicalWorkers{fail: "run_accept"}
	r := New(Config{Journal: j, Workers: workers, Worker: protocol.WorkerOrca, Now: clock.Now})
	resource := protocol.WorkspaceReconcile{WorkspaceID: "ws_legacy", TaskID: "task_legacy", RunIDs: []string{"run_impl", "run_accept"}}
	if err := r.reconcileWorkspace(ctx, resource); err == nil {
		t.Fatal("manual acceptance terminal did not block release")
	}
	releases, err := j.WorkspaceReleases(ctx)
	if err != nil || len(releases) != 0 {
		t.Fatalf("premature release: %+v %v", releases, err)
	}
	workers.fail = ""
	if err := r.reconcileWorkspace(ctx, resource); err != nil {
		t.Fatal(err)
	}
	releases, err = j.WorkspaceReleases(ctx)
	if err != nil || len(releases) != 1 {
		t.Fatalf("release missing: %+v %v", releases, err)
	}
	if len(workers.stopped) != 2 || workers.stopped[0] != "run_impl" || workers.stopped[1] != "run_accept" {
		t.Fatalf("per-run stop proof not retained: %v", workers.stopped)
	}
	missing := resource
	missing.RunIDs = append(missing.RunIDs, "missing")
	if err := r.reconcileWorkspace(ctx, missing); err == nil {
		t.Fatal("missing journal record counted as stopped")
	}
}
