package store

import (
	"bytes"
	"context"
	"errors"

	"github.com/GhostFlying/tend/internal/blobstore"
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

// InsertDeliverableFile stores the manifest of an already staged original.
// Content remains a compatibility input for small in-process callers.
func (t *Tx) InsertDeliverableFile(ctx context.Context, f DeliverableFileRow) error {
	if f.Content != nil {
		if t.files == nil {
			return errors.New("store: deliverable file storage unavailable")
		}
		object, err := t.files.Put(ctx, bytes.NewReader(f.Content))
		if err != nil {
			return err
		}
		if object.SHA256 != f.SHA256 || object.Size != f.Size {
			return errors.New("store: deliverable original differs from metadata")
		}
	}
	_, err := t.tx.ExecContext(ctx, `INSERT INTO deliverable_files(file_id,task_id,run_id,name,size,sha256,content,created_at,externalized) VALUES(?,?,?,?,?,?,X'',?,1)`, f.FileID, f.TaskID, f.RunID, f.Name, f.Size, f.SHA256, f.CreatedAt)
	return err
}

// GetDeliverableFile loads metadata without reading original content.
func GetDeliverableFile(ctx context.Context, q Querier, id string) (DeliverableFileRow, error) {
	var f DeliverableFileRow
	err := q.QueryRowContext(ctx, `SELECT file_id,task_id,run_id,name,size,sha256,created_at FROM deliverable_files WHERE file_id=?`, id).Scan(&f.FileID, &f.TaskID, &f.RunID, &f.Name, &f.Size, &f.SHA256, &f.CreatedAt)
	if err != nil {
		return DeliverableFileRow{}, notFound(err, "file "+id)
	}
	return f, nil
}

// compatibleFileProducerSQL follows the actual producing execution, not merely
// Task membership. Acceptance drafts remain local to their original Run.
const compatibleFileProducerSQL = `(producer.run_id=consumer.run_id OR (producer.task_id=consumer.task_id AND producer.role=consumer.role AND producer.role IN ('implement','issue_manager')))`

func CompatibleFileProducer(ctx context.Context, q Querier, file DeliverableFileRow, run string) (bool, error) {
	var compatible bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs producer JOIN runs consumer ON consumer.run_id=? WHERE producer.run_id=? AND producer.task_id=? AND `+compatibleFileProducerSQL+`)`, run, file.RunID, file.TaskID).Scan(&compatible)
	return compatible, err
}

// GetFinalizedDeliverableFile resolves a file's immutable artifact provenance.
// Uploads not included in a submitted artifact are not task inputs. Acceptance
// is intentionally separate: a failed result can be evidence for later work.
// Historical ID-only references use the original upload's metadata only when
// that same producing Run submitted the artifact; cross-Run reuse stays exact.
func GetFinalizedDeliverableFile(ctx context.Context, q Querier, id string) (DeliverableFileRow, string, error) {
	f, err := GetDeliverableFile(ctx, q, id)
	if err != nil {
		return DeliverableFileRow{}, "", err
	}
	var artifactID string
	err = q.QueryRowContext(ctx, `SELECT a.artifact_id
 FROM artifacts a JOIN runs consumer ON consumer.run_id=a.run_id JOIN runs producer ON producer.run_id=?, json_each(a.ref_json, '$.files') entry
 WHERE `+compatibleFileProducerSQL+` AND producer.task_id=a.task_id AND consumer.task_id=a.task_id AND a.task_id=?
 AND json_extract(entry.value, '$.file_id')=?
 AND ((json_extract(entry.value, '$.name')=?
 AND json_extract(entry.value, '$.size')=?
 AND json_extract(entry.value, '$.sha256')=?)
 OR (producer.run_id=consumer.run_id AND CASE WHEN entry.type='object' THEN
 json_type(entry.value, '$.file_id')='text' AND (SELECT COUNT(*) FROM json_each(entry.value))=1 ELSE 0 END))
 ORDER BY a.created_at, a.artifact_id LIMIT 1`, f.RunID, f.TaskID, f.FileID, f.Name, f.Size, f.SHA256).Scan(&artifactID)
	if err != nil {
		return DeliverableFileRow{}, "", notFound(err, "finalized file "+id)
	}
	return f, artifactID, nil
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
func ReadFinalizedDeliverableFile(ctx context.Context, q Querier, id string, offset int64, count int, files ...*blobstore.Store) (DeliverableFileRow, error) {
	if offset < 0 || count <= 0 || count > 65536 {
		return DeliverableFileRow{}, protocol.Errorf(protocol.CodeInvalidRequest, "file offset or read size is out of range")
	}
	f, _, err := GetFinalizedDeliverableFile(ctx, q, id)
	if errors.Is(err, ErrNotFound) {
		f, _, err = GetFinalizedAcceptanceFile(ctx, q, id)
	}
	if err != nil {
		return DeliverableFileRow{}, err
	}

	if len(files) == 0 || files[0] == nil {
		return DeliverableFileRow{}, errors.New("store: file storage required for original byte window")
	}
	original, err := files[0].Open(f.SHA256)
	if err != nil {
		return DeliverableFileRow{}, err
	}
	defer original.Close()
	f.Content = make([]byte, min(int64(count), max(int64(0), f.Size-offset)))
	n, err := original.ReadAt(f.Content, min(offset, f.Size))
	if err != nil {
		return DeliverableFileRow{}, err
	}
	f.Content = f.Content[:n]
	return f, nil
}
