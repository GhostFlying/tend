package runner

import "testing"

func TestRuntimeGenerationBelongsToProcessNotHeartbeat(t *testing.T) {
	r := controlRunner(t)
	first, err := r.runtimeObservation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.runtimeObservation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if first.Generation < 1 || second.Generation != first.Generation || second.Sequence <= first.Sequence {
		t.Fatalf("heartbeat changed incarnation: first=%+v second=%+v", first, second)
	}
	cfg := r.cfg
	cfg.InstanceID = "next-instance"
	restarted := New(cfg)
	next, err := restarted.runtimeObservation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if next.Generation <= second.Generation || next.InstanceID == second.InstanceID {
		t.Fatalf("restart did not advance identity: before=%+v after=%+v", second, next)
	}
}
