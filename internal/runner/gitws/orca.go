package gitws

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/harness/orca"
	"github.com/GhostFlying/tend/internal/protocol"
)

// NewOrca returns Workspaces that lay out single-repository workspaces as
// orca worktrees (D63, D65). The repository must already be registered in
// orca, selected by Repo.Orca; tend creates worktrees in it but never
// registers or removes repositories.
func NewOrca(cfg Config, client *orca.Client) *Workspaces {
	w := New(cfg)
	w.layout = orcaLayout{w: w, orca: client, poll: 5 * time.Second}
	return w
}

// orcaLayout checks the only mount out as an orca worktree, on the branch
// orca names, and starts the worker in it. The runner's state stays in the
// workspace directory, out of the repository.
type orcaLayout struct {
	w    *Workspaces
	orca *orca.Client
	// poll is how often await looks for the worktree.
	poll time.Duration
}

func (orcaLayout) branch(protocol.Assignment) string { return "" }

func (orcaLayout) root(home string, st state) string {
	for _, ms := range st.Mounts {
		if ms.Dir != "" {
			return ms.Dir
		}
	}
	return home
}

func (l orcaLayout) add(ctx context.Context, as protocol.Assignment, m protocol.AssignedMount, home string, st *state) (mountState, error) {
	w := l.w
	if len(as.Workspace.Mounts) != 1 {
		return mountState{}, fmt.Errorf("workspace_prepare_failed: orca lays out workspaces with one mount, not %d", len(as.Workspace.Mounts))
	}
	selector := w.repo(m.Repo).Orca
	if selector == "" {
		return mountState{}, fmt.Errorf("workspace_prepare_failed: repo %s has no orca repository on this runner", m.Repo)
	}
	repo, err := l.orca.Repo(ctx, selector)
	if err != nil {
		return mountState{}, fmt.Errorf("workspace_prepare_failed: orca repository %s: %w", selector, err)
	}
	// Serialize with other runs fetching or creating worktrees in the
	// same repository.
	unlock, err := w.lock(ctx, "orca:"+repo.ID)
	if err != nil {
		return mountState{}, err
	}
	defer unlock()
	// An attempt that asked orca for the worktree recorded the base first;
	// keep it, and only take up a worktree that matches it.
	pending, asked := st.Mounts[m.Path]
	asked = asked && pending.Base != ""
	if !asked {
		pending, err = l.resolve(ctx, repo, m)
		if err != nil {
			return mountState{}, err
		}
	}
	wt, found, err := l.findResource(ctx, repo, as, pending)
	if err == nil && !found && pending.Asked != 0 {
		// The request may have reached orca and still be running: stopping
		// the CLI does not stop orca. Look again for a while before asking
		// for another, which orca would create beside it (invariant 4).
		if as.Workspace.ID != "" {
			return mountState{}, fmt.Errorf("workspace_prepare_failed: create outcome unknown; reconcile provider before retry")
		}
		wt, found, err = l.await(ctx, repo, as.Workspace.Name, pending.Asked)
	}
	switch {
	case err != nil:
		return mountState{}, err
	case found && (!asked || wt.BaseRef != pending.Base):
		// The name is tend's, but the worktree is not one tend asked for.
		return mountState{}, fmt.Errorf("workspace_prepare_failed: orca already has a worktree named %s at %s that tend did not create from %s; remove or rename it",
			as.Workspace.Name, wt.Path, pending.Base)
	case !found:
		pending.Asked = w.cfg.Now().UnixMilli()
		st.Mounts[m.Path] = pending
		if err := saveState(home, *st); err != nil {
			return mountState{}, err
		}
		// orca does not refuse a name it already has but picks another, so
		// the record above and the lookup are what keep this idempotent.
		wt, err = l.orca.CreateWorktree(ctx, orca.CreateWorktree{Repo: "id:" + repo.ID, Name: as.Workspace.Name, BaseBranch: pending.Base, Comment: resourceMarker(as)})
		if err != nil {
			if !orca.Unconfirmed(err) {
				// orca refused it: nothing was created.
				pending.Asked = 0
				st.Mounts[m.Path] = pending
				_ = saveState(home, *st)
			}
			return mountState{}, fmt.Errorf("workspace_prepare_failed: create the orca worktree: %w", err)
		}
	}
	if wt.Path == "" || wt.BranchName() == "" {
		return mountState{}, fmt.Errorf("workspace_prepare_failed: orca worktree %s has no path or branch", wt.ID)
	}
	head, err := w.git.run(ctx, wt.Path, "rev-parse", "HEAD")
	if err != nil {
		return mountState{}, err
	}
	ms := pending
	ms.Dir, ms.Branch, ms.Head, ms.Asked = wt.Path, wt.BranchName(), head, 0
	ms.ProviderID, ms.ProviderInstanceID = wt.ID, wt.InstanceID
	return ms, nil
}

// createSettle is how long after asking orca for a worktree the runner
// still waits for it to appear before asking again.
const createSettle = 2 * time.Minute

// await looks for the worktree until createSettle has passed since asked.
func (l orcaLayout) await(ctx context.Context, repo orca.Repo, name string, asked int64) (orca.Worktree, bool, error) {
	for {
		wait := time.UnixMilli(asked).Add(createSettle).Sub(l.w.cfg.Now())
		if wait <= 0 {
			return orca.Worktree{}, false, nil
		}
		select {
		case <-ctx.Done():
			return orca.Worktree{}, false, context.Cause(ctx)
		case <-time.After(min(wait, l.poll)):
		}
		if wt, found, err := l.find(ctx, repo, name); err != nil || found {
			return wt, found, err
		}
	}
}

// resolve fetches origin into the orca repository and resolves the
// mount's base there (D36).
func (l orcaLayout) resolve(ctx context.Context, repo orca.Repo, m protocol.AssignedMount) (mountState, error) {
	w := l.w
	if url, err := w.git.run(ctx, repo.Path, "remote", "get-url", "origin"); err == nil && url != m.RemoteURL && w.cfg.Log != nil {
		w.cfg.Log.WarnContext(ctx, "the orca repository's origin is not the registered remote",
			"repo", m.Repo, "origin", redact(url), "registered", redact(m.RemoteURL))
	}
	// Every branch, whatever the owner's checkout is configured to fetch,
	// so that a base or candidate branch is never missed; the fetch leaves
	// the configuration alone.
	remote, err := w.fetch(ctx, repo.Path, "+refs/heads/*:refs/remotes/origin/*")
	if err != nil {
		return mountState{}, err
	}
	if m.BaseCommit != "" && !w.hasCommitIn(ctx, repo.Path, m.BaseCommit) {
		// Commits not reachable from a branch need fetching by hash.
		_, _ = w.git.run(ctx, repo.Path, "fetch", "--quiet", "origin", m.BaseCommit)
	}
	intent := BaseIntent{Branch: m.BaseBranch, Commit: m.BaseCommit, Candidates: m.BaseCandidates}
	base, from, err := ResolveBase(intent, remote, func(sha string) bool { return w.hasCommitIn(ctx, repo.Path, sha) })
	if err != nil {
		return mountState{}, fmt.Errorf("mount %s: %w", m.Path, err)
	}
	return mountState{Repo: m.Repo, Base: base, From: from}, nil
}

// find returns the worktree of the workspace if orca already has it.
func (l orcaLayout) find(ctx context.Context, repo orca.Repo, name string) (orca.Worktree, bool, error) {
	all, err := l.orca.Worktrees(ctx, "id:"+repo.ID)
	if err != nil {
		return orca.Worktree{}, false, fmt.Errorf("workspace_prepare_failed: list orca worktrees: %w", err)
	}
	var found []orca.Worktree
	for _, wt := range all {
		if !wt.IsMainWorktree && wt.DisplayName == name {
			found = append(found, wt)
		}
	}
	switch len(found) {
	case 0:
		return orca.Worktree{}, false, nil
	case 1:
		return found[0], true, nil
	}
	var paths []string
	for _, wt := range found {
		paths = append(paths, wt.Path)
	}
	return orca.Worktree{}, false, fmt.Errorf("workspace_prepare_failed: orca has %d worktrees named %s (%s); remove the extra ones",
		len(found), name, strings.Join(paths, ", "))
}

func resourceMarker(as protocol.Assignment) string {
	if as.Workspace.ID == "" {
		return ""
	}
	return "tend workspace " + as.Workspace.ID
}

func (l orcaLayout) findResource(ctx context.Context, repo orca.Repo, as protocol.Assignment, pending mountState) (orca.Worktree, bool, error) {
	if as.Workspace.ID == "" {
		return l.find(ctx, repo, as.Workspace.Name)
	}
	if pending.ProviderID != "" {
		wt, err := l.orca.Worktree(ctx, "id:"+pending.ProviderID)
		if err != nil {
			return orca.Worktree{}, false, err
		}
		if wt.Path != pending.Dir || pending.ProviderInstanceID != "" && wt.InstanceID != pending.ProviderInstanceID {
			return orca.Worktree{}, false, fmt.Errorf("workspace_prepare_failed: provider identity changed")
		}
		return wt, true, nil
	}
	if as.Workspace.ID == "" {
		return l.find(ctx, repo, as.Workspace.Name)
	}
	all, err := l.orca.Worktrees(ctx, "id:"+repo.ID)
	if err != nil {
		return orca.Worktree{}, false, err
	}
	var found orca.Worktree
	for _, wt := range all {
		if !wt.IsMainWorktree && wt.Comment == resourceMarker(as) {
			if found.ID != "" {
				return orca.Worktree{}, false, fmt.Errorf("workspace_prepare_failed: duplicate provider resource marker")
			}
			found = wt
		}
	}
	return found, found.ID != "", nil
}
