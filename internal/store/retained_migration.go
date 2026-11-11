package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

type originalColumn struct{ table, column, where string }

var originalColumns = []originalColumn{
	{"agent_rollout_events", "data_json", ""},
	{"run_logs", "body_json", ""},
	{"manager_native_events", "body_json", ""},
	{"manager_native_requests", "payload_json", ` AND status IN ('pending','started','unknown')`},
	{"manager_native_requests", "result_json", ` AND status IN ('pending','started','unknown')`},
	{"background_replies", "tool_json", ""},
	{"background_replies", "body", ` AND kind='message'`},
	{"conversation_messages", "body", ` AND author='main_agent'`},
	{"conversation_tool_uses", "input", ""},
	{"conversation_tool_uses", "output", ""},
	{"context_packets", "body", ` AND status<>'injected'`},
	{"main_thread_handoffs", "prompt", ` AND phase<>'committed'`},
}

// Native completion output becomes a business ToolUse fact in schema 122.
// A bounded historical representation migration can also run at schema 119.
func originalColumnsFor(ctx context.Context, q Querier) ([]originalColumn, error) {
	var version int
	if err := q.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		return nil, err
	}
	columns := append([]originalColumn(nil), originalColumns...)
	if version >= 122 {
		for i := range columns {
			if columns[i].table == "manager_native_requests" {
				columns[i].where = ` AND (status IN ('pending','started','unknown') OR status='completed' AND coordinator_consumed=0)`
			}
		}
		columns = append(columns, originalColumn{"conversation_tool_uses", "native_result_output", ""})
	}
	return columns, nil
}

type originalMigrationRow struct {
	Column    originalColumn
	RowID     int64
	Raw       string
	Operation string
	Body      string
}
type legacyOriginalRule struct {
	hash, raw string
	rowID     int64
	valueID   int64
}
type OriginalMigrationBatch struct {
	rows       []originalMigrationRow
	more       bool
	positions  map[originalColumn]int64
	rescan     bool
	rules      []legacyOriginalRule
	ruleSchema int
}

// PrepareOriginalMigration performs bounded reads and durable staging without
// holding the writer. A changed row is rejected during Apply, never overwritten.
func (s *Store) PrepareOriginalMigration(ctx context.Context, limit int, rescan ...bool) (context.Context, *OriginalMigrationBatch, error) {
	if limit < 1 || limit > 1000 {
		return ctx, nil, protocol.Errorf(protocol.CodeInvalidRequest, "migration limit must be 1..1000")
	}
	columns, err := originalColumnsFor(ctx, fileQuerier{s.read, s.files})
	if err != nil {
		return ctx, nil, err
	}
	batch := &OriginalMigrationBatch{positions: map[originalColumn]int64{}, rescan: len(rescan) > 0 && rescan[0]}
	totalBytes := 0
	err = s.Read(ctx, func(tx *ReadTx) error {
		for _, col := range columns {
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
			query := `SELECT rowid,` + col.column + `,'' FROM ` + col.table + ` WHERE rowid>? AND rowid<=? AND ` + originalPendingSQL(col) + col.where + ` ORDER BY rowid LIMIT ?`
			rows, err := tx.Q().QueryContext(ctx, query, after, maxRow, remaining)
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
				if col.table == "background_replies" && col.column == "tool_json" {
					row.Body, err = ResolveToolRecord(ctx, tx.Q(), row.Raw)
				} else {
					row.Body, err = ResolveJSON(ctx, tx.Q(), row.Raw)
				}
				if err != nil {
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
	for _, row := range batch.rows {
		if row.Column.table == "background_replies" && row.Column.column == "tool_json" {
			ctx, err = s.StageToolRecord(ctx, "legacy-tools", row.Body)
			if err != nil {
				return ctx, nil, err
			}
			continue
		}
		stream := "legacy-ledger:" + row.Column.table + ":" + row.Column.column
		if row.Column.table == "agent_rollout_events" {
			ctx, err = s.StageRolloutOriginals(ctx, stream, []byte(row.Body))
		} else if derivedEventColumn(row.Column) {
			ctx, err = s.StageEventOriginals(ctx, stream, []byte(row.Body))
		} else {
			ctx, err = s.stageOriginals(ctx, stream, 0, []byte(row.Body))
		}
		if err != nil {
			return ctx, nil, fmt.Errorf("store: stage original %s.%s row %d: %w", row.Column.table, row.Column.column, row.RowID, err)
		}
	}
	// Finish ordinary business fields before sweeping old shared rules. Needed
	// recipes have then been verified and rebound by the real business rows;
	// retired response-cache wrappers must never become new archive files.
	if len(batch.rows) > 0 {
		batch.more = true
	} else if !batch.more {
		if err = s.prepareLegacyRuleSweep(ctx, batch, columns, limit); err != nil {
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
		columns, err := originalColumnsFor(ctx, q)
		if err != nil {
			return v, err
		}
		v.PendingRows = 0
		for _, col := range columns {
			var n int64
			query := `SELECT COUNT(*) FROM ` + col.table + ` WHERE ` + originalPendingSQL(col) + col.where
			if err = q.QueryRowContext(ctx, query).Scan(&n); err != nil {
				return v, err
			}
			v.PendingRows += n
			v.Fields = append(v.Fields, protocol.OriginalStorageField{Table: col.table, Column: col.column, PendingRows: n})
		}
		var n int64
		if err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM retained_body_recipes`).Scan(&n); err != nil {
			return v, err
		}
		v.PendingRows += n
		v.Fields = append(v.Fields, protocol.OriginalStorageField{Table: "retained_body_recipes", Column: "recipe_json", PendingRows: n})

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
	if _, err := t.Exec(ctx, `UPDATE retained_body_migration SET rewriting=1 WHERE singleton=1`); err != nil {
		return result, err
	}
	for _, row := range batch.rows {
		var replacement string
		var err error
		if row.Column.table == "background_replies" && row.Column.column == "tool_json" {
			replacement, err = t.RetainToolRecord(ctx, row.Body)
			if err == nil {
				var before, after string
				before, err = ResolveToolRecord(ctx, t.Q(), row.Body)
				if err == nil {
					after, err = ResolveToolRecord(ctx, t.Q(), replacement)
				}
				if err == nil && before != after {
					err = errors.New("store: tool migration readback differs")
				}
			}
		} else {
			if derivedEventColumn(row.Column) {
				replacement, err = t.RetainEventJSON(ctx, row.Body)
			} else {
				replacement, err = t.RetainJSON(ctx, row.Body)
			}
			if err == nil {
				var restored string
				restored, err = ResolveJSON(ctx, t.Q(), replacement)
				if err == nil && (derivedEventColumn(row.Column) && !SemanticJSONEqual(restored, row.Body) || !derivedEventColumn(row.Column) && restored != row.Body) {
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
		args := []any{replacement, row.RowID, row.Raw}
		if row.Column.table == "conversation_messages" {
			update = `UPDATE conversation_messages SET body=?,body_preview=? WHERE rowid=? AND body=?`
			preview := []rune(row.Body)
			args = []any{replacement, string(preview[:min(160, len(preview))]), row.RowID, row.Raw}
		}
		changed, err := t.Exec(ctx, update, args...)
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
	if err := t.applyLegacyRuleSweep(ctx, batch, &result); err != nil {
		return result, err
	}

	for col, last := range batch.positions {
		if _, err := t.Exec(ctx, `INSERT INTO retained_body_migration_positions(table_name,column_name,last_rowid) VALUES(?,?,?) ON CONFLICT(table_name,column_name) DO UPDATE SET last_rowid=excluded.last_rowid`, col.table, col.column, last); err != nil {
			return result, err
		}
	}
	if _, err := t.Exec(ctx, `UPDATE retained_body_migration SET rewriting=0,revision=revision+1,migrated_rows=migrated_rows+?,migrated_bytes=migrated_bytes+?,updated_at=? WHERE singleton=1`, result.MigratedRows, result.MigratedBytes, now); err != nil {
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
 UNION ALL SELECT 'value:'||printf('%020d',body_id),lower(hex(sha256)),'',0,0 FROM retained_original_values
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
		if strings.HasPrefix(cursor, "value:") {
			id, e := strconv.ParseInt(strings.TrimPrefix(cursor, "value:"), 10, 64)
			if e != nil {
				return result, e
			}
			ref, e := loadOriginalValue(ctx, fileQuerier{s.read, s.files}, id)
			if e != nil {
				return result, e
			}
			body, e := resolveOriginalReference(ctx, s.files, ref)
			if e != nil {
				return result, e
			}
			p.Window.Length = int64(len(body))
		} else if _, err = s.files.ReadWindow(ctx, p.Window); err != nil {
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

// Pending is evaluated per field: raw log originals, old embedded rules and
// large tool values all require conversion. Small business text stays inline.
func originalPendingSQL(col originalColumn) string {
	threshold := retainBodyThreshold
	if derivedEventColumn(col) {
		return `length(CAST(` + col.column + ` AS BLOB))>0 AND NOT CASE WHEN json_valid(` + col.column + `) THEN json_type(` + col.column + `,'$."` + derivedEventMarker + `"') IS NOT NULL ELSE 0 END`
	}
	c := col.column
	if col.table == "background_replies" && col.column == "tool_json" {
		return `CASE WHEN json_valid(tool_json) THEN (` + originalPendingSQL(originalColumn{column: `json_extract(tool_json,'$.input')`}) + ` OR ` + originalPendingSQL(originalColumn{column: `json_extract(tool_json,'$.output')`}) + ` OR json_type(tool_json,'$."` + legacyRetainedMarker + `"') IS NOT NULL) ELSE 0 END`
	}
	return `(length(CAST(` + c + ` AS BLOB))>=` + fmt.Sprint(threshold) + ` OR CASE WHEN json_valid(` + c + `) THEN json_type(` + c + `,'$."` + legacyRetainedMarker + `"') IS NOT NULL ELSE 0 END) AND NOT CASE WHEN json_valid(` + c + `) THEN json_type(` + c + `,'$."` + retainedMarker + `"') IS NOT NULL ELSE 0 END`
}

func derivedEventColumn(col originalColumn) bool {
	return col.table == "manager_native_events" || col.table == "agent_rollout_events" || col.table == "run_logs"
}
