package gitws

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReuseRequiresEveryRecordedMountAndNeverRecreatesMissingState(t *testing.T) {
	for _, removed := range []bool{false, true} {
		t.Run(map[bool]string{false: "checkout remains", true: "checkout missing"}[removed], func(t *testing.T) {
			f := newFixture(t, nil)
			as := f.implement()
			as.Workspace.ID = "ws_partial"
			as.Spec = json.RawMessage(`{"deliverables":["report"]}`)
			p := f.prepare(as)
			home := f.ws.home(as)
			st, err := loadState(home)
			if err != nil {
				t.Fatal(err)
			}
			delete(st.Mounts, "tend")
			if err := saveState(home, st); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(statePath(home))
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(p.Dir, "tend")
			if removed {
				if err := os.RemoveAll(dir); err != nil {
					t.Fatal(err)
				}
				run(t, f.env, f.ws.repoPath("tend"), "worktree", "prune")
			}
			followup := as
			followup.TaskID, followup.RunID = "task_followup", "run_followup"
			followup.Workspace.Reused = true
			if _, err := f.ws.Prepare(context.Background(), followup); !errors.Is(err, ErrWorkspaceMoved) {
				t.Fatalf("missing mount identity = %v, want ErrWorkspaceMoved", err)
			}
			after, err := os.ReadFile(statePath(home))
			if err != nil || string(before) != string(after) {
				t.Fatalf("rejected reuse changed recorded identity: %v", err)
			}
			if removed {
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatal("rejected reuse recreated its missing checkout")
				}
			}
			// The original creator can still recover an interrupted initial layout.
			if _, err := f.ws.Prepare(context.Background(), as); err != nil {
				t.Fatalf("original task cannot finish its partial layout: %v", err)
			}
		})
	}
}
