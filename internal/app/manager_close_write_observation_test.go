package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestManagerCloseWriteObservation(t *testing.T) {
	for _, method := range []string{"close", ""} {
		name := method
		if name == "" {
			name = "legacy_reply"
		}
		t.Run(name, func(t *testing.T) {
			h, _, _, as, _ := reviewManagerAssignment(t)
			var observations []store.WriteObservation
			store.WithWriteObserver(time.Now, 0, func(o store.WriteObservation) {
				observations = append(observations, o)
			})(h.store)
			closeID, err := h.app.QueueManagerNative(t.Context(), as.RunID, "close", json.RawMessage(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			reused, err := h.app.QueueManagerNative(t.Context(), as.RunID, "close", json.RawMessage(`{}`))
			if err != nil || reused != closeID {
				t.Fatalf("reused close = %q, %v, want %q", reused, err, closeID)
			}
			if op := h.runAs(as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			env := h.env(protocol.OpManagerNativeReply, runTarget(as.RunID), protocol.ManagerNativeReply{
				ID: closeID, Method: method, Error: "private-native-error",
			}, nil)
			if op, err := h.app.Execute(t.Context(), runPrincipal(as.RunID, as.Role), env, ManagerNativeCommand{OperationName: env.Operation}); err != nil || op.Status != protocol.OpSucceeded {
				t.Fatalf("close receipt = %+v, %v", op, err)
			}

			var enqueued []store.WriteSource
			var replied store.WriteSource
			for _, o := range observations {
				switch o.Source.Operation {
				case "manager.native_enqueue":
					enqueued = append(enqueued, o.Source)
				case protocol.OpManagerNativeReply:
					replied = o.Source
				}
			}
			if len(enqueued) != 2 || enqueued[0].RequestID != closeID || enqueued[0].CloseRequestID != closeID || enqueued[0].RunID != as.RunID ||
				enqueued[1].RequestID == closeID || enqueued[1].CloseRequestID != closeID || enqueued[1].RunID != as.RunID {
				t.Fatalf("queued source = %+v", enqueued)
			}
			if replied.RequestID != env.RequestID || replied.RequestID == closeID || replied.NativeRequestID != closeID || replied.RunID != as.RunID {
				t.Fatalf("reply source = %+v", replied)
			}
			if replied.CloseRequestID != closeID {
				t.Fatalf("reply close identity = %q, want %q", replied.CloseRequestID, closeID)
			}
			raw, err := json.Marshal(observations)
			if err != nil || strings.Contains(string(raw), "private-native-error") {
				t.Fatalf("diagnostic privacy: %s, %v", raw, err)
			}
		})
	}
}
