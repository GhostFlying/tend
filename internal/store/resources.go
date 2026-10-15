package store

import (
	"context"
	"encoding/json"
	"fmt"
)

// HostRow is a configured execution host.
type HostRow struct {
	HostID    string
	OS        string
	Arch      string
	Labels    []string
	Repos     []string
	Slots     int64
	TokenHash string
	Nodes     []string
	Status    string
}

// Host states.
const (
	HostActive  = "active"
	HostRetired = "retired"
)

func jsonList(v []string) string {
	if v == nil {
		v = []string{}
	}
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // a string slice always encodes
	}
	return string(b)
}

func parseList(s string) ([]string, error) {
	var v []string
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, fmt.Errorf("store: decode list: %w", err)
	}
	return v, nil
}

// UpsertHost creates or updates a configured host and marks it active.
func (t *Tx) UpsertHost(ctx context.Context, h HostRow, now int64) error {
	_, err := t.Exec(ctx, `INSERT INTO hosts (host_id, os, arch, labels_json, repos_json, slots, token_hash, nodes_json,
		status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?)
		ON CONFLICT (host_id) DO UPDATE SET os = excluded.os, arch = excluded.arch, labels_json = excluded.labels_json,
		repos_json = excluded.repos_json, slots = excluded.slots, token_hash = excluded.token_hash,
		nodes_json = excluded.nodes_json, status = 'active', updated_at = excluded.updated_at`,
		h.HostID, h.OS, h.Arch, jsonList(h.Labels), jsonList(h.Repos), h.Slots, h.TokenHash, jsonList(h.Nodes), now, now)
	if err != nil {
		return fmt.Errorf("store: upsert host: %w", err)
	}
	return nil
}

// RetireHostsExcept retires every active host not in keep.
func (t *Tx) RetireHostsExcept(ctx context.Context, keep []string, now int64) error {
	_, err := t.Exec(ctx, `UPDATE hosts SET status = 'retired', updated_at = ?
		WHERE status = 'active' AND host_id NOT IN (SELECT value FROM json_each(?))`, now, jsonList(keep))
	if err != nil {
		return fmt.Errorf("store: retire hosts: %w", err)
	}
	return nil
}

// GetHost returns a host, or ErrNotFound.
func GetHost(ctx context.Context, q Querier, hostID string) (HostRow, error) {
	h := HostRow{HostID: hostID}
	var labels, repos, nodes string
	err := q.QueryRowContext(ctx, `SELECT os, arch, labels_json, repos_json, slots, token_hash, nodes_json, status
		FROM hosts WHERE host_id = ?`, hostID).Scan(&h.OS, &h.Arch, &labels, &repos, &h.Slots, &h.TokenHash, &nodes, &h.Status)
	if err != nil {
		return HostRow{}, notFound(err, "host "+hostID)
	}
	if h.Labels, err = parseList(labels); err != nil {
		return HostRow{}, err
	}
	if h.Repos, err = parseList(repos); err != nil {
		return HostRow{}, err
	}
	if h.Nodes, err = parseList(nodes); err != nil {
		return HostRow{}, err
	}
	return h, nil
}

// RepoRow is a registered repository.
type RepoRow struct {
	Repo           string
	RemoteURL      string
	BaseCandidates []string
	Retired        bool
}

// UpsertRepo registers a repository or updates its registration.
func (t *Tx) UpsertRepo(ctx context.Context, r RepoRow, now int64) error {
	_, err := t.Exec(ctx, `INSERT INTO repos (repo, remote_url, base_candidates_json, retired, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (repo) DO UPDATE SET remote_url = excluded.remote_url,
		base_candidates_json = excluded.base_candidates_json, retired = excluded.retired, updated_at = excluded.updated_at`,
		r.Repo, r.RemoteURL, jsonList(r.BaseCandidates), r.Retired, now, now)
	if err != nil {
		return fmt.Errorf("store: upsert repo: %w", err)
	}
	return nil
}

// RetireReposExcept retires every repository not in keep. Retired
// repositories stay referenced by existing specs but cannot be mounted by
// new ones.
func (t *Tx) RetireReposExcept(ctx context.Context, keep []string, now int64) error {
	_, err := t.Exec(ctx, `UPDATE repos SET retired = 1, updated_at = ?
		WHERE retired = 0 AND repo NOT IN (SELECT value FROM json_each(?))`, now, jsonList(keep))
	if err != nil {
		return fmt.Errorf("store: retire repos: %w", err)
	}
	return nil
}

// GetRepo returns a repository, or ErrNotFound.
func GetRepo(ctx context.Context, q Querier, repo string) (RepoRow, error) {
	r := RepoRow{Repo: repo}
	var candidates string
	err := q.QueryRowContext(ctx, `SELECT remote_url, base_candidates_json, retired FROM repos WHERE repo = ?`, repo).
		Scan(&r.RemoteURL, &candidates, &r.Retired)
	if err != nil {
		return RepoRow{}, notFound(err, "repo "+repo)
	}
	if r.BaseCandidates, err = parseList(candidates); err != nil {
		return RepoRow{}, err
	}
	return r, nil
}

// PutTemplates replaces the workspace templates with the given set, keyed by
// name with the workspace JSON as value.
func (t *Tx) PutTemplates(ctx context.Context, templates map[string]string, now int64) error {
	if _, err := t.Exec(ctx, `DELETE FROM workspace_templates`); err != nil {
		return fmt.Errorf("store: clear templates: %w", err)
	}
	for name, body := range templates {
		if _, err := t.Exec(ctx, `INSERT INTO workspace_templates (name, workspace_json, updated_at) VALUES (?, ?, ?)`,
			name, body, now); err != nil {
			return fmt.Errorf("store: insert template: %w", err)
		}
	}
	return nil
}

// GetTemplate returns a template's workspace JSON, or ErrNotFound.
func GetTemplate(ctx context.Context, q Querier, name string) (string, error) {
	var body string
	if err := q.QueryRowContext(ctx, `SELECT workspace_json FROM workspace_templates WHERE name = ?`, name).Scan(&body); err != nil {
		return "", notFound(err, "template "+name)
	}
	return body, nil
}

// WorkspaceNameTaken reports whether a task already reserved the name.
func WorkspaceNameTaken(ctx context.Context, q Querier, name string) (bool, error) {
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE workspace_name = ?`, name).Scan(&n); err != nil {
		return false, fmt.Errorf("store: check workspace name: %w", err)
	}
	return n > 0, nil
}
