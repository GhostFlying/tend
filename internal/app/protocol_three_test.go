package app

import (
	"strconv"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestProtocolFourAdmitsThreeAndRejectsTwoBeforeAllocation(t *testing.T) {
	for _, version := range []int64{2, 3, 4} {
		t.Run(strconv.FormatInt(version, 10), func(t *testing.T) {
			h := newHarness(t)
			runtime := testRuntime("protocol-window")
			runtime.Protocol, runtime.ProtocolMin, runtime.ProtocolMax = version, version, version
			op := h.versionedHello("linux-1", runtime)
			if version == 2 {
				if op.Status != protocol.OpFailed || protocol.CodeOf(op.Error) != protocol.CodeInvalidRequest || h.count("runs") != 0 {
					t.Fatal("retired communication was admitted", op)
				}
				return
			}
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				actual, err := store.GetHostRuntime(t.Context(), tx.Q(), "linux-1")
				if err == nil && actual.Runtime.Protocol != version {
					t.Fatalf("negotiated %d, expected %d", actual.Runtime.Protocol, version)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
