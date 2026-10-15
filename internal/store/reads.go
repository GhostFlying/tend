package store

import (
	"context"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

// MapNativeRequest returns the request ID of a native identity, allocating
// newID for it on first sight.
func (t *Tx) MapNativeRequest(ctx context.Context, source, nativeKey, newID string, now int64) (string, error) {
	if _, err := t.Exec(ctx, `INSERT INTO native_requests (source, native_key, request_id, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (source, native_key) DO NOTHING`, source, nativeKey, newID, now); err != nil {
		return "", fmt.Errorf("store: map native request: %w", err)
	}
	var id string
	if err := t.Q().QueryRowContext(ctx, `SELECT request_id FROM native_requests WHERE source = ? AND native_key = ?`, source, nativeKey).Scan(&id); err != nil {
		return "", fmt.Errorf("store: read native request: %w", err)
	}
	return id, nil
}

// ListTasks returns tasks, unfinished ones first, then the most recently
// updated finished ones, up to limit in total.
func ListTasks(ctx context.Context, q Querier, limit int) ([]TaskRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT task_id FROM tasks
		ORDER BY lifecycle IN ('completed', 'cancelled'), priority DESC, updated_at DESC, task_id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list tasks: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("store: scan task: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("store: iterate: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := make([]TaskRow, 0, len(ids))
	for _, id := range ids {
		r, err := GetTask(ctx, q, id)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// ListRunsByTask returns a task's runs in creation order.
func ListRunsByTask(ctx context.Context, q Querier, taskID string) ([]RunRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+runColumns+` FROM runs WHERE task_id = ? ORDER BY created_at, run_id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("store: list runs: %w", err)
	}
	defer rows.Close()
	var out []RunRow
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan run: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ArtifactRow is a stored artifact.
type ArtifactRow struct {
	ArtifactID string
	TaskID     string
	RunID      string
	Kind       string
	RefJSON    string
	Digest     string
	CreatedAt  int64
}

const artifactColumns = `artifact_id, task_id, run_id, kind, ref_json, digest, created_at`

func scanArtifact(row interface{ Scan(...any) error }) (ArtifactRow, error) {
	var a ArtifactRow
	err := row.Scan(&a.ArtifactID, &a.TaskID, &a.RunID, &a.Kind, &a.RefJSON, &a.Digest, &a.CreatedAt)
	return a, err
}

// GetArtifact returns an artifact, or ErrNotFound.
func GetArtifact(ctx context.Context, q Querier, artifactID string) (ArtifactRow, error) {
	a, err := scanArtifact(q.QueryRowContext(ctx, `SELECT `+artifactColumns+` FROM artifacts WHERE artifact_id = ?`, artifactID))
	if err != nil {
		return ArtifactRow{}, notFound(err, "artifact "+artifactID)
	}
	return a, nil
}

// ListArtifactsByTask returns a task's artifacts in creation order.
func ListArtifactsByTask(ctx context.Context, q Querier, taskID string) ([]ArtifactRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+artifactColumns+` FROM artifacts WHERE task_id = ? ORDER BY created_at, artifact_id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("store: list artifacts: %w", err)
	}
	defer rows.Close()
	var out []ArtifactRow
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan artifact: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AcceptanceRow is a stored verdict.
type AcceptanceRow struct {
	AcceptanceID string
	TaskID       string
	RunID        string
	SpecVersion  int64
	ArtifactID   string
	Verdict      protocol.Verdict
	EvidenceJSON string
	IssuesJSON   string
	CreatedAt    int64
}

// ListAcceptances returns the verdicts of a task, or of one artifact when
// artifactID is set, in creation order.
func ListAcceptances(ctx context.Context, q Querier, taskID, artifactID string) ([]AcceptanceRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT acceptance_id, task_id, run_id, spec_version, artifact_id, verdict, evidence_json,
		issues_json, created_at FROM acceptances WHERE task_id = ? AND (? = '' OR artifact_id = ?) ORDER BY created_at, acceptance_id`,
		taskID, artifactID, artifactID)
	if err != nil {
		return nil, fmt.Errorf("store: list acceptances: %w", err)
	}
	defer rows.Close()
	var out []AcceptanceRow
	for rows.Next() {
		var a AcceptanceRow
		if err := rows.Scan(&a.AcceptanceID, &a.TaskID, &a.RunID, &a.SpecVersion, &a.ArtifactID, &a.Verdict, &a.EvidenceJSON,
			&a.IssuesJSON, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: scan acceptance: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListDecisions returns a task's decisions, or every open decision when
// taskID is empty, in creation order.
func ListDecisions(ctx context.Context, q Querier, taskID string) ([]DecisionRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT decision_id FROM decisions
		WHERE (? <> '' AND task_id = ?) OR (? = '' AND status = 'open') ORDER BY created_at, decision_id`, taskID, taskID, taskID)
	if err != nil {
		return nil, fmt.Errorf("store: list decisions: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("store: scan decision: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("store: iterate: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := make([]DecisionRow, 0, len(ids))
	for _, id := range ids {
		d, err := GetDecision(ctx, q, id)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// ListRepos returns every registered repository by name.
func ListRepos(ctx context.Context, q Querier) ([]RepoRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT repo FROM repos ORDER BY repo`)
	if err != nil {
		return nil, fmt.Errorf("store: list repos: %w", err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("store: iterate: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := make([]RepoRow, 0, len(names))
	for _, n := range names {
		r, err := GetRepo(ctx, q, n)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// ListTemplates returns the workspace templates' JSON by name.
func ListTemplates(ctx context.Context, q Querier) (map[string]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT name, workspace_json FROM workspace_templates ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("store: list templates: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var n, body string
		if err := rows.Scan(&n, &body); err != nil {
			return nil, err
		}
		out[n] = body
	}
	return out, rows.Err()
}

// ListActiveHosts returns the active hosts by ID.
func ListActiveHosts(ctx context.Context, q Querier) ([]HostRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT host_id FROM hosts WHERE status = 'active' ORDER BY host_id`)
	if err != nil {
		return nil, fmt.Errorf("store: list hosts: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("store: iterate: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := make([]HostRow, 0, len(ids))
	for _, id := range ids {
		h, err := GetHost(ctx, q, id)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, nil
}
