package store

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

type legacyRuleRoots struct {
	legacy  map[string]bool
	compact map[string]int64
}

// Only fields with a retained business reading contract are roots. Retired
// request envelopes and completed RPC response caches are deliberately absent.
func readLegacyRuleRoots(ctx context.Context, q Querier, columns []originalColumn) (legacyRuleRoots, error) {
	roots := legacyRuleRoots{legacy: map[string]bool{}, compact: map[string]int64{}}
	ids := map[int64]bool{}
	add := func(raw string) error {
		var object map[string]json.RawMessage
		if json.Unmarshal([]byte(raw), &object) != nil {
			return nil // ordinary text, with the same interpretation as ResolveJSON
		}
		if v, ok := object[derivedEventMarker]; ok {
			var meta eventMetadata
			if json.Unmarshal(v, &meta) != nil || len(meta.OriginalSHA256) != 32 {
				return errors.New("store: invalid derived event root")
			}
			for _, ref := range meta.References {
				if ref.ID <= 0 {
					return errors.New("store: invalid derived original root")
				}
				ids[ref.ID] = true
			}
		} else if v, ok := object[retainedMarker]; ok {
			var id int64
			if json.Unmarshal(v, &id) != nil || id <= 0 {
				return errors.New("store: invalid compact original root")
			}
			ids[id] = true
		} else if v, ok := object[legacyRetainedMarker]; ok {
			var ref originalReference
			if json.Unmarshal(v, &ref) != nil || len(hashBytes(ref.SHA256)) != 32 {
				return errors.New("store: invalid legacy original root")
			}
			roots.legacy[ref.SHA256] = true
		}
		return nil
	}
	for _, col := range columns {
		rows, err := q.QueryContext(ctx, `SELECT `+col.column+` FROM `+col.table+` WHERE (instr(`+col.column+`, 'tend:retained-json:')>0 OR instr(`+col.column+`, '`+derivedEventMarker+`')>0)`+col.where)
		if err != nil {
			return roots, err
		}
		for rows.Next() {
			var raw string
			if err = rows.Scan(&raw); err == nil {
				err = add(raw)
			}
			if err == nil && col.table == "background_replies" && col.column == "tool_json" {
				var object map[string]json.RawMessage
				if json.Unmarshal([]byte(raw), &object) == nil {
					for _, key := range []string{"input", "output"} {
						var value string
						if part, ok := object[key]; ok && json.Unmarshal(part, &value) == nil {
							if err = add(value); err != nil {
								break
							}
						}
					}
				}
			}
			if err != nil {
				rows.Close()
				return roots, err
			}
		}
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			return roots, err
		}
	}
	if len(ids) == 0 {
		return roots, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT body_id,sha256 FROM retained_original_values`)
	if err != nil {
		return roots, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var hash []byte
		if err = rows.Scan(&id, &hash); err != nil {
			return roots, err
		}
		if ids[id] {
			roots.compact[hex.EncodeToString(hash)] = id
			delete(ids, id)
		}
	}
	if err = rows.Err(); err != nil {
		return roots, err
	}
	if len(ids) != 0 {
		return roots, errors.New("store: business original root is missing")
	}
	return roots, nil
}

func (s *Store) prepareLegacyRuleSweep(ctx context.Context, batch *OriginalMigrationBatch, columns []originalColumn, limit int) error {
	return s.Read(ctx, func(tx *ReadTx) error {
		if err := tx.Q().QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&batch.ruleSchema); err != nil {
			return err
		}
		roots, err := readLegacyRuleRoots(ctx, tx.Q(), columns)
		if err != nil {
			return err
		}
		// The schema is part of this private scan position. A 119-only dry run
		// may defer cache-only rows; 122 must revisit them after RPC retirement.
		position := originalColumn{"retained_body_recipes", fmt.Sprintf("recipe_json@%d", batch.ruleSchema), ""}
		var after int64
		if !batch.rescan {
			if err = tx.Q().QueryRowContext(ctx, `SELECT COALESCE((SELECT last_rowid FROM retained_body_migration_positions WHERE table_name=? AND column_name=?),0)`, position.table, position.column).Scan(&after); err != nil {
				return err
			}
		}
		rows, err := tx.Q().QueryContext(ctx, `SELECT rowid,sha256,recipe_json FROM retained_body_recipes WHERE rowid>? ORDER BY rowid LIMIT ?`, after, limit)
		if err != nil {
			return err
		}
		count, bytes := 0, 0
		for rows.Next() {
			var rule legacyOriginalRule
			if err = rows.Scan(&rule.rowID, &rule.hash, &rule.raw); err != nil {
				rows.Close()
				return err
			}
			if roots.legacy[rule.hash] {
				rows.Close()
				return protocol.Errorf(protocol.CodeStaleRevision, "legacy business originals require a migration rescan")
			}
			rule.valueID = roots.compact[rule.hash]
			if rule.valueID > 0 || batch.ruleSchema >= 122 {
				batch.rules = append(batch.rules, rule)
			}
			batch.positions[position] = rule.rowID
			count++
			bytes += len(rule.raw)
			if bytes >= 8<<20 {
				batch.more = true
				break
			}
		}
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		if count == limit {
			batch.more = true
		}
		return nil
	})
}

func (t *Tx) applyLegacyRuleSweep(ctx context.Context, batch *OriginalMigrationBatch, result *protocol.OriginalMigrationResult) error {
	if len(batch.rules) == 0 {
		return nil
	}
	columns, err := originalColumnsFor(ctx, t.Q())
	if err != nil {
		return err
	}
	roots, err := readLegacyRuleRoots(ctx, t.Q(), columns)
	if err != nil {
		return err
	}
	for _, rule := range batch.rules {
		if roots.legacy[rule.hash] || batch.ruleSchema < 122 && roots.compact[rule.hash] == 0 {
			return protocol.Errorf(protocol.CodeStaleRevision, "legacy rule business roots changed during staging")
		}
		if id := roots.compact[rule.hash]; id > 0 {
			ref, err := loadOriginalValue(ctx, t.Q(), id)
			if err != nil {
				return err
			}
			body, err := resolveOriginalReference(ctx, t.files, ref)
			if err != nil {
				return err
			}
			if bodyHash(body) != rule.hash {
				return errors.New("store: shared business rule readback differs")
			}
		}
		// No new file is created for an unreferenced retired cache wrapper.
		changed, err := t.Exec(ctx, `DELETE FROM retained_body_recipes WHERE rowid=? AND sha256=? AND recipe_json=?`, rule.rowID, rule.hash, rule.raw)
		if err != nil {
			return err
		}
		n, err := changed.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return protocol.Errorf(protocol.CodeStaleRevision, "legacy rule changed during staging")
		}
		result.MigratedRows++
		result.MigratedBytes += int64(len(rule.raw))
	}
	return nil
}
