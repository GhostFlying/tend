package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

// openRunStates are the observed states of runs that may still be working:
// they hold a slot and, for implement runs, the task's workspace.
const openRunStates = `('starting', 'running', 'waiting', 'interrupted', 'unknown')`

// CandidateRow is a pending runnable action with the facts the scheduler
// needs.
type CandidateRow struct {
	Action              ActionRow
	Lifecycle           protocol.TaskLifecycle
	Control             protocol.ControlMode
	Block               protocol.BlockReason
	SpecVersion         int64
	ImplementGeneration int64
	CurrentArtifactID   string
	Priority            int64
	DependenciesDone    bool
	WorkspaceHost       string
	WorkspaceOccupied   bool
	SpecJSON            string
}

// ListClaimCandidates returns every pending implement or accept action.
func ListClaimCandidates(ctx context.Context, q Querier) ([]CandidateRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT a.action_id, a.task_id, a.kind, a.effect_key, a.trigger_json,
		a.target_spec_version, a.target_artifact_id, a.authorization_json, a.generation, a.status, a.plan_commit_ref,
		a.created_request_id, a.created_at,
		t.lifecycle, t.control_mode, t.block_reason, t.spec_version, t.implement_generation,
		COALESCE(t.current_artifact_id, ''), t.priority,
		NOT EXISTS (SELECT 1 FROM task_dependencies d JOIN tasks dt ON dt.task_id = d.depends_on
			WHERE d.task_id = t.task_id AND dt.lifecycle <> 'completed'),
		COALESCE((SELECT w.host_id FROM workspaces w WHERE w.task_id = t.task_id), ''),
		EXISTS (SELECT 1 FROM runs r WHERE r.task_id = t.task_id AND r.observed_state IN `+openRunStates+`),
		s.body_json
		FROM plan_actions a
		JOIN tasks t ON t.task_id = a.task_id
		JOIN task_specs s ON s.task_id = t.task_id AND s.spec_version = t.spec_version
		WHERE a.status = 'pending' AND a.kind IN ('implement', 'accept')`)
	if err != nil {
		return nil, fmt.Errorf("store: list claim candidates: %w", err)
	}
	defer rows.Close()
	var out []CandidateRow
	for rows.Next() {
		var c CandidateRow
		var artifact, commit sql.NullString
		a := &c.Action
		if err := rows.Scan(&a.ActionID, &a.TaskID, &a.Kind, &a.EffectKey, &a.TriggerJSON, &a.TargetSpecVersion, &artifact,
			&a.AuthorizationJSON, &a.Generation, &a.Status, &commit, &a.CreatedRequestID, &a.CreatedAt,
			&c.Lifecycle, &c.Control, &c.Block, &c.SpecVersion, &c.ImplementGeneration, &c.CurrentArtifactID, &c.Priority,
			&c.DependenciesDone, &c.WorkspaceHost, &c.WorkspaceOccupied, &c.SpecJSON); err != nil {
			return nil, fmt.Errorf("store: scan claim candidate: %w", err)
		}
		a.TargetArtifactID, a.PlanCommitRef = artifact.String, commit.String
		out = append(out, c)
	}
	return out, rows.Err()
}

// CountOpenRuns returns how many runs on the host may still be working.
func CountOpenRuns(ctx context.Context, q Querier, hostID string) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE host_id = ? AND observed_state IN `+openRunStates, hostID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count open runs: %w", err)
	}
	return n, nil
}

// ListOpenRunsOnHost returns the runs allocated to a host that may still be
// working, oldest first.
func ListOpenRunsOnHost(ctx context.Context, q Querier, hostID string) ([]RunRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+runColumns+` FROM runs
		WHERE host_id = ? AND observed_state IN `+openRunStates+` ORDER BY created_at, run_id`, hostID)
	if err != nil {
		return nil, fmt.Errorf("store: list open runs: %w", err)
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

// SetActionStatus moves an action to a new status.
func (t *Tx) SetActionStatus(ctx context.Context, actionID string, status protocol.ActionStatus, now int64) error {
	_, err := t.Exec(ctx, `UPDATE plan_actions SET status = ?, updated_at = ? WHERE action_id = ?`, status, now, actionID)
	if err != nil {
		return fmt.Errorf("store: set action status: %w", err)
	}
	return nil
}

// Lease states.
const (
	LeaseActive   = "active"
	LeaseLapsed   = "lapsed"
	LeaseReleased = "released"
)

// LeaseRow is a run lease.
type LeaseRow struct {
	LeaseID   string
	RunID     string
	HostID    string
	ExpiresAt int64
	Status    string
}

// InsertLease stores a new active lease.
func (t *Tx) InsertLease(ctx context.Context, l LeaseRow, now int64) error {
	_, err := t.Exec(ctx, `INSERT INTO leases (lease_id, run_id, host_id, expires_at, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'active', ?, ?)`, l.LeaseID, l.RunID, l.HostID, l.ExpiresAt, now, now)
	if err != nil {
		return fmt.Errorf("store: insert lease: %w", err)
	}
	return nil
}

// GetLeaseByRun returns the lease of a run, or ErrNotFound.
func GetLeaseByRun(ctx context.Context, q Querier, runID string) (LeaseRow, error) {
	l := LeaseRow{RunID: runID}
	err := q.QueryRowContext(ctx, `SELECT lease_id, host_id, expires_at, status FROM leases WHERE run_id = ?`, runID).
		Scan(&l.LeaseID, &l.HostID, &l.ExpiresAt, &l.Status)
	if err != nil {
		return LeaseRow{}, notFound(err, "lease of run "+runID)
	}
	return l, nil
}

// SetLease changes a lease's status and expiry if it is still in the given
// status. It returns ErrStale otherwise.
func (t *Tx) SetLease(ctx context.Context, leaseID, from, to string, expiresAt, now int64) error {
	res, err := t.Exec(ctx, `UPDATE leases SET status = ?, expires_at = ?, updated_at = ? WHERE lease_id = ? AND status = ?`,
		to, expiresAt, now, leaseID, from)
	if err != nil {
		return fmt.Errorf("store: set lease: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return ErrStale
	}
	return nil
}

// ListExpiredLeases returns active leases that expired before now.
func ListExpiredLeases(ctx context.Context, q Querier, now int64) ([]LeaseRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT lease_id, run_id, host_id, expires_at, status FROM leases
		WHERE status = 'active' AND expires_at < ? ORDER BY expires_at`, now)
	if err != nil {
		return nil, fmt.Errorf("store: list expired leases: %w", err)
	}
	defer rows.Close()
	var out []LeaseRow
	for rows.Next() {
		var l LeaseRow
		if err := rows.Scan(&l.LeaseID, &l.RunID, &l.HostID, &l.ExpiresAt, &l.Status); err != nil {
			return nil, fmt.Errorf("store: scan lease: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// TouchHost records that the host was heard from.
func (t *Tx) TouchHost(ctx context.Context, hostID string, now int64) error {
	_, err := t.Exec(ctx, `UPDATE hosts SET last_seen_at = ? WHERE host_id = ?`, now, hostID)
	if err != nil {
		return fmt.Errorf("store: touch host: %w", err)
	}
	return nil
}

// WorkspaceRow is a task's workspace on a host.
type WorkspaceRow struct {
	WorkspaceID string
	TaskID      string
	Name        string
	HostID      string
	Status      string
}

// Workspace states.
const (
	WorkspaceAssigned = "assigned"
	WorkspaceReady    = "ready"
)

// InsertWorkspace records a task's workspace on a host.
func (t *Tx) InsertWorkspace(ctx context.Context, w WorkspaceRow, now int64) error {
	_, err := t.Exec(ctx, `INSERT INTO workspaces (workspace_id, task_id, name, host_id, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, w.WorkspaceID, w.TaskID, w.Name, w.HostID, w.Status, now, now)
	if err != nil {
		return fmt.Errorf("store: insert workspace: %w", err)
	}
	return nil
}

// GetWorkspaceByTask returns a task's workspace, or ErrNotFound.
func GetWorkspaceByTask(ctx context.Context, q Querier, taskID string) (WorkspaceRow, error) {
	w := WorkspaceRow{TaskID: taskID}
	err := q.QueryRowContext(ctx, `SELECT workspace_id, name, host_id, status FROM workspaces WHERE task_id = ?`, taskID).
		Scan(&w.WorkspaceID, &w.Name, &w.HostID, &w.Status)
	if err != nil {
		return WorkspaceRow{}, notFound(err, "workspace of task "+taskID)
	}
	return w, nil
}

// SetWorkspaceStatus changes a workspace's status.
func (t *Tx) SetWorkspaceStatus(ctx context.Context, workspaceID, status string, now int64) error {
	_, err := t.Exec(ctx, `UPDATE workspaces SET status = ?, updated_at = ? WHERE workspace_id = ?`, status, now, workspaceID)
	if err != nil {
		return fmt.Errorf("store: set workspace status: %w", err)
	}
	return nil
}

// MountFact is the recorded start of one mount.
type MountFact struct {
	Path            string
	Repo            string
	BaseJSON        string
	ResolvedBaseSHA string
	ResolvedFrom    string
	Branch          string
}

// InsertMountFact records how a mount was laid out. A mount is recorded
// once; a second record for the same path is a unique violation.
func (t *Tx) InsertMountFact(ctx context.Context, workspaceID string, m MountFact, now int64) error {
	_, err := t.Exec(ctx, `INSERT INTO workspace_mounts (workspace_id, path, repo, base_json, resolved_base_sha, resolved_from,
		branch, added_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		workspaceID, m.Path, m.Repo, m.BaseJSON, m.ResolvedBaseSHA, m.ResolvedFrom, m.Branch, now)
	if err != nil {
		return fmt.Errorf("store: insert mount fact: %w", err)
	}
	return nil
}

// ListMountFacts returns the recorded mounts of a workspace by path.
func ListMountFacts(ctx context.Context, q Querier, workspaceID string) (map[string]MountFact, error) {
	rows, err := q.QueryContext(ctx, `SELECT path, repo, base_json, resolved_base_sha, resolved_from, branch
		FROM workspace_mounts WHERE workspace_id = ?`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("store: list mount facts: %w", err)
	}
	defer rows.Close()
	out := map[string]MountFact{}
	for rows.Next() {
		var m MountFact
		if err := rows.Scan(&m.Path, &m.Repo, &m.BaseJSON, &m.ResolvedBaseSHA, &m.ResolvedFrom, &m.Branch); err != nil {
			return nil, fmt.Errorf("store: scan mount fact: %w", err)
		}
		out[m.Path] = m
	}
	return out, rows.Err()
}

// InsertInbox records an external fact's identity. It reports false when the
// fact was already received.
func (t *Tx) InsertInbox(ctx context.Context, source, sourceEventID string, now int64) (bool, error) {
	res, err := t.Exec(ctx, `INSERT INTO inbox (source, source_event_id, received_at) VALUES (?, ?, ?)
		ON CONFLICT (source, source_event_id) DO NOTHING`, source, sourceEventID, now)
	if err != nil {
		return false, fmt.Errorf("store: insert inbox: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: insert inbox: %w", err)
	}
	return n == 1, nil
}

// InsertRunLog keeps a run fact that does not change state.
func (t *Tx) InsertRunLog(ctx context.Context, runID string, e protocol.RunEvent, now int64) error {
	body := string(e.Body)
	if body == "" {
		body = "{}"
	}
	_, err := t.Exec(ctx, `INSERT INTO run_logs (run_id, seq, native_id, kind, body_json, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		runID, e.Seq, nullString(e.NativeID), e.Kind, body, now)
	if err != nil {
		return fmt.Errorf("store: insert run log: %w", err)
	}
	return nil
}
