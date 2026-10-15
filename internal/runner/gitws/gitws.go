// Package gitws lays out run workspaces with git worktrees (D30, D33-D37).
//
// Every registered repository has one bare clone under $TEND_HOME/repos.
// A task's workspace is a directory under $TEND_HOME/workspaces holding one
// worktree per mount, all on the task branch; the worker starts in the
// workspace root, which is not itself a repository. An acceptance run gets
// a throwaway workspace with the artifact's commits checked out detached.
//
// The runner, not the worker, owns git history: it creates the worktrees,
// checks after each run that the worker did not move HEAD, add worktrees
// or change git configuration, and commits the changes. Progress is saved
// after every step, so a failure halfway leaves a workspace the next run
// can pick up.
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
	"slices"
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

// ErrTampered means the worker changed git history, worktrees or git
// configuration, which only the runner may do (D33).
var ErrTampered = runner.ErrTampered

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

func (w *Workspaces) root(as protocol.Assignment) string {
	if as.Role == protocol.RoleAccept {
		return filepath.Join(w.cfg.Home, "workspaces", ".accept", as.RunID)
	}
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
	// Head is the commit the runner expects the worktree to be at.
	Head string `json:"head"`
	// Config is the hash of the git configuration the worktree had when
	// the runner handed it to a worker.
	Config string `json:"config"`
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

// configHash fingerprints the git configuration a worktree sees from its
// repository, leaving out the remote URL, which the runner itself keeps
// in line with the registration.
func (w *Workspaces) configHash(ctx context.Context, dir string) (string, error) {
	out, err := w.git.run(ctx, dir, "config", "--list", "--local", "--show-origin")
	if err != nil {
		return "", err
	}
	var kept []string
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "\tremote.origin.url=") {
			kept = append(kept, line)
		}
	}
	if wt, err := w.git.run(ctx, dir, "config", "--list", "--worktree"); err == nil {
		kept = append(kept, "worktree:"+wt)
	}
	sum := sha256.Sum256([]byte(strings.Join(kept, "\n")))
	return hex.EncodeToString(sum[:]), nil
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
	// Fingerprint the configuration only now, after the runner's own
	// steps, as the state the worker receives.
	for path, ms := range st.Mounts {
		if ms.Config, err = w.configHash(ctx, filepath.Join(root, path)); err != nil {
			return runner.Prepared{}, err
		}
		st.Mounts[path] = ms
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

func (w *Workspaces) layoutAcceptance(ctx context.Context, as protocol.Assignment, root string, st *state) error {
	var artifact protocol.ArtifactPayload
	if err := json.Unmarshal(as.Artifact, &artifact); err != nil {
		return fmt.Errorf("gitws: decode artifact: %w", err)
	}
	urls := map[string]string{}
	for _, m := range as.Workspace.Mounts {
		urls[m.Path] = m.RemoteURL
	}
	for _, m := range artifact.Mounts {
		dir := filepath.Join(root, m.Path)
		if _, ok := st.Mounts[m.Path]; ok {
			continue
		}
		if _, err := w.ensureClone(ctx, m.Repo, urls[m.Path], false); err != nil {
			return err
		}
		if !w.hasCommit(ctx, m.Repo, m.CommitSHA) {
			// Artifacts are local to the host that produced them until
			// they can be transferred.
			return fmt.Errorf("gitws: artifact commit %s of %s is not on this host", m.CommitSHA, m.Path)
		}
		unlock, err := w.lock(ctx, "repo:"+m.Repo)
		if err != nil {
			return err
		}
		_, err = w.git.run(ctx, w.repoPath(m.Repo), "worktree", "add", "--quiet", "--detach", dir, m.CommitSHA)
		unlock()
		if err != nil {
			return err
		}
		st.Mounts[m.Path] = mountState{Repo: m.Repo, Base: m.BaseSHA, From: "artifact", Head: m.CommitSHA}
		if err := saveState(root, *st); err != nil {
			return err
		}
	}
	return nil
}

// Collect implements runner.Workspaces.
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
	for _, m := range as.Workspace.Mounts {
		if err := w.checkIntact(ctx, root, as, m, &st); err != nil {
			return protocol.ArtifactPayload{}, err
		}
	}
	var out protocol.ArtifactPayload
	changed := false
	for _, m := range as.Workspace.Mounts {
		dir := filepath.Join(root, m.Path)
		ms := st.Mounts[m.Path]
		if _, err := w.git.run(ctx, dir, "add", "--all"); err != nil {
			return protocol.ArtifactPayload{}, err
		}
		clean, err := w.git.test(ctx, dir, "diff", "--cached", "--quiet")
		if err != nil {
			return protocol.ArtifactPayload{}, err
		}
		if !clean {
			// Hooks are the worker's and the acceptance run's business;
			// the runner's commit only records the result.
			if _, err := w.git.run(ctx, dir, "commit", "--quiet", "--no-verify", "-m", commitMessage(as)); err != nil {
				return protocol.ArtifactPayload{}, fmt.Errorf("gitws: commit %s (is a git identity configured?): %w", m.Path, err)
			}
			head, err := w.git.run(ctx, dir, "rev-parse", "HEAD")
			if err != nil {
				return protocol.ArtifactPayload{}, err
			}
			ms.Head = head
			st.Mounts[m.Path] = ms
			if err := saveState(root, st); err != nil {
				return protocol.ArtifactPayload{}, err
			}
			changed = true
		}
		if ms.Head != ms.Base {
			// Keep the commit reachable whatever happens to the branch.
			ref := "refs/tend/runs/" + as.RunID + "/" + hex.EncodeToString([]byte(m.Path))
			if _, err := w.git.run(ctx, dir, "update-ref", ref, ms.Head); err != nil {
				return protocol.ArtifactPayload{}, err
			}
		}
		out.Mounts = append(out.Mounts, protocol.ArtifactMount{
			Repo: m.Repo, Path: m.Path, BaseSHA: ms.Base, Branch: ms.Branch, CommitSHA: ms.Head, Changed: !clean,
		})
	}
	if !changed {
		return protocol.ArtifactPayload{}, runner.ErrNoChanges
	}
	return out, nil
}

func commitMessage(as protocol.Assignment) string {
	return fmt.Sprintf("tend: %s\n\nTask: %s\nRun: %s", as.Workspace.Name, as.TaskID, as.RunID)
}

// checkIntact verifies that a mount is still on its branch at the commit
// the runner left it at, with the configuration it had, and that the
// workspace holds no worktree of the mount's repository besides the
// recorded mounts. A commit this run's own collection made but could not
// record before stopping is recognized and adopted.
func (w *Workspaces) checkIntact(ctx context.Context, root string, as protocol.Assignment, m protocol.AssignedMount, st *state) error {
	ms := st.Mounts[m.Path]
	dir := filepath.Join(root, m.Path)
	branch, err := w.git.run(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || branch != ms.Branch {
		return fmt.Errorf("%w: mount %s is not on %s", ErrTampered, m.Path, ms.Branch)
	}
	head, err := w.git.run(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if head != ms.Head {
		parent, perr := w.git.run(ctx, dir, "rev-parse", "HEAD^")
		msg, merr := w.git.run(ctx, dir, "log", "-1", "--format=%B", "HEAD")
		if perr != nil || merr != nil || parent != ms.Head || strings.TrimSpace(msg) != commitMessage(as) {
			return fmt.Errorf("%w: mount %s moved from %s to %s", ErrTampered, m.Path, ms.Head, head)
		}
		ms.Head = head
		st.Mounts[m.Path] = ms
	}
	if config, err := w.configHash(ctx, dir); err != nil || config != ms.Config {
		return fmt.Errorf("%w: git configuration of mount %s changed", ErrTampered, m.Path)
	}
	list, err := w.git.run(ctx, w.repoPath(m.Repo), "worktree", "list", "--porcelain")
	if err != nil {
		return err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("gitws: resolve workspace: %w", err)
	}
	for _, line := range strings.Split(list, "\n") {
		path, ok := strings.CutPrefix(line, "worktree ")
		if !ok {
			continue
		}
		if real, err := filepath.EvalSymlinks(path); err == nil {
			path = real
		}
		rel, err := filepath.Rel(realRoot, path)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			continue
		}
		if _, ok := st.Mounts[rel]; !ok {
			return fmt.Errorf("%w: unexpected worktree %s", ErrTampered, path)
		}
	}
	return nil
}

// Finish implements runner.Workspaces. Implementation workspaces stay for
// the task's next run; acceptance workspaces are removed.
func (w *Workspaces) Finish(ctx context.Context, as protocol.Assignment) error {
	if as.Role != protocol.RoleAccept {
		return nil
	}
	root := w.root(as)
	unlock, err := w.lock(ctx, "ws:"+root)
	if err != nil {
		return err
	}
	defer unlock()
	st, err := loadState(root)
	if err != nil {
		return err
	}
	paths := make([]string, 0, len(st.Mounts))
	for path := range st.Mounts {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	var errs []error
	for _, path := range paths {
		repo := st.Mounts[path].Repo
		unlockRepo, err := w.lock(ctx, "repo:"+repo)
		if err != nil {
			return err
		}
		_, err = w.git.run(ctx, w.repoPath(repo), "worktree", "remove", "--force", filepath.Join(root, path))
		unlockRepo()
		errs = append(errs, err)
	}
	errs = append(errs, os.RemoveAll(root), os.RemoveAll(filepath.Join(w.cfg.Home, "scratch", ".accept", as.RunID)))
	return errors.Join(errs...)
}
