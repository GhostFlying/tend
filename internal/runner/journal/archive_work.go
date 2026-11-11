package journal

import (
	"context"
	"database/sql"

	"github.com/GhostFlying/tend/internal/protocol"
)

// ArchiveWork is a transport watermark, not a Run, slot or business result.
type ArchiveWork struct {
	RunID      string
	Protocol   int64
	Generation int64
}

const dirtyArchiveQuery = `UPDATE runs SET archive_generation=archive_generation+1
 WHERE run_id=? AND COALESCE(json_extract(assignment_json,'$.protocol'),0)>=3
 AND EXISTS(SELECT 1 FROM rollout_sources WHERE run_id=?)`

func dirtyArchive(ctx context.Context, tx *sql.Tx, run string) error {
	_, err := tx.ExecContext(ctx, dirtyArchiveQuery, run, run)
	return err
}

// PendingArchives seeks only dirty executions. Completed Runs remain eligible;
// paging prevents a failed prefix from starving later executions.
func (j *Journal) PendingArchives(ctx context.Context, after string, limit int) ([]ArchiveWork, error) {
	rows, err := j.db.QueryContext(ctx, `SELECT run_id,COALESCE(json_extract(assignment_json,'$.protocol'),0),archive_generation
 FROM runs WHERE archive_generation>archive_uploaded_generation AND run_id>? ORDER BY run_id LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArchiveWork
	for rows.Next() {
		var w ArchiveWork
		if err := rows.Scan(&w.RunID, &w.Protocol, &w.Generation); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (j *Journal) CompleteArchive(ctx context.Context, w ArchiveWork) error {
	return j.exec(ctx, `UPDATE runs SET archive_uploaded_generation=MAX(archive_uploaded_generation,?)
 WHERE run_id=? AND archive_generation>=?`, w.Generation, w.RunID, w.Generation)
}

func (w ArchiveWork) Assignment() protocol.Assignment {
	return protocol.Assignment{RunID: w.RunID, Protocol: w.Protocol}
}
