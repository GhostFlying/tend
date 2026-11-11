package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/blobstore"
)

var originalChildIndexPosition = originalColumn{"retained_original_windows", "json_children@1", ""}
var originalChildIndexHighWater = originalColumn{"retained_original_windows", "json_children_high@1", ""}

// prepareOriginalChildren repairs indices of existing whole JSON windows,
// including concatenated legacy packs that cannot be read as JSONL files. It
// stages no bytes. Immutable window identities are the source and the cursor;
// newly inserted subwindows only ever point into those same verified originals.
// A durable high-water mark excludes windows made by this repair or normal body
// staging. A rescan may extend a completed pass to new windows; it never
// resets the cursor when callers repeat the flag on every mutable-row batch.
func (s *Store) prepareOriginalChildren(ctx context.Context, batch *OriginalMigrationBatch, limit int) (bool, error) {
	var after, high int64
	if err := s.read.QueryRowContext(ctx, `SELECT COALESCE((SELECT last_rowid FROM retained_body_migration_positions WHERE table_name=? AND column_name=?),0)`, originalChildIndexPosition.table, originalChildIndexPosition.column).Scan(&after); err != nil {
		return false, err
	}
	if err := s.read.QueryRowContext(ctx, `SELECT COALESCE((SELECT last_rowid FROM retained_body_migration_positions WHERE table_name=? AND column_name=?),-1)`, originalChildIndexHighWater.table, originalChildIndexHighWater.column).Scan(&high); err != nil {
		return false, err
	}
	if high < 0 || batch.rescan && after >= high {
		if err := s.read.QueryRowContext(ctx, `SELECT COALESCE(MAX(window_id),0) FROM retained_original_windows`).Scan(&high); err != nil {
			return false, err
		}
	}
	batch.positions[originalChildIndexHighWater] = high
	rows, err := s.read.QueryContext(ctx, `SELECT w.window_id,a.archive_id,w.byte_offset,w.byte_length,lower(hex(w.sha256))
		FROM retained_original_windows w JOIN retained_original_archives a USING(archive_key)
		WHERE w.window_id>? AND w.window_id<=? ORDER BY w.window_id LIMIT ?`, after, high, limit)
	if err != nil {
		return false, err
	}
	type source struct {
		id int64
		w  blobstore.Window
	}
	var sources []source
	for rows.Next() {
		var p source
		if err = rows.Scan(&p.id, &p.w.Archive, &p.w.Offset, &p.w.Length, &p.w.SHA256); err != nil {
			rows.Close()
			return false, err
		}
		sources = append(sources, p)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return false, err
	}
	if len(sources) == 0 {
		batch.positions[originalChildIndexPosition] = after
		return false, nil
	}
	needed, err := s.originalIndexNeeds(ctx, after)
	if err != nil {
		return false, err
	}
	var bytesRead int64
	emitted := map[string]bool{}
	for _, p := range sources {
		if bytesRead >= 8<<20 {
			break
		}
		if p.w.Length >= retainBodyThreshold && p.w.Length <= 16<<20 {
			// A non-JSON opaque range remains untouched. The tiny prefilter is
			// never used as proof: the whole selected range is hashed below.
			r, e := s.files.OpenOriginalRange(p.w.Archive, p.w.Offset, 1)
			if e != nil {
				return false, e
			}
			var first [1]byte
			_, e = io.ReadFull(r, first[:])
			e = errors.Join(e, r.Close())
			if e != nil {
				return false, e
			}
			if first[0] == '"' || first[0] == '{' || first[0] == '[' || strings.ContainsRune(" \t\r\n", rune(first[0])) || p.w.Length > 8192 && len(needed) > 0 {
				raw, e := s.files.ReadWindow(ctx, p.w)
				if e != nil {
					return false, fmt.Errorf("store: read indexed original window %d: %w", p.id, e)
				}
				bytesRead += p.w.Length
				if jsonValidOriginal(raw) {
					children := OriginalImportBatch{whole: preparedOriginal{Window: p.w}}
					indexOriginalJSON(&children, raw, p.w.Offset, 0)
					for _, child := range children.originals {
						part := raw[child.Window.Offset-p.w.Offset : child.Window.Offset-p.w.Offset+child.Window.Length]
						var text string
						textNeeded := json.Unmarshal(part, &text) == nil && needed[bodyHash([]byte(text))]
						aliases := neededOriginalPresentations(part, child.Window, needed)
						batch.values = append(batch.values, aliases...)
						if child.Window.SHA256 != p.w.SHA256 && (textNeeded || needed["canonical:"+child.CanonicalSHA256] || needed[child.Window.SHA256] || len(aliases) != 0) {
							batch.originals = append(batch.originals, child)
						}
					}
				}
				for _, alias := range neededOriginalPresentations(raw, p.w, needed) {
					if !emitted[alias.Reference.SHA256] {
						batch.values = append(batch.values, alias)
						emitted[alias.Reference.SHA256] = true
					}
				}
			}
		}
		after = p.id
	}
	// A rescan of mutable business rows must not erase progress through these
	// immutable source windows. Later normal writes already index their own data.
	batch.positions[originalChildIndexPosition] = after
	batch.originals, err = s.missingOriginalChildren(ctx, batch.originals)
	return true, err
}

// This cache belongs only to one immutable-window pass. It cannot settle or
// modify business facts. Restart or cursor rollback rebuilds it; finishing the
// overall migration releases it. Rebuilding the same full root set for every 100 windows
// would turn a linear historical migration into repeated full-ledger scans.
func (s *Store) originalIndexNeeds(ctx context.Context, after int64) (map[string]bool, error) {
	s.originalIndexMu.Lock()
	defer s.originalIndexMu.Unlock()
	if s.originalIndexNeed != nil && after >= s.originalIndexAt {
		s.originalIndexAt = after
		return s.originalIndexNeed, nil
	}
	var values map[string]bool
	err := s.Read(ctx, func(tx *ReadTx) error {
		var err error
		values, err = neededOriginalHashes(ctx, tx.Q())
		return err
	})
	if err != nil {
		return nil, err
	}
	s.originalIndexNeed, s.originalIndexAt = values, after
	s.originalIndexBuilds++
	return values, nil
}

func (s *Store) missingOriginalChildren(ctx context.Context, children []preparedOriginal) ([]preparedOriginal, error) {
	unique := map[string]preparedOriginal{}
	for _, child := range children {
		unique[child.Window.SHA256] = child
	}
	keys := make([]string, 0, len(unique))
	for hash := range unique {
		keys = append(keys, hash)
	}
	sort.Strings(keys)
	for start := 0; start < len(keys); start += 500 {
		group := keys[start:min(start+500, len(keys))]
		args := make([]any, len(group))
		for i, hash := range group {
			args[i] = hashBytes(hash)
		}
		rows, err := s.read.QueryContext(ctx, `SELECT lower(hex(sha256)) FROM retained_original_windows WHERE sha256 IN (`+strings.TrimSuffix(strings.Repeat("?,", len(group)), ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var hash string
			if err = rows.Scan(&hash); err != nil {
				break
			}
			delete(unique, hash)
		}
		if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
	}
	result := make([]preparedOriginal, 0, len(unique))
	for _, hash := range keys {
		if child, ok := unique[hash]; ok {
			result = append(result, child)
		}
	}
	return result, nil
}

// Needed values come from real current business rows, not a catalog of every
// possible preview. Existing references expose their exact value hash without
// rereading large originals merely to decide whether to create a clip index.
func neededOriginalHashes(ctx context.Context, q Querier) (map[string]bool, error) {
	needed := map[string]bool{}
	var ids []int64
	var walk func(json.RawMessage, string)
	walk = func(value json.RawMessage, key string) {
		if len(value) < retainBodyThreshold {
			return
		}
		if canonical := canonicalOriginalHash(value); canonical != "" {
			needed["canonical:"+canonical] = true
		}
		var text string
		if json.Unmarshal(value, &text) == nil {
			needed[bodyHash([]byte(text))] = true
			if key == "command" {
				if args, _, ok := commandDisplay([]byte(text)); ok {
					body, _ := json.Marshal(args)
					needed["canonical:"+bodyHash(body)] = true
				}
			}
			return
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(value, &fields) == nil {
			for child, body := range fields {
				walk(body, child)
			}
			return
		}
		var items []json.RawMessage
		if json.Unmarshal(value, &items) == nil {
			for _, body := range items {
				walk(body, "")
			}
		}
	}
	add := func(raw string) {
		if len(raw) < retainBodyThreshold {
			// A compact marker can refer to a large value.
			if len(raw) == 0 || raw[0] != '{' {
				return
			}
		}
		var object map[string]json.RawMessage
		if json.Unmarshal([]byte(raw), &object) == nil {
			if part, ok := object[retainedMarker]; ok {
				var id int64
				if json.Unmarshal(part, &id) == nil {
					ids = append(ids, id)
				}
				return
			}
			if part, ok := object[legacyRetainedMarker]; ok {
				var ref originalReference
				if json.Unmarshal(part, &ref) == nil && ref.SHA256 != "" {
					needed[ref.SHA256] = true
				}
				return
			}
			if part, ok := object[derivedEventMarker]; ok {
				var metadata eventMetadata
				if json.Unmarshal(part, &metadata) == nil {
					for _, ref := range metadata.References {
						ids = append(ids, ref.ID)
					}
				}
			}
		}
		if len(raw) >= retainBodyThreshold {
			needed[bodyHash([]byte(raw))] = true
			walk(json.RawMessage(raw), "")
		}
	}
	columns, err := originalColumnsFor(ctx, q)
	if err != nil {
		return nil, err
	}
	for _, col := range columns {
		rows, err := q.QueryContext(ctx, `SELECT `+col.column+` FROM `+col.table+` WHERE `+col.column+` IS NOT NULL AND `+col.column+`<>''`+col.where)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var raw string
			if err = rows.Scan(&raw); err != nil {
				break
			}
			if col.table == "background_replies" && col.column == "tool_json" {
				var object map[string]string
				if json.Unmarshal([]byte(raw), &object) == nil {
					add(object["input"])
					add(object["output"])
				}
			} else {
				add(raw)
			}
		}
		if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
	}
	// Read the small identity index once, rather than issuing a query per root.
	wanted := map[int64]bool{}
	for _, id := range ids {
		wanted[id] = true
	}
	rows, err := q.QueryContext(ctx, `SELECT body_id,lower(hex(sha256)) FROM retained_original_values`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var hash string
		if err = rows.Scan(&id, &hash); err != nil {
			break
		}
		if wanted[id] {
			needed[hash] = true
		}
	}
	return needed, errors.Join(err, rows.Err(), rows.Close())
}

func neededOriginalPresentations(raw []byte, window blobstore.Window, needed map[string]bool) []preparedOriginal {
	var result []preparedOriginal
	seen := map[string]bool{}
	for _, encoding := range []string{"raw", "json-string", "compact", "canonical"} {
		body, err := encodeOriginal(raw, encoding)
		if err != nil {
			continue
		}
		clip := historicalToolClip(body)
		hash := bodyHash(clip)
		if clip == nil || !needed[hash] || seen[hash] {
			continue
		}
		w := window
		result = append(result, preparedOriginal{Window: w, Characters: int64(utf8.RuneCount(clip)), Reference: originalReference{Window: &w, Encoding: encoding + "-clip", SHA256: hash}})
		seen[hash] = true
	}
	if len(raw) > 0 && raw[0] == '"' {
		result = append(result, originalStringFields(raw, window, needed)...)
	}
	return result
}
