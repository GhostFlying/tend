package app

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// resolveProject returns the project a task joins: the one named, or,
// when none is, the project of the calling agent session, or else the
// only active project.
func resolveProject(ctx context.Context, q store.Querier, name string, self protocol.Principal) (store.ProjectRow, error) {
	if name == "" && self.Addon() && self.Thread != "" {
		sp, err := store.GetSessionProject(ctx, q, string(self.Agent), self.Thread)
		if err != nil {
			return store.ProjectRow{}, err
		}
		if p, err := store.GetProject(ctx, q, sp.Project); sp.Project != "" && err == nil && !p.Retired {
			return p, nil
		}
	}
	if name != "" {
		p, err := store.GetProject(ctx, q, name)
		if errors.Is(err, store.ErrNotFound) {
			return store.ProjectRow{}, protocol.Errorf(protocol.CodeInvalidRequest, "project %q does not exist", name)
		}
		if err != nil {
			return store.ProjectRow{}, err
		}
		if p.Retired {
			return store.ProjectRow{}, protocol.Errorf(protocol.CodeInvalidRequest, "project %q is retired", name)
		}
		return p, nil
	}
	all, err := store.ListProjects(ctx, q)
	if err != nil {
		return store.ProjectRow{}, err
	}
	var active []store.ProjectRow
	var names []string
	for _, p := range all {
		if !p.Retired {
			active = append(active, p)
			names = append(names, p.Project)
		}
	}
	if len(active) == 1 {
		return active[0], nil
	}
	return store.ProjectRow{}, protocol.Errorf(protocol.CodeInvalidRequest, "name the task's project, one of: %s", strings.Join(names, ", "))
}

// normalizeRemote reduces a git remote URL to host/path, so the forms git
// accepts for one repository compare equal: the scheme, user and port and
// a .git suffix are dropped, and the result is lowercased.
func normalizeRemote(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.TrimSuffix(strings.TrimSuffix(s, "/"), ".git")
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return s
		}
		return strings.TrimPrefix(u.Hostname()+"/"+strings.TrimPrefix(u.Path, "/"), "/")
	}
	// scp-like syntax, user@host:path or user@[host]:path; a colon after a
	// slash is part of a local path.
	if i := strings.Index(s, "]:"); i > 0 && !strings.Contains(s[:i], "/") {
		host := strings.TrimPrefix(s[strings.LastIndex(s[:i], "@")+1:i], "[")
		return host + "/" + strings.TrimPrefix(s[i+2:], "/")
	}
	if host, path, ok := strings.Cut(s, ":"); ok && !strings.Contains(host, "/") {
		if _, h, ok := strings.Cut(host, "@"); ok {
			host = h
		}
		return host + "/" + strings.TrimPrefix(path, "/")
	}
	return s
}

// projectForRemotes returns the active project that holds the repository
// at remotes, or "" when none or several do.
func projectForRemotes(ctx context.Context, q store.Querier, remotes []string) (string, error) {
	want := map[string]bool{}
	for _, r := range remotes {
		want[normalizeRemote(r)] = true
	}
	repos, err := store.ListRepos(ctx, q)
	if err != nil {
		return "", err
	}
	var matched []string
	for _, r := range repos {
		if want[normalizeRemote(r.RemoteURL)] {
			matched = append(matched, r.Repo)
		}
	}
	if len(matched) == 0 {
		return "", nil
	}
	projects, err := store.ListProjects(ctx, q)
	if err != nil {
		return "", err
	}
	found := ""
	for _, p := range projects {
		if p.Retired || !slices.ContainsFunc(p.Repos, func(r string) bool { return slices.Contains(matched, r) }) {
			continue
		}
		if found != "" {
			return "", nil
		}
		found = p.Project
	}
	return found, nil
}

// bindSessionProject binds a recorded agent session to the project of the
// repository it works in, unless the session chose its project. Remotes
// that say nothing, as from an older CLI or a failed git call, leave the
// binding as it is. Remotes that are only a hint, as the MCP bridge's from
// the directory the agent started it in, bind a session tend has not
// bound yet but do not override what a hook, which sees where the session
// is now, established. It reports whether the binding changed, which makes
// the session's context start over with a full snapshot.
func bindSessionProject(ctx context.Context, tx *store.Tx, self protocol.Principal, remotes []string, hint bool) (bool, error) {
	if len(remotes) == 0 {
		return false, nil
	}
	sp, err := store.GetSessionProject(ctx, tx.Q(), string(self.Agent), self.Thread)
	if err != nil || sp.Source == store.ProjectExplicit || (hint && sp.Source != "") {
		return false, err
	}
	project, err := projectForRemotes(ctx, tx.Q(), remotes)
	if err != nil {
		return false, err
	}
	// A session in a repository no single project holds is bound to no
	// project, which is still a binding: hints no longer apply.
	next := store.SessionProject{Project: project, Source: store.ProjectAuto}
	if next == sp {
		return false, nil
	}
	return true, tx.SetSessionProject(ctx, string(self.Agent), self.Thread, next)
}

// invalidateBaseline makes a session's next hook answer with a full
// snapshot.
func invalidateBaseline(ctx context.Context, tx *store.Tx, key string, now int64) error {
	base, err := store.GetBaseline(ctx, tx.Q(), key)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	base.Valid = false
	return tx.PutBaseline(ctx, base, now)
}

// activeProjects returns the names of the active projects, sorted.
func activeProjects(ctx context.Context, q store.Querier) ([]string, error) {
	all, err := store.ListProjects(ctx, q)
	var names []string
	for _, p := range all {
		if !p.Retired {
			names = append(names, p.Project)
		}
	}
	return names, err
}

// SessionScope returns the project an agent session's context and default
// listings cover, or "" for every project: its bound project while that is
// active (D59).
func SessionScope(ctx context.Context, q store.Querier, self protocol.Principal) (string, error) {
	if !self.Addon() || self.Thread == "" {
		return "", nil
	}
	sp, err := store.GetSessionProject(ctx, q, string(self.Agent), self.Thread)
	if err != nil || sp.Project == "" {
		return "", err
	}
	p, err := store.GetProject(ctx, q, sp.Project)
	if errors.Is(err, store.ErrNotFound) || (err == nil && p.Retired) {
		return "", nil
	}
	return sp.Project, err
}
