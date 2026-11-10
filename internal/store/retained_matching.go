package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/blobstore"
)

// originalReference is a compact reconstruction recipe. Large values point at
// existing original windows, including when provider JSON has other whitespace
// or object ordering. Literal syntax is small; new large unmatched bytes alone
// become independent originals rather than copying already archived values.
type originalReference struct {
	Window   *blobstore.Window `json:"window,omitempty"`
	Encoding string            `json:"encoding,omitempty"`
	Parts    []originalPart    `json:"parts,omitempty"`
	SHA256   string            `json:"sha256"`
}
type originalPart struct {
	Literal  string            `json:"literal,omitempty"`
	Window   *blobstore.Window `json:"window,omitempty"`
	Encoding string            `json:"encoding,omitempty"`
}

func canonicalOriginal(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, errors.New("store: original JSON has trailing data")
	}
	return json.Marshal(v)
}
func canonicalOriginalHash(raw []byte) string {
	value, err := canonicalOriginal(raw)
	if err != nil {
		return ""
	}
	return bodyHash(value)
}
func encodeOriginal(raw []byte, encoding string) ([]byte, error) {
	switch encoding {
	case "", "raw":
		return raw, nil
	case "canonical":
		return canonicalOriginal(raw)
	case "compact":
		var b bytes.Buffer
		err := json.Compact(&b, raw)
		return b.Bytes(), err
	default:
		return nil, errors.New("store: unknown original presentation")
	}
}

func (s *Store) matchOriginal(ctx context.Context, raw []byte) (*preparedOriginal, error) {
	canonical := canonicalOriginalHash(raw)
	if canonical == "" {
		return nil, nil
	}
	rows, err := s.read.QueryContext(ctx, `SELECT sha256,archive_id,byte_offset,byte_length,characters FROM retained_original_bodies WHERE canonical_sha256=? LIMIT 16`, canonical)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		p := preparedOriginal{CanonicalSHA256: canonical}
		if err = rows.Scan(&p.Window.SHA256, &p.Window.Archive, &p.Window.Offset, &p.Window.Length, &p.Characters); err != nil {
			return nil, err
		}
		source, err := s.files.ReadWindow(ctx, p.Window)
		if err != nil {
			return nil, err
		}
		for _, encoding := range []string{"raw", "compact", "canonical"} {
			presented, err := encodeOriginal(source, encoding)
			if err != nil {
				return nil, err
			}
			if bytes.Equal(raw, presented) {
				w := p.Window
				p.Reference = originalReference{Window: &w, Encoding: encoding, SHA256: bodyHash(raw)}
				return &p, nil
			}
		}
	}
	return nil, rows.Err()
}

type originalReplacement struct {
	start, end int
	prepared   preparedOriginal
}

func (s *Store) originalReplacements(ctx context.Context, raw []byte, base, depth int) ([]originalReplacement, error) {
	if len(raw) < retainBodyThreshold || depth > 8 {
		return nil, nil
	}
	if p, err := s.matchOriginal(ctx, raw); err != nil {
		return nil, err
	} else if p != nil {
		return []originalReplacement{{start: base, end: base + len(raw), prepared: *p}}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	token, err := dec.Token()
	if err != nil {
		return nil, nil
	}
	delim, ok := token.(json.Delim)
	if !ok || (delim != '{' && delim != '[') {
		return nil, nil
	}
	var matches []originalReplacement
	for dec.More() {
		if delim == '{' {
			if _, err = dec.Token(); err != nil {
				return nil, err
			}
		}
		start := int(dec.InputOffset())
		for start < len(raw) && bytes.ContainsRune([]byte(" \r\n\t:,"), rune(raw[start])) {
			start++
		}
		var value json.RawMessage
		if err = dec.Decode(&value); err != nil {
			return nil, err
		}
		replacements, err := s.originalReplacements(ctx, value, base+start, depth+1)
		if err != nil {
			return nil, err
		}
		matches = append(matches, replacements...)
	}
	return matches, nil
}
func (s *Store) prepareOriginalRecipe(ctx context.Context, stream string, raw []byte) (*preparedOriginal, error) {
	matches, err := s.originalReplacements(ctx, raw, 0, 0)
	if err != nil || len(matches) == 0 {
		return nil, err
	}
	p := &preparedOriginal{Characters: int64(utf8.RuneCount(raw)), Reference: originalReference{SHA256: bodyHash(raw)}}
	at := 0
	literal := func(value []byte) error {
		if len(value) == 0 {
			return nil
		}
		if len(value) < retainBodyThreshold {
			p.Reference.Parts = append(p.Reference.Parts, originalPart{Literal: string(value)})
			return nil
		}
		w, err := s.files.AppendOriginal(ctx, stream, value)
		if err != nil {
			return err
		}
		p.Children = append(p.Children, preparedOriginal{Window: w, Characters: int64(utf8.RuneCount(value))})
		p.Reference.Parts = append(p.Reference.Parts, originalPart{Window: &w})
		return nil
	}
	for _, match := range matches {
		if err = literal(raw[at:match.start]); err != nil {
			return nil, err
		}
		ref := match.prepared.Reference
		p.Reference.Parts = append(p.Reference.Parts, originalPart{Window: ref.Window, Encoding: ref.Encoding})
		p.Children = append(p.Children, match.prepared)
		at = match.end
	}
	if err = literal(raw[at:]); err != nil {
		return nil, err
	}
	return p, nil
}

func resolveOriginalReference(ctx context.Context, files *blobstore.Store, ref originalReference) ([]byte, error) {
	if files == nil {
		return nil, errors.New("store: original file store unavailable")
	}
	var result []byte
	if ref.Window != nil {
		raw, err := files.ReadWindow(ctx, *ref.Window)
		if err != nil {
			return nil, err
		}
		result, err = encodeOriginal(raw, ref.Encoding)
		if err != nil {
			return nil, err
		}
	} else {
		var body bytes.Buffer
		for _, part := range ref.Parts {
			if part.Window == nil {
				body.WriteString(part.Literal)
				continue
			}
			raw, err := files.ReadWindow(ctx, *part.Window)
			if err != nil {
				return nil, err
			}
			raw, err = encodeOriginal(raw, part.Encoding)
			if err != nil {
				return nil, err
			}
			body.Write(raw)
		}
		result = body.Bytes()
	}
	if ref.SHA256 == "" || bodyHash(result) != ref.SHA256 {
		return nil, errors.New("store: reconstructed original integrity mismatch")
	}
	return result, nil
}

func (t *Tx) bindOriginalWindow(ctx context.Context, p preparedOriginal) (preparedOriginal, error) {
	if _, err := t.Exec(ctx, `INSERT OR IGNORE INTO retained_original_bodies(sha256,archive_id,byte_offset,byte_length,characters,canonical_sha256) VALUES(?,?,?,?,?,?)`, p.Window.SHA256, p.Window.Archive, p.Window.Offset, p.Window.Length, p.Characters, p.CanonicalSHA256); err != nil {
		return p, err
	}
	if err := t.Q().QueryRowContext(ctx, `SELECT archive_id,byte_offset,byte_length,characters FROM retained_original_bodies WHERE sha256=?`, p.Window.SHA256).Scan(&p.Window.Archive, &p.Window.Offset, &p.Window.Length, &p.Characters); err != nil {
		return p, err
	}
	return p, nil
}

func (s *Store) retainedRecipe(ctx context.Context, hash string) (*preparedOriginal, error) {
	var raw string
	err := s.read.QueryRowContext(ctx, `SELECT recipe_json FROM retained_body_recipes WHERE sha256=?`, hash).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p := &preparedOriginal{}
	if err = json.Unmarshal([]byte(raw), &p.Reference); err != nil {
		return nil, err
	}
	if p.Reference.SHA256 != hash {
		return nil, errors.New("store: retained recipe identity differs")
	}
	body, err := resolveOriginalReference(ctx, s.files, p.Reference)
	if err != nil {
		return nil, fmt.Errorf("store: read retained recipe: %w", err)
	}
	p.Characters = int64(utf8.RuneCount(body))
	return p, nil
}
