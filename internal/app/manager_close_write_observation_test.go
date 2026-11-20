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
			if op := h.runAs(as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			env := h.env(protocol.OpManagerNativeReply, runTarget(as.RunID), protocol.ManagerNativeReply{
				ID: closeID, Method: method, Error: "private-native-error",
			}, nil)
			if op, err := h.app.Execute(t.Context(), runPrincipal(as.RunID, as.Role), env, ManagerNativeCommand{OperationName: env.Operation}); err != nil || op.Status != protocol.OpSucceeded {
				t.Fatalf("close receipt = %+v, %v", op, err)
			}

			var enqueued, replied store.WriteSource
			for _, o := range observations {
				switch o.Source.Operation {
				case "manager.native_enqueue":
					enqueued = o.Source
				case protocol.OpManagerNativeReply:
					replied = o.Source
				}
			}
			if enqueued.RequestID != closeID || enqueued.CloseRequestID != closeID || enqueued.RunID != as.RunID {
				t.Fatalf("queued source = %+v", enqueued)
			}
			if replied.RequestID != env.RequestID || replied.RequestID == closeID || replied.NativeRequestID != closeID || replied.RunID != as.RunID {
				t.Fatalf("reply source = %+v", replied)
			}
			wantClose := closeID
			if method == "" {
				wantClose = ""
			}
			if replied.CloseRequestID != wantClose {
				t.Fatalf("reply close identity = %q, want %q", replied.CloseRequestID, wantClose)
			}
			raw, err := json.Marshal(observations)
			if err != nil || strings.Contains(string(raw), "private-native-error") {
				t.Fatalf("diagnostic privacy: %s, %v", raw, err)
			}
		})
	}
}
