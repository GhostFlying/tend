package gitws

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner"
)

func TestFault_PreparationDiagnosisPreservesIndexAndFiles(t *testing.T) {
	f := newFixture(t, nil)
	as := f.implement()
	as.Workspace.ID = "ws_original"
	p := f.prepare(as)
	dir := filepath.Join(p.Dir, "tend")
	write(t, filepath.Join(dir, "README"), "retained unstaged\n")
	write(t, filepath.Join(dir, "staged file.txt"), "retained staged\n")
	run(t, f.env, dir, "add", "staged file.txt")
	write(t, filepath.Join(dir, "original.txt"), "retained untracked\n")
	before := run(t, f.env, dir, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	indexPath := run(t, f.env, dir, "rev-parse", "--path-format=absolute", "--git-path", "index")
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	as.TaskID, as.RunID, as.Workspace.Reused = "task_continue", "run_continue", true
	as.Spec = []byte(`{"deliverables":["commit"],"workspace":{"source_task_id":"task_original"}}`)
	_, err = f.ws.Prepare(t.Context(), as)
	diagnosis, ok := errors.AsType[*runner.PreparationError](err)
	if !ok {
		t.Fatalf("missing typed failure: %v", err)
	}
	d := diagnosis.Failure
	if d.Code != protocol.PreparationInheritedChanges || d.WorkerStarted || d.SourceTaskID != "task_original" || d.WorkspaceID != as.Workspace.ID || d.ChangeCount != 3 || d.Truncated {
		t.Fatal(d)
	}
	statuses := map[string]string{}
	for _, change := range d.Changes {
		if change.HEAD != f.mainSHA || change.Mount != "tend" {
			t.Fatal(change)
		}
		statuses[change.Path] = change.Status
	}
	if !reflect.DeepEqual(statuses, map[string]string{"README": " M", "staged file.txt": "A ", "original.txt": "??"}) {
		t.Fatal("porcelain status or filenames changed", statuses)
	}
	if after := run(t, f.env, dir, "status", "--porcelain=v1", "-z", "--untracked-files=all"); after != before {
		t.Fatal("diagnosis altered status")
	}
	indexAfter, err := os.ReadFile(indexPath)
	if err != nil || !reflect.DeepEqual(indexBefore, indexAfter) {
		t.Fatal("diagnosis altered real index", err)
	}
	for _, name := range []string{"README", "staged file.txt", "original.txt"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !strings.HasPrefix(string(b), "retained ") {
			t.Fatal("inherited content lost", name, err)
		}
	}
	as.Spec = []byte(`{"deliverables":["commit"],"workspace":{"source_task_id":"task_original","include_existing_changes":true}}`)
	if _, err := f.ws.Prepare(t.Context(), as); err != nil {
		t.Fatal("explicit scope correction failed", err)
	}
}

func TestPreparationDiagnosisBoundsAndSeparatesRenamedPaths(t *testing.T) {
	f := newFixture(t, nil)
	as := f.implement()
	as.Workspace.ID = "ws_original"
	p := f.prepare(as)
	dir := filepath.Join(p.Dir, "tend")
	run(t, f.env, dir, "mv", "README", "renamed file.txt")
	for i := 0; i < protocol.MaxPreparationChanges+3; i++ {
		write(t, filepath.Join(dir, fmt.Sprintf("untracked-%03d", i)), "retained")
	}
	as.TaskID, as.Workspace.Reused = "task_followup", true
	as.Spec = []byte(`{"deliverables":["commit"],"workspace":{"source_task_id":"task_original"}}`)
	_, err := f.ws.Prepare(t.Context(), as)
	d, ok := errors.AsType[*runner.PreparationError](err)
	if !ok || !d.Failure.Truncated || len(d.Failure.Changes) != protocol.MaxPreparationChanges || d.Failure.ChangeCount != protocol.MaxPreparationChanges+4 {
		t.Fatal("unbounded or incorrect rename accounting", d, err)
	}
	if first := d.Failure.Changes[0]; first.Path != "renamed file.txt" || first.Status != "R " {
		t.Fatal("rename source parsed as a separate change", first)
	}
}
