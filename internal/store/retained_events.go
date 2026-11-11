package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/blobstore"
)

const derivedEventMarker = "tend:derived-json:v1"

type eventReference struct {
	Path []string `json:"p"`
	ID   int64    `json:"i"`
	Text bool     `json:"t,omitempty"`
}
type eventDefaults struct {
	Path []string `json:"p"`
	Mask uint64   `json:"m"`
}
type eventMetadata struct {
	OriginalSHA256 []byte           `json:"h"`
	References     []eventReference `json:"r,omitempty"`
	Defaults       []eventDefaults  `json:"d,omitempty"`
	Boxed          bool             `json:"b,omitempty"`
}
type preparedEventReference struct {
	eventReference
	Original preparedOriginal
}
type preparedEvent struct {
	OriginalSHA256 []byte
	Value          any
	References     []preparedEventReference
	Defaults       []eventDefaults
}
type preparedEventsKey struct{}

// The storage format records which defaults were explicitly present. An absent
// field remains absent; provider Raw JSON is never stripped with these defaults.
var nativeEventDefaults = [...]struct {
	Key   string
	Value any
}{
	{"ItemID", ""}, {"ItemType", ""}, {"Text", ""}, {"TurnStatus", ""},
	{"Tool", nil}, {"RequestUsage", nil}, {"ContextUsage", nil}, {"Failure", nil}, {"Raw", nil},
}

// StageEventOriginals keeps transport structure as semantic JSON. Only actual
// opaque large text becomes a new original. Provider JSON reuses registered
// windows or recursively shares its values; wrappers never create new originals.
func (s *Store) StageEventOriginals(ctx context.Context, stream string, bodies ...[]byte) (context.Context, error) {
	return s.stageEvents(ctx, stream, false, bodies...)
}

// StageRolloutOriginals identifies provider JSON for whole-value window reuse.
// An unmatched wrapper is reconstructed from its values, not copied as text.
func (s *Store) StageRolloutOriginals(ctx context.Context, stream string, bodies ...[]byte) (context.Context, error) {
	return s.stageEvents(ctx, stream, true, bodies...)
}
func (s *Store) stageEvents(ctx context.Context, stream string, provider bool, bodies ...[]byte) (context.Context, error) {
	staged := map[string]preparedEvent{}
	originals := map[string]preparedOriginal{}
	if prior, ok := ctx.Value(preparedOriginalKey{}).(map[string]preparedOriginal); ok {
		for k, v := range prior {
			originals[k] = v
		}
	}
	ctx = context.WithValue(ctx, preparedOriginalKey{}, originals)
	if old, ok := ctx.Value(preparedEventsKey{}).(map[string]preparedEvent); ok {
		for k, v := range old {
			staged[k] = v
		}
	}
	for _, raw := range bodies {
		if len(raw) == 0 {
			raw = []byte(`{}`)
		}
		hash := bodyHash(raw)
		if _, ok := staged[hash]; ok {
			continue
		}
		p := preparedEvent{OriginalSHA256: hashBytes(hash)}
		value, err := s.prepareEventValue(ctx, stream, raw, nil, provider, &p)
		if err != nil {
			return ctx, err
		}
		p.Value = value
		staged[hash] = p
	}
	return context.WithValue(ctx, preparedEventsKey{}, staged), nil
}

func (s *Store) prepareEventValue(ctx context.Context, stream string, raw []byte, path []string, provider bool, p *preparedEvent) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw = bytes.TrimSpace(raw)
	if !json.Valid(raw) {
		return nil, errors.New("store: derived event is not valid JSON")
	}
	if len(raw) >= retainBodyThreshold && (provider || len(path) > 0) {
		original, err := s.matchSemanticOriginal(ctx, raw)
		if err != nil {
			return nil, err
		}
		if original != nil {
			p.References = append(p.References, preparedEventReference{eventReference: eventReference{Path: append([]string(nil), path...)}, Original: *original})
			var obj map[string]any
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.UseNumber()
			if dec.Decode(&obj) == nil && obj != nil {
				return bodyProjection(obj, 0), nil
			}
			return nil, nil
		}
	}
	switch raw[0] {
	case '{':
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, err
		}
		value := map[string]any{}
		keys := make([]string, 0, len(fields))
		for key := range fields {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			rank := func(k string) int {
				if k == "Raw" || k == "data" {
					return 0
				}
				return 1
			}
			if rank(keys[i]) != rank(keys[j]) {
				return rank(keys[i]) < rank(keys[j])
			}
			return keys[i] < keys[j]
		})
		for _, key := range keys {
			child := fields[key]
			next := append(append([]string(nil), path...), key)
			boundary := key == "Raw" && len(path) == 1 && path[0] == "native" || key == "data" && len(path) == 0 && fields["thread_id"] != nil && fields["turn_id"] != nil
			v, err := s.prepareEventValue(ctx, stream, child, next, boundary, p)
			if err != nil {
				return nil, err
			}
			value[key] = v
		}
		if len(path) == 1 && path[0] == "native" {
			var mask uint64
			for i, def := range nativeEventDefaults {
				if v, ok := value[def.Key]; ok && v == def.Value {
					delete(value, def.Key)
					mask |= 1 << i
				}
			}
			if mask != 0 {
				p.Defaults = append(p.Defaults, eventDefaults{append([]string(nil), path...), mask})
			}
		}
		return value, nil
	case '[':
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, err
		}
		value := make([]any, len(items))
		for i, child := range items {
			v, err := s.prepareEventValue(ctx, stream, child, append(append([]string(nil), path...), strconv.Itoa(i)), false, p)
			if err != nil {
				return nil, err
			}
			value[i] = v
		}
		return value, nil
	case '"':
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, err
		}
		if len(text) < retainBodyThreshold {
			return text, nil
		}
		var original *preparedOriginal
		var err error
		if len(path) > 0 && path[len(path)-1] == "command" {
			original, err = s.matchCommandDisplay(ctx, []byte(text))
		}
		if err == nil && original == nil {
			original, err = s.prepareOpaqueOriginal(ctx, stream, []byte(text))
		}
		if err != nil {
			return nil, err
		}
		p.References = append(p.References, preparedEventReference{eventReference: eventReference{Path: append([]string(nil), path...), Text: true}, Original: *original})
		return nil, nil
	default:
		var value any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		err := dec.Decode(&value)
		return value, err
	}
}

// A semantic match reuses the original file even when only key order/spacing
// differs. The original file's hash remains exact; presentation is canonical.
func (s *Store) matchSemanticOriginal(ctx context.Context, raw []byte) (*preparedOriginal, error) {
	canonical, err := canonicalOriginal(raw)
	if err != nil {
		return nil, err
	}
	if cache, ok := ctx.Value(preparedOriginalKey{}).(map[string]preparedOriginal); ok {
		if p, exists := cache["canonical:"+bodyHash(canonical)]; exists {
			source, e := s.files.ReadWindow(ctx, p.Window)
			if e != nil {
				return nil, e
			}
			value, e := canonicalOriginal(source)
			if e != nil {
				return nil, e
			}
			if bytes.Equal(value, canonical) {
				w := p.Window
				p.Reference = originalReference{Window: &w, Encoding: "canonical", SHA256: bodyHash(canonical)}
				p.Characters = int64(utf8.RuneCount(canonical))
				return &p, nil
			}
		}
	}
	rows, err := s.read.QueryContext(ctx, canonicalOriginalLookupQuery, hashBytes(bodyHash(canonical)))
	if err != nil {
		return nil, err
	}
	var candidates []preparedOriginal
	for rows.Next() {
		var p preparedOriginal
		if err = rows.Scan(&p.Window.SHA256, &p.Window.Archive, &p.Window.Offset, &p.Window.Length, &p.Characters); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, p)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for _, p := range candidates {
		source, e := s.files.ReadWindow(ctx, p.Window)
		if e != nil {
			return nil, e
		}
		value, e := canonicalOriginal(source)
		if e != nil {
			return nil, e
		}
		if !bytes.Equal(canonical, value) {
			continue
		}
		w := p.Window
		p.Reference = originalReference{Window: &w, Encoding: "canonical", SHA256: bodyHash(canonical)}
		p.Characters = int64(utf8.RuneCount(canonical))
		return &p, nil
	}
	return nil, nil
}

// Opaque text, including text that happens to contain JSON, never receives a
// recipe for its formatting. Reuse a real window or retain these actual bytes.
func (s *Store) prepareOpaqueOriginal(ctx context.Context, stream string, raw []byte) (*preparedOriginal, error) {
	hash := bodyHash(raw)
	cache, _ := ctx.Value(preparedOriginalKey{}).(map[string]preparedOriginal)
	if cached, ok := cache[hash]; ok && cached.Reference.SHA256 == hash {
		return &cached, nil
	}
	quoted, _ := json.Marshal(string(raw))
	if cached, ok := cache["canonical:"+canonicalOriginalHash(quoted)]; ok {
		value, e := s.files.ReadWindow(ctx, cached.Window)
		if e != nil {
			return nil, e
		}
		var text string
		if json.Unmarshal(value, &text) == nil && text == string(raw) {
			w := cached.Window
			cached.Reference = originalReference{Window: &w, Encoding: "json-string", SHA256: hash}
			cached.Characters = int64(utf8.RuneCount(raw))
			return &cached, nil
		}
	}
	p := &preparedOriginal{Characters: int64(utf8.RuneCount(raw))}
	err := s.read.QueryRowContext(ctx, `SELECT body_id FROM retained_original_values WHERE sha256=?`, hashBytes(hash)).Scan(&p.ValueID)
	if err == nil {
		p.Reference, err = loadOriginalValue(ctx, fileQuerier{s.read, s.files}, p.ValueID)
		if err == nil {
			_, err = resolveOriginalReference(ctx, s.files, p.Reference)
		}
		return p, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	err = s.read.QueryRowContext(ctx, `SELECT a.archive_id,w.byte_offset,w.byte_length,w.characters FROM retained_original_windows w JOIN retained_original_archives a USING(archive_key) WHERE w.sha256=?`, hashBytes(hash)).Scan(&p.Window.Archive, &p.Window.Offset, &p.Window.Length, &p.Characters)
	if err == nil {
		p.Window.SHA256 = hash
		_, err = s.files.ReadWindow(ctx, p.Window)
	} else if errors.Is(err, sql.ErrNoRows) {
		match, e := s.matchOriginal(ctx, raw)
		if e != nil {
			return nil, e
		}
		if match != nil {
			return match, nil
		}
		p.Window, err = s.files.AppendOriginal(ctx, stream, raw)
		p.CanonicalSHA256 = canonicalOriginalHash(raw)
		if err == nil && json.Valid(raw) {
			batch := OriginalImportBatch{whole: *p}
			indexOriginalJSON(&batch, raw, p.Window.Offset, 0)
			p.Children = batch.originals
		}
	}
	if err != nil {
		return nil, err
	}
	w := p.Window
	p.Reference = originalReference{Window: &w, SHA256: hash}
	if cache != nil {
		cache[hash] = *p
		if p.CanonicalSHA256 != "" {
			cache["canonical:"+p.CanonicalSHA256] = *p
		}
		for _, child := range p.Children {
			w := child.Window
			child.Reference = originalReference{Window: &w, SHA256: w.SHA256}
			cache[w.SHA256] = child
			if child.CanonicalSHA256 != "" {
				cache["canonical:"+child.CanonicalSHA256] = child
			}
		}
	}
	return p, nil
}

func (t *Tx) RetainEventJSON(ctx context.Context, raw string) (string, error) {
	staged, _ := ctx.Value(preparedEventsKey{}).(map[string]preparedEvent)
	p, ok := staged[bodyHash([]byte(raw))]
	if !ok {
		return raw, nil
	} // Internal small facts can be inserted without IO.
	meta := eventMetadata{Defaults: p.Defaults, OriginalSHA256: p.OriginalSHA256}
	for _, ref := range p.References {
		id, err := t.bindOriginalValue(ctx, ref.Original)
		if err != nil {
			return "", err
		}
		r := ref.eventReference
		r.ID = id
		meta.References = append(meta.References, r)
	}
	root, ok := p.Value.(map[string]any)
	_, reserved := root[derivedEventMarker]
	value := map[string]any{}
	if !ok || reserved {
		value = map[string]any{"value": p.Value}
		meta.Boxed = true
	} else {
		for key, v := range root {
			value[key] = v
		}
	}
	value[derivedEventMarker] = meta
	data, err := json.Marshal(value)
	return string(data), err
}

func resolveEventJSON(ctx context.Context, q Querier, raw string) (string, bool, error) {
	if !bytes.Contains([]byte(raw), []byte(derivedEventMarker)) {
		return "", false, nil
	}
	var object map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &object) != nil || object[derivedEventMarker] == nil {
		return "", false, nil
	}
	var meta eventMetadata
	if err := json.Unmarshal(object[derivedEventMarker], &meta); err != nil {
		return "", true, err
	}
	if len(meta.OriginalSHA256) != 32 {
		return "", true, errors.New("store: event original identity missing")
	}
	delete(object, derivedEventMarker)
	data, err := json.Marshal(object)
	if err != nil {
		return "", true, err
	}
	var value any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err = dec.Decode(&value); err != nil {
		return "", true, err
	}
	if meta.Boxed {
		value = value.(map[string]any)["value"]
	}
	for _, def := range meta.Defaults {
		if def.Mask>>len(nativeEventDefaults) != 0 {
			return "", true, errors.New("store: invalid event defaults")
		}
		node, e := eventPath(value, def.Path)
		if e != nil {
			return "", true, e
		}
		fields, ok := node.(map[string]any)
		if !ok {
			return "", true, errors.New("store: invalid native event defaults target")
		}
		for i, d := range nativeEventDefaults {
			if def.Mask&(1<<i) != 0 {
				if _, exists := fields[d.Key]; exists {
					return "", true, errors.New("store: duplicate native event default")
				}
				fields[d.Key] = d.Value
			}
		}
	}
	for _, ref := range meta.References {
		original, e := loadOriginalValue(ctx, q, ref.ID)
		if e != nil {
			return "", true, e
		}
		fq, ok := q.(interface{ FileStore() *blobstore.Store })
		if !ok {
			return "", true, errors.New("store: event original file store unavailable")
		}
		body, e := resolveOriginalReference(ctx, fq.FileStore(), original)
		if e != nil {
			return "", true, e
		}
		var replacement any
		if ref.Text {
			replacement = string(body)
		} else {
			dec := json.NewDecoder(bytes.NewReader(body))
			dec.UseNumber()
			if e = dec.Decode(&replacement); e != nil {
				return "", true, e
			}
		}
		if len(ref.Path) == 0 {
			value = replacement
		} else {
			parent, e := eventPath(value, ref.Path[:len(ref.Path)-1])
			if e != nil {
				return "", true, e
			}
			key := ref.Path[len(ref.Path)-1]
			switch parent := parent.(type) {
			case map[string]any:
				parent[key] = replacement
			case []any:
				i, e := strconv.Atoi(key)
				if e != nil || i < 0 || i >= len(parent) {
					return "", true, errors.New("store: event original index invalid")
				}
				parent[i] = replacement
			default:
				return "", true, errors.New("store: event original path invalid")
			}
		}
	}
	data, err = json.Marshal(value)
	return string(data), true, err
}
func eventPath(value any, path []string) (any, error) {
	for _, key := range path {
		switch node := value.(type) {
		case map[string]any:
			var ok bool
			value, ok = node[key]
			if !ok {
				return nil, errors.New("store: event original field missing")
			}
		case []any:
			i, err := strconv.Atoi(key)
			if err != nil || i < 0 || i >= len(node) {
				return nil, errors.New("store: event original index invalid")
			}
			value = node[i]
		default:
			return nil, errors.New("store: event original path invalid")
		}
	}
	return value, nil
}

// SemanticJSONEqual compares JSON values, retaining distinctions such as absent
// versus explicit null and exact string contents. Formatting is not identity.
func SemanticJSONEqual(a, b string) bool {
	x, e := canonicalOriginal([]byte(a))
	if e != nil {
		return false
	}
	y, e := canonicalOriginal([]byte(b))
	return e == nil && bytes.Equal(x, y)
}

// EventOriginalSHA256 exposes the initial transport fact identity without
// preserving a second copy of its derived JSON wrapper. Legacy exact originals
// continue to obtain their identity from ResolveJSON.
func EventOriginalSHA256(stored string) (string, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(stored), &fields) != nil || fields[derivedEventMarker] == nil {
		return "", false
	}
	var meta eventMetadata
	if json.Unmarshal(fields[derivedEventMarker], &meta) != nil || len(meta.OriginalSHA256) != 32 {
		return "", false
	}
	return hex.EncodeToString(meta.OriginalSHA256), true
}
