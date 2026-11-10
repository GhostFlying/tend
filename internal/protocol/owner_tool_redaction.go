package protocol

import (
	"encoding/json"
	"strings"
)

// RedactOwnerToolInput keeps private webhook staging bytes out of durable tool
// observations, including rejected attempts. Legitimate calls use only an opaque
// reference; other owner operations retain their normal diagnostic arguments.
func RedactOwnerToolInput(tool, input string) string {
	const credentialTool = "owner_put__api_v1_webhook_keys_id"
	if tool != credentialTool && tool != "owner_call" {
		return input
	}
	var object map[string]json.RawMessage
	if json.Unmarshal([]byte(input), &object) != nil {
		return "[invalid owner invocation redacted]"
	}
	if tool == "owner_call" {
		var invocation struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal([]byte(input), &invocation) != nil || invocation.Name == "" {
			return "[invalid owner invocation redacted]"
		}
		private := false
		decoder := json.NewDecoder(strings.NewReader(input))
		if _, err := decoder.Token(); err != nil {
			return "[invalid owner invocation redacted]"
		}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return "[invalid owner invocation redacted]"
			}
			var raw json.RawMessage
			if err := decoder.Decode(&raw); err != nil {
				return "[invalid owner invocation redacted]"
			}
			if !strings.EqualFold(key.(string), "name") {
				continue
			}
			var name string
			if json.Unmarshal(raw, &name) == nil && name == credentialTool {
				private = true
			}
		}
		if !private {
			return input
		}
		// JSON struct decoding accepts casing aliases. Sanitize every spelling,
		// including shadowed duplicates, before map marshaling retains diagnostics.
		for key, raw := range object {
			if !strings.EqualFold(key, "arguments") {
				continue
			}
			var target map[string]json.RawMessage
			if json.Unmarshal(raw, &target) != nil {
				object[key] = json.RawMessage(`"[private webhook arguments redacted]"`)
				continue
			}
			redactOwnerCredentialFields(target)
			object[key], _ = json.Marshal(target)
		}
	} else {
		redactOwnerCredentialFields(object)
	}
	result, _ := json.Marshal(object)
	return string(result)
}

func redactOwnerCredentialFields(object map[string]json.RawMessage) {
	for key := range object {
		if strings.EqualFold(key, "body") || strings.EqualFold(key, "content_base64") || strings.EqualFold(key, "payload") {
			object[key] = json.RawMessage(`"[redacted]"`)
		}
	}
}
