package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

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
	Rating              protocol.Rating
	Project             string
	Title               string
	// Weight is the weight of the task's project.
	Weight           int64
	DependenciesDone bool
	// NamedExecutionAssigned refers only to this task and current spec, never
	// a reused workspace owner or an earlier execution agreement.
	NamedExecutionAssigned  bool
	WorkspaceLegacyBindings bool
	WorkspaceBackendID      string
	WorkspaceHost           string
	WorkspaceOccupied       bool
	// AbandonedHosts are the hosts the task gave up a workspace on.
	AbandonedHosts []string
	SpecJSON       string
}

// ListClaimCandidates returns every pending implement or accept action.
func ListClaimCandidates(ctx context.Context, q Querier) ([]CandidateRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT a.action_id, a.task_id, a.kind, a.effect_key, a.trigger_json,
		a.target_spec_version, a.target_artifact_id, a.authorization_json, a.generation, a.status, a.plan_commit_ref,
		a.created_request_id, a.created_at,
		t.lifecycle, t.control_mode, t.block_reason, t.spec_version, t.implement_generation,
		COALESCE(t.current_artifact_id, ''), t.rating, t.project, t.title,
		COALESCE((SELECT p.weight FROM projects p WHERE p.project = t.project), 1),
		NOT EXISTS (SELECT 1 FROM task_dependencies d JOIN tasks dt ON dt.task_id = d.depends_on
			WHERE d.task_id = t.task_id AND dt.lifecycle <> 'completed'),
		COALESCE((SELECT w.host_id FROM task_workspace_bindings b JOIN workspaces w ON w.workspace_id=b.workspace_id WHERE b.task_id=t.task_id), ''),
		COALESCE((SELECT CASE WHEN w.backend_id<>'' THEN w.backend_id WHEN w.path<>'' AND w.provider_id<>'' THEN 'orca' WHEN w.path<>'' THEN 'git' ELSE '' END FROM task_workspace_bindings b JOIN workspaces w ON w.workspace_id=b.workspace_id WHERE b.task_id=t.task_id), ''),
        EXISTS (SELECT 1 FROM runs nr JOIN run_agent_executions ne ON ne.run_id=nr.run_id
          JOIN plan_actions na ON na.action_id=nr.action_id
          WHERE nr.task_id=t.task_id AND na.target_spec_version=t.spec_version),
        EXISTS (SELECT 1 FROM task_workspace_bindings current_binding
          JOIN workspaces shared ON shared.workspace_id=current_binding.workspace_id
          JOIN task_workspace_bindings peer ON peer.workspace_id=shared.workspace_id
          JOIN tasks peer_task ON peer_task.task_id=peer.task_id
          JOIN task_specs peer_spec ON peer_spec.task_id=peer_task.task_id AND peer_spec.spec_version=peer_task.spec_version
          WHERE current_binding.task_id=t.task_id AND shared.backend_id=''
          AND peer.task_id<>t.task_id AND peer_task.lifecycle NOT IN ('completed','cancelled')
          AND json_extract(peer_spec.body_json,'$.agent_selection') IS NULL),
		EXISTS (SELECT 1 FROM task_workspace_bindings b JOIN workspaces w ON w.workspace_id=b.workspace_id
			WHERE b.task_id=t.task_id AND (w.status IN ('removing','removed') OR (w.occupied_by IS NOT NULL AND w.occupied_by<>t.task_id)))
		OR EXISTS (SELECT 1 FROM runs r JOIN workspace_runs rb ON rb.run_id=r.run_id
			JOIN task_workspace_bindings tb ON tb.workspace_id=rb.workspace_id
			WHERE tb.task_id=t.task_id AND r.observed_state IN `+openRunStates+`),
		(SELECT json_group_array(ab.host_id) FROM abandoned_workspaces ab WHERE ab.task_id = t.task_id),
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
		var abandoned string
		a := &c.Action
		if err := rows.Scan(&a.ActionID, &a.TaskID, &a.Kind, &a.EffectKey, &a.TriggerJSON, &a.TargetSpecVersion, &artifact,
			&a.AuthorizationJSON, &a.Generation, &a.Status, &commit, &a.CreatedRequestID, &a.CreatedAt,
			&c.Lifecycle, &c.Control, &c.Block, &c.SpecVersion, &c.ImplementGeneration, &c.CurrentArtifactID, &c.Rating, &c.Project, &c.Title, &c.Weight,
			&c.DependenciesDone, &c.WorkspaceHost, &c.WorkspaceBackendID, &c.NamedExecutionAssigned, &c.WorkspaceLegacyBindings, &c.WorkspaceOccupied, &abandoned, &c.SpecJSON); err != nil {
			return nil, fmt.Errorf("store: scan claim candidate: %w", err)
		}
		if c.AbandonedHosts, err = parseList(abandoned); err != nil {
			return nil, err
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

// CountOpenRunsByProject counts, by project, the runs that may still be
// working.
func CountOpenRunsByProject(ctx context.Context, q Querier) (map[string]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT t.project, COUNT(*) FROM runs r JOIN tasks t ON t.task_id = r.task_id
		WHERE r.observed_state IN `+openRunStates+` GROUP BY t.project`)
	if err != nil {
		return nil, fmt.Errorf("store: count open runs by project: %w", err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var p string
		var n int64
		if err := rows.Scan(&p, &n); err != nil {
			return nil, fmt.Errorf("store: scan open runs: %w", err)
		}
		out[p] = n
	}
	return out, rows.Err()
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

// SeeHost records that the host was heard from, unless that was recorded
// less than a minute ago. Claims call it on every poll.
func (t *Tx) SeeHost(ctx context.Context, hostID string, now int64) error {
	_, err := t.Exec(ctx, `UPDATE hosts SET last_seen_at = ? WHERE host_id = ? AND COALESCE(last_seen_at, 0) < ?`,
		now, hostID, now-time.Minute.Milliseconds())
	if err != nil {
		return fmt.Errorf("store: see host: %w", err)
	}
	return nil
}

// TouchHost records that the host was heard from.
func (t *Tx) TouchHost(ctx context.Context, hostID string, now int64) error {
	_, err := t.Exec(ctx, `UPDATE hosts SET last_seen_at = ? WHERE host_id = ?`, now, hostID)
	if err != nil {
		return fmt.Errorf("store: touch host: %w", err)
	}
	return nil
}

// WorkspaceHostRow is an unfinished task with a workspace, and that
// workspace's host.
type WorkspaceHostRow struct {
	TaskID     string
	HostID     string
	HostStatus string
	// LastSeenAt is 0 for a host never heard from.
	LastSeenAt int64
	Block      protocol.BlockReason
	// Waiting reports that the task's work waits on the host: an
	// implementation or acceptance to be claimed, or a run on the host
	// that tend lost track of.
	Waiting bool
}

// ListWorkspaceHosts returns the unfinished tasks that have a workspace,
// with what their workspace's host looks like.
func ListWorkspaceHosts(ctx context.Context, q Querier) ([]WorkspaceHostRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT t.task_id, w.host_id, h.status, COALESCE(h.last_seen_at, 0), t.block_reason,
		EXISTS (SELECT 1 FROM plan_actions a WHERE a.task_id = t.task_id AND a.status = 'pending' AND a.kind IN ('implement', 'accept'))
		OR EXISTS (SELECT 1 FROM runs r WHERE r.task_id = t.task_id AND r.host_id = w.host_id AND r.observed_state = 'unknown')
		FROM tasks t JOIN task_workspace_bindings b ON b.task_id = t.task_id JOIN workspaces w ON w.workspace_id = b.workspace_id JOIN hosts h ON h.host_id = w.host_id
		WHERE t.lifecycle NOT IN ('completed', 'cancelled') ORDER BY t.task_id`)
	if err != nil {
		return nil, fmt.Errorf("store: list workspace hosts: %w", err)
	}
	defer rows.Close()
	var out []WorkspaceHostRow
	for rows.Next() {
		var r WorkspaceHostRow
		if err := rows.Scan(&r.TaskID, &r.HostID, &r.HostStatus, &r.LastSeenAt, &r.Block, &r.Waiting); err != nil {
			return nil, fmt.Errorf("store: scan workspace host: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
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
	Directory       string
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
		branch, directory, added_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		workspaceID, m.Path, m.Repo, m.BaseJSON, m.ResolvedBaseSHA, m.ResolvedFrom, m.Branch, m.Directory, now)
	if err != nil {
		return fmt.Errorf("store: insert mount fact: %w", err)
	}
	return nil
}

// ListMountFacts returns the recorded mounts of a workspace by path.
func ListMountFacts(ctx context.Context, q Querier, workspaceID string) (map[string]MountFact, error) {
	rows, err := q.QueryContext(ctx, `SELECT path, repo, base_json, resolved_base_sha, resolved_from, branch, directory
		FROM workspace_mounts WHERE workspace_id = ?`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("store: list mount facts: %w", err)
	}
	defer rows.Close()
	out := map[string]MountFact{}
	for rows.Next() {
		var m MountFact
		if err := rows.Scan(&m.Path, &m.Repo, &m.BaseJSON, &m.ResolvedBaseSHA, &m.ResolvedFrom, &m.Branch, &m.Directory); err != nil {
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

// CountOpenRunsOnRetiredHosts prevents maintenance from excluding an execution
// that still depends on a host omitted from the enabled upgrade participants.
func CountOpenRunsOnRetiredHosts(ctx context.Context, q Querier) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs r JOIN hosts h ON h.host_id=r.host_id
 WHERE h.status <> 'active' AND r.observed_state IN `+openRunStates).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count retired host runs: %w", err)
	}
	return n, nil
}

// RecordMountDirectory adds a physical location without erasing it when an older
// runner reports only the original logical mount fields.
func (t *Tx) RecordMountDirectory(ctx context.Context, workspaceID, path, directory string) error {
	if directory == "" {
		return nil
	}
	_, err := t.Exec(ctx, `UPDATE workspace_mounts SET directory=? WHERE workspace_id=? AND path=?`, directory, workspaceID, path)
	if err != nil {
		return fmt.Errorf("store: record mount directory: %w", err)
	}
	return nil
}
