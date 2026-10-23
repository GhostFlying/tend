package protocol

import "testing"

func TestRunnerProtocolWindow(t *testing.T) {
	for _, value := range []int64{0, 1} {
		if !RunnerProtocolSupported(value) {
			t.Fatalf("unsupported %d", value)
		}
	}
	for _, value := range []int64{-1, 2} {
		if RunnerProtocolSupported(value) {
			t.Fatalf("supported %d", value)
		}
	}
	if got, err := RuntimeRunnerProtocol(nil); err != nil || got != 0 {
		t.Fatalf("legacy: %d %v", got, err)
	}
	if _, err := NegotiatedRunnerProtocol(2, 3); err == nil {
		t.Fatal("future accepted")
	}
	if got, err := NegotiatedRunnerProtocol(0, 2); err != nil || got != 1 {
		t.Fatalf("negotiated %d %v", got, err)
	}
}
