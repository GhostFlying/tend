package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/blobstore"
)

const retainedMarker = "tend:retained-json:v2"
const legacyRetainedMarker = "tend:retained-json:v1"
const retainBodyThreshold = 1024

type preparedOriginal struct {
	Window          blobstore.Window
	Characters      int64
	CanonicalSHA256 string
	StringSHA256    string
	Reference       originalReference
	Children        []preparedOriginal
	ValueID         int64
	RecipeWindow    *blobstore.Window
}
type preparedOriginalKey struct{}
type fileQuerier struct {
	Querier
	files *blobstore.Store
}

func (q fileQuerier) FileStore() *blobstore.Store { return q.files }

func reservedOriginalMarker(body []byte) bool {
	var object map[string]json.RawMessage
	if !bytes.Contains(body, []byte("tend:retained-json:")) || json.Unmarshal(body, &object) != nil {
		return false
	}
	_, ok := object[retainedMarker]
	_, legacy := object[legacyRetainedMarker]
	return ok || legacy
}

func bodyHash(body []byte) string { s := sha256.Sum256(body); return hex.EncodeToString(s[:]) }

// StageOriginals writes a batch before taking the single database writer. It is
// safe to abandon a staged range: no ledger reference exists before BindOriginal.
// Identical bytes already retained by another run reuse the original window.
func (s *Store) StageOriginals(ctx context.Context, stream string, bodies ...[]byte) (context.Context, error) {
	return s.stageOriginals(ctx, stream, retainBodyThreshold, bodies...)
}

func (s *Store) stageOriginals(ctx context.Context, stream string, threshold int, bodies ...[]byte) (context.Context, error) {
	staged := map[string]preparedOriginal{}
	if old, ok := ctx.Value(preparedOriginalKey{}).(map[string]preparedOriginal); ok {
		for k, v := range old {
			staged[k] = v
		}
	}
	for _, body := range bodies {
		if len(body) < threshold && !reservedOriginalMarker(body) {
			continue
		}
		hash := bodyHash(body)
		if _, ok := staged[hash]; ok {
			continue
		}
		p, err := s.prepareOriginal(ctx, stream, body)
		if err != nil {
			return ctx, err
		}
		staged[hash] = p
	}
	return context.WithValue(ctx, preparedOriginalKey{}, staged), nil
}

func (s *Store) prepareOriginal(ctx context.Context, stream string, body []byte) (preparedOriginal, error) {
	hash := bodyHash(body)
	p := preparedOriginal{Characters: int64(utf8.RuneCount(body))}
	err := s.read.QueryRowContext(ctx, `SELECT body_id FROM retained_original_values WHERE sha256=?`, hashBytes(hash)).Scan(&p.ValueID)
	if err == nil {
		p.Reference, err = loadOriginalValue(ctx, fileQuerier{s.read, s.files}, p.ValueID)
		if err == nil {
			_, err = resolveOriginalReference(ctx, s.files, p.Reference)
		}
		return p, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	err = s.read.QueryRowContext(ctx, `SELECT a.archive_id,w.byte_offset,w.byte_length,w.characters FROM retained_original_windows w JOIN retained_original_archives a USING(archive_key) WHERE w.sha256=?`, hashBytes(hash)).Scan(&p.Window.Archive, &p.Window.Offset, &p.Window.Length, &p.Characters)
	if err == nil {
		p.Window.SHA256 = hash
		_, err = s.files.ReadWindow(ctx, p.Window)
	} else if errors.Is(err, sql.ErrNoRows) {
		err = nil
		if old, e := s.retainedRecipe(ctx, hash); e != nil {
			return p, e
		} else if old != nil {
			p = *old
		} else if match, e := s.matchOriginal(ctx, body); e != nil {
			return p, e
		} else if match != nil {
			p = *match
		} else if recipe, e := s.prepareOriginalRecipe(ctx, stream, body); e != nil {
			return p, e
		} else if recipe != nil {
			p = *recipe
		} else {
			p.Window, err = s.files.AppendOriginal(ctx, stream, body)
			p.CanonicalSHA256 = canonicalOriginalHash(body)
			batch := OriginalImportBatch{whole: p}
			indexOriginalJSON(&batch, body, p.Window.Offset, 0)
			p.Children = batch.originals
		}
	} else {
		return p, err
	}
	if err != nil {
		return p, err
	}
	if p.Reference.SHA256 == "" {
		w := p.Window
		p.Reference = originalReference{Window: &w, SHA256: hash}
	}
	p.Characters = int64(utf8.RuneCount(body))
	if p.Reference.Window == nil {
		rule, e := json.Marshal(p.Reference)
		if e != nil {
			return p, e
		}
		w, e := s.files.AppendOriginal(ctx, "rules:"+stream, rule)
		if e != nil {
			return p, e
		}
		p.RecipeWindow = &w
	}
	return p, nil
}

// RetainJSON binds only a pre-synced original. Internal small/generated facts
// remain inline; this method performs no filesystem writes inside a transaction.
func (t *Tx) RetainJSON(ctx context.Context, raw string) (string, error) {
	staged, _ := ctx.Value(preparedOriginalKey{}).(map[string]preparedOriginal)
	p, ok := staged[bodyHash([]byte(raw))]
	if !ok && (len(raw) >= retainBodyThreshold || reservedOriginalMarker([]byte(raw))) {
		err := t.Q().QueryRowContext(ctx, `SELECT body_id FROM retained_original_values WHERE sha256=?`, hashBytes(bodyHash([]byte(raw)))).Scan(&p.ValueID)
		if err == nil {
			ok = true
		} else if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	if !ok {
		return raw, nil
	}
	return t.bindOriginal(ctx, raw, p)
}
func (t *Tx) bindOriginal(ctx context.Context, raw string, p preparedOriginal) (string, error) {
	id, err := t.bindOriginalValue(ctx, p)
	if err != nil {
		return "", err
	}
	projection := map[string]any{}
	dec := json.NewDecoder(bytes.NewBufferString(raw))
	dec.UseNumber()
	var value any
	if dec.Decode(&value) == nil {
		if obj, ok := value.(map[string]any); ok {
			projection, _ = bodyProjection(obj, 0).(map[string]any)
		}
	}
	if projection == nil {
		projection = map[string]any{}
	}
	projection[retainedMarker] = id
	encoded, err := json.Marshal(projection)
	return string(encoded), err
}

// The projection contains queryable identity, lifecycle and routing facts only.
// It is never returned as the original body. Long strings and collections must
// be read through ResolveJSON, which verifies the immutable original.
func bodyProjection(v any, depth int) any {
	object, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]any{}
	for key, value := range object {
		if depth == 0 && (key == "native" || key == "call") {
			if child := bodyProjection(value, 1); child != nil {
				out[key] = child
			}
			continue
		}
		allowed := originalIdentityKey(key) || key == "provider" || key == "purpose" || key == "parent_session_id"
		if !allowed {
			continue
		}
		switch x := value.(type) {
		case string:
			if len(x) <= 512 {
				out[key] = x
			}
		case bool, json.Number:
			out[key] = x
		}
	}
	return out
}

// ResolveJSON restores exact bytes before application parsing or returning a
// body. Missing, truncated or corrupt originals are errors, never empty JSON.
func ResolveJSON(ctx context.Context, q Querier, raw string) (string, error) {
	if body, ok, err := resolveEventJSON(ctx, q, raw); ok || err != nil {
		return body, err
	}
	reference, ok, err := parseOriginalReference(ctx, q, raw)
	if err != nil {
		return "", err
	}
	if !ok {
		return raw, nil
	}
	fq, ok := q.(interface{ FileStore() *blobstore.Store })
	if !ok {
		return "", errors.New("store: original file store unavailable")
	}
	body, err := resolveOriginalReference(ctx, fq.FileStore(), reference)
	if err != nil {
		return "", fmt.Errorf("store: restore original JSON: %w", err)
	}
	return string(body), nil
}

// DecodeJSON is the shared original-aware decoding path for ledger bodies.
func DecodeJSON(ctx context.Context, q Querier, raw string, dst any) error {
	body, err := ResolveJSON(ctx, q, raw)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(body), dst)
}

// JSONTextWindow reads Unicode character windows without expanding an original
// into memory. A corrupt range is rejected before selected text is returned.
func JSONTextWindow(ctx context.Context, q Querier, raw string, offset, limit int64) (string, int64, error) {
	if offset < 0 || limit < 0 {
		return "", 0, errors.New("store: invalid original text window")
	}
	reference, ok, err := parseOriginalReference(ctx, q, raw)
	if err != nil {
		return "", 0, err
	}
	if ok {
		fq, ok := q.(interface{ FileStore() *blobstore.Store })
		if !ok || fq.FileStore() == nil {
			return "", 0, errors.New("store: original file store unavailable")
		}
		if reference.Window != nil && (reference.Encoding == "" || reference.Encoding == "raw") {
			if reference.SHA256 != reference.Window.SHA256 {
				return "", 0, errors.New("store: original window identity mismatch")
			}
			return fq.FileStore().ReadTextWindow(ctx, *reference.Window, offset, limit)
		}
		body, err := resolveOriginalReference(ctx, fq.FileStore(), reference)
		if err != nil {
			return "", 0, err
		}
		raw = string(body)
	}
	if !ok {
		raw, err = ResolveJSON(ctx, q, raw)
		if err != nil {
			return "", 0, err
		}
	}
	runes := []rune(raw)
	n := int64(len(runes))
	start := min(offset, n)
	end := start + min(limit, n-start)
	return string(runes[start:end]), n, nil
}

func originalIdentityKey(key string) bool {
	switch key {
	case "id", "ID", "thread_id", "turn_id", "item_id", "session_id", "call_id", "ThreadID", "TurnID", "ItemID", "CallID", "Kind", "type", "state":
		return true
	}
	return false
}
