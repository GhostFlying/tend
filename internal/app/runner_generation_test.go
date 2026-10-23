package app

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestFault_DelayedFirstHelloCannotRetireCurrentProcess(t *testing.T) {
	h := newHarness(t)
	delayed := testRuntime("before-crash")
	delayed.Generation = 1
	successor := testRuntime("successor")
	successor.Generation = 2
	if op := h.versionedHello("linux-1", successor); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.versionedHello("linux-1", delayed); op.Status != protocol.OpFailed {
		t.Fatal("delayed old first hello replaced successor", op)
	}
	successor.Sequence++
	if _, err := h.app.RuntimeHeartbeat(t.Context(), runner("linux-1"), protocol.HeartbeatPayload{Runtime: successor}); err != nil {
		t.Fatal("successor retired", err)
	}
	equal := testRuntime("concurrent")
	equal.Generation = 2
	if op := h.versionedHello("linux-1", equal); op.Status != protocol.OpFailed {
		t.Fatal("equal generation replaced successor", op)
	}
	changed := *successor
	changed.Generation++
	if op := h.versionedHello("linux-1", &changed); op.Status != protocol.OpFailed {
		t.Fatal("same instance changed generation", op)
	}
	next := testRuntime("after-rollback")
	next.Generation = 3
	if op := h.versionedHello("linux-1", next); op.Status != protocol.OpSucceeded {
		t.Fatal("new incarnation rejected", op)
	}
}
