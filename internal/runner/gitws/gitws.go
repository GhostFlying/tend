// Package gitws lays out run workspaces with git worktrees (D30, D33-D37).
//
// Every registered repository has one bare clone under $TEND_HOME/repos.
// A task's workspace is a directory under $TEND_HOME/workspaces holding one
// worktree per mount, all on the task branch; the worker starts in the
// workspace root, which is not itself a repository. An acceptance run gets
// a throwaway workspace with the artifact's commits checked out detached.
//
// The runner creates the worktrees; what happens in them is the worker's
// work, commits included (D57). When an implement run ends, the runner
// records each mount as it is, its HEAD and a snapshot of its working tree,
// without committing or checking anything. Progress is saved after every
// step, so a failure halfway leaves a workspace the next run can pick up.
package gitws

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner"
)

// Profile is a prepare step: commands run in a mount or the workspace
// root after checkout, for example to sync dependencies (D35).
type Profile struct {
	// Commands run in order; each is an argv.
	Commands [][]string `json:"commands"`
	// RerunIfChanged lists paths, relative to where the profile runs, whose
	// content decides whether a reused workspace needs the profile again. A
	// path ending in "/**" covers a whole directory. With no paths the
	// profile runs once per workspace.
	RerunIfChanged []string `json:"rerun_if_changed,omitempty"`
	// Timeout bounds the whole profile; zero means 30 minutes.
	Timeout time.Duration `json:"timeout,omitempty"`
}

// Repo is the runner-side configuration of a repository.
type Repo struct {
	// Reference is an existing local clone to borrow objects from on the
	// first clone; the new clone does not keep depending on it.
	Reference string `json:"reference,omitempty"`
	// Profiles are the prepare steps mounts of this repository can select.
	Profiles map[string]Profile `json:"profiles,omitempty"`
}

// Config configures the workspaces of one runner.
type Config struct {
	// Home is TEND_HOME.
	Home string
	// Git is the git binary; empty means "git" on PATH.
	Git string
	// Env is added to the environment of git and prepare commands.
	Env      []string
	Repos    map[string]Repo
	Profiles map[string]Profile // workspace-level profiles
	Log      *slog.Logger
}

// Workspaces implements runner.Workspaces with git worktrees.
type Workspaces struct {
	cfg Config
	git git

	mu    sync.Mutex
	locks map[string]chan struct{}
}

var _ runner.Workspaces = (*Workspaces)(nil)

// New returns Workspaces rooted at cfg.Home.
func New(cfg Config) *Workspaces {
	bin := cfg.Git
	if bin == "" {
		bin = "git"
	}
	return &Workspaces{cfg: cfg, git: git{bin: bin, env: cfg.Env}, locks: map[string]chan struct{}{}}
}

// ErrWorkspaceMoved means a workspace is not as the artifact under
// acceptance recorded it.
var ErrWorkspaceMoved = runner.ErrWorkspaceMoved

// lock serializes work on one repository clone or one workspace. Waiting
// gives up when ctx ends, so a fenced run does not wait for another run's
// slow fetch.
func (w *Workspaces) lock(ctx context.Context, key string) (func(), error) {
	w.mu.Lock()
	l, ok := w.locks[key]
	if !ok {
		l = make(chan struct{}, 1)
		w.locks[key] = l
	}
	w.mu.Unlock()
	select {
	case l <- struct{}{}:
		return func() { <-l }, nil
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}

func (w *Workspaces) repoPath(repo string) string {
	return filepath.Join(w.cfg.Home, "repos", repo+".git")
}

// root is the task's workspace. Acceptance runs there too (D41).
func (w *Workspaces) root(as protocol.Assignment) string {
	return filepath.Join(w.cfg.Home, "workspaces", as.Workspace.Name)
}

// state is what the runner remembers about a workspace, kept in the
// workspace root next to the mounts.
type state struct {
	Mounts map[string]mountState `json:"mounts"`
	// Prepared maps a profile's key to the hash of its inputs when it last
	// ran successfully.
	Prepared map[string]string `json:"prepared"`
}

type mountState struct {
	Repo   string `json:"repo"`
	Base   string `json:"base"`
	From   string `json:"from"`
	Branch string `json:"branch"`
	// Head and Tree are the mount as last recorded: HEAD and the tree of
	// the working tree in the latest artifact, or the base before there is
	// one. An empty Tree means Head's.
	Head string `json:"head"`
	Tree string `json:"tree,omitempty"`
	// Run is the run that recorded them and Prev* what they were before
	// it, so that run's collection can be repeated with the same result.
	Run      string `json:"run,omitempty"`
	PrevHead string `json:"prev_head,omitempty"`
	PrevTree string `json:"prev_tree,omitempty"`
}

func statePath(root string) string { return filepath.Join(root, ".tend", "state.json") }

func loadState(root string) (state, error) {
	s := state{Mounts: map[string]mountState{}, Prepared: map[string]string{}}
	b, err := os.ReadFile(statePath(root))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, fmt.Errorf("gitws: read state: %w", err)
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("gitws: decode state: %w", err)
	}
	return s, nil
}

func saveState(root string, s state) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("gitws: encode state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(statePath(root)), 0o755); err != nil {
		return fmt.Errorf("gitws: state dir: %w", err)
	}
	tmp := statePath(root) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("gitws: write state: %w", err)
	}
	return os.Rename(tmp, statePath(root))
}

// ensureClone makes sure the repository's bare clone exists, points at
// remoteURL and, if fetch is set, is fetched; it returns what the remote
// has.
func (w *Workspaces) ensureClone(ctx context.Context, repo, remoteURL string, fetch bool) (Remote, error) {
	unlock, err := w.lock(ctx, "repo:"+repo)
	if err != nil {
		return Remote{}, err
	}
	defer unlock()
	path := w.repoPath(repo)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := w.clone(ctx, repo, remoteURL, path); err != nil {
			return Remote{}, err
		}
		fetch = true
	} else if err != nil {
		return Remote{}, fmt.Errorf("gitws: stat clone: %w", err)
	} else if remoteURL != "" {
		// The registration may have moved the repository.
		if current, err := w.git.run(ctx, path, "remote", "get-url", "origin"); err != nil || current != remoteURL {
			if _, err := w.git.run(ctx, path, "remote", "set-url", "origin", remoteURL); err != nil {
				return Remote{}, err
			}
		}
	}
	if !fetch {
		return Remote{}, nil
	}
	if _, err := w.git.run(ctx, path, "fetch", "--prune", "--quiet", "origin"); err != nil {
		return Remote{}, fmt.Errorf("%w: %w", runner.ErrFetchFailed, err)
	}
	remote := Remote{Branches: map[string]string{}}
	refs, err := w.git.run(ctx, path, "for-each-ref", "--format=%(refname:strip=3) %(objectname)", "refs/remotes/origin/")
	if err != nil {
		return Remote{}, err
	}
	for _, line := range strings.Split(refs, "\n") {
		if name, sha, ok := strings.Cut(line, " "); ok && name != "HEAD" {
			remote.Branches[name] = sha
		}
	}
	if out, err := w.git.run(ctx, path, "ls-remote", "--symref", "origin", "HEAD"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			if rest, ok := strings.CutPrefix(line, "ref: refs/heads/"); ok {
				remote.Head, _, _ = strings.Cut(rest, "\t")
			}
		}
	}
	return remote, nil
}

func (w *Workspaces) clone(ctx context.Context, repo, remoteURL, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("gitws: repos dir: %w", err)
	}
	tmp := path + ".partial"
	_ = os.RemoveAll(tmp)
	args := []string{"clone", "--bare", "--quiet"}
	if ref := w.cfg.Repos[repo].Reference; ref != "" {
		args = append(args, "--reference", ref, "--dissociate")
	}
	if _, err := w.git.run(ctx, filepath.Dir(path), append(args, remoteURL, tmp)...); err != nil {
		return fmt.Errorf("%w: %w", runner.ErrFetchFailed, err)
	}
	// A bare clone copies the remote's branches into refs/heads and has no
	// fetch refspec. Keep remote state under refs/remotes/origin instead
	// and drop the copies, so refs/heads only ever holds task branches and
	// a remote branch can never pass for one.
	if _, err := w.git.run(ctx, tmp, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"); err != nil {
		return err
	}
	heads, err := w.git.run(ctx, tmp, "for-each-ref", "--format=%(refname)", "refs/heads/")
	if err != nil {
		return err
	}
	for _, ref := range strings.Fields(heads) {
		if _, err := w.git.run(ctx, tmp, "update-ref", "-d", ref); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("gitws: install clone: %w", err)
	}
	return nil
}

func (w *Workspaces) hasCommit(ctx context.Context, repo, sha string) bool {
	ok, err := w.git.test(ctx, w.repoPath(repo), "cat-file", "-e", sha+"^{commit}")
	return err == nil && ok
}

// Prepare implements runner.Workspaces.
func (w *Workspaces) Prepare(ctx context.Context, as protocol.Assignment) (runner.Prepared, error) {
	root := w.root(as)
	unlock, err := w.lock(ctx, "ws:"+root)
	if err != nil {
		return runner.Prepared{}, err
	}
	defer unlock()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return runner.Prepared{}, fmt.Errorf("gitws: workspace dir: %w", err)
	}
	st, err := loadState(root)
	if err != nil {
		return runner.Prepared{}, err
	}
	if as.Role == protocol.RoleAccept {
		err = w.layoutAcceptance(ctx, as, root, &st)
	} else {
		err = w.layoutImplementation(ctx, as, root, &st)
	}
	if err != nil {
		return runner.Prepared{}, err
	}
	if err := writeAgentsFile(root, as, st); err != nil {
		return runner.Prepared{}, err
	}
	if err := w.runProfiles(ctx, as, root, &st); err != nil {
		return runner.Prepared{}, err
	}
	if err := saveState(root, st); err != nil {
		return runner.Prepared{}, err
	}
	scratch := filepath.Join(w.cfg.Home, "scratch", as.Workspace.Name)
	if as.Role == protocol.RoleAccept {
		scratch = filepath.Join(w.cfg.Home, "scratch", ".accept", as.RunID)
	}
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		return runner.Prepared{}, fmt.Errorf("gitws: scratch dir: %w", err)
	}
	p := runner.Prepared{Dir: root, ScratchDir: scratch}
	if as.Role == protocol.RoleImplement {
		for _, m := range as.Workspace.Mounts {
			ms := st.Mounts[m.Path]
			p.Report.Mounts = append(p.Report.Mounts, protocol.ResolvedMount{
				Path: m.Path, ResolvedBaseSHA: ms.Base, ResolvedFrom: ms.From, Branch: ms.Branch,
			})
		}
	}
	return p, nil
}

func (w *Workspaces) layoutImplementation(ctx context.Context, as protocol.Assignment, root string, st *state) error {
	branch := as.Workspace.Branch
	for _, m := range as.Workspace.Mounts {
		dir := filepath.Join(root, m.Path)
		if ms, ok := st.Mounts[m.Path]; ok {
			if ms.Repo != m.Repo || ms.Branch != branch {
				return fmt.Errorf("gitws: mount %s holds %s on %s, not %s on %s", m.Path, ms.Repo, ms.Branch, m.Repo, branch)
			}
			if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
				continue
			}
		}
		// New mounts start from the current remote (D36).
		remote, err := w.ensureClone(ctx, m.Repo, m.RemoteURL, true)
		if err != nil {
			return err
		}
		if m.BaseCommit != "" && !w.hasCommit(ctx, m.Repo, m.BaseCommit) {
			// Commits not reachable from a branch need fetching by hash.
			_, _ = w.git.run(ctx, w.repoPath(m.Repo), "fetch", "--quiet", "origin", m.BaseCommit)
		}
		intent := BaseIntent{Branch: m.BaseBranch, Commit: m.BaseCommit, Candidates: m.BaseCandidates}
		base, from, err := ResolveBase(intent, remote, func(sha string) bool { return w.hasCommit(ctx, m.Repo, sha) })
		if err != nil {
			return fmt.Errorf("mount %s: %w", m.Path, err)
		}
		if err := w.addWorktree(ctx, m.Repo, dir, branch, base); err != nil {
			return err
		}
		head, err := w.git.run(ctx, dir, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		st.Mounts[m.Path] = mountState{Repo: m.Repo, Base: base, From: from, Branch: branch, Head: head}
		// Save each mount as it appears, so a later failure does not leave
		// worktrees the next attempt cannot account for.
		if err := saveState(root, *st); err != nil {
			return err
		}
	}
	return nil
}

// addWorktree puts the task branch of repo at dir, creating the branch at
// base if it does not exist. A worktree already at dir on the branch, left
// by an attempt that failed before saving its state, is adopted.
func (w *Workspaces) addWorktree(ctx context.Context, repo, dir, branch, base string) error {
	unlock, err := w.lock(ctx, "repo:"+repo)
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		if b, err := w.git.run(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil && b == branch {
			return nil
		}
		return fmt.Errorf("gitws: %s exists but is not a worktree on %s", dir, branch)
	}
	exists, err := w.git.test(ctx, w.repoPath(repo), "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if err != nil {
		return err
	}
	if exists {
		// The branch outlived its worktree; continue on it.
		_, err = w.git.run(ctx, w.repoPath(repo), "worktree", "add", "--quiet", dir, branch)
	} else {
		_, err = w.git.run(ctx, w.repoPath(repo), "worktree", "add", "--quiet", "-b", branch, dir, base)
	}
	return err
}

// layoutAcceptance checks that the task's workspace holds the artifact
// under acceptance: each mount on the recorded branch at the recorded
// HEAD. Acceptance runs in the workspace as the implementation left it
// (D41); this checks tend's own record, so the verdict is about the
// artifact, and limits nothing the worker does. Uncommitted files are not
// compared: prepare profiles regenerate files and an earlier acceptance
// may leave build output, both of which D41 allows.
func (w *Workspaces) layoutAcceptance(ctx context.Context, as protocol.Assignment, root string, st *state) error {
	var artifact protocol.ArtifactPayload
	if err := json.Unmarshal(as.Artifact, &artifact); err != nil {
		return fmt.Errorf("gitws: decode artifact: %w", err)
	}
	for _, m := range artifact.Mounts {
		dir := filepath.Join(root, m.Path)
		if _, ok := st.Mounts[m.Path]; !ok {
			return fmt.Errorf("gitws: the workspace has no mount %s to review", m.Path)
		}
		branch, _ := w.git.run(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD")
		head, err := w.git.run(ctx, dir, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		switch {
		case branch != m.Branch:
			return fmt.Errorf("%w: mount %s is on %q, not on the artifact's %q", ErrWorkspaceMoved, m.Path, branch, m.Branch)
		case head != m.CommitSHA:
			return fmt.Errorf("%w: mount %s is at %s, not at the artifact's %s", ErrWorkspaceMoved, m.Path, head, m.CommitSHA)
		}
	}
	return nil
}

// mountNow is a mount as it is: the branch HEAD is on ("" when
// detached), HEAD, and the tree of its working tree.
type mountNow struct {
	branch, head, tree string
}

// read looks at a mount without changing it. The working tree is staged
// into a scratch copy of the index, so the worktree's own index stays as
// the worker left it; ignored files stay out, as git add leaves them.
func (w *Workspaces) read(ctx context.Context, root, dir string) (mountNow, error) {
	var now mountNow
	if b, err := w.git.run(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		now.branch = b
	}
	head, err := w.git.run(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return now, fmt.Errorf("gitws: read mount %s: %w", dir, err)
	}
	now.head = head
	index, err := w.git.run(ctx, dir, "rev-parse", "--git-path", "index")
	if err != nil {
		return now, err
	}
	if !filepath.IsAbs(index) {
		index = filepath.Join(dir, index)
	}
	if err := os.MkdirAll(filepath.Join(root, ".tend"), 0o755); err != nil {
		return now, fmt.Errorf("gitws: state dir: %w", err)
	}
	sum := sha256.Sum256([]byte(dir))
	scratch := filepath.Join(root, ".tend", "index-"+hex.EncodeToString(sum[:8]))
	defer os.Remove(scratch)
	g := w.git.with("GIT_INDEX_FILE=" + scratch)
	// Starting from the worktree's index lets git reuse what it already
	// hashed; without one, start from HEAD. The copy keeps the index's
	// time, which git compares with the files' to catch edits made in the
	// same instant as the index (racy git).
	if b, err := os.ReadFile(index); err == nil {
		info, err := os.Stat(index)
		if err != nil {
			return now, fmt.Errorf("gitws: copy index: %w", err)
		}
		if err := os.WriteFile(scratch, b, 0o644); err != nil {
			return now, fmt.Errorf("gitws: copy index: %w", err)
		}
		if err := os.Chtimes(scratch, info.ModTime(), info.ModTime()); err != nil {
			return now, fmt.Errorf("gitws: copy index: %w", err)
		}
	} else if _, err := g.run(ctx, dir, "read-tree", "HEAD"); err != nil {
		return now, err
	}
	if _, err := g.run(ctx, dir, "add", "--all"); err != nil {
		return now, err
	}
	if now.tree, err = g.run(ctx, dir, "write-tree"); err != nil {
		return now, err
	}
	return now, nil
}

// Collect implements runner.Workspaces. It records each mount as the
// worker left it (D57): HEAD as it is, and, when the working tree holds
// changes HEAD does not, a snapshot commit of them kept under the run's
// ref. Neither the branch, the index nor the files are changed.
func (w *Workspaces) Collect(ctx context.Context, as protocol.Assignment) (protocol.ArtifactPayload, error) {
	root := w.root(as)
	unlock, err := w.lock(ctx, "ws:"+root)
	if err != nil {
		return protocol.ArtifactPayload{}, err
	}
	defer unlock()
	st, err := loadState(root)
	if err != nil {
		return protocol.ArtifactPayload{}, err
	}
	var out protocol.ArtifactPayload
	changed := false
	for _, m := range as.Workspace.Mounts {
		dir := filepath.Join(root, m.Path)
		ms, ok := st.Mounts[m.Path]
		if !ok {
			return protocol.ArtifactPayload{}, fmt.Errorf("gitws: the workspace has no mount %s", m.Path)
		}
		now, err := w.read(ctx, root, dir)
		if err != nil {
			return protocol.ArtifactPayload{}, err
		}
		recorded, snapshot := now.head, ""
		headTree, err := w.git.run(ctx, dir, "rev-parse", now.head+"^{tree}")
		if err != nil {
			return protocol.ArtifactPayload{}, err
		}
		if now.tree != headTree {
			if snapshot, err = w.snapshot(ctx, dir, as, now); err != nil {
				return protocol.ArtifactPayload{}, err
			}
			recorded = snapshot
		}
		// Compare with the mount as it was before this run, also when this
		// run's collection is repeated. A new commit is a change even when
		// the files are not: the task may be to commit them.
		prevHead, prevTree := ms.Head, ms.Tree
		if ms.Run == as.RunID {
			prevHead, prevTree = ms.PrevHead, ms.PrevTree
		}
		if prevTree == "" {
			if prevTree, err = w.git.run(ctx, dir, "rev-parse", prevHead+"^{tree}"); err != nil {
				return protocol.ArtifactPayload{}, err
			}
		}
		mountChanged := now.head != prevHead || now.tree != prevTree
		if recorded != ms.Base {
			// Keep the recorded commits reachable whatever happens to the
			// branch.
			ref := "refs/tend/runs/" + as.RunID + "/" + hex.EncodeToString([]byte(m.Path))
			if _, err := w.git.run(ctx, dir, "update-ref", ref, recorded); err != nil {
				return protocol.ArtifactPayload{}, err
			}
		}
		ms.PrevHead, ms.PrevTree, ms.Head, ms.Tree, ms.Run = prevHead, prevTree, now.head, now.tree, as.RunID
		st.Mounts[m.Path] = ms
		if err := saveState(root, st); err != nil {
			return protocol.ArtifactPayload{}, err
		}
		changed = changed || mountChanged
		out.Mounts = append(out.Mounts, protocol.ArtifactMount{
			Repo: m.Repo, Path: m.Path, BaseSHA: ms.Base, Branch: now.branch,
			CommitSHA: now.head, SnapshotSHA: snapshot, Changed: mountChanged,
		})
	}
	if !changed {
		return protocol.ArtifactPayload{}, runner.ErrNoChanges
	}
	return out, nil
}

// snapshot stores the mount's working tree as a commit on top of HEAD,
// without moving any branch. It uses the machine's git identity with a
// fixed date, so collecting the same run again gives the same commit.
func (w *Workspaces) snapshot(ctx context.Context, dir string, as protocol.Assignment, now mountNow) (string, error) {
	g := w.git.with("GIT_AUTHOR_DATE=@0 +0000", "GIT_COMMITTER_DATE=@0 +0000")
	msg := fmt.Sprintf("tend: snapshot of the working tree\n\nTask: %s\nRun: %s", as.TaskID, as.RunID)
	sha, err := g.run(ctx, dir, "commit-tree", now.tree, "-p", now.head, "-m", msg)
	if err != nil {
		return "", fmt.Errorf("gitws: snapshot %s (is a git identity configured?): %w", dir, err)
	}
	return sha, nil
}

// Finish implements runner.Workspaces. Workspaces stay for the task's
// next run, whatever an acceptance run left in them (D41); only an
// acceptance run's scratch directory is removed.
func (w *Workspaces) Finish(_ context.Context, as protocol.Assignment) error {
	if as.Role != protocol.RoleAccept {
		return nil
	}
	if err := os.RemoveAll(filepath.Join(w.cfg.Home, "scratch", ".accept", as.RunID)); err != nil {
		return fmt.Errorf("gitws: remove scratch: %w", err)
	}
	return nil
}
