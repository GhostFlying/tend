package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Event delivery can precede archival. Prefer a verified native presentation
// once it arrives, preserving the compact value identity used by prior events.
// Existing windows are not relocated: older shared rules can embed their exact
// physical ranges, which remain backup dependencies until independently retired.
func (s *Store) prepareLateNativeValues(ctx context.Context, record []byte, offset int64, originals []preparedOriginal) ([]preparedOriginal, error) {
	candidates := map[string]preparedOriginal{}
	var hashes []string
	add := func(original preparedOriginal, hash, encoding string) {
		if hash == "" || hash == original.Window.SHA256 {
			return
		}
		if _, exists := candidates[hash]; exists {
			return
		}
		window := original.Window
		candidates[hash] = preparedOriginal{Window: window, Reference: originalReference{Window: &window, Encoding: encoding, SHA256: hash}}
		hashes = append(hashes, hash)
	}
	for _, original := range originals {
		add(original, original.CanonicalSHA256, "canonical")
		at := original.Window.Offset - offset
		raw := record[at : at+original.Window.Length]
		if len(raw) > 0 && raw[0] == '"' {
			body, err := encodeOriginal(raw, "json-string")
			if err == nil && len(body) >= retainBodyThreshold {
				add(original, bodyHash(body), "json-string")
			}
		}
	}
	var values []preparedOriginal
	for at := 0; at < len(hashes); at += 256 {
		batch := hashes[at:min(at+256, len(hashes))]
		args := make([]any, len(batch))
		for i, hash := range batch {
			args[i] = hashBytes(hash)
		}
		rows, err := s.read.QueryContext(ctx, `SELECT body_id,lower(hex(sha256)) FROM retained_original_values WHERE sha256 IN (`+strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var hash string
			if err = rows.Scan(&id, &hash); err != nil {
				_ = rows.Close()
				return nil, err
			}
			value := candidates[hash]
			value.ValueID = id
			values = append(values, value)
		}
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
	}
	return values, nil
}

func (t *Tx) bindLateNativeValue(ctx context.Context, value preparedOriginal) error {
	window, err := t.bindOriginalWindow(ctx, preparedOriginal{Window: value.Window})
	if err != nil {
		return err
	}
	_, err = t.Exec(ctx, `UPDATE retained_original_values SET window_id=(SELECT window_id FROM retained_original_windows WHERE sha256=?),encoding=?,recipe_window_id=NULL WHERE body_id=? AND sha256=?`, hashBytes(window.Window.SHA256), value.Reference.Encoding, value.ValueID, hashBytes(value.Reference.SHA256))
	return err
}

// Staging runs outside the writer. An archive can commit after event staging
// but before its value is bound; recheck the verified string index atomically
// so that a stale staged fallback does not overwrite the native source.
func (t *Tx) bindNativeStringValue(ctx context.Context, p preparedOriginal) (int64, error) {
	var window int64
	err := t.Q().QueryRowContext(ctx, `SELECT w.window_id FROM retained_original_windows w WHERE w.canonical_sha256=? AND w.canonical_sha256<>X'' AND EXISTS(SELECT 1 FROM retained_native_source_index n WHERE n.archive_key=w.archive_key) LIMIT 1`, hashBytes(p.StringSHA256)).Scan(&window)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if _, err = t.Exec(ctx, `INSERT INTO retained_original_values(sha256,window_id,encoding,characters) VALUES(?,?,'json-string',?) ON CONFLICT(sha256) DO UPDATE SET window_id=excluded.window_id,encoding=excluded.encoding,recipe_window_id=NULL`, hashBytes(p.Reference.SHA256), window, p.Characters); err != nil {
		return 0, err
	}
	var id int64
	err = t.Q().QueryRowContext(ctx, `SELECT body_id FROM retained_original_values WHERE sha256=?`, hashBytes(p.Reference.SHA256)).Scan(&id)
	return id, err
}
