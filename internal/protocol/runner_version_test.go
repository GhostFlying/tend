package protocol

import (
	"encoding/json"
	"testing"
)

func TestRunnerProtocolRejectsRetiredGenerationsAfterColdCutover(t *testing.T) {
	for version := int64(0); version < CurrentRunnerProtocol; version++ {
		if RunnerProtocolSupported(version) {
			t.Fatalf("retired protocol %d accepted", version)
		}
		if _, err := NegotiatedRunnerProtocol(version, version); err == nil {
			t.Fatalf("retired range %d accepted", version)
		}
	}
	if got, err := NegotiatedRunnerProtocol(MinimumRunnerProtocol, CurrentRunnerProtocol); err != nil || got != CurrentRunnerProtocol {
		t.Fatal(got, err)
	}
	if _, err := RuntimeRunnerProtocol(nil); err == nil {
		t.Fatal("unversioned runtime accepted")
	}
	if _, err := NegotiatedRunnerProtocol(CurrentRunnerProtocol+1, CurrentRunnerProtocol+2); err == nil {
		t.Fatal("future-only runtime accepted")
	}
}

func TestRunnerRequiresBusinessIdentitiesWithoutRequestAliases(t *testing.T) {
	runtime := &RunnerRuntime{Protocol: CurrentRunnerProtocol, ProtocolMin: MinimumRunnerProtocol, ProtocolMax: CurrentRunnerProtocol}
	request := NativeObjectID(PrefixRequest, "claim", "transport")
	p := ClaimPayload{Runtime: runtime}
	raw, _ := json.Marshal(p)
	if _, err := ValidateRunnerClaim(Envelope{RequestID: request, Payload: raw}); err == nil {
		t.Fatal("request identity substituted for Run identity")
	}
	p.RunID = NativeObjectID(PrefixRun, "claim", "business")
	raw, _ = json.Marshal(p)
	got, err := ValidateRunnerClaim(Envelope{RequestID: request, Payload: raw})
	if err != nil || string(got.Payload) != string(raw) {
		t.Fatal("business identity rewritten", err)
	}
	takeover := TakeoverPayload{}
	raw, _ = json.Marshal(takeover)
	if _, err := ValidateRunnerTakeover(Envelope{RequestID: request, Payload: raw}, CurrentRunnerProtocol); err == nil {
		t.Fatal("transport identity substituted for observation")
	}
	takeover.ObservationID = NativeObjectID(PrefixRequest, "takeover", "observation")
	raw, _ = json.Marshal(takeover)
	got, err = ValidateRunnerTakeover(Envelope{RequestID: request, Payload: raw}, CurrentRunnerProtocol)
	if err != nil || string(got.Payload) != string(raw) {
		t.Fatal("observation rewritten", err)
	}
}
