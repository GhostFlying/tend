package store

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
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
	// Worker is the host's harness; empty means the app-server.
	Worker            protocol.WorkerKind
	WorkspaceProtocol int64
	Status            string
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
		worker, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?)
		ON CONFLICT (host_id) DO UPDATE SET os = excluded.os, arch = excluded.arch, labels_json = excluded.labels_json,
		repos_json = excluded.repos_json, slots = excluded.slots, token_hash = excluded.token_hash,
		nodes_json = excluded.nodes_json, worker = excluded.worker, status = 'active', updated_at = excluded.updated_at`,
		h.HostID, h.OS, h.Arch, jsonList(h.Labels), jsonList(h.Repos), h.Slots, h.TokenHash, jsonList(h.Nodes), cmp.Or(h.Worker, protocol.WorkerAppServer), now, now)
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
	err := q.QueryRowContext(ctx, `SELECT os, arch, labels_json, repos_json, slots, token_hash, nodes_json, worker, status, workspace_protocol
		FROM hosts WHERE host_id = ?`, hostID).Scan(&h.OS, &h.Arch, &labels, &repos, &h.Slots, &h.TokenHash, &nodes, &h.Worker, &h.Status, &h.WorkspaceProtocol)
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

// ProjectRow is a configured project (D58).
type ProjectRow struct {
	Project  string
	Title    string
	Weight   int64
	Repos    []string
	Template string
	Retired  bool
}

// UpsertProject registers a project or updates its registration; a
// project configured again is no longer retired.
func (t *Tx) UpsertProject(ctx context.Context, p ProjectRow, now int64) error {
	_, err := t.Exec(ctx, `INSERT INTO projects (project, title, weight, repos_json, template, retired, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 0, ?, ?) ON CONFLICT (project) DO UPDATE SET title = excluded.title,
		weight = excluded.weight, repos_json = excluded.repos_json, template = excluded.template, retired = 0,
		updated_at = excluded.updated_at`,
		p.Project, p.Title, p.Weight, jsonList(p.Repos), nullString(p.Template), now, now)
	if err != nil {
		return fmt.Errorf("store: upsert project: %w", err)
	}
	return nil
}

// RetireProjectsExcept retires every project not in keep. Their tasks go
// on; new tasks cannot join them.
func (t *Tx) RetireProjectsExcept(ctx context.Context, keep []string, now int64) error {
	_, err := t.Exec(ctx, `UPDATE projects SET retired = 1, updated_at = ?
		WHERE retired = 0 AND project NOT IN (SELECT value FROM json_each(?))`, now, jsonList(keep))
	if err != nil {
		return fmt.Errorf("store: retire projects: %w", err)
	}
	return nil
}

// ListProjects returns every project, retired ones included, by name.
func ListProjects(ctx context.Context, q Querier) ([]ProjectRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT project, title, weight, repos_json, template, retired FROM projects ORDER BY project`)
	if err != nil {
		return nil, fmt.Errorf("store: list projects: %w", err)
	}
	defer rows.Close()
	var out []ProjectRow
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetProject returns a project, or ErrNotFound.
func GetProject(ctx context.Context, q Querier, project string) (ProjectRow, error) {
	p, err := scanProject(q.QueryRowContext(ctx, `SELECT project, title, weight, repos_json, template, retired
		FROM projects WHERE project = ?`, project))
	if err != nil {
		return ProjectRow{}, notFound(err, "project "+project)
	}
	return p, nil
}

func scanProject(s interface{ Scan(...any) error }) (ProjectRow, error) {
	var (
		p        ProjectRow
		repos    string
		template sql.NullString
	)
	if err := s.Scan(&p.Project, &p.Title, &p.Weight, &repos, &template, &p.Retired); err != nil {
		return ProjectRow{}, fmt.Errorf("store: scan project: %w", err)
	}
	p.Template = template.String
	var err error
	if p.Repos, err = parseList(repos); err != nil {
		return ProjectRow{}, err
	}
	return p, nil
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

// TaskByWorkspaceName returns the task that reserved a workspace name, or
// ErrNotFound.
func TaskByWorkspaceName(ctx context.Context, q Querier, name string) (string, error) {
	var id string
	if err := q.QueryRowContext(ctx, `SELECT task_id FROM tasks WHERE workspace_name = ?`, name).Scan(&id); err != nil {
		return "", notFound(err, "task with workspace "+name)
	}
	return id, nil
}

// BindAgentSession records that an agent session called a tend tool. The
// first call binds it; later ones only mark it seen.
func (t *Tx) BindAgentSession(ctx context.Context, agent, sessionID, clientID string, now int64) error {
	_, err := t.Exec(ctx, `INSERT INTO agent_sessions (agent, session_id, client_id, first_seen_at, last_seen_at, bound_at)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (agent, session_id) DO UPDATE SET client_id = excluded.client_id,
		last_seen_at = excluded.last_seen_at, bound_at = COALESCE(agent_sessions.bound_at, excluded.bound_at)`,
		agent, sessionID, clientID, now, now, now)
	if err != nil {
		return fmt.Errorf("store: bind agent session: %w", err)
	}
	return nil
}

// AgentSessionBound reports whether an agent session has called a tend
// tool.
func AgentSessionBound(ctx context.Context, q Querier, agent, sessionID string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_sessions WHERE agent = ? AND session_id = ? AND bound_at IS NOT NULL`,
		agent, sessionID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("store: read agent session: %w", err)
	}
	return n > 0, nil
}

// How a session's project was set (D59).
const (
	ProjectAuto     = "auto"
	ProjectExplicit = "explicit"
)

// SessionProject is the project an agent session works in. An empty
// Source means tend has not bound the session yet; a binding with an
// empty Project means every project.
type SessionProject struct {
	Project string
	Source  string
}

// GetSessionProject returns an agent session's project. A session tend has
// not recorded has none.
func GetSessionProject(ctx context.Context, q Querier, agent, sessionID string) (SessionProject, error) {
	var project, source sql.NullString
	err := q.QueryRowContext(ctx, `SELECT project, project_source FROM agent_sessions WHERE agent = ? AND session_id = ?`,
		agent, sessionID).Scan(&project, &source)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionProject{}, nil
	}
	if err != nil {
		return SessionProject{}, fmt.Errorf("store: read session project: %w", err)
	}
	return SessionProject{Project: project.String, Source: source.String}, nil
}

// SetSessionProject records the project of a recorded agent session.
func (t *Tx) SetSessionProject(ctx context.Context, agent, sessionID string, sp SessionProject) error {
	_, err := t.Exec(ctx, `UPDATE agent_sessions SET project = ?, project_source = ? WHERE agent = ? AND session_id = ?`,
		nullString(sp.Project), nullString(sp.Source), agent, sessionID)
	if err != nil {
		return fmt.Errorf("store: set session project: %w", err)
	}
	return nil
}

// UpsertAgentSession records that an agent session was seen.
func (t *Tx) UpsertAgentSession(ctx context.Context, agent, sessionID, clientID string, now int64) error {
	_, err := t.Exec(ctx, `INSERT INTO agent_sessions (agent, session_id, client_id, first_seen_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT (agent, session_id) DO UPDATE SET client_id = excluded.client_id,
		last_seen_at = excluded.last_seen_at`, agent, sessionID, clientID, now, now)
	if err != nil {
		return fmt.Errorf("store: upsert agent session: %w", err)
	}
	return nil
}
