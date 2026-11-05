package store

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/hostedconfig"
)

// CompactionRuntime is private native identity. A nil Selection pins legacy.
type CompactionRuntime struct {
	OperationID string
	ThreadID    string
	Selection   *hostedconfig.Selection
	CreatedAt   int64
}

func GetCompactionRuntime(ctx context.Context, q Querier, operation string) (CompactionRuntime, error) {
	var row CompactionRuntime
	var raw string
	err := q.QueryRowContext(ctx, `SELECT operation_id,thread_id,private_snapshot,created_at FROM agent_compaction_runtimes WHERE operation_id=?`, operation).Scan(&row.OperationID, &row.ThreadID, &raw, &row.CreatedAt)
	if err != nil {
		return row, notFound(err, "compaction runtime")
	}
	err = json.Unmarshal([]byte(raw), &row.Selection)
	return row, err
}

func (t *Tx) InsertCompactionRuntime(ctx context.Context, row CompactionRuntime) error {
	raw, err := json.Marshal(row.Selection)
	if err != nil {
		return err
	}
	_, err = t.Exec(ctx, `INSERT INTO agent_compaction_runtimes(operation_id,thread_id,private_snapshot,created_at) VALUES(?,?,?,?)`, row.OperationID, row.ThreadID, string(raw), row.CreatedAt)
	return err
}
