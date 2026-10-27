package mainthread

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestOwnerSourceCannotSelectUnconsumedOrSystemMessage(t *testing.T) {
	h := testkit.NewHarness()
	started := make(chan *testkit.FakeThread, 1)
	release := make(chan struct{})
	defer close(release)
	h.Turn = func(th *testkit.FakeThread, _ string, _ harness.TurnInput) string {
		started <- th
		<-release
		return ""
	}
	e := newEnv(t, h, withSteer(h, func(context.Context, harness.Thread, string, harness.TurnInput) error { return nil }))
	e.say("original")
	th := <-started
	future := e.say("not yet consumed")
	eventually(t, "native accepted but not consumed", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_steers WHERE message_id=? AND status='accepted'`, future) == 1
	})
	e.m.mu.Lock()
	id := e.m.active.turnID
	e.m.mu.Unlock()
	args, _ := json.Marshal(map[string]any{"title": "must not create", "spec": spec, "commit_seq": 1, "source_message_id": future})
	result := e.m.onToolCall(t.Context(), harness.ToolCall{ThreadID: th.ID(), TurnID: id, CallID: "unconsumed-source", Tool: "tasks_create", Arguments: args})
	if result.Success || e.count(`SELECT COUNT(*) FROM tasks`) != 0 {
		t.Fatal("unconsumed owner source gained authority")
	}
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: "system-source", Author: "system", Body: "background suggestion", CreatedAt: 1})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	args, _ = json.Marshal(map[string]any{"title": "must not create", "spec": spec, "commit_seq": 1, "source_message_id": "system-source"})
	result = e.m.onToolCall(t.Context(), harness.ToolCall{ThreadID: th.ID(), TurnID: id, CallID: "invalid-source", Tool: "tasks_create", Arguments: args})
	if result.Success || e.count(`SELECT COUNT(*) FROM tasks`) != 0 {
		t.Fatal("non-owner source gained authority")
	}
}
