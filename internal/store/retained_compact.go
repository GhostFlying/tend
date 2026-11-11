package store

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/blobstore"
)

func hashBytes(hash string) []byte { b, _ := hex.DecodeString(hash); return b }

func parseOriginalReference(ctx context.Context, q Querier, raw string) (originalReference, bool, error) {
	var ref originalReference
	if !bytes.Contains([]byte(raw), []byte("tend:retained-json:")) {
		return ref, false, nil
	}
	var object map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &object) != nil {
		return ref, false, nil
	}
	if v, ok := object[retainedMarker]; ok {
		var id int64
		if err := json.Unmarshal(v, &id); err != nil || id <= 0 {
			return ref, true, errors.New("store: invalid compact original identity")
		}
		ref, err := loadOriginalValue(ctx, q, id)
		return ref, true, err
	}
	if v, ok := object[legacyRetainedMarker]; ok {
		err := json.Unmarshal(v, &ref)
		return ref, true, err
	}
	return ref, false, nil
}

func loadOriginalWindow(ctx context.Context, q Querier, id int64) (blobstore.Window, error) {
	var w blobstore.Window
	var hash []byte
	err := q.QueryRowContext(ctx, `SELECT w.sha256,a.archive_id,w.byte_offset,w.byte_length FROM retained_original_windows w JOIN retained_original_archives a USING(archive_key) WHERE w.window_id=?`, id).Scan(&hash, &w.Archive, &w.Offset, &w.Length)
	w.SHA256 = hex.EncodeToString(hash)
	return w, err
}
func loadOriginalValue(ctx context.Context, q Querier, id int64) (originalReference, error) {
	var ref originalReference
	var hash []byte
	var window, recipe *int64
	if err := q.QueryRowContext(ctx, `SELECT sha256,window_id,encoding,recipe_window_id FROM retained_original_values WHERE body_id=?`, id).Scan(&hash, &window, &ref.Encoding, &recipe); err != nil {
		return ref, fmt.Errorf("store: compact original %d: %w", id, err)
	}
	ref.SHA256 = hex.EncodeToString(hash)
	if window != nil {
		w, err := loadOriginalWindow(ctx, q, *window)
		if err != nil {
			return ref, err
		}
		ref.Window = &w
		return ref, nil
	}
	if recipe == nil {
		return ref, errors.New("store: compact original has no source")
	}
	w, err := loadOriginalWindow(ctx, q, *recipe)
	if err != nil {
		return ref, err
	}
	fq, ok := q.(interface{ FileStore() *blobstore.Store })
	if !ok || fq.FileStore() == nil {
		return ref, errors.New("store: original file store unavailable")
	}
	raw, err := fq.FileStore().ReadWindow(ctx, w)
	if err != nil {
		return ref, err
	}
	if err = json.Unmarshal(raw, &ref); err != nil {
		return ref, err
	}
	if ref.SHA256 != hex.EncodeToString(hash) {
		return ref, errors.New("store: compact rule identity differs")
	}
	return ref, nil
}

func (t *Tx) bindOriginalValue(ctx context.Context, p preparedOriginal) (int64, error) {
	if p.StringSHA256 != "" {
		id, err := t.bindNativeStringValue(ctx, p)
		if err != nil || id != 0 {
			return id, err
		}
	}
	if p.ValueID > 0 {
		return p.ValueID, nil
	}
	for _, child := range p.Children {
		if _, err := t.bindOriginalWindow(ctx, child); err != nil {
			return 0, err
		}
	}
	var window, recipe any
	if p.Reference.Window != nil {
		p.Window = *p.Reference.Window
		canonical, err := t.bindOriginalWindow(ctx, p)
		if err != nil {
			return 0, err
		}
		var id int64
		if err = t.Q().QueryRowContext(ctx, `SELECT window_id FROM retained_original_windows WHERE sha256=?`, hashBytes(canonical.Window.SHA256)).Scan(&id); err != nil {
			return 0, err
		}
		window = id
	} else {
		if p.RecipeWindow == nil {
			return 0, errors.New("store: original rule was not durably staged")
		}
		if _, err := t.bindOriginalWindow(ctx, preparedOriginal{Window: *p.RecipeWindow}); err != nil {
			return 0, err
		}
		var id int64
		if err := t.Q().QueryRowContext(ctx, `SELECT window_id FROM retained_original_windows WHERE sha256=?`, hashBytes(p.RecipeWindow.SHA256)).Scan(&id); err != nil {
			return 0, err
		}
		recipe = id
	}
	if _, err := t.Exec(ctx, `INSERT OR IGNORE INTO retained_original_values(sha256,window_id,encoding,recipe_window_id,characters) VALUES(?,?,?,?,?)`, hashBytes(p.Reference.SHA256), window, p.Reference.Encoding, recipe, p.Characters); err != nil {
		return 0, err
	}
	var id int64
	err := t.Q().QueryRowContext(ctx, `SELECT body_id FROM retained_original_values WHERE sha256=?`, hashBytes(p.Reference.SHA256)).Scan(&id)
	return id, err
}

// RetainedArchiveRoot is the durable prefix required by current native sources,
// windows and shared reconstruction rules. Long archive identities live once in
// metadata; offsets and compact keys are stable across backups and restarts.
type RetainedArchiveRoot struct {
	ArchiveID string
	Bytes     int64
}

func RetainedArchiveRoots(ctx context.Context, q Querier) ([]RetainedArchiveRoot, error) {
	rows, err := q.QueryContext(ctx, `SELECT archive_id,MAX(bytes) FROM (SELECT a.archive_id,w.byte_offset+w.byte_length bytes FROM retained_original_windows w JOIN retained_original_archives a USING(archive_key) UNION ALL SELECT archive_id,next_offset bytes FROM retained_native_sources) GROUP BY archive_id ORDER BY archive_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var roots []RetainedArchiveRoot
	for rows.Next() {
		var r RetainedArchiveRoot
		if err = rows.Scan(&r.ArchiveID, &r.Bytes); err != nil {
			return nil, err
		}
		roots = append(roots, r)
	}
	return roots, rows.Err()
}
