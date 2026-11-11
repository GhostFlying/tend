package coordination

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestHistoryThenOrdinaryResumeStartsEventForwarder(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	fake := testkit.NewHarness()
	fake.Models = []harness.Model{{ID: "selected", Name: "selected"}}
	session, err := fake.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	native := permissionSession{session.(*testkit.FakeSession)}
	original, err := native.StartThread(ctx, harness.ThreadConfig{Model: "selected", Sandbox: "read-only", ApprovalPolicy: "never"})
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan Event, 32)
	driver := Driver{Session: native, NativeOwnerTools: true, Profile: protocol.ExecutionProfile{Model: "selected"}, Emit: func(_ context.Context, e Event) error { events <- e; return nil }}
	defer driver.Handle(ctx, "close", json.RawMessage(`{}`))
	cfg := harness.ThreadConfig{Model: "selected", Sandbox: "read-only", ApprovalPolicy: "never", Purpose: harness.ReceiptHistoryPurpose}
	raw, _ := json.Marshal(Request{ThreadID: original.ID(), Config: cfg})
	if _, err := driver.Handle(ctx, "resume", raw); err != nil {
		t.Fatal(err)
	}
	cfg.Purpose = ""
	raw, _ = json.Marshal(Request{ThreadID: original.ID(), Config: cfg})
	if _, err := driver.Handle(ctx, "resume", raw); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(Request{ThreadID: original.ID(), Input: harness.TurnInput{Text: "continue after temporary history read"}})
	if _, err := driver.Handle(ctx, "turn", raw); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case e := <-events:
			if e.Native != nil && e.Native.Kind == harness.EventTurnCompleted {
				return
			}
		case <-ctx.Done():
			t.Fatal("accepted turn lost its completion event")
		}
	}
}
