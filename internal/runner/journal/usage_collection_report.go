package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
)

// PendingCollectionReports selects only changed snapshots. Settled histories
// are never decoded merely because a reporting-capable runner is alive.
func (j *Journal) PendingCollectionReports(ctx context.Context) ([]protocol.UsageCollectionObservation, error) {
	rows, err := j.db.QueryContext(ctx, `SELECT state_json,writer_stopped,collection_report_sequence FROM native_usage_sources WHERE collection_report_pending=1 ORDER BY source_id LIMIT 64`)
	if err != nil {
		return nil, err
	}
	var out []protocol.UsageCollectionObservation
	for rows.Next() {
		var raw string
		var stopped bool
		var sequence int64
		if err = rows.Scan(&raw, &stopped, &sequence); err != nil {
			_ = rows.Close()
			return nil, err
		}
		var s UsageSource
		if err = json.Unmarshal([]byte(raw), &s); err != nil {
			_ = rows.Close()
			return nil, err
		}
		offset := int64(0)
		for _, cursor := range s.Cursors {
			offset += cursor.Offset
		}
		out = append(out, protocol.UsageCollectionObservation{CursorFiles: int64(len(s.Cursors)), CursorOffset: offset, LocalSourceID: s.ID, RunID: s.RunID, ThreadID: s.Source.ThreadID, SessionID: s.Source.SessionID, WriterStopped: stopped, Collection: s.Collection, Sequence: sequence + 1})
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil || len(out) == 0 {
		return out, err
	}
	requests, err := j.Requests(ctx)
	if err != nil {
		return nil, err
	}
	for i := range out {
		p := &out[i]
		for _, r := range requests {
			if r.Envelope.Operation != protocol.OpRunnerUsageUpload && r.Envelope.Operation != protocol.OpRunnerUsageRegister {
				continue
			}
			var scope protocol.UsageUpload
			if err = json.Unmarshal(r.Envelope.Payload, &scope); err != nil {
				return nil, err
			}
			var reg protocol.NativeUsageRegistration
			if r.Envelope.Operation == protocol.OpRunnerUsageRegister {
				_ = json.Unmarshal(r.Envelope.Payload, &reg)
				scope.RootThreadID = reg.ThreadID
			}
			if scope.RunID == p.RunID && scope.RootThreadID == p.ThreadID {
				p.PendingRequestIDs = append(p.PendingRequestIDs, r.Envelope.RequestID)
			}
		}
		err = j.db.QueryRowContext(ctx, `SELECT request_id,result FROM native_usage_collection_controls WHERE source_id=? AND queued=0 ORDER BY request_id LIMIT 1`, p.LocalSourceID).Scan(&p.ControlRequestID, &p.ControlStatus)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	return out, nil
}

func (j *Journal) QueueCollectionReport(ctx context.Context, p protocol.UsageCollectionObservation, env protocol.Envelope) error {
	return j.tx(ctx, func(tx *sql.Tx) error {
		s, err := getUsageSource(ctx, tx, p.LocalSourceID)
		if err != nil {
			return err
		}
		var sequence int64
		if err = tx.QueryRowContext(ctx, `SELECT collection_report_sequence FROM native_usage_sources WHERE source_id=?`, s.ID).Scan(&sequence); err != nil {
			return err
		}
		if s.Collection != p.Collection || s.WriterStopped != p.WriterStopped || p.Sequence != sequence+1 {
			return protocol.Errorf(protocol.CodeStaleRevision, "collection report snapshot changed")
		}
		if err = putRequest(ctx, tx, Request{Envelope: env}, j.now()); err != nil {
			return err
		}
		if p.ControlRequestID != "" {
			if _, err = tx.ExecContext(ctx, `UPDATE native_usage_collection_controls SET queued=1 WHERE request_id=? AND source_id=?`, p.ControlRequestID, s.ID); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE native_usage_sources SET collection_report_sequence=?,collection_report_pending=EXISTS(SELECT 1 FROM native_usage_collection_controls WHERE source_id=? AND queued=0) WHERE source_id=?`, p.Sequence, s.ID, s.ID)
		return err
	})
}

func (j *Journal) HasCollectionReports(ctx context.Context) (bool, error) {
	var pending bool
	err := j.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM native_usage_sources WHERE collection_report_pending=1)`).Scan(&pending)
	return pending, err
}

// ApplyUsageCollectionControl is a local journal change, not a native process or
// business command. The receipt is durable before acknowledging the host control.
func (j *Journal) ApplyUsageCollectionControl(ctx context.Context, command protocol.UsageCollectionCommand) error {
	raw, err := json.Marshal(command)
	if err != nil {
		return err
	}
	err = j.tx(ctx, func(tx *sql.Tx) error {
		var previous string
		err := tx.QueryRowContext(ctx, `SELECT command_json FROM native_usage_collection_controls WHERE request_id=?`, command.RequestID).Scan(&previous)
		if err == nil {
			if previous != string(raw) {
				return protocol.Errorf(protocol.CodeConflictPayload, "collection command input changed")
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if command.RequestID == "" || command.RunID == "" || command.SourceID == "" || command.Action != "retire" && command.Action != "resume" || command.Action == "retire" && command.Reason != "unavailable" || command.Action == "resume" && command.Reason != "" {
			return protocol.Errorf(protocol.CodeInvalidRequest, "invalid collection control")
		}
		s, err := getUsageSource(ctx, tx, command.SourceID)
		if err != nil {
			return err
		}
		if s.RunID != command.RunID {
			return protocol.Errorf(protocol.CodeForbidden, "collection command run changed")
		}
		result := "stale"
		if s.Collection.Epoch == command.ExpectedEpoch && s.Collection.Revision == command.ExpectedRevision {
			if command.Action == "resume" {
				s.Collection.Reopen("reconcile/" + command.RequestID)
				s.Collection.State = protocol.UsageSettling
			} else {
				s.Collection.RetireUnavailable(j.now().UnixMilli(), s.WriterStopped)
			}
			if err = replaceUsageCollection(ctx, tx, s, j.now().UnixMilli()); err != nil {
				return err
			}
			result = "applied"
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO native_usage_collection_controls(request_id,command_json,source_id,result) VALUES(?,?,?,?)`, command.RequestID, string(raw), s.ID, result); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE native_usage_sources SET collection_report_pending=1 WHERE source_id=?`, s.ID)
		return err
	})
	if err == nil {
		j.WakeUsage()
	}
	return err
}
