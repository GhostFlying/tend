package gitws

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestConflictedReuseBaselinePreservesIndexAndWorktree(t *testing.T) {
	for _, spec := range []string{`{"deliverables":["report"]}`, `{"deliverables":["commit"],"workspace":{"include_existing_changes":true}}`} {
		t.Run(spec, func(t *testing.T) {
			f := newFixture(t, nil)
			as := f.implement()
			as.Workspace.ID = "ws_conflicted"
			as.Spec = json.RawMessage(`{"deliverables":["report"]}`)
			p := f.prepare(as)
			dir := filepath.Join(p.Dir, "tend")
			run(t, f.env, dir, "checkout", "-b", "conflict-theirs")
			write(t, filepath.Join(dir, "README"), "theirs\n")
			run(t, f.env, dir, "commit", "-am", "theirs")
			run(t, f.env, dir, "checkout", as.Workspace.Branch)
			write(t, filepath.Join(dir, "README"), "ours\n")
			run(t, f.env, dir, "commit", "-am", "ours")
			cmd := exec.Command("git", "-C", dir, "merge", "conflict-theirs")
			cmd.Env = append(os.Environ(), f.env...)
			if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "CONFLICT") {
				t.Fatalf("merge did not create conflict: %s %v", out, err)
			}
			write(t, filepath.Join(dir, "evidence.txt"), "inherited untracked\n")
			entries := run(t, f.env, dir, "ls-files", "--stage", "-z")
			if !strings.Contains(run(t, f.env, dir, "status", "--porcelain=v1"), "UU README") {
				t.Fatal("missing UU fixture")
			}
			index := run(t, f.env, dir, "rev-parse", "--git-path", "index")
			if !filepath.IsAbs(index) {
				index = filepath.Join(dir, index)
			}
			before, err := os.ReadFile(index)
			if err != nil {
				t.Fatal(err)
			}
			conflict, err := os.ReadFile(filepath.Join(dir, "README"))
			if err != nil {
				t.Fatal(err)
			}
			as.TaskID = "task_conflicted_followup"
			as.RunID = "run_conflicted_followup"
			as.Workspace.Reused = true
			as.Spec = json.RawMessage(spec)
			q := f.prepare(as)
			var mounts []baselineMount
			if err := json.Unmarshal(q.Report.Baseline, &mounts); err != nil {
				t.Fatal(err)
			}
			if len(mounts) != 1 || mounts[0].Index != "" || !strings.Contains(mounts[0].Status, "UU README") {
				t.Fatalf("conflict baseline: %+v", mounts)
			}
			stages := map[int]string{}
			for _, entry := range mounts[0].IndexEntries {
				if entry.Path == "README" {
					if entry.Mode != "100644" {
						t.Fatalf("mode: %+v", entry)
					}
					stages[entry.Stage] = run(t, f.env, dir, "cat-file", "blob", entry.ObjectID)
				}
			}
			if stages[1] != "hello" || stages[2] != "ours" || stages[3] != "theirs" {
				t.Fatalf("stage contents: %+v", stages)
			}
			if got := run(t, f.env, dir, "show", mounts[0].Tree+":README"); got != strings.TrimSpace(string(conflict)) {
				t.Fatal("snapshot lost conflict markers")
			}
			if got := run(t, f.env, dir, "show", mounts[0].Tree+":evidence.txt"); got != "inherited untracked" {
				t.Fatal("snapshot lost untracked evidence")
			}
			// A retry uses the immutable baseline, including its original stages.
			retry := f.prepare(as)
			if !bytes.Equal(retry.Report.Baseline, q.Report.Baseline) {
				t.Fatal("cached conflict baseline changed")
			}
			as.Spec = json.RawMessage(`{"deliverables":["commit"]}`)
			if _, err := f.ws.Prepare(t.Context(), as); err == nil || !strings.Contains(err.Error(), "include_existing_changes") {
				t.Fatalf("cached conflict bypassed authorization: %v", err)
			}
			after, err := os.ReadFile(index)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("real conflicted index was modified")
			}
			if got := run(t, f.env, dir, "ls-files", "--stage", "-z"); got != entries {
				t.Fatal("real index stages changed")
			}
			after, err = os.ReadFile(filepath.Join(dir, "README"))
			if err != nil || !bytes.Equal(conflict, after) {
				t.Fatal("conflicted worktree content changed")
			}
			if got, err := os.ReadFile(filepath.Join(dir, "evidence.txt")); err != nil || string(got) != "inherited untracked\n" {
				t.Fatal("untracked contents changed")
			}
		})
	}
}

func TestBaselineIndexPropagatesGitErrors(t *testing.T) {
	f := newFixture(t, nil)
	as := f.implement()
	p := f.prepare(as)
	dir := filepath.Join(p.Dir, "tend")
	t.Run("index read", func(t *testing.T) {
		if _, _, err := f.ws.baselineIndex(t.Context(), t.TempDir()); err == nil {
			t.Fatal("Git index read failure ignored")
		}
	})
	t.Run("tree write", func(t *testing.T) {
		index := run(t, f.env, dir, "rev-parse", "--git-path", "index")
		if !filepath.IsAbs(index) {
			index = filepath.Join(dir, index)
		}
		write(t, index+".lock", "busy\n")
		defer os.Remove(index + ".lock")
		if _, _, err := f.ws.baselineIndex(context.Background(), dir); err == nil {
			t.Fatal("non-conflict write-tree error ignored")
		}
	})
}

func TestLargeConflictedBaselineReportExposesIndexFacts(t *testing.T) {
	full, err := json.Marshal([]baselineMount{{Path: "repo", Head: "head", TaskBase: "task-origin", Tree: "working-tree", Status: "UU conflict", Staged: strings.Repeat("large evidence\n", maxWireBaseline), IndexEntries: []baselineIndexEntry{
		{Path: "conflict", Mode: "100644", ObjectID: "base", Stage: 1},
		{Path: "conflict", Mode: "100644", ObjectID: "ours", Stage: 2},
		{Path: "conflict", Mode: "100644", ObjectID: "theirs", Stage: 3},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	report := baselineReport(full, "git", "task_conflicted")
	var summary struct {
		External bool   `json:"manifest_external"`
		Path     string `json:"local_snapshot_path"`
		Mounts   []struct {
			Index      string               `json:"index_tree"`
			TaskBase   string               `json:"task_base"`
			Conflicted bool                 `json:"conflicted_index"`
			Unmerged   int                  `json:"unmerged_entries"`
			Entries    []baselineIndexEntry `json:"index_entries"`
		} `json:"mounts"`
	}
	if err := json.Unmarshal(report, &summary); err != nil {
		t.Fatal(err)
	}
	if len(report) >= maxWireBaseline || !summary.External || summary.Path != ".tend/baselines/task_conflicted.json" || len(summary.Mounts) != 1 {
		t.Fatalf("invalid summary: %s", report)
	}
	m := summary.Mounts[0]
	if m.TaskBase != "task-origin" || m.Index != "" || !m.Conflicted || m.Unmerged != 3 || len(m.Entries) != 0 {
		t.Fatalf("lost conflict meaning: %s", report)
	}
}
