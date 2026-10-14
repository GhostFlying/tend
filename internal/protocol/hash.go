package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// PayloadHash returns the hash that detects a request ID being reused for a
// different intent. It covers the operation, target, expected control
// revision and payload in canonical JSON form. Transport fields such as the
// parent request, trace and retry metadata are excluded because they do not
// change what the request asks for.
func PayloadHash(e *Envelope) (string, error) {
	var payload any
	if len(e.Payload) > 0 {
		dec := json.NewDecoder(bytes.NewReader(e.Payload))
		dec.UseNumber()
		if err := dec.Decode(&payload); err != nil {
			return "", fmt.Errorf("protocol: decode payload: %w", err)
		}
	}
	doc := map[string]any{
		"operation": e.Operation,
		"target":    map[string]any{"type": e.Target.Type, "id": e.Target.ID},
		"payload":   payload,
	}
	if e.ExpectedControlRevision != nil {
		doc["expected_control_revision"] = *e.ExpectedControlRevision
	}
	canon, err := canonicalJSON(doc)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:]), nil
}

// canonicalJSON encodes v with sorted object keys, numbers kept as written
// and no insignificant whitespace. encoding/json already sorts map keys, and
// json.Number values are emitted verbatim.
func canonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("protocol: encode canonical JSON: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
