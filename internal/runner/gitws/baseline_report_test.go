package gitws

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestLargeGitBaselineRetainedPrivatelyWithBoundedReport(t *testing.T) {
	f := newFixture(t, nil)
	as := f.implement()
	as.Workspace.ID = "ws_1"
	as.Spec = json.RawMessage(`{"deliverables":["report"]}`)
	p := f.prepare(as)
	write(t, filepath.Join(p.Dir, "tend", "README"), strings.Repeat("large inherited change\n", 80000))
	as.TaskID = "task_large"
	as.Workspace.Reused = true
	q := f.prepare(as)
	checkExternalBaseline(t, q.Report.Baseline, filepath.Join(f.ws.home(as), ".tend", "baselines", as.TaskID+".json"))
	var descriptor struct {
		Mounts []struct {
			Head string `json:"head"`
		} `json:"mounts"`
	}
	if err := json.Unmarshal(q.Report.Baseline, &descriptor); err != nil || len(descriptor.Mounts) != 1 || descriptor.Mounts[0].Head != f.mainSHA {
		t.Fatalf("essential HEAD absent: %+v %v", descriptor, err)
	}
}

func TestLargeDirectoryManifestRetainedPrivatelyWithBoundedReport(t *testing.T) {
	w := New(Config{Home: t.TempDir()})
	ctx := context.Background()
	as := protocol.Assignment{TaskID: "task_first", Role: protocol.RoleImplement, Workspace: protocol.AssignmentWorkspace{ID: "ws_dir", Kind: "directory"}}
	p, err := w.Prepare(ctx, as)
	if err != nil {
		t.Fatal(err)
	}
	for n := range 4200 {
		write(t, filepath.Join(p.Dir, fmt.Sprintf("evidence-%04d-%s", n, strings.Repeat("x", 48))), "evidence")
	}
	as.TaskID = "task_inventory"
	as.Workspace.Reused = true
	q, err := w.Prepare(ctx, as)
	if err != nil {
		t.Fatal(err)
	}
	checkExternalBaseline(t, q.Report.Baseline, filepath.Join(p.Dir, ".tend", "baselines", as.TaskID+".json"))
}

func checkExternalBaseline(t *testing.T, report json.RawMessage, path string) {
	t.Helper()
	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(full) <= maxWireBaseline || len(report) >= maxWireBaseline {
		t.Fatalf("full=%d report=%d", len(full), len(report))
	}
	var descriptor struct {
		External bool   `json:"manifest_external"`
		Bytes    int    `json:"bytes"`
		Path     string `json:"local_snapshot_path"`
		SHA256   string `json:"sha256"`
	}
	if err := json.Unmarshal(report, &descriptor); err != nil {
		t.Fatal(err)
	}
	if !descriptor.External || descriptor.Bytes != len(full) || descriptor.SHA256 == "" || filepath.IsAbs(descriptor.Path) {
		t.Fatalf("descriptor = %+v", descriptor)
	}
}
