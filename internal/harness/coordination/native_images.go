package coordination

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// CompactNativeImages replaces duplicate inline image URLs in native evidence
// with their content identity. Original files remain in the issue's file store
// and original native history stays on the runner. Text and item identities are
// unchanged, so receipt and source attribution use the same evidence.
func CompactNativeImages(raw json.RawMessage) (json.RawMessage, error) {
	if !bytes.Contains(raw, []byte(";base64,")) {
		return raw, nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var compact func(any) any
	compact = func(value any) any {
		switch value := value.(type) {
		case string:
			if strings.HasPrefix(value, "data:image/") && strings.Contains(value, ";base64,") {
				digest := sha256.Sum256([]byte(value))
				return "tend:inline-image-url:sha256:" + hex.EncodeToString(digest[:])
			}
		case []any:
			for index := range value {
				value[index] = compact(value[index])
			}
		case map[string]any:
			for key, item := range value {
				value[key] = compact(item)
			}
		}
		return value
	}
	return json.Marshal(compact(value))
}
