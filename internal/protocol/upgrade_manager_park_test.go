package protocol

import (
	"encoding/json"
	"testing"
)

func TestUpgradeManagerParkOmittedPreservesLegacyRequest(t *testing.T) {
	const old = `{"delivery_task_id":"task","request_id":"request","upgrade_id":"window","release":"bundle"}`
	var request UpgradeStartRequest
	if err := json.Unmarshal([]byte(old), &request); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(request)
	if err != nil || string(raw) != old {
		t.Fatal("default false changed old wire bytes", string(raw), err)
	}
	original := baseHashEnvelope()
	original.Operation = "upgrades.start"
	original.Payload = []byte(old)
	current := original
	current.Payload = raw
	if hashOf(t, current) != hashOf(t, original) {
		t.Fatal("legacy request hash changed")
	}
	request.ParkIdleManagers = true
	current.Payload, _ = json.Marshal(request)
	if hashOf(t, current) == hashOf(t, original) {
		t.Fatal("opt-in failed to change intent hash")
	}
}
