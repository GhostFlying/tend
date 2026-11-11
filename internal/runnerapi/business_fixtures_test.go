package runnerapi

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

// composeFixtureBusinessInput models the caller's first composition of a
// mutation. Retries retain the resulting envelope and actual business keys.
func composeFixtureBusinessInput(t *testing.T, ids protocol.IDGen, operation string, target protocol.Target, payload any) (protocol.Target, json.RawMessage) {
	t.Helper()
	if prefix := protocol.CreationTargetPrefix(operation); prefix != "" && target.ID == "" {
		target.ID = ids.New(prefix)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	identities := protocol.NativePayloadIdentities(operation)
	if operation == protocol.OpRunnerClaim {
		identities = map[string]protocol.Prefix{"run_id": protocol.PrefixRun}
	}
	if len(identities) > 0 {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		if fields == nil {
			fields = map[string]json.RawMessage{}
		}
		for field, prefix := range identities {
			if len(fields[field]) == 0 || string(fields[field]) == `""` || string(fields[field]) == "null" {
				fields[field], _ = json.Marshal(ids.New(prefix))
			}
		}
		raw, _ = json.Marshal(fields)
	}
	return target, raw
}
