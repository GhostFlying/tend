package gitws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness/orca"
	"github.com/GhostFlying/tend/internal/protocol"
)

// fakeOrca stands in for orca's CLI over a real checkout: it creates
// worktrees with git the way orca does, branch <user>/<name>, and takes a
// name it already has by adding a suffix.
type fakeOrca struct {
	t         *testing.T
	env       []string
	checkout  string
	dir       string
	worktrees []orca.Worktree
	creates   int
	// lose makes the next create lose its reply; the worktree appears
	// only after hide more listings, as if orca were still creating it.
	lose         bool
	hide, hidden int
}

func (o *fakeOrca) exec(_ context.Context, _ string, args []string) ([]byte, error) {
	flag := func(name string) string {
		if i := slices.Index(args, name); i >= 0 {
			return args[i+1]
		}
		return ""
	}
	var result any
	switch args[0] + " " + args[1] {
	case "repo show":
		result = map[string]any{"repo": orca.Repo{ID: "repo-1", Path: o.checkout, DisplayName: "tend"}}
	case "worktree list":
		visible := o.worktrees
		if o.hidden > 0 && len(visible) > 0 {
			o.hidden--
			visible = visible[:len(visible)-1]
		}
		result = map[string]any{"worktrees": visible, "truncated": false}
	case "worktree show":
		selector := flag("--worktree")
		for _, wt := range o.worktrees {
			if selector == "id:"+wt.ID || selector == "path:"+wt.Path {
				return json.Marshal(map[string]any{"id": "r", "ok": true, "result": map[string]any{"worktree": wt}})
			}
		}
		return []byte(`{"ok":false,"error":{"code":"not_found","message":"missing worktree"}}`), nil
	case "worktree create":
		o.creates++
		name, path := flag("--name"), filepath.Join(o.dir, flag("--name"))
		for n := 2; ; n++ {
			if _, err := os.Stat(path); os.IsNotExist(err) {
				break
			}
			path = filepath.Join(o.dir, fmt.Sprintf("%s-%d", name, n))
		}
		branch := "me/" + filepath.Base(path)
		run(o.t, o.env, o.checkout, "worktree", "add", "--quiet", "-b", branch, path, flag("--base-branch"))
		wt := orca.Worktree{ID: "repo-1::" + path, RepoID: "repo-1", Path: path, Head: run(o.t, o.env, path, "rev-parse", "HEAD"),
			Branch: "refs/heads/" + branch, DisplayName: name, BaseRef: flag("--base-branch")}
		o.worktrees = append(o.worktrees, wt)
		if o.lose {
			o.lose, o.hidden = false, o.hide
			return []byte(`{"id":"local","ok":false,"error":{"code":"runtime_timeout","message":"Timed out waiting for the Orca runtime to respond."}}`), errors.New("exit status 1")
		}
		result = map[string]any{"worktree": wt}
	default:
		return nil, fmt.Errorf("unexpected orca %v", args)
	}
	b, err := json.Marshal(map[string]any{"id": "r", "ok": true, "result": result})
	return b, err
}

func newOrcaFixture(t *testing.T) (*fixture, *fakeOrca) {
	t.Helper()
	f := newFixture(t, nil)
	checkout := filepath.Join(t.TempDir(), "tend")
	run(t, f.env, filepath.Dir(checkout), "clone", "--quiet", f.url, checkout)
	o := &fakeOrca{t: t, env: f.env, checkout: checkout, dir: t.TempDir()}
	f.ws = NewOrca(Config{Home: t.TempDir(), Env: f.env, Repos: map[string]Repo{"tend": {Orca: "name:tend"}}}, &orca.Client{Exec: o.exec})
	return f, o
}

func TestOrcaWorkspace(t *testing.T) {
	f, o := newOrcaFixture(t)
	as := f.implement()
	p := f.prepare(as)
	wt := filepath.Join(o.dir, "feature_a")
	if p.Dir != wt {
		t.Fatalf("worker starts in %s, want the orca worktree %s", p.Dir, wt)
	}
	if got := p.Report.Mounts; len(got) != 1 || got[0].Branch != "me/feature_a" || got[0].ResolvedBaseSHA != f.mainSHA || got[0].ResolvedFrom != "remote_head:main" {
		t.Errorf("report = %+v", got)
	}
	// The repository's own AGENTS.md is left alone; the worker gets the
	// guide in its brief, and the runner's state stays out of the
	// repository.
	if _, err := os.Stat(filepath.Join(wt, "AGENTS.md")); !os.IsNotExist(err) {
		t.Errorf("AGENTS.md written into the repository: %v", err)
	}
	if !strings.Contains(p.Guide, "on branch `me/feature_a`") || !strings.Contains(p.Guide, "base commit `"+f.mainSHA+"`") ||
		!strings.Contains(p.Guide, "Whether and how to commit is yours to decide") {
		t.Errorf("guide = %q", p.Guide)
	}
	if status := run(t, f.env, wt, "status", "--porcelain", "--ignored"); status != "" {
		t.Errorf("the worktree holds files tend left: %q", status)
	}

	// Preparing again finds the workspace without asking orca for another.
	if again := f.prepare(as); again.Dir != wt || o.creates != 1 {
		t.Errorf("second prepare = %+v after %d creates", again, o.creates)
	}

	write(t, filepath.Join(wt, "fix.txt"), "fixed\n")
	art, err := f.ws.Collect(context.Background(), as)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	m := art.Mounts[0]
	if !m.Changed || m.Branch != "me/feature_a" || m.BaseSHA != f.mainSHA || m.SnapshotSHA == "" {
		t.Errorf("artifact = %+v", m)
	}

	// Acceptance checks the same worktree.
	b, _ := json.Marshal(art)
	acc := as
	acc.RunID, acc.Role, acc.Artifact = "run_acc", protocol.RoleAccept, b
	if p := f.prepare(acc); p.Dir != wt || !strings.Contains(p.Guide, "do not edit the code") {
		t.Errorf("acceptance prepare = %+v", p)
	}
}

// pendOnly turns the workspace's recorded mount back into what was saved
// before orca was asked for the worktree.
func pendOnly(t *testing.T, f *fixture, as protocol.Assignment) {
	t.Helper()
	home := f.ws.home(as)
	st, err := loadState(home)
	if err != nil {
		t.Fatal(err)
	}
	ms := st.Mounts["tend"]
	ms.Dir, ms.Branch, ms.Head = "", "", ""
	st.Mounts["tend"] = ms
	if err := saveState(home, st); err != nil {
		t.Fatal(err)
	}
}

func TestOrcaWorkspaceTakesUpItsOwnWorktree(t *testing.T) {
	f, o := newOrcaFixture(t)
	as := f.implement()
	f.prepare(as)
	// The runner asked orca for the worktree but failed before recording
	// it; meanwhile the remote moved on.
	pendOnly(t, f, as)
	seed := filepath.Join(filepath.Dir(f.url), "seed")
	run(t, f.env, seed, "commit", "--quiet", "--allow-empty", "-m", "third")
	run(t, f.env, seed, "push", "--quiet", f.url, "main")
	p := f.prepare(as)
	if o.creates != 1 || p.Dir != filepath.Join(o.dir, "feature_a") || p.Report.Mounts[0].ResolvedBaseSHA != f.mainSHA ||
		p.Report.Mounts[0].Branch != "me/feature_a" {
		t.Errorf("prepare after a lost record = %+v after %d creates", p, o.creates)
	}
}

func TestOrcaWorkspaceRefusesAWorktreeItDidNotAskFor(t *testing.T) {
	f, o := newOrcaFixture(t)
	as := f.implement()
	f.prepare(as)
	// A worktree by the name that tend has no record of, for example the
	// owner's own.
	if err := os.RemoveAll(filepath.Join(f.ws.home(as), ".tend")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ws.Prepare(context.Background(), as); err == nil || !strings.Contains(err.Error(), "that tend did not create") {
		t.Errorf("Prepare over an unrecorded worktree = %v", err)
	}
	// One that tend asked for from another base.
	pending := state{Mounts: map[string]mountState{"tend": {Repo: "tend", Base: f.mainSHA, From: "remote_head:main"}}, Prepared: map[string]string{}}
	if err := saveState(f.ws.home(as), pending); err != nil {
		t.Fatal(err)
	}
	o.worktrees[0].BaseRef = f.releaseSHA
	if _, err := f.ws.Prepare(context.Background(), as); err == nil || !strings.Contains(err.Error(), "that tend did not create") {
		t.Errorf("Prepare over a worktree from another base = %v", err)
	}
	// Two by the name leave the choice to the owner.
	o.worktrees = append(o.worktrees, o.worktrees[0])
	o.worktrees[1].Path += "-2"
	if _, err := f.ws.Prepare(context.Background(), as); err == nil || !strings.Contains(err.Error(), "2 worktrees named feature_a") {
		t.Errorf("Prepare with two worktrees by the name = %v", err)
	}
	if o.creates != 1 {
		t.Errorf("%d creates, want 1", o.creates)
	}
}

func TestOrcaWorkspaceFetchesEveryBranch(t *testing.T) {
	f, o := newOrcaFixture(t)
	// The owner's checkout fetches only main.
	if err := os.RemoveAll(o.checkout); err != nil {
		t.Fatal(err)
	}
	run(t, f.env, filepath.Dir(o.checkout), "clone", "--quiet", "--single-branch", "--branch", "main", f.url, o.checkout)
	as := f.implement(protocol.AssignedMount{Repo: "tend", Path: "tend", RemoteURL: f.url, BaseBranch: "release/1"})
	p := f.prepare(as)
	if got := p.Report.Mounts[0]; got.ResolvedBaseSHA != f.releaseSHA || got.ResolvedFrom != "branch:release/1" {
		t.Errorf("report = %+v", got)
	}
	if refspec := run(t, f.env, o.checkout, "config", "--get-all", "remote.origin.fetch"); refspec != "+refs/heads/main:refs/remotes/origin/main" {
		t.Errorf("the owner's fetch configuration changed to %q", refspec)
	}
}

func TestOrcaWorkspaceNeedsOneMappedMount(t *testing.T) {
	f, _ := newOrcaFixture(t)
	two := f.implement(protocol.AssignedMount{Repo: "tend", Path: "a", RemoteURL: f.url}, protocol.AssignedMount{Repo: "tend", Path: "b", RemoteURL: f.url})
	if _, err := f.ws.Prepare(context.Background(), two); err == nil || !strings.Contains(err.Error(), "one mount") {
		t.Errorf("two mounts = %v", err)
	}
	other := f.implement(protocol.AssignedMount{Repo: "web", Path: "web", RemoteURL: f.url})
	if _, err := f.ws.Prepare(context.Background(), other); err == nil || !strings.Contains(err.Error(), "no orca repository") {
		t.Errorf("unmapped repo = %v", err)
	}
}

func TestFault_OrcaCreateReplyLost(t *testing.T) {
	f, o := newOrcaFixture(t)
	now := time.UnixMilli(1_000_000)
	f.ws.cfg.Now = func() time.Time { return now }
	f.ws.layout = orcaLayout{w: f.ws, orca: f.ws.layout.(orcaLayout).orca, poll: time.Millisecond}
	as := f.implement()
	// The reply is lost while orca goes on creating the worktree.
	o.lose, o.hide = true, 3
	if _, err := f.ws.Prepare(context.Background(), as); err == nil {
		t.Fatal("Prepare succeeded without orca's reply")
	}
	// The retry waits for it instead of asking for another.
	p := f.prepare(as)
	if o.creates != 1 || p.Dir != filepath.Join(o.dir, "feature_a") {
		t.Errorf("prepare after a lost reply = %+v after %d creates", p, o.creates)
	}

	// When nothing appears within the settle time, the runner asks again.
	as.Workspace.Name = "feature_b"
	as.TaskID = "task_2"
	o.lose, o.hide = true, 1000
	if _, err := f.ws.Prepare(context.Background(), as); err == nil {
		t.Fatal("Prepare succeeded without orca's reply")
	}
	o.worktrees = o.worktrees[:len(o.worktrees)-1] // the create had failed after all
	o.hidden = 0
	now = now.Add(createSettle)
	if p := f.prepare(as); o.creates != 3 || p.Dir == "" {
		t.Errorf("prepare after the settle time = %+v after %d creates", p, o.creates)
	}
}
