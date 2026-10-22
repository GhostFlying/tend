package gitws

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/orca"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestCachedBaselineRechecksCommitAuthorizationWithoutBlamingOwnEdits(t *testing.T) {
	for _, inherited := range []bool{true, false} {
		t.Run(map[bool]string{true: "inherited dirty files", false: "task own later edits"}[inherited], func(t *testing.T) {
			f := newFixture(t, nil)
			as := f.implement()
			as.Workspace.ID = "ws_cached"
			as.Spec = json.RawMessage(`{"deliverables":["report"]}`)
			p := f.prepare(as)
			dir := filepath.Join(p.Dir, "tend")
			if inherited {
				write(t, filepath.Join(dir, "README"), "inherited work\n")
			}
			as.TaskID = "task_followup"
			as.Workspace.Reused = true
			f.prepare(as)
			if !inherited {
				write(t, filepath.Join(dir, "README"), "this task own work\n")
			}
			as.Spec = json.RawMessage(`{"deliverables":["commit"],"workspace":{"include_existing_changes":false}}`)
			_, err := f.ws.Prepare(context.Background(), as)
			if inherited {
				if err == nil || !strings.Contains(err.Error(), "include_existing_changes") {
					t.Fatalf("cached inherited baseline bypassed authorization: %v", err)
				}
			} else if err != nil {
				t.Fatalf("own later edits were treated as inherited: %v", err)
			}
			as.Spec = json.RawMessage(`{"deliverables":["commit"],"workspace":{"include_existing_changes":true}}`)
			if _, err := f.ws.Prepare(context.Background(), as); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUncertainGroupCreationIsNotRepeatedAcrossTasksOrRestart(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	parent := filepath.Join(home, "temporary-workspaces")
	creates := 0
	visible := false
	c := &orca.Client{Exec: func(_ context.Context, _ string, args []string) ([]byte, error) {
		if args[0] == "project-group" && args[1] == "create" {
			creates++
			return []byte(`{"ok":false,"error":{"code":"runtime_timeout","message":"outcome unknown"}}`), nil
		}
		groups := []orca.ProjectGroup{}
		if visible {
			groups = append(groups, orca.ProjectGroup{ID: "group-created", ParentPath: parent})
		}
		return json.Marshal(map[string]any{"ok": true, "result": map[string]any{"groups": groups}})
	}}
	w := New(Config{Home: home})
	if _, err := w.temporaryGroup(ctx, c, parent); err == nil {
		t.Fatal("expected lost reply")
	}
	w = New(Config{Home: home})
	if _, err := w.temporaryGroup(ctx, c, parent); err == nil || !strings.Contains(err.Error(), "outcome unknown") {
		t.Fatalf("pending creation was not retained: %v", err)
	}
	if creates != 1 {
		t.Fatalf("created %d groups before original mutation became visible", creates)
	}
	visible = true
	id, err := w.temporaryGroup(ctx, c, parent)
	if err != nil || id != "group-created" || creates != 1 {
		t.Fatalf("reconciled id=%s creates=%d err=%v", id, creates, err)
	}
	w = New(Config{Home: home})
	id, err = w.temporaryGroup(ctx, c, parent)
	if err != nil || id != "group-created" || creates != 1 {
		t.Fatal("stable group identity was not retained")
	}
}

func TestFollowupGuidanceUsesTaskStartingCommitForImplementerAndReviewer(t *testing.T) {
	f := newFixture(t, nil)
	as := f.implement()
	as.Workspace.ID = "ws_history"
	as.Spec = json.RawMessage(`{"deliverables":["report"]}`)
	p := f.prepare(as)
	dir := filepath.Join(p.Dir, "tend")
	write(t, filepath.Join(dir, "README"), "source task commit\n")
	run(t, f.env, dir, "add", "README")
	run(t, f.env, dir, "commit", "-qm", "source change")
	sourceHEAD := run(t, f.env, dir, "rev-parse", "HEAD")
	as.TaskID = "task_followup"
	as.RunID = "run_followup"
	as.Workspace.Reused = true
	as.Spec = json.RawMessage(`{"deliverables":["commit"]}`)
	check := func(pGuide string, bases []harness.MountBaseline) {
		t.Helper()
		if len(bases) != 1 || bases[0].StartHEAD != sourceHEAD || bases[0].ResourceBase != f.mainSHA {
			t.Fatalf("task vs resource baseline = %+v", bases)
		}
		if !strings.Contains(pGuide, "base commit `"+sourceHEAD+"`") {
			t.Fatalf("repository guidance uses wrong starting HEAD: %s", pGuide)
		}
		_, input := harness.Brief(harness.WorkerSpec{Role: as.Role, Baseline: bases, Spec: as.Spec})
		if !strings.Contains(input, "task starts at "+sourceHEAD) || !strings.Contains(input, "resource original base "+f.mainSHA) {
			t.Fatalf("worker/reviewer brief lacks separate baselines: %s", input)
		}
	}
	follow := f.prepare(as)
	st, err := loadState(f.ws.home(as))
	if err != nil {
		t.Fatal(err)
	}
	check(repoGuide(as, st), follow.Baseline)
	agents, err := os.ReadFile(filepath.Join(follow.Dir, "AGENTS.md"))
	if err != nil || !strings.Contains(string(agents), "`"+sourceHEAD+"` | `"+f.mainSHA+"`") {
		t.Fatalf("generated workspace guidance = %s %v", agents, err)
	}
	write(t, filepath.Join(dir, "followup.txt"), "one new commit\n")
	run(t, f.env, dir, "add", "followup.txt")
	run(t, f.env, dir, "commit", "-qm", "followup change")
	artifact, err := f.ws.Collect(context.Background(), as)
	if err != nil {
		t.Fatal(err)
	}
	as.Artifact, _ = json.Marshal(artifact)
	as.Role = protocol.RoleAccept
	as.RunID = "run_accept_followup"
	accepted := f.prepare(as)
	st, err = loadState(f.ws.home(as))
	if err != nil {
		t.Fatal(err)
	}
	check(repoGuide(as, st), accepted.Baseline)
	if st.Mounts["tend"].Base != f.mainSHA {
		t.Fatal("resource historical base changed")
	}
}
