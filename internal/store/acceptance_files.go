package store

import (
	"context"
	"database/sql"
	"fmt"
)

// SetAcceptanceFiles binds already validated manifests in the verdict transaction.
// The original evidence JSON and uploaded bytes remain independent immutable facts.
func (t *Tx) SetAcceptanceFiles(ctx context.Context, acceptanceID, filesJSON string) error {
	result, err := t.Exec(ctx, `UPDATE acceptances SET files_json=? WHERE acceptance_id=?`, filesJSON, acceptanceID)
	if err != nil {
		return fmt.Errorf("store: bind acceptance evidence files: %w", err)
	}
	if count, err := result.RowsAffected(); err != nil {
		return err
	} else if count != 1 {
		return notFound(sql.ErrNoRows, "acceptance "+acceptanceID)
	}
	return nil
}

// GetFinalizedAcceptanceFile resolves exact manifest membership in a submitted
// verdict. An unreferenced upload is not evidence available to other runs.
func GetFinalizedAcceptanceFile(ctx context.Context, q Querier, id string) (DeliverableFileRow, AcceptanceRow, error) {
	f, err := GetDeliverableFile(ctx, q, id)
	if err != nil {
		return DeliverableFileRow{}, AcceptanceRow{}, err
	}
	var a AcceptanceRow
	err = q.QueryRowContext(ctx, `SELECT a.acceptance_id,a.task_id,a.run_id,a.spec_version,a.artifact_id,a.verdict,a.evidence_json,a.issues_json,a.files_json,a.created_at
 FROM acceptances a,json_each(a.files_json) entry
 WHERE a.task_id=? AND a.run_id=?
 AND json_extract(entry.value,'$.file_id')=?
 AND json_extract(entry.value,'$.name')=?
 AND json_extract(entry.value,'$.size')=?
 AND json_extract(entry.value,'$.sha256')=?
 ORDER BY a.created_at,a.acceptance_id LIMIT 1`, f.TaskID, f.RunID, f.FileID, f.Name, f.Size, f.SHA256).Scan(
		&a.AcceptanceID, &a.TaskID, &a.RunID, &a.SpecVersion, &a.ArtifactID, &a.Verdict, &a.EvidenceJSON, &a.IssuesJSON, &a.FilesJSON, &a.CreatedAt)
	if err != nil {
		return DeliverableFileRow{}, AcceptanceRow{}, notFound(err, "finalized acceptance file "+id)
	}
	return f, a, nil
}
