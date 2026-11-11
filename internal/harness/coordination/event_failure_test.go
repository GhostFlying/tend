package coordination

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestFault_NativeEventFailureIsReportedToSupervisor(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	fake := testkit.NewHarness()
	native, err := fake.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fault := errors.New("persist manager event: injected journal failure")
	d := Driver{Session: permissionSession{native.(*testkit.FakeSession)}, Profile: protocol.ExecutionProfile{Model: "model"}, Emit: func(context.Context, Event) error { return fault }}
	defer func() { _, _ = d.Handle(ctx, "close", json.RawMessage(`{}`)) }()
	raw, _ := json.Marshal(Request{Config: harness.ThreadConfig{Sandbox: "read-only", ApprovalPolicy: "never"}})
	info, err := d.Handle(ctx, "start", raw)
	if err != nil {
		t.Fatal(err)
	}
	id := info.(ThreadInfo).ID
	raw, _ = json.Marshal(Request{ThreadID: id, Input: harness.TurnInput{Text: "Produce a result"}})
	if _, err := d.Handle(ctx, "turn", raw); err != nil {
		t.Fatal(err)
	}
	// A provider may remain alive after a turn. The supervisor must hear the
	// original IO error without waiting for Session.Done or a heartbeat timeout.
	select {
	case err := <-d.Failures():
		if !errors.Is(err, fault) || !strings.Contains(err.Error(), id) {
			t.Fatal("failure lost its original error or native identity", err)
		}
	case <-ctx.Done():
		t.Fatal("event consumer silently disappeared", ctx.Err())
	}
}

func TestFault_ToolEmitFailureRetainsCauseForSupervisor(t *testing.T) {
	fault := errors.New("deliver persisted manager event original-request: disconnected")
	d := Driver{Emit: func(context.Context, Event) error { return fault }}
	result := d.tool(t.Context(), harness.ToolCall{ThreadID: "original-thread", TurnID: "original-turn", CallID: "original-call", Tool: "issues_context_read"})
	if result.Success {
		t.Fatal("failed transport was a successful business call")
	}
	select {
	case err := <-d.Failures():
		if !errors.Is(err, fault) {
			t.Fatal(err)
		}
	default:
		t.Fatal("tool failure did not reach its supervisor")
	}
}
