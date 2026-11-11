package store

import (
	"context"
	"encoding/json"
)

// StageToolRecord retains only tool originals; the small routing/status wrapper
// is rebuilt from its business fields rather than archived as another body.
func (s *Store) StageToolRecord(ctx context.Context, stream, raw string) (context.Context, error) {
	full, err := ResolveToolRecord(ctx, fileQuerier{s.read, s.files}, raw)
	if err != nil {
		return ctx, err
	}
	var object map[string]json.RawMessage
	if err = json.Unmarshal([]byte(full), &object); err != nil {
		return ctx, err
	}
	var bodies [][]byte
	for _, key := range []string{"input", "output"} {
		var value string
		if part, ok := object[key]; ok {
			if err = json.Unmarshal(part, &value); err != nil {
				return ctx, err
			}
			bodies = append(bodies, []byte(value))
		}
	}
	return s.StageOriginals(ctx, stream, bodies...)
}
func (t *Tx) RetainToolRecord(ctx context.Context, raw string) (string, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &object); err != nil {
		return "", err
	}
	for _, key := range []string{"input", "output"} {
		part, ok := object[key]
		if !ok {
			continue
		}
		var value string
		if err := json.Unmarshal(part, &value); err != nil {
			return "", err
		}
		retained, err := t.RetainJSON(ctx, value)
		if err != nil {
			return "", err
		}
		object[key], err = json.Marshal(retained)
		if err != nil {
			return "", err
		}
	}
	encoded, err := json.Marshal(object)
	return string(encoded), err
}
func ResolveToolRecord(ctx context.Context, q Querier, raw string) (string, error) {
	body, err := ResolveJSON(ctx, q, raw)
	if err != nil {
		return "", err
	}
	var object map[string]json.RawMessage
	if err = json.Unmarshal([]byte(body), &object); err != nil {
		return "", err
	}
	for _, key := range []string{"input", "output"} {
		part, ok := object[key]
		if !ok {
			continue
		}
		var value string
		if err = json.Unmarshal(part, &value); err != nil {
			return "", err
		}
		value, err = ResolveJSON(ctx, q, value)
		if err != nil {
			return "", err
		}
		object[key], err = json.Marshal(value)
		if err != nil {
			return "", err
		}
	}
	encoded, err := json.Marshal(object)
	return string(encoded), err
}
