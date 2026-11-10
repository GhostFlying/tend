package protocol

import "testing"

func TestRunnerProtocolWindow(t *testing.T) {
	for _, value := range []int64{PreviousRunnerProtocol, CurrentRunnerProtocol} {
		if !RunnerProtocolSupported(value) {
			t.Fatalf("unsupported %d", value)
		}
	}
	for _, value := range []int64{-1, 0, 1, CurrentRunnerProtocol + 1} {
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
