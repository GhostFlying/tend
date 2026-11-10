package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

type originalColumn struct{ table, column, where string }

var originalColumns = []originalColumn{
	{"agent_rollout_events", "data_json", ""},
	{"run_logs", "body_json", ""},
	{"manager_native_events", "body_json", ""},
	{"manager_native_requests", "payload_json", ""},
	{"manager_native_requests", "result_json", ""},
	{"request_records", "result_json", ` AND operation IN ('manager.native_enqueue','manager.native_claim','manager.native_reply')`},
}

type originalMigrationRow struct {
	Column    originalColumn
	RowID     int64
	Raw       string
	Operation string
}
type OriginalMigrationBatch struct {
	rows      []originalMigrationRow
	more      bool
	positions map[originalColumn]int64
	rescan    bool
}

// PrepareOriginalMigration performs bounded reads and durable staging without
// holding the writer. A changed row is rejected during Apply, never overwritten.
func (s *Store) PrepareOriginalMigration(ctx context.Context, limit int, rescan ...bool) (context.Context, *OriginalMigrationBatch, error) {
	if limit < 1 || limit > 1000 {
		return ctx, nil, protocol.Errorf(protocol.CodeInvalidRequest, "migration limit must be 1..1000")
	}
	batch := &OriginalMigrationBatch{positions: map[originalColumn]int64{}, rescan: len(rescan) > 0 && rescan[0]}
	totalBytes := 0
	err := s.Read(ctx, func(tx *ReadTx) error {
		for _, col := range originalColumns {
			remaining := limit - len(batch.rows)
			if remaining == 0 || totalBytes >= 8<<20 {
				batch.more = true
				break
			}
			var after, maxRow int64
			if !batch.rescan {
				if err := tx.Q().QueryRowContext(ctx, `SELECT COALESCE((SELECT last_rowid FROM retained_body_migration_positions WHERE table_name=? AND column_name=?),0)`, col.table, col.column).Scan(&after); err != nil {
					return err
				}
			}
			if err := tx.Q().QueryRowContext(ctx, `SELECT COALESCE(MAX(rowid),0) FROM `+col.table).Scan(&maxRow); err != nil {
				return err
			}
			operation := `''`
			if col.table == "request_records" {
				operation = "operation"
			}
			query := `SELECT rowid,` + col.column + `,` + operation + ` FROM ` + col.table + ` WHERE rowid>? AND rowid<=? AND length(CAST(` + col.column + ` AS BLOB))>=? AND json_type(` + col.column + `,?) IS NULL AND json_type(` + col.column + `,'$."tend:command-receipt:v1"') IS NULL` + col.where + ` ORDER BY rowid LIMIT ?`
			rows, err := tx.Q().QueryContext(ctx, query, after, maxRow, retainBodyThreshold, `$."`+retainedMarker+`"`, remaining)
			if err != nil {
				return err
			}
			count := 0
			last := after
			for rows.Next() {
				row := originalMigrationRow{Column: col}
				if err := rows.Scan(&row.RowID, &row.Raw, &row.Operation); err != nil {
					rows.Close()
					return err
				}
				batch.rows = append(batch.rows, row)
				totalBytes += len(row.Raw)
				last = row.RowID
				count++
				if totalBytes >= 8<<20 {
					batch.more = true
					break
				}
			}
			if err = errors.Join(rows.Err(), rows.Close()); err != nil {
				return err
			}
			if count < remaining && totalBytes < 8<<20 {
				last = maxRow
			} else {
				batch.more = true
			}
			batch.positions[col] = last
		}
		return nil
	})
	if err != nil {
		return ctx, nil, err
	}
	for _, column := range originalColumns {
		var bodies [][]byte
		for _, row := range batch.rows {
			if row.Column == column && (column.table != "request_records" || row.Operation == "manager.native_reply") {
				bodies = append(bodies, []byte(row.Raw))
			}
		}
		if len(bodies) == 0 {
			continue
		}
		ctx, err = s.StageOriginals(ctx, "legacy-ledger:"+column.table+":"+column.column, bodies...)
		if err != nil {
			return ctx, nil, err
		}
	}
	return ctx, batch, nil
}

func OriginalStorageStatus(ctx context.Context, q Querier) (protocol.OriginalStorageStatus, error) {
	return originalStorageStatus(ctx, q, true)
}
func originalStorageStatus(ctx context.Context, q Querier, scanPending bool) (protocol.OriginalStorageStatus, error) {
	var v protocol.OriginalStorageStatus
	err := q.QueryRowContext(ctx, `SELECT revision,migrated_rows,migrated_bytes FROM retained_body_migration WHERE singleton=1`).Scan(&v.Revision, &v.MigratedRows, &v.MigratedBytes)
	if err != nil {
		return v, err
	}
	err = q.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(byte_length),0) FROM retained_original_bodies`).Scan(&v.Bodies, &v.ReferencedBytes)
	if err != nil {
		return v, err
	}
	err = q.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(bytes),0) FROM (SELECT MAX(bytes) bytes FROM (SELECT archive_id,byte_offset+byte_length bytes FROM retained_original_bodies UNION ALL SELECT archive_id,next_offset bytes FROM retained_native_sources) GROUP BY archive_id)`).Scan(&v.OriginalFiles, &v.OriginalPrefixBytes)
	if err != nil {
		return v, err
	}
	v.PendingRows = -1
	if scanPending {
		v.PendingRows = 0
		for _, col := range originalColumns {
			var n int64
			query := `SELECT COUNT(*) FROM ` + col.table + ` WHERE length(CAST(` + col.column + ` AS BLOB))>=? AND json_type(` + col.column + `,?) IS NULL AND json_type(` + col.column + `,'$."tend:command-receipt:v1"') IS NULL AND NOT EXISTS(SELECT 1 FROM retained_body_migration_exceptions x WHERE x.table_name='` + col.table + `' AND x.column_name='` + col.column + `' AND x.original_rowid=` + col.table + `.rowid)` + col.where
			err = q.QueryRowContext(ctx, query, retainBodyThreshold, `$."`+retainedMarker+`"`).Scan(&n)
			if err != nil {
				return v, err
			}
			v.PendingRows += n
		}

	}
	if err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM retained_body_migration_exceptions`).Scan(&v.UnmatchedRows); err != nil {
		return v, err
	}
	return v, nil
}

// ApplyOriginalMigration verifies reconstructed bytes before replacing one exact
// historical field. Original identities, status, event order and timestamps stay.
func (t *Tx) ApplyOriginalMigration(ctx context.Context, batch *OriginalMigrationBatch, revision, now int64) (protocol.OriginalMigrationResult, error) {
	var result protocol.OriginalMigrationResult
	var current int64
	if err := t.Q().QueryRowContext(ctx, `SELECT revision FROM retained_body_migration WHERE singleton=1`).Scan(&current); err != nil {
		return result, err
	}
	if revision != current {
		return result, protocol.Errorf(protocol.CodeStaleRevision, "original migration revision is %d", current)
	}
	if batch == nil {
		return result, errors.New("store: original migration was not prepared")
	}
	if batch.rescan {
		if _, err := t.Exec(ctx, `DELETE FROM retained_body_migration_positions`); err != nil {
			return result, err
		}
		if _, err := t.Exec(ctx, `DELETE FROM retained_body_migration_exceptions`); err != nil {
			return result, err
		}
	}
	for _, row := range batch.rows {
		var replacement string
		var err error
		if row.Column.table == "request_records" && row.Operation != "manager.native_reply" {
			var r RequestRecord
			if err = t.Q().QueryRowContext(ctx, `SELECT domain,request_id,operation,result_json FROM request_records WHERE rowid=?`, row.RowID).Scan(&r.Domain, &r.RequestID, &r.Operation, &r.ResultJSON); err != nil {
				return result, err
			}
			if r.ResultJSON != row.Raw {
				return result, protocol.Errorf(protocol.CodeStaleRevision, "historical receipt changed during staging")
			}
			replacement, err = compactCommandReceipt(ctx, t, r)
			if errors.Is(err, sql.ErrNoRows) {
				replacement = row.Raw
				err = nil
			}
			if err == nil && replacement != row.Raw {
				var restored string
				restored, err = restoreCommandReceipt(ctx, t.Q(), replacement)
				if err == nil && restored != row.Raw {
					err = errors.New("store: receipt migration readback differs")
				}
			}
		} else {
			replacement, err = t.RetainJSON(ctx, row.Raw)
			if err == nil {
				var restored string
				restored, err = ResolveJSON(ctx, t.Q(), replacement)
				if err == nil && restored != row.Raw {
					err = errors.New("store: body migration readback differs")
				}
			}
		}
		if err != nil {
			return result, err
		}
		if replacement == row.Raw {
			if _, err = t.Exec(ctx, `INSERT OR IGNORE INTO retained_body_migration_exceptions(table_name,column_name,original_rowid,sha256,reason) VALUES(?,?,?,?,?)`, row.Column.table, row.Column.column, row.RowID, bodyHash([]byte(row.Raw)), "Original receipt cannot be exactly linked; inline bytes retained"); err != nil {
				return result, err
			}
			continue
		}
		update := `UPDATE ` + row.Column.table + ` SET ` + row.Column.column + `=? WHERE rowid=? AND ` + row.Column.column + `=?`
		changed, err := t.Exec(ctx, update, replacement, row.RowID, row.Raw)
		if err != nil {
			return result, err
		}
		n, err := changed.RowsAffected()
		if err != nil {
			return result, err
		}
		if n != 1 {
			return result, protocol.Errorf(protocol.CodeStaleRevision, "historical body changed during staging")
		}
		result.MigratedRows++
		result.MigratedBytes += int64(len(row.Raw) - len(replacement))
	}
	for col, last := range batch.positions {
		if _, err := t.Exec(ctx, `INSERT INTO retained_body_migration_positions(table_name,column_name,last_rowid) VALUES(?,?,?) ON CONFLICT(table_name,column_name) DO UPDATE SET last_rowid=excluded.last_rowid`, col.table, col.column, last); err != nil {
			return result, err
		}
	}
	if _, err := t.Exec(ctx, `UPDATE retained_body_migration SET revision=revision+1,migrated_rows=migrated_rows+?,migrated_bytes=migrated_bytes+?,updated_at=? WHERE singleton=1`, result.MigratedRows, result.MigratedBytes, now); err != nil {
		return result, err
	}
	status, err := originalStorageStatus(ctx, t.Q(), false)
	result.Status = status
	result.More = batch.more
	return result, err
}

func (s *Store) VerifyOriginals(ctx context.Context, after string, limit int) (protocol.OriginalVerification, error) {
	var result protocol.OriginalVerification
	if limit < 1 || limit > 1000 || strings.ContainsAny(after, "\x00\n") {
		return result, protocol.Errorf(protocol.CodeInvalidRequest, "invalid original verification cursor or limit")
	}
	rows, err := s.read.QueryContext(ctx, `SELECT cursor,sha256,archive_id,byte_offset,byte_length FROM (
 SELECT sha256 cursor,sha256,archive_id,byte_offset,byte_length FROM retained_original_bodies
 UNION ALL SELECT 'native:'||hex(c.host_id)||':'||hex(c.source_id)||':'||printf('%020d',c.byte_offset),c.sha256,s.archive_id,c.byte_offset,c.byte_length FROM retained_native_chunks c JOIN retained_native_sources s USING(host_id,source_id)
) WHERE cursor>? ORDER BY cursor LIMIT ?`, after, limit+1)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var p preparedOriginal
		var cursor string
		if err = rows.Scan(&cursor, &p.Window.SHA256, &p.Window.Archive, &p.Window.Offset, &p.Window.Length); err != nil {
			return result, err
		}
		if result.Checked == limit {
			result.More = true
			break
		}
		if _, err = s.files.ReadWindow(ctx, p.Window); err != nil {
			return result, fmt.Errorf("verify retained original %s: %w", p.Window.SHA256, err)
		}
		result.Checked++
		result.Bytes += p.Window.Length
		result.Next = cursor
	}
	return result, rows.Err()
}

// OriginalStorageState is a consistent read for management and diagnostics.
func (s *Store) OriginalStorageState(ctx context.Context) (protocol.OriginalStorageStatus, error) {
	var result protocol.OriginalStorageStatus
	err := s.Read(ctx, func(tx *ReadTx) error { var err error; result, err = OriginalStorageStatus(ctx, tx.Q()); return err })
	return result, err
}
