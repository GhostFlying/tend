package gitws

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestDirectoryWorkspaceRetainedAcrossTasks(t *testing.T) {
	ctx := context.Background()
	w := New(Config{Home: t.TempDir()})
	as := protocol.Assignment{TaskID: "task_abcdefgh", RunID: "run_1", Role: protocol.RoleImplement, Spec: json.RawMessage(`{"deliverables":["report"]}`), Workspace: protocol.AssignmentWorkspace{ID: "ws_1", Kind: "directory", Name: "ordinary"}}
	p, err := w.Prepare(ctx, as)
	if err != nil {
		t.Fatal(err)
	}
	if p.Dir != filepath.Join(w.cfg.Home, "temporary-workspaces", "ws_1") {
		t.Fatalf("directory = %s", p.Dir)
	}
	write(t, filepath.Join(p.Dir, "evidence.txt"), "retain me")
	as.TaskID = "task_followup"
	as.RunID = "run_2"
	as.Workspace.Reused = true
	as.Workspace.Path = p.Dir
	q, err := w.Prepare(ctx, as)
	if err != nil {
		t.Fatal(err)
	}
	if q.Dir != p.Dir {
		t.Fatal("reuse changed physical directory")
	}
	b, err := os.ReadFile(filepath.Join(q.Dir, "evidence.txt"))
	if err != nil || string(b) != "retain me" {
		t.Fatalf("inherited file = %q, %v", b, err)
	}
	if err := w.Cleanup(ctx, protocol.WorkspaceCleanup{WorkspaceID: "ws_1", Kind: "directory", Path: p.Dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Prepare(ctx, as); err == nil {
		t.Fatal("missing reuse silently recreated")
	}
}

func TestDirectoryCleanupRejectsWrongIdentity(t *testing.T) {
	w := New(Config{Home: t.TempDir()})
	ctx := context.Background()
	as := protocol.Assignment{TaskID: "task_1", Role: protocol.RoleImplement, Workspace: protocol.AssignmentWorkspace{ID: "ws_1", Kind: "directory"}}
	p, err := w.Prepare(ctx, as)
	if err != nil {
		t.Fatal(err)
	}
	wrong := protocol.WorkspaceCleanup{WorkspaceID: "ws_1", Kind: "directory", Path: p.Dir, ProviderID: "folder:other"}
	if err := w.Cleanup(ctx, wrong); err == nil {
		t.Fatal("wrong provider identity allowed")
	}
	if _, err := os.Stat(p.Dir); err != nil {
		t.Fatal("cleanup removed directory on identity mismatch")
	}
}

func TestReuseBaselinePreservesDirtyGitWorktree(t *testing.T) {
	f := newFixture(t, nil)
	as := f.implement()
	as.Workspace.ID = "ws_original"
	as.Spec = json.RawMessage(`{"deliverables":["report"]}`)
	p := f.prepare(as)
	mount := filepath.Join(p.Dir, "tend")
	write(t, filepath.Join(mount, "README"), "inherited unstaged\n")
	write(t, filepath.Join(mount, "evidence.txt"), "inherited untracked\n")
	before := run(t, f.env, mount, "status", "--porcelain=v1")
	as.TaskID = "task_followup"
	as.RunID = "run_followup"
	as.Workspace.Reused = true
	q := f.prepare(as)
	if after := run(t, f.env, mount, "status", "--porcelain=v1"); after != before {
		t.Fatalf("reuse altered worktree: %q -> %q", before, after)
	}
	var baseline []baselineMount
	if err := json.Unmarshal(q.Report.Baseline, &baseline); err != nil {
		t.Fatal(err)
	}
	if len(baseline) != 1 || baseline[0].Status == "" || baseline[0].Head != f.mainSHA || baseline[0].Unstaged == "" {
		t.Fatalf("baseline = %+v", baseline)
	}
	as.TaskID = "task_commit"
	as.Spec = json.RawMessage(`{"deliverables":["commit"],"workspace":{"include_existing_changes":false}}`)
	if _, err := f.ws.Prepare(context.Background(), as); err == nil {
		t.Fatal("commit task implicitly inherited dirty changes")
	}
	as.Spec = json.RawMessage(`{"deliverables":["commit"],"workspace":{"include_existing_changes":true}}`)
	if _, err := f.ws.Prepare(context.Background(), as); err != nil {
		t.Fatal(err)
	}
	as.TaskID = "task_after_deletion"
	if err := os.RemoveAll(mount); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ws.Prepare(context.Background(), as); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing reused checkout = %v", err)
	}
}
