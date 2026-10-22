package gitws

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner"
)

func TestLegacyReworkBaselineRetainsOriginalTaskReviewBase(t *testing.T) {
	f := newFixture(t, nil)
	as := f.implement()
	p := f.prepare(as) // Legacy assignment did not have a workspace resource ID.
	dir := filepath.Join(p.Dir, "tend")
	write(t, filepath.Join(dir, "first.txt"), "first implementation\n")
	run(t, f.env, dir, "add", "first.txt")
	run(t, f.env, dir, "commit", "-qm", "first implementation")
	firstHead := run(t, f.env, dir, "rev-parse", "HEAD")
	if _, err := f.ws.Collect(t.Context(), as); err != nil {
		t.Fatal(err)
	}
	// Work left since the last collected generation must remain incremental work.
	write(t, filepath.Join(dir, "rework.txt"), "uncollected same-task edits\n")
	as.Workspace.ID = "ws_upgraded"
	as.RunID = "run_rework"
	as.Spec = json.RawMessage(`{"deliverables":["commit"]}`)
	check := func(pGuide string, bases []harness.MountBaseline) {
		t.Helper()
		if len(bases) != 1 || bases[0].StartHEAD != f.mainSHA {
			t.Fatalf("original task base lost: %+v", bases)
		}
		if !strings.Contains(pGuide, "base commit `"+f.mainSHA+"`") {
			t.Fatalf("guidance excludes initial implementation: %s", pGuide)
		}
		_, brief := harness.Brief(harness.WorkerSpec{Role: as.Role, Spec: as.Spec, Baseline: bases})
		if !strings.Contains(brief, "task starts at "+f.mainSHA) {
			t.Fatalf("brief excludes initial implementation: %s", brief)
		}
	}
	q := f.prepare(as)
	st, err := loadState(f.ws.home(as))
	if err != nil {
		t.Fatal(err)
	}
	check(repoGuide(as, st), q.Baseline)
	var baseline []baselineMount
	if err := json.Unmarshal(q.Report.Baseline, &baseline); err != nil {
		t.Fatal(err)
	}
	if len(baseline) != 1 || baseline[0].Head != firstHead || baseline[0].TaskBase != f.mainSHA {
		t.Fatalf("snapshot and task origin conflated: %+v", baseline)
	}
	agents, err := os.ReadFile(filepath.Join(q.Dir, "AGENTS.md"))
	if err != nil || !strings.Contains(string(agents), "`"+f.mainSHA+"` | `"+f.mainSHA+"`") {
		t.Fatalf("workspace guidance: %s %v", agents, err)
	}
	// A failed state save after baseline publication also reloads the task base.
	st.Mounts["tend"] = func() mountState { m := st.Mounts["tend"]; m.TaskBase = ""; m.BaselineTask = ""; return m }()
	if err := saveState(f.ws.home(as), st); err != nil {
		t.Fatal(err)
	}
	retry := f.prepare(as)
	st, err = loadState(f.ws.home(as))
	if err != nil {
		t.Fatal(err)
	}
	check(repoGuide(as, st), retry.Baseline)
	art, err := f.ws.Collect(t.Context(), as)
	if err != nil || len(art.Mounts) != 1 || !art.Mounts[0].Changed || art.Mounts[0].CommitSHA != firstHead || art.Mounts[0].SnapshotSHA == "" {
		t.Fatalf("lost uncollected changes: %+v %v", art, err)
	}
	as.Artifact, err = json.Marshal(art)
	if err != nil {
		t.Fatal(err)
	}
	as.Role = protocol.RoleAccept
	as.RunID = "run_accept_upgrade"
	accepted := f.prepare(as)
	st, err = loadState(f.ws.home(as))
	if err != nil {
		t.Fatal(err)
	}
	check(repoGuide(as, st), accepted.Baseline)
	// A subsequent implementation baseline retry does not reset Collect's state.
	as.Role = protocol.RoleImplement
	as.RunID = "run_rework_unchanged"
	f.prepare(as)
	if _, err := f.ws.Collect(t.Context(), as); !errors.Is(err, runner.ErrNoChanges) {
		t.Fatalf("baseline reset incremental collection: %v", err)
	}
}
