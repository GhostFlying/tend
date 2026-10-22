package store

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
)

// DeliverableFileRow is a durable immutable result file.
type DeliverableFileRow struct {
	protocol.ArtifactFile
	TaskID    string
	RunID     string
	Content   []byte
	CreatedAt int64
}

// InsertDeliverableFile stores bytes and their manifest atomically.
func (t *Tx) InsertDeliverableFile(ctx context.Context, f DeliverableFileRow) error {
	_, err := t.tx.ExecContext(ctx, `INSERT INTO deliverable_files(file_id,task_id,run_id,name,size,sha256,content,created_at) VALUES(?,?,?,?,?,?,?,?)`, f.FileID, f.TaskID, f.RunID, f.Name, f.Size, f.SHA256, f.Content, f.CreatedAt)
	return err
}

// GetDeliverableFile loads an immutable uploaded file.
func GetDeliverableFile(ctx context.Context, q Querier, id string) (DeliverableFileRow, error) {
	var f DeliverableFileRow
	err := q.QueryRowContext(ctx, `SELECT file_id,task_id,run_id,name,size,sha256,content,created_at FROM deliverable_files WHERE file_id=?`, id).Scan(&f.FileID, &f.TaskID, &f.RunID, &f.Name, &f.Size, &f.SHA256, &f.Content, &f.CreatedAt)
	if err != nil {
		return DeliverableFileRow{}, notFound(err, "file "+id)
	}
	return f, nil
}

// RunFileBytes bounds the complete durable upload set of one run.
func RunFileBytes(ctx context.Context, q Querier, runID string) (int64, error) {
	var size int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(size),0) FROM deliverable_files WHERE run_id=?`, runID).Scan(&size)
	return size, err
}

// ReadFinalizedDeliverableFile returns a bounded byte window only after an
// immutable artifact references the upload. Content contains the window;
// Size remains the complete file length. Offsets are zero-based byte offsets.
func ReadFinalizedDeliverableFile(ctx context.Context, q Querier, id string, offset int64, count int) (DeliverableFileRow, error) {
	if offset < 0 || count <= 0 || count > 65536 {
		return DeliverableFileRow{}, protocol.Errorf(protocol.CodeInvalidRequest, "file offset or read size is out of range")
	}
	var f DeliverableFileRow
	err := q.QueryRowContext(ctx, `SELECT f.file_id,f.task_id,f.run_id,f.name,f.size,f.sha256,
 substr(f.content, MIN(?, f.size)+1, ?),f.created_at
 FROM deliverable_files f WHERE f.file_id=? AND EXISTS (
 SELECT 1 FROM artifacts a, json_each(a.ref_json, '$.files') entry
 WHERE a.task_id=f.task_id AND a.run_id=f.run_id
 AND json_extract(entry.value, '$.file_id')=f.file_id
 )`, offset, count, id).Scan(&f.FileID, &f.TaskID, &f.RunID, &f.Name, &f.Size, &f.SHA256, &f.Content, &f.CreatedAt)
	if err != nil {
		return DeliverableFileRow{}, notFound(err, "finalized file "+id)
	}
	return f, nil
}
