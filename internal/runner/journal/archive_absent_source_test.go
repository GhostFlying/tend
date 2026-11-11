package journal

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestEventsWithoutNativeOriginalDoNotCreateArchiveWork(t *testing.T) {
	for _, manager := range []bool{false, true} {
		name := "worker"
		if manager {
			name = "manager"
		}
		t.Run(name, func(t *testing.T) {
			j := open(t, filepath.Join(t.TempDir(), "journal.db"))
			defer j.Close()
			as := protocol.Assignment{RunID: "original-run", Protocol: protocol.CurrentRunnerProtocol}
			if err := j.AcceptAssignment(t.Context(), as, ""); err != nil {
				t.Fatal(err)
			}
			if manager {
				env := &protocol.Envelope{RequestID: "original-event", Operation: protocol.OpManagerNativeEvent}
				if err := j.SaveManagerEvent(t.Context(), as.RunID, env, json.RawMessage(`{"thread_id":"thread"}`)); err != nil {
					t.Fatal(err)
				}
				requests, err := j.Requests(t.Context())
				if err != nil || len(requests) != 1 || requests[0].Envelope.RequestID != env.RequestID {
					t.Fatal("manager event was not retained", requests, err)
				}
			} else {
				if err := j.AppendEvent(t.Context(), as.RunID, protocol.RunEvent{NativeID: "original-event", Kind: "native.item_completed", Body: json.RawMessage(`{}`)}); err != nil {
					t.Fatal(err)
				}
				events, err := j.PendingEvents(t.Context(), as.RunID, 10)
				if err != nil || len(events) != 1 || events[0].NativeID != "original-event" {
					t.Fatal("worker event was not retained", events, err)
				}
			}
			if work, err := j.PendingArchives(t.Context(), "", 10); err != nil || len(work) != 0 {
				t.Fatal("a run without a registered original entered the archive queue", work, err)
			}
			if err := j.RegisterRollout(t.Context(), "deployment", as.RunID, protocol.RolloutPreviewSource{SessionID: "original-thread"}); err != nil {
				t.Fatal(err)
			}
			work, err := j.PendingArchives(t.Context(), "", 10)
			if err != nil || len(work) != 1 || work[0].RunID != as.RunID {
				t.Fatal("the real original did not become archive work when registered", work, err)
			}
		})
	}
}
