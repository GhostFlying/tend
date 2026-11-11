package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

// RolloutArchiveCursor identifies one native file generation. A thread may span
// multiple Runs; its acknowledged prefix is retained once for the deployment.
type RolloutArchiveCursor struct {
	SourceID   string `json:"source_id"`
	Offset     int64  `json:"offset"`
	FileID     string `json:"file_id"`
	PrefixSize int64  `json:"prefix_size"`
	PrefixHash string `json:"prefix_hash"`
	AnchorHash string `json:"anchor_hash"`
	LastRunID  string `json:"last_run_id"`
}

// RolloutArchive retains the exact uncertain request separately from the
// acknowledged cursor. Only a matching durable server receipt advances it.
type RolloutArchive struct {
	Cursor  RolloutArchiveCursor
	Pending *protocol.Envelope
	Next    RolloutArchiveCursor
}

func (j *Journal) RolloutArchive(ctx context.Context, deployment, thread string) (RolloutArchive, error) {
	j.spoolMu.RLock()
	defer j.spoolMu.RUnlock()
	return j.readRolloutArchive(ctx, j.db, deployment, thread)
}

type archiveQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (j *Journal) readRolloutArchive(ctx context.Context, q archiveQuerier, deployment, thread string) (RolloutArchive, error) {
	var out RolloutArchive
	var cursor string
	var pending, next sql.NullString
	err := q.QueryRowContext(ctx, `SELECT cursor_json,pending_json,next_cursor_json FROM rollout_archive_cursors WHERE deployment_id=? AND thread_id=?`, deployment, thread).Scan(&cursor, &pending, &next)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal([]byte(cursor), &out.Cursor); err != nil {
		return out, err
	}
	if pending.Valid {
		pending.String, err = j.readBody(ctx, pending.String)
		if err != nil {
			return out, err
		}
		out.Pending = &protocol.Envelope{}
		if err = json.Unmarshal([]byte(pending.String), out.Pending); err != nil {
			return out, err
		}
		if err = json.Unmarshal([]byte(next.String), &out.Next); err != nil {
			return out, err
		}
	}
	return out, nil
}

// QueueRolloutArchive atomically retains the request body and its prospective
// cursor. It does not claim that any bytes have reached the server.
func (j *Journal) QueueRolloutArchive(ctx context.Context, deployment, thread string, prior, next RolloutArchiveCursor, env protocol.Envelope) error {
	if deployment == "" || thread == "" || next.SourceID == "" || env.RequestID == "" || env.Operation != protocol.OpRunnerRolloutArchive {
		return errors.New("journal: incomplete rollout archive request")
	}
	var chunk protocol.NativeRolloutChunk
	if err := json.Unmarshal(env.Payload, &chunk); err != nil {
		return err
	}
	if chunk.SourceID != next.SourceID || chunk.ThreadID != thread || chunk.Offset+int64(len(chunk.Data)) != next.Offset || chunk.Offset < 0 || len(chunk.Data) > protocol.MaxNativeRolloutChunkBytes {
		return errors.New("journal: rollout archive cursor differs from request")
	}
	if len(chunk.Data) == 0 && (prior.SourceID != next.SourceID || prior.Offset == 0 || next.Offset != prior.Offset || next.LastRunID != chunk.RunID) {
		return errors.New("journal: rollout archive binding requires an acknowledged original")
	}
	if chunk.SourceID == prior.SourceID && chunk.Offset != prior.Offset || chunk.SourceID != prior.SourceID && chunk.Offset != 0 {
		return errors.New("journal: rollout archive prefix is not contiguous")
	}
	before, _ := json.Marshal(prior)
	after, _ := json.Marshal(next)
	pending, err := json.Marshal(env)
	if err != nil {
		return err
	}
	return j.tx(ctx, func(tx *sql.Tx) error {
		current, err := j.readRolloutArchive(ctx, tx, deployment, thread)
		if err != nil {
			return err
		}
		if current.Pending != nil || current.Cursor != prior {
			return errors.New("journal: rollout archive changed before queue")
		}
		saved, err := j.retainBody(ctx, string(pending))
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO rollout_archive_cursors(deployment_id,thread_id,cursor_json,pending_json,next_cursor_json) VALUES(?,?,?,?,?) ON CONFLICT(deployment_id,thread_id) DO UPDATE SET pending_json=excluded.pending_json,next_cursor_json=excluded.next_cursor_json`, deployment, thread, string(before), saved, string(after))
		return err
	})
}

// AcknowledgeRolloutArchive verifies the original request and durable byte range;
// a malformed or missing receipt leaves both the request and cursor unchanged.
func (j *Journal) AcknowledgeRolloutArchive(ctx context.Context, deployment, thread, requestID string, receipt protocol.NativeRolloutArchiveReceipt) error {
	return j.tx(ctx, func(tx *sql.Tx) error {
		current, err := j.readRolloutArchive(ctx, tx, deployment, thread)
		if err != nil {
			return err
		}
		if current.Pending == nil || current.Pending.RequestID != requestID {
			return errors.New("journal: rollout archive original request absent")
		}
		var chunk protocol.NativeRolloutChunk
		if err = json.Unmarshal(current.Pending.Payload, &chunk); err != nil {
			return err
		}
		if receipt.SourceID != chunk.SourceID || receipt.Offset != chunk.Offset || receipt.NextOffset != current.Next.Offset || receipt.SHA256 != chunk.SHA256 || receipt.IndexedOffset < 0 || receipt.IndexedOffset > receipt.NextOffset {
			return fmt.Errorf("journal: rollout archive receipt does not match original request")
		}
		cursor, err := json.Marshal(current.Next)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE rollout_archive_cursors SET cursor_json=?,pending_json=NULL,next_cursor_json=NULL WHERE deployment_id=? AND thread_id=?`, string(cursor), deployment, thread)
		return err
	})
}
