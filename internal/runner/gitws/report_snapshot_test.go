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

func TestReportSnapshotAllowsUnchangedGitAndPreservesIndexAndBranch(t *testing.T) {
	f := newFixture(t, nil)
	as := f.implement()
	as.Workspace.ID = "ws_report"
	as.Spec = json.RawMessage(`{"deliverables":["report"]}`)
	p := f.prepare(as)
	dir := filepath.Join(p.Dir, "tend")
	indexBefore := run(t, f.env, dir, "write-tree")
	branchBefore := run(t, f.env, dir, "symbolic-ref", "HEAD")
	artifact, err := f.ws.Snapshot(context.Background(), as)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifact.Mounts) != 1 || artifact.Mounts[0].Changed || artifact.Mounts[0].CommitSHA != f.mainSHA {
		t.Fatalf("unchanged investigation snapshot = %+v", artifact)
	}
	if got := run(t, f.env, dir, "write-tree"); got != indexBefore {
		t.Fatal("snapshot altered the staged index")
	}
	if got := run(t, f.env, dir, "symbolic-ref", "HEAD"); got != branchBefore {
		t.Fatal("snapshot changed the branch")
	}
	artifact.Report = &protocol.ResultReport{Body: "investigation complete"}
	body, _ := json.Marshal(artifact)
	as.Role = protocol.RoleAccept
	as.RunID = "run_accept_report"
	as.Artifact = body
	if _, err := f.ws.Prepare(context.Background(), as); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ws.Prepare(context.Background(), as); !errors.Is(err, ErrWorkspaceMoved) {
		t.Fatalf("missing review checkout = %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("acceptance recreated the missing checkout")
	}
	// Legacy report payloads still require the assigned physical mounts.
	as.Artifact = json.RawMessage(`{"report":{"body":"legacy report"}}`)
	if _, err := f.ws.Prepare(context.Background(), as); !errors.Is(err, ErrWorkspaceMoved) {
		t.Fatalf("empty artifact mounts bypassed physical check: %v", err)
	}
}

func TestReportSnapshotOfDirtyGitPreservesStaging(t *testing.T) {
	f := newFixture(t, nil)
	as := f.implement()
	p := f.prepare(as)
	dir := filepath.Join(p.Dir, "tend")
	write(t, filepath.Join(dir, "README"), "staged evidence\n")
	run(t, f.env, dir, "add", "README")
	write(t, filepath.Join(dir, "README"), "unstaged evidence\n")
	write(t, filepath.Join(dir, "new.txt"), "untracked evidence\n")
	indexBefore := run(t, f.env, dir, "write-tree")
	statusBefore := run(t, f.env, dir, "status", "--porcelain=v1")
	artifact, err := f.ws.Snapshot(context.Background(), as)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifact.Mounts) != 1 || artifact.Mounts[0].SnapshotSHA == "" {
		t.Fatalf("missing dirty snapshot: %+v", artifact)
	}
	if got := run(t, f.env, dir, "write-tree"); got != indexBefore {
		t.Fatal("report snapshot modified staging")
	}
	if got := run(t, f.env, dir, "status", "--porcelain=v1"); got != statusBefore {
		t.Fatal("report snapshot modified files")
	}
}
