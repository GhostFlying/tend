package tools

import (
	"encoding/json"
	"github.com/GhostFlying/tend/internal/protocol"
)

// Native callers allocate and send actual object identities before admission.
// Nothing is persisted beside the business object itself.
func nativeCreationTarget(c Call, operation string, target protocol.Target) protocol.Target {
	if target.ID != "" || c.Native == "" {
		return target
	}
	prefix := protocol.CreationTargetPrefix(operation)
	if prefix != "" {
		target.ID = protocol.NativeObjectID(prefix, "harness:"+c.Principal.Subject, c.Native)
	}
	return target
}

func nativePayloadIdentities(c Call, operation string, raw json.RawMessage) (json.RawMessage, error) {
	fields := protocol.NativePayloadIdentities(operation)
	if len(fields) == 0 || c.Native == "" {
		return raw, nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	if values == nil {
		values = map[string]json.RawMessage{}
	}
	for field, prefix := range fields {
		if len(values[field]) == 0 || string(values[field]) == "\"\"" {
			values[field], _ = json.Marshal(protocol.NativeObjectID(prefix, "harness:"+c.Principal.Subject, c.Native+"/"+field))
		}
	}
	return json.Marshal(values)
}
