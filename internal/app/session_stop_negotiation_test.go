package app

import (
	"strconv"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestSessionStopMetadataNegotiatedForSupportedAssignments(t *testing.T) {
	for _, version := range []int64{3, 4} {
		t.Run(strconv.FormatInt(version, 10), func(t *testing.T) {
			h := newHarness(t)
			runtime := testRuntime("session-stop-negotiation")
			runtime.Protocol, runtime.ProtocolMin, runtime.ProtocolMax = version, version, version
			if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if op, _ := h.createTask(owner, createTaskPayload{Title: "same-goal execution", Spec: testSpec}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			as, ok := h.claimInputRun(runtime)
			if !ok || as.Protocol != version || !as.SessionStopReports {
				t.Fatal("supported assignment lost negotiated terminal reporting", as, ok)
			}
		})
	}
}
