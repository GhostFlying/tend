package gitws

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner"
)

// gitEnv isolates test git commands from any user configuration.
func gitEnv(t *testing.T) []string {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return []string{
		"GIT_CONFIG_GLOBAL=" + cfg, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
	}
}

func run(t *testing.T, env []string, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	c.Env = append(os.Environ(), env...)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// remote creates a bare repository with a main and a release branch and
// returns its path and the tips of both.
func remote(t *testing.T, env []string) (url, main, release string) {
	t.Helper()
	dir := t.TempDir()
	seed := filepath.Join(dir, "seed")
	url = filepath.Join(dir, "remote.git")
	run(t, env, dir, "init", "--quiet", "--initial-branch=main", seed)
	if err := os.WriteFile(filepath.Join(seed, "README"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "deps.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, env, seed, "add", ".")
	run(t, env, seed, "commit", "--quiet", "-m", "first")
	run(t, env, seed, "branch", "release/1")
	run(t, env, seed, "commit", "--quiet", "--allow-empty", "-m", "second")
	run(t, env, dir, "clone", "--quiet", "--bare", seed, url)
	return url, run(t, env, seed, "rev-parse", "main"), run(t, env, seed, "rev-parse", "release/1")
}

type fixture struct {
	t   *testing.T
	env []string
	ws  *Workspaces
	url string
	// mainSHA and releaseSHA are the remote's branch tips.
	mainSHA, releaseSHA string
}

func newFixture(t *testing.T, repos map[string]Repo) *fixture {
	t.Helper()
	env := gitEnv(t)
	url, main, release := remote(t, env)
	return &fixture{t: t, env: env, url: url, mainSHA: main, releaseSHA: release,
		ws: New(Config{Home: t.TempDir(), Env: env, Repos: repos})}
}

func (f *fixture) implement(mounts ...protocol.AssignedMount) protocol.Assignment {
	if len(mounts) == 0 {
		mounts = []protocol.AssignedMount{{Repo: "tend", Path: "tend", RemoteURL: f.url}}
	}
	return protocol.Assignment{RunID: "run_1", TaskID: "task_1", Role: protocol.RoleImplement,
		Workspace: protocol.AssignmentWorkspace{Name: "feature_a", Branch: "tend/feature_a", Mounts: mounts}}
}

func (f *fixture) prepare(as protocol.Assignment) runner.Prepared {
	f.t.Helper()
	p, err := f.ws.Prepare(context.Background(), as)
	if err != nil {
		f.t.Fatalf("Prepare: %v", err)
	}
	return p
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestImplementationWorkspace(t *testing.T) {
	f := newFixture(t, nil)
	as := f.implement()
	p := f.prepare(as)

	if got := p.Report.Mounts; len(got) != 1 || got[0].ResolvedBaseSHA != f.mainSHA || got[0].ResolvedFrom != "remote_head:main" || got[0].Branch != "tend/feature_a" {
		t.Errorf("report = %+v", got)
	}
	mount := filepath.Join(p.Dir, "tend")
	if b := run(t, f.env, mount, "symbolic-ref", "--short", "HEAD"); b != "tend/feature_a" {
		t.Errorf("mount is on %s", b)
	}
	agents, err := os.ReadFile(filepath.Join(p.Dir, "AGENTS.md"))
	if err != nil || !strings.Contains(string(agents), "| `tend/` | tend | `tend/feature_a` | `"+f.mainSHA+"` |") ||
		!strings.Contains(string(agents), "- `tend/`: the tip of `origin/main` when the workspace was laid out") ||
		!strings.Contains(string(agents), "lists new files that are not committed") ||
		!strings.Contains(string(agents), "Whether and how to commit is yours to decide") {
		t.Errorf("AGENTS.md = %q, %v", agents, err)
	}

	// A worker that does not commit: tend records the working tree in a
	// snapshot and leaves the branch, the index and the files alone (D57).
	write(t, filepath.Join(mount, "fix.txt"), "fixed\n")
	art, err := f.ws.Collect(context.Background(), as)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	m := art.Mounts[0]
	if !m.Changed || m.BaseSHA != f.mainSHA || m.CommitSHA != f.mainSHA || m.Branch != "tend/feature_a" || m.SnapshotSHA == "" {
		t.Errorf("artifact = %+v", m)
	}
	if head, status := run(t, f.env, mount, "rev-parse", "HEAD"), run(t, f.env, mount, "status", "--porcelain"); head != f.mainSHA || status != "?? fix.txt" {
		t.Errorf("after Collect HEAD = %s, status = %q; want both untouched", head, status)
	}
	if parent, file := run(t, f.env, mount, "rev-parse", m.SnapshotSHA+"^"), run(t, f.env, mount, "show", m.SnapshotSHA+":fix.txt"); parent != f.mainSHA || file != "fixed" {
		t.Errorf("snapshot has parent %s and fix.txt %q", parent, file)
	}
	// Collecting the same run again gives the same artifact.
	if again, err := f.ws.Collect(context.Background(), as); err != nil || again.Mounts[0] != m {
		t.Errorf("Collect again = %+v, %v; want %+v", again, err, m)
	}

	// The next generation reuses the workspace and keeps the base.
	as.RunID = "run_2"
	p2 := f.prepare(as)
	if p2.Dir != p.Dir || p2.Report.Mounts[0].ResolvedBaseSHA != f.mainSHA {
		t.Errorf("second prepare = %+v", p2)
	}
	if _, err := f.ws.Collect(context.Background(), as); !errors.Is(err, runner.ErrNoChanges) {
		t.Errorf("Collect without changes = %v, want ErrNoChanges", err)
	}
}

// TestFault_WorkerRewritesHistory: how the worker uses git is its own
// work (D57). Whatever it does, tend records the mount as it is, and
// acceptance and the next implementation start from there.
func TestFault_WorkerRewritesHistory(t *testing.T) {
	cases := map[string]struct {
		work   func(f *fixture, root, mount string)
		branch string
	}{
		"worker commits": {func(f *fixture, _, mount string) {
			write(f.t, filepath.Join(mount, "x"), "x")
			run(f.t, f.env, mount, "add", ".")
			run(f.t, f.env, mount, "commit", "--quiet", "-m", "feat: add x")
		}, "tend/feature_a"},
		"worker rewrites history below the base": {func(f *fixture, _, mount string) {
			run(f.t, f.env, mount, "reset", "--quiet", "--hard", f.releaseSHA)
			write(f.t, filepath.Join(mount, "x"), "x")
			run(f.t, f.env, mount, "add", ".")
			run(f.t, f.env, mount, "commit", "--quiet", "-m", "feat: add x")
		}, "tend/feature_a"},
		"worker switches branch": {func(f *fixture, _, mount string) {
			run(f.t, f.env, mount, "switch", "--quiet", "-c", "other")
			write(f.t, filepath.Join(mount, "x"), "x")
		}, "other"},
		"worker detaches HEAD": {func(f *fixture, _, mount string) {
			run(f.t, f.env, mount, "switch", "--quiet", "--detach")
			write(f.t, filepath.Join(mount, "x"), "x")
		}, ""},
		"worker changes git configuration and adds a worktree": {func(f *fixture, root, mount string) {
			run(f.t, f.env, mount, "config", "core.excludesFile", "/dev/null")
			run(f.t, f.env, mount, "worktree", "add", "--quiet", "--detach", filepath.Join(root, "extra"))
			write(f.t, filepath.Join(mount, "x"), "x")
		}, "tend/feature_a"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, nil)
			as := f.implement()
			p := f.prepare(as)
			mount := filepath.Join(p.Dir, "tend")
			c.work(f, p.Dir, mount)
			art, err := f.ws.Collect(context.Background(), as)
			if err != nil {
				t.Fatalf("Collect = %v", err)
			}
			m := art.Mounts[0]
			if !m.Changed || m.Branch != c.branch || m.CommitSHA != run(t, f.env, mount, "rev-parse", "HEAD") || m.BaseSHA != f.mainSHA {
				t.Errorf("artifact = %+v", m)
			}
			raw, _ := json.Marshal(art)
			acc := protocol.Assignment{RunID: "run_acc", TaskID: "task_1", Role: protocol.RoleAccept, Artifact: raw, Workspace: as.Workspace}
			f.prepare(acc)
			next := f.implement()
			next.RunID = "run_2"
			f.prepare(next)
			if head := run(t, f.env, mount, "rev-parse", "HEAD"); head != m.CommitSHA {
				t.Errorf("the next implementation starts at %s, not at the artifact's %s", head, m.CommitSHA)
			}
		})
	}
}

func TestMountsAndExplicitBases(t *testing.T) {
	f := newFixture(t, nil)
	url2, main2, _ := remote(t, f.env)
	as := f.implement(
		protocol.AssignedMount{Repo: "tend", Path: "tend", RemoteURL: f.url, BaseBranch: "release/1"},
		protocol.AssignedMount{Repo: "lynx", Path: "lynx", RemoteURL: url2, BaseCommit: main2},
	)
	p := f.prepare(as)
	got := map[string]protocol.ResolvedMount{}
	for _, m := range p.Report.Mounts {
		got[m.Path] = m
	}
	if got["tend"].ResolvedBaseSHA != f.releaseSHA || got["tend"].ResolvedFrom != "branch:release/1" {
		t.Errorf("tend mount = %+v", got["tend"])
	}
	if got["lynx"].ResolvedBaseSHA != main2 || got["lynx"].ResolvedFrom != "commit" {
		t.Errorf("lynx mount = %+v", got["lynx"])
	}

	// Only the changed mount gets a new commit; both are in the artifact.
	write(t, filepath.Join(p.Dir, "lynx", "fix.txt"), "fixed\n")
	art, err := f.ws.Collect(context.Background(), as)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range art.Mounts {
		if m.Path == "tend" && (m.Changed || m.CommitSHA != f.releaseSHA) || m.Path == "lynx" && !m.Changed {
			t.Errorf("artifact mount %+v", m)
		}
	}

	missing := f.implement(protocol.AssignedMount{Repo: "tend", Path: "tend", RemoteURL: f.url, BaseBranch: "nope"})
	missing.Workspace.Name, missing.Workspace.Branch = "other", "tend/other"
	if _, err := f.ws.Prepare(context.Background(), missing); !errors.Is(err, runner.ErrBaseNotFound) {
		t.Errorf("missing base branch: %v", err)
	}
}

func TestAcceptanceRunsInTaskWorkspace(t *testing.T) {
	f := newFixture(t, nil)
	impl := f.implement()
	p := f.prepare(impl)
	write(t, filepath.Join(p.Dir, "tend", "fix.txt"), "fixed\n")
	art, err := f.ws.Collect(context.Background(), impl)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(art)
	acc := protocol.Assignment{RunID: "run_acc", TaskID: "task_1", Role: protocol.RoleAccept, Artifact: raw, Workspace: impl.Workspace}

	ap := f.prepare(acc)
	if ap.Dir != p.Dir {
		t.Fatalf("acceptance dir = %s, want the task workspace %s", ap.Dir, p.Dir)
	}
	if head := run(t, f.env, filepath.Join(ap.Dir, "tend"), "rev-parse", "HEAD"); head != art.Mounts[0].CommitSHA {
		t.Errorf("acceptance HEAD = %s", head)
	}
	if agents, err := os.ReadFile(filepath.Join(ap.Dir, "AGENTS.md")); err != nil || !strings.Contains(string(agents), "`git diff <base commit>` shows all changes to tracked files, committed or not") ||
		!strings.Contains(string(agents), "The implementation's commits, and whatever it left uncommitted, are part of the result") {
		t.Errorf("acceptance AGENTS.md = %q, %v", agents, err)
	}
	// Whatever the acceptance run leaves, such as build output, stays.
	write(t, filepath.Join(ap.Dir, "tend", "build.log"), "ok\n")
	if err := f.ws.Finish(context.Background(), acc); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ap.Dir, "tend", "build.log")); err != nil {
		t.Errorf("the workspace was cleaned after acceptance: %v", err)
	}
	if _, err := os.Stat(ap.ScratchDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("acceptance scratch left behind: %v", err)
	}

	// The next generation continues in the same workspace.
	impl.RunID = "run_2"
	if p2 := f.prepare(impl); p2.Dir != p.Dir {
		t.Errorf("next implementation dir = %s", p2.Dir)
	}
}

func TestAcceptanceRejectsMovedWorkspace(t *testing.T) {
	for name, move := range map[string]func(f *fixture, mount string){
		"HEAD moved": func(f *fixture, mount string) {
			run(f.t, f.env, mount, "add", ".")
			run(f.t, f.env, mount, "commit", "--quiet", "-m", "later")
		},
		"branch changed": func(f *fixture, mount string) {
			run(f.t, f.env, mount, "switch", "--quiet", "-c", "other")
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, nil)
			impl := f.implement()
			p := f.prepare(impl)
			mount := filepath.Join(p.Dir, "tend")
			write(t, filepath.Join(mount, "fix.txt"), "fixed\n")
			art, err := f.ws.Collect(context.Background(), impl)
			if err != nil {
				t.Fatal(err)
			}
			move(f, mount)
			raw, _ := json.Marshal(art)
			acc := protocol.Assignment{RunID: "run_acc", TaskID: "task_1", Role: protocol.RoleAccept, Artifact: raw, Workspace: impl.Workspace}
			if _, err := f.ws.Prepare(context.Background(), acc); !errors.Is(err, runner.ErrWorkspaceMoved) {
				t.Errorf("Prepare = %v, want ErrWorkspaceMoved", err)
			}
		})
	}
}

func TestProfilesRerunWhenInputsChange(t *testing.T) {
	count := filepath.Join(t.TempDir(), "count")
	f := newFixture(t, map[string]Repo{"tend": {Profiles: map[string]Profile{"sync": {
		Commands:       [][]string{{"sh", "-c", `echo run >> "$COUNT_FILE"`}},
		RerunIfChanged: []string{"deps.txt"},
	}}}})
	f.ws.cfg.Env = append(f.ws.cfg.Env, "COUNT_FILE="+count)
	as := f.implement(protocol.AssignedMount{Repo: "tend", Path: "tend", RemoteURL: f.url, Profile: "sync"})
	runs := func() int {
		b, _ := os.ReadFile(count)
		return strings.Count(string(b), "run")
	}

	p := f.prepare(as)
	f.prepare(as)
	if n := runs(); n != 1 {
		t.Errorf("profile ran %d times for unchanged inputs, want 1", n)
	}
	write(t, filepath.Join(p.Dir, "tend", "deps.txt"), "v2\n")
	f.prepare(as)
	if n := runs(); n != 2 {
		t.Errorf("profile ran %d times after its input changed, want 2", n)
	}

	unknown := as
	unknown.Workspace.Mounts = []protocol.AssignedMount{{Repo: "tend", Path: "tend", RemoteURL: f.url, Profile: "missing"}}
	unknown.Workspace.Name, unknown.Workspace.Branch = "other", "tend/other"
	if _, err := f.ws.Prepare(context.Background(), unknown); err == nil {
		t.Error("an unknown profile was accepted")
	}
}

// Files the runner's profiles regenerate, or an earlier acceptance left,
// do not make the workspace "moved": only the branch and HEAD are tend's
// record to check.
func TestAcceptanceToleratesRegeneratedFiles(t *testing.T) {
	f := newFixture(t, map[string]Repo{"tend": {Profiles: map[string]Profile{"gen": {
		Commands:       [][]string{{"cp", "deps.txt", "generated.txt"}},
		RerunIfChanged: []string{"deps.txt"},
	}}}})
	impl := f.implement(protocol.AssignedMount{Repo: "tend", Path: "tend", RemoteURL: f.url, Profile: "gen"})
	p := f.prepare(impl)
	write(t, filepath.Join(p.Dir, "tend", "deps.txt"), "v2\n")
	art, err := f.ws.Collect(context.Background(), impl)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(art)
	acc := protocol.Assignment{RunID: "run_acc", TaskID: "task_1", Role: protocol.RoleAccept, Artifact: raw, Workspace: impl.Workspace}
	// The runner prepares an acceptance run twice; the profile reruns on
	// the first.
	f.prepare(acc)
	f.prepare(acc)
	write(t, filepath.Join(p.Dir, "tend", "build.log"), "ok\n")
	again := acc
	again.RunID = "run_acc_2"
	f.prepare(again)
}

func TestFetchFailureStopsPrepare(t *testing.T) {
	f := newFixture(t, nil)
	f.prepare(f.implement())
	// The remote goes away; a new workspace must not start from the stale
	// clone.
	if err := os.RemoveAll(f.url); err != nil {
		t.Fatal(err)
	}
	as := f.implement()
	as.Workspace.Name, as.Workspace.Branch = "feature_b", "tend/feature_b"
	if _, err := f.ws.Prepare(context.Background(), as); !errors.Is(err, runner.ErrFetchFailed) {
		t.Errorf("Prepare with an unreachable remote = %v, want ErrFetchFailed", err)
	}
	if _, err := os.Stat(filepath.Join(f.ws.cfg.Home, "workspaces", "feature_b", "tend")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a worktree was created from the stale clone")
	}
}

func TestPrepareRecoversAfterProfileFailure(t *testing.T) {
	flag := filepath.Join(t.TempDir(), "ok")
	f := newFixture(t, map[string]Repo{"tend": {Profiles: map[string]Profile{"sync": {
		Commands: [][]string{{"sh", "-c", `test -f "$OK_FLAG"`}},
	}}}})
	f.ws.cfg.Env = append(f.ws.cfg.Env, "OK_FLAG="+flag)
	as := f.implement(protocol.AssignedMount{Repo: "tend", Path: "tend", RemoteURL: f.url, Profile: "sync"})
	if _, err := f.ws.Prepare(context.Background(), as); err == nil {
		t.Fatal("Prepare succeeded with a failing profile")
	}
	write(t, flag, "")
	p := f.prepare(as) // the worktree from the failed attempt is reused
	if p.Report.Mounts[0].ResolvedBaseSHA != f.mainSHA {
		t.Errorf("report after recovery = %+v", p.Report)
	}
}

// An edit of the same size within the index's timestamp is still
// recorded: the scratch index keeps the index's time, so git still treats
// the entry as racy and rehashes the file.
func TestSnapshotSeesRacyEdits(t *testing.T) {
	f := newFixture(t, nil)
	as := f.implement()
	p := f.prepare(as)
	mount := filepath.Join(p.Dir, "tend")
	// Compare whole-second mtimes and sizes only.
	run(t, f.env, mount, "config", "core.checkStat", "minimal")
	run(t, f.env, mount, "config", "core.trustctime", "false")
	past := time.Now().Add(-100 * time.Second).Truncate(time.Second)
	file := filepath.Join(mount, "README")
	touch := func(path string) {
		t.Helper()
		if err := os.Chtimes(path, past, past); err != nil {
			t.Fatal(err)
		}
	}
	write(t, file, "aaaaa\n")
	touch(file)
	run(t, f.env, mount, "add", "README")
	// The same size and second: only the racy check can tell.
	write(t, file, "bbbbb\n")
	touch(file)
	index := run(t, f.env, mount, "rev-parse", "--git-path", "index")
	if !filepath.IsAbs(index) {
		index = filepath.Join(mount, index)
	}
	touch(index)
	art, err := f.ws.Collect(context.Background(), as)
	if err != nil {
		t.Fatalf("Collect = %v", err)
	}
	if got := run(t, f.env, mount, "show", art.Mounts[0].SnapshotSHA+":README"); got != "bbbbb" {
		t.Errorf("snapshot README = %q, want the latest edit", got)
	}
}

func TestCommittingIsAChange(t *testing.T) {
	f := newFixture(t, nil)
	as := f.implement()
	p := f.prepare(as)
	mount := filepath.Join(p.Dir, "tend")
	write(t, filepath.Join(mount, "fix.txt"), "fixed\n")
	if _, err := f.ws.Collect(context.Background(), as); err != nil {
		t.Fatal(err)
	}
	// The next run only commits what the first left uncommitted, as an
	// acceptance may ask: the files are the same, the result is not.
	next := f.implement()
	next.RunID = "run_2"
	f.prepare(next)
	run(t, f.env, mount, "add", ".")
	run(t, f.env, mount, "commit", "--quiet", "-m", "feat: fix")
	art, err := f.ws.Collect(context.Background(), next)
	if err != nil {
		t.Fatalf("Collect = %v", err)
	}
	if m := art.Mounts[0]; !m.Changed || m.SnapshotSHA != "" || m.CommitSHA != run(t, f.env, mount, "rev-parse", "HEAD") {
		t.Errorf("artifact = %+v", m)
	}
}

func TestRemoteBranchCannotPassForTaskBranch(t *testing.T) {
	f := newFixture(t, nil)
	// The remote already has a branch with the task branch's name, at an
	// older commit.
	run(t, f.env, f.url, "branch", "tend/feature_a", f.releaseSHA)
	p := f.prepare(f.implement())
	if head := run(t, f.env, filepath.Join(p.Dir, "tend"), "rev-parse", "HEAD"); head != f.mainSHA {
		t.Errorf("task branch starts at %s, want the resolved base %s", head, f.mainSHA)
	}
}

func TestRemoteURLChangeIsFollowed(t *testing.T) {
	f := newFixture(t, nil)
	f.prepare(f.implement())
	moved := filepath.Join(t.TempDir(), "moved.git")
	if err := os.Rename(f.url, moved); err != nil {
		t.Fatal(err)
	}
	as := f.implement(protocol.AssignedMount{Repo: "tend", Path: "tend", RemoteURL: moved})
	as.Workspace.Name, as.Workspace.Branch = "feature_b", "tend/feature_b"
	f.prepare(as)
	if got := run(t, f.env, f.ws.repoPath("tend"), "remote", "get-url", "origin"); got != moved {
		t.Errorf("origin = %s, want %s", got, moved)
	}
}

func TestRefUnsafeMountPath(t *testing.T) {
	f := newFixture(t, nil)
	as := f.implement(protocol.AssignedMount{Repo: "tend", Path: "src.lock", RemoteURL: f.url})
	p := f.prepare(as)
	write(t, filepath.Join(p.Dir, "src.lock", "fix.txt"), "x")
	if _, err := f.ws.Collect(context.Background(), as); err != nil {
		t.Errorf("Collect with a ref-unsafe mount path = %v", err)
	}
}

func TestRedact(t *testing.T) {
	in := "clone https://user:s3cret@example.com/r.git and ssh://git@host/r failed"
	out := redact(in)
	if strings.Contains(out, "s3cret") || !strings.Contains(out, "https://***@example.com/r.git") {
		t.Errorf("redact = %q", out)
	}
}

func TestLockWaitHonorsCancellation(t *testing.T) {
	w := New(Config{Home: t.TempDir()})
	unlock, err := w.lock(context.Background(), "repo:x")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("fenced")
	cancel(cause)
	if _, err := w.lock(ctx, "repo:x"); !errors.Is(err, cause) {
		t.Errorf("lock on a cancelled context = %v, want the cause", err)
	}
}

func TestProfileTimeoutKillsChildren(t *testing.T) {
	late := filepath.Join(t.TempDir(), "late")
	f := newFixture(t, map[string]Repo{"tend": {Profiles: map[string]Profile{"slow": {
		Commands: [][]string{{"sh", "-c", `(sleep 1; touch "$LATE_FILE") & sleep 10`}},
		Timeout:  200 * time.Millisecond,
	}}}})
	f.ws.cfg.Env = append(f.ws.cfg.Env, "LATE_FILE="+late)
	as := f.implement(protocol.AssignedMount{Repo: "tend", Path: "tend", RemoteURL: f.url, Profile: "slow"})
	start := time.Now()
	if _, err := f.ws.Prepare(context.Background(), as); err == nil {
		t.Fatal("Prepare succeeded past the profile timeout")
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Errorf("Prepare returned after %v", d)
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(late); !errors.Is(err, os.ErrNotExist) {
		t.Error("a child of the timed-out profile kept running and wrote to disk")
	}
}
