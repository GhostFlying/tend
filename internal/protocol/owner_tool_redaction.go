package protocol

import "encoding/json"

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
	target := object
	if tool == "owner_call" {
		var name string
		if json.Unmarshal(object["name"], &name) != nil {
			return "[invalid owner invocation redacted]"
		}
		if name != credentialTool {
			return input
		}
		target = nil
		if json.Unmarshal(object["arguments"], &target) != nil {
			return "[private webhook arguments redacted]"
		}
	}
	for _, field := range []string{"body", "content_base64", "payload"} {
		if _, ok := target[field]; ok {
			target[field] = json.RawMessage(`"[redacted]"`)
		}
	}
	if tool == "owner_call" {
		object["arguments"], _ = json.Marshal(target)
	}
	result, _ := json.Marshal(object)
	return string(result)
}
