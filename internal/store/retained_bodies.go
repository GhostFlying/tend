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

const retainedMarker = "tend:retained-json:v1"
const retainBodyThreshold = 1024

type preparedOriginal struct {
	Window          blobstore.Window
	Characters      int64
	CanonicalSHA256 string
	Reference       originalReference
	Children        []preparedOriginal
}
type preparedOriginalKey struct{}
type fileQuerier struct {
	Querier
	files *blobstore.Store
}

func (q fileQuerier) FileStore() *blobstore.Store { return q.files }

func reservedOriginalMarker(body []byte) bool {
	var object map[string]json.RawMessage
	if !bytes.Contains(body, []byte(retainedMarker)) || json.Unmarshal(body, &object) != nil {
		return false
	}
	_, ok := object[retainedMarker]
	return ok
}

func bodyHash(body []byte) string { s := sha256.Sum256(body); return hex.EncodeToString(s[:]) }

// StageOriginals writes a batch before taking the single database writer. It is
// safe to abandon a staged range: no ledger reference exists before BindOriginal.
// Identical bytes already retained by another run reuse the original window.
func (s *Store) StageOriginals(ctx context.Context, stream string, bodies ...[]byte) (context.Context, error) {
	staged := map[string]preparedOriginal{}
	if old, ok := ctx.Value(preparedOriginalKey{}).(map[string]preparedOriginal); ok {
		for k, v := range old {
			staged[k] = v
		}
	}
	for _, body := range bodies {
		if len(body) < retainBodyThreshold && !reservedOriginalMarker(body) {
			continue
		}
		hash := bodyHash(body)
		if _, ok := staged[hash]; ok {
			continue
		}
		p := preparedOriginal{Characters: int64(utf8.RuneCount(body))}
		err := s.read.QueryRowContext(ctx, `SELECT archive_id,byte_offset,byte_length,characters FROM retained_original_bodies WHERE sha256=?`, hash).Scan(&p.Window.Archive, &p.Window.Offset, &p.Window.Length, &p.Characters)
		if err == nil {
			p.Window.SHA256 = hash
			if _, err = s.files.ReadWindow(ctx, p.Window); err != nil {
				return ctx, err
			}
		} else if errors.Is(err, sql.ErrNoRows) {
			if recipe, e := s.retainedRecipe(ctx, hash); e != nil {
				return ctx, e
			} else if recipe != nil {
				staged[hash] = *recipe
				continue
			}
			if match, e := s.matchOriginal(ctx, body); e != nil {
				return ctx, e
			} else if match != nil {
				staged[hash] = *match
				continue
			}
			if recipe, e := s.prepareOriginalRecipe(ctx, stream, body); e != nil {
				return ctx, e
			} else if recipe != nil {
				staged[hash] = *recipe
				continue
			}
			p.Window, err = s.files.AppendOriginal(ctx, stream, body)
			if err != nil {
				return ctx, err
			}
			p.CanonicalSHA256 = canonicalOriginalHash(body)
		} else {
			return ctx, err
		}
		if p.Reference.SHA256 == "" {
			w := p.Window
			p.Reference = originalReference{Window: &w, SHA256: hash}
		}
		staged[hash] = p
	}
	return context.WithValue(ctx, preparedOriginalKey{}, staged), nil
}

// RetainJSON binds only a pre-synced original. Internal small/generated facts
// remain inline; this method performs no filesystem writes inside a transaction.
func (t *Tx) RetainJSON(ctx context.Context, raw string) (string, error) {
	staged, _ := ctx.Value(preparedOriginalKey{}).(map[string]preparedOriginal)
	p, ok := staged[bodyHash([]byte(raw))]
	if !ok {
		return raw, nil
	}
	return t.bindOriginal(ctx, raw, p)
}
func (t *Tx) bindOriginal(ctx context.Context, raw string, p preparedOriginal) (string, error) {
	if p.Reference.Window != nil {
		p.Window = *p.Reference.Window
		canonical, err := t.bindOriginalWindow(ctx, p)
		if err != nil {
			return "", err
		}
		p.Window = canonical.Window
		w := p.Window
		p.Reference.Window = &w
	}
	for i, child := range p.Children {
		canonical, err := t.bindOriginalWindow(ctx, child)
		if err != nil {
			return "", err
		}
		p.Children[i] = canonical
		for j, part := range p.Reference.Parts {
			if part.Window != nil && part.Window.SHA256 == canonical.Window.SHA256 {
				w := canonical.Window
				p.Reference.Parts[j].Window = &w
			}
		}
	}
	reference, err := json.Marshal(p.Reference)
	if err != nil {
		return "", err
	}
	if _, err = t.Exec(ctx, `INSERT OR IGNORE INTO retained_body_recipes(sha256,recipe_json) VALUES(?,?)`, bodyHash([]byte(raw)), string(reference)); err != nil {
		return "", err
	}
	projection := map[string]any{}
	dec := json.NewDecoder(bytes.NewBufferString(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return "", err
	}
	if obj, ok := value.(map[string]any); ok {
		projection, _ = bodyProjection(obj, 0).(map[string]any)
	}
	if projection == nil {
		projection = map[string]any{}
	}
	projection[retainedMarker] = p.Reference
	encoded, err := json.Marshal(projection)
	return string(encoded), err
}

// The projection contains queryable identity, lifecycle and routing facts only.
// It is never returned as the original body. Long strings and collections must
// be read through ResolveJSON, which verifies the immutable original.
func bodyProjection(v any, depth int) any {
	switch x := v.(type) {
	case map[string]any:
		if depth > 6 {
			return nil
		}
		out := map[string]any{}
		for k, value := range x {
			if k == "error" || originalIdentityKey(k) {
				out[k] = value
				continue
			}
			if len(k) <= 128 {
				if small := bodyProjection(value, depth+1); small != nil {
					out[k] = small
				}
			}
		}
		return out
	case string:
		if len(x) <= 256 {
			return x
		}
	case bool, json.Number:
		return x
	}
	return nil
}

// ResolveJSON restores exact bytes before application parsing or returning a
// body. Missing, truncated or corrupt originals are errors, never empty JSON.
func ResolveJSON(ctx context.Context, q Querier, raw string) (string, error) {
	if !bytes.Contains([]byte(raw), []byte(retainedMarker)) {
		return raw, nil
	}
	var marker map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &marker); err != nil {
		return raw, nil
	}
	ref, ok := marker[retainedMarker]
	if !ok {
		return raw, nil
	}
	var reference originalReference
	if err := json.Unmarshal(ref, &reference); err != nil {
		return "", err
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

// compactCommandReceipt replaces only the immutable command payload in native
// enqueue/claim snapshots. Prefix/suffix retain the exact historical return
// bytes, independent of the command's later execution status.
func compactCommandReceipt(ctx context.Context, t *Tx, r RequestRecord) (string, error) {
	if r.Operation != "manager.native_enqueue" && r.Operation != "manager.native_claim" {
		return r.ResultJSON, nil
	}
	var command struct {
		ID      string          `json:"id"`
		Method  string          `json:"method"`
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal([]byte(r.ResultJSON), &command) != nil || command.ID == "" || len(command.Payload) < retainBodyThreshold {
		return r.ResultJSON, nil
	}
	var payload string
	if err := t.Q().QueryRowContext(ctx, `SELECT payload_json FROM manager_native_requests WHERE request_id=? AND method=?`, command.ID, command.Method).Scan(&payload); err != nil {
		return "", err
	}
	original, err := ResolveJSON(ctx, t.Q(), payload)
	if err != nil {
		return "", err
	}
	canonical, err := json.Marshal(json.RawMessage(original))
	if err != nil {
		return "", err
	}
	start, end, err := jsonMemberRange([]byte(r.ResultJSON), "payload")
	if err != nil {
		return "", err
	}
	if !bytes.Equal(canonical, []byte(r.ResultJSON)[start:end]) {
		return r.ResultJSON, nil
	}
	marker := commandReceipt{CommandID: command.ID, CommandSHA256: bodyHash([]byte(original)), Prefix: r.ResultJSON[:start], Suffix: r.ResultJSON[end:], SHA256: bodyHash([]byte(r.ResultJSON))}
	out, err := json.Marshal(map[string]any{"tend:command-receipt:v1": marker})
	return string(out), err
}

type commandReceipt struct {
	CommandID     string `json:"command_id"`
	CommandSHA256 string `json:"command_sha256"`
	Prefix        string `json:"prefix"`
	Suffix        string `json:"suffix"`
	SHA256        string `json:"sha256"`
}

func restoreCommandReceipt(ctx context.Context, q Querier, raw string) (string, error) {
	var envelope struct {
		Receipt *commandReceipt `json:"tend:command-receipt:v1"`
	}
	if !bytes.Contains([]byte(raw), []byte("tend:command-receipt:v1")) {
		return raw, nil
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return "", err
	}
	if envelope.Receipt == nil {
		return raw, nil
	}
	v := envelope.Receipt
	var payload string
	if err := q.QueryRowContext(ctx, `SELECT payload_json FROM manager_native_requests WHERE request_id=?`, v.CommandID).Scan(&payload); err != nil {
		return "", err
	}
	payload, err := ResolveJSON(ctx, q, payload)
	if err != nil {
		return "", err
	}
	if bodyHash([]byte(payload)) != v.CommandSHA256 {
		return "", errors.New("store: command receipt original changed")
	}
	canonical, err := json.Marshal(json.RawMessage(payload))
	if err != nil {
		return "", err
	}
	result := v.Prefix + string(canonical) + v.Suffix
	if bodyHash([]byte(result)) != v.SHA256 {
		return "", errors.New("store: command receipt snapshot integrity mismatch")
	}
	return result, nil
}
func jsonMemberRange(raw []byte, key string) (int, int, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if token, err := dec.Token(); err != nil || token != json.Delim('{') {
		return 0, 0, errors.New("store: expected result object")
	}
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return 0, 0, err
		}
		start := int(dec.InputOffset())
		for start < len(raw) && (raw[start] == ':' || raw[start] == ' ' || raw[start] == '\n' || raw[start] == '\r' || raw[start] == '\t') {
			start++
		}
		var value json.RawMessage
		if err = dec.Decode(&value); err != nil {
			return 0, 0, err
		}
		if token == key {
			return start, int(dec.InputOffset()), nil
		}
	}
	return 0, 0, errors.New("store: missing command payload")
}

// JSONTextWindow reads Unicode character windows without expanding an original
// into memory. A corrupt range is rejected before selected text is returned.
func JSONTextWindow(ctx context.Context, q Querier, raw string, offset, limit int64) (string, int64, error) {
	var marker map[string]json.RawMessage
	if bytes.Contains([]byte(raw), []byte(retainedMarker)) && json.Unmarshal([]byte(raw), &marker) == nil {
		if ref, ok := marker[retainedMarker]; ok {
			var reference originalReference
			if err := json.Unmarshal(ref, &reference); err != nil {
				return "", 0, err
			}
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
			runes := []rune(string(body))
			n := int64(len(runes))
			start := min(offset, n)
			end := min(start+limit, n)
			return string(runes[start:end]), n, nil
		}
	}
	runes := []rune(raw)
	length := int64(len(runes))
	start := min(offset, length)
	end := min(start+limit, length)
	return string(runes[start:end]), length, nil
}

func originalIdentityKey(key string) bool {
	switch key {
	case "id", "ID", "thread_id", "turn_id", "item_id", "session_id", "call_id", "ThreadID", "TurnID", "ItemID", "CallID", "Kind", "type", "state":
		return true
	}
	return false
}
