package protocol

import (
	"encoding/json"
	"testing"
)

func TestRunnerProtocolWindow(t *testing.T) {
	for _, value := range []int64{PreviousRunnerProtocol, CurrentRunnerProtocol} {
		if !RunnerProtocolSupported(value) {
			t.Fatalf("unsupported %d", value)
		}
	}
	for _, value := range []int64{-1, 0, 1, 2, CurrentRunnerProtocol + 1} {
		if RunnerProtocolSupported(value) {
			t.Fatalf("supported %d", value)
		}
	}
	if got, err := RuntimeRunnerProtocol(nil); err == nil || got != 0 {
		t.Fatalf("legacy: %d %v", got, err)
	}
	if _, err := NegotiatedRunnerProtocol(CurrentRunnerProtocol+1, CurrentRunnerProtocol+2); err == nil {
		t.Fatal("future accepted")
	}
	if got, err := NegotiatedRunnerProtocol(PreviousRunnerProtocol, CurrentRunnerProtocol); err != nil || got != CurrentRunnerProtocol {
		t.Fatalf("negotiated %d %v", got, err)
	}
}

func TestRunnerV3ClaimUsesBusinessRunIdentity(t *testing.T) {
	request := NativeObjectID(PrefixRequest, "claim", "one")
	for _, version := range []int64{2, 3, 4} {
		raw, _ := json.Marshal(ClaimPayload{Runtime: &RunnerRuntime{Protocol: version, ProtocolMin: version, ProtocolMax: version}})
		env := Envelope{RequestID: request, Payload: raw}
		adapted, err := AdaptRunnerClaim(env)
		if version != 3 {
			if err == nil {
				t.Fatalf("protocol %d accepted a claim without run identity", version)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		var p ClaimPayload
		if err := json.Unmarshal(adapted.Payload, &p); err != nil || !ValidID(PrefixRun, p.RunID) {
			t.Fatalf("adapted claim %s: %v", adapted.Payload, err)
		}
		again, err := AdaptRunnerClaim(env)
		if err != nil || string(again.Payload) != string(adapted.Payload) {
			t.Fatalf("unstable adaptation: %v", err)
		}
	}
	for _, version := range []int64{3, 4} {
		id := NativeObjectID(PrefixRun, "run", "explicit")
		raw, _ := json.Marshal(ClaimPayload{RunID: id, Runtime: &RunnerRuntime{Protocol: version, ProtocolMin: version, ProtocolMax: version}})
		env := Envelope{RequestID: request, Payload: raw}
		got, err := AdaptRunnerClaim(env)
		if err != nil || string(got.Payload) != string(raw) {
			t.Fatalf("protocol %d changed explicit identity: %s %v", version, got.Payload, err)
		}
	}
}

func TestRunnerV3InventoryStatusRetainsActualCatalogWithoutRequestCache(t *testing.T) {
	receipt := &AgentInventoryReceipt{HostID: "runner", SnapshotID: "actual", ServerRevision: 7}
	for _, version := range []int64{3, 4} {
		input := AgentInventoryStatus{RequestState: "business_state", Current: receipt, Matched: receipt}
		got := AdaptRunnerInventoryStatus(&RunnerRuntime{Protocol: version}, input)
		if got.Current != receipt || got.Current.SnapshotID != "actual" {
			t.Fatal("compatibility changed actual catalog", got)
		}
		if version == 3 && (got.RequestState != "not_recorded" || got.Request != nil || got.RequestPayloadHash != "" || got.Matched != nil) {
			t.Fatal(got)
		}
		if version == 4 && (got.RequestState != "business_state" || got.Matched != receipt) {
			t.Fatal(got)
		}
	}
}
