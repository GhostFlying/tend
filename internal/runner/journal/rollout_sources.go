package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

// RegisterRollout stores only native identities and locators; never transcript bodies.
func (j *Journal) RegisterRollout(ctx context.Context, deployment, run string, source protocol.RolloutPreviewSource) error {
	if deployment == "" || run == "" || source.SessionID == "" {
		return errors.New("journal: incomplete rollout identity")
	}
	return j.tx(ctx, func(tx *sql.Tx) error {
		var conflict int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM rollout_sources WHERE session_id=? AND (deployment_id<>? OR (run_id<>? AND turn_id=? AND turn_id<>''))`, source.SessionID, deployment, run, source.TurnID).Scan(&conflict); err != nil {
			return err
		}
		if conflict != 0 {
			return errors.New("journal: rollout source belongs to another execution")
		}
		if source.Path == "" {
			_ = tx.QueryRowContext(ctx, `SELECT path FROM rollout_sources WHERE deployment_id=? AND run_id=? AND session_id=? AND path<>'' ORDER BY ordinal LIMIT 1`, deployment, run, source.SessionID).Scan(&source.Path)
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO rollout_sources(deployment_id,run_id,session_id,turn_id,path) VALUES(?,?,?,?,?) ON CONFLICT(deployment_id,run_id,session_id,turn_id) DO UPDATE SET path=CASE WHEN excluded.path<>'' THEN excluded.path ELSE rollout_sources.path END`, deployment, run, source.SessionID, source.TurnID, source.Path)
		if err != nil {
			return err
		}
		return dirtyArchive(ctx, tx, run)
	})
}

func (j *Journal) RolloutSources(ctx context.Context, deployment, run string) ([]protocol.RolloutPreviewSource, error) {
	rows, err := j.db.QueryContext(ctx, `SELECT session_id,turn_id,path FROM rollout_sources WHERE deployment_id=? AND run_id=? ORDER BY ordinal`, deployment, run)
	if err != nil {
		return nil, fmt.Errorf("journal: read rollout sources: %w", err)
	}
	defer rows.Close()
	var out []protocol.RolloutPreviewSource
	for rows.Next() {
		var s protocol.RolloutPreviewSource
		if err := rows.Scan(&s.SessionID, &s.TurnID, &s.Path); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// HistoricalRolloutThread reads retained completed execution metadata as well as live runs.
func (j *Journal) HistoricalRolloutThread(ctx context.Context, run string) (string, bool, error) {
	var thread, role string
	err := j.db.QueryRowContext(ctx, `SELECT COALESCE(thread_id,''),json_extract(assignment_json,'$.role') FROM runs WHERE run_id=?`, run).Scan(&thread, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return thread, role == string(protocol.RoleIssueManager), err
}

// ProtectRollouts prevents Tend cleanup from removing registered originals or
// their native artifact directories, including originals inside a workspace.
func (j *Journal) ProtectRollouts(ctx context.Context, directory string) error {
	rows, err := j.db.QueryContext(ctx, `SELECT DISTINCT path FROM rollout_sources WHERE path<>''`)
	if err != nil {
		return err
	}
	defer rows.Close()
	contains := func(parent, child string) bool {
		rel, err := filepath.Rel(parent, child)
		return err == nil && filepath.IsLocal(rel)
	}
	canonical := func(path string) string {
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			return resolved
		}
		return filepath.Clean(path)
	}
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return err
		}
		artifact := strings.TrimSuffix(path, ".jsonl") + ".artifacts"
		for _, pair := range [][2]string{{directory, path}, {directory, artifact}, {artifact, directory}, {canonical(directory), canonical(path)}, {canonical(directory), canonical(artifact)}, {canonical(artifact), canonical(directory)}} {
			if contains(pair[0], pair[1]) {
				return errors.New("journal: cleanup would remove registered native rollout")
			}
		}
	}
	return rows.Err()
}
