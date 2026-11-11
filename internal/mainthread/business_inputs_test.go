package mainthread

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

// positiveFixtureInput composes a new action once; saved envelopes retain these
// actual business identities unchanged across transport retries.
func positiveFixtureInput(t *testing.T, ids protocol.IDGen, operation string, target protocol.Target, payload any) (protocol.Target, json.RawMessage) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if prefix := protocol.CreationTargetPrefix(operation); prefix != "" && target.ID == "" {
		target.ID = ids.New(prefix)
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
		for key, prefix := range identities {
			if len(fields[key]) == 0 || string(fields[key]) == `""` || string(fields[key]) == "null" {
				fields[key], _ = json.Marshal(ids.New(prefix))
			}
		}
		raw, err = json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
	}
	return target, raw
}
