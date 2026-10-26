package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestObservationScopeArgumentsAndFailure(t *testing.T) {
	calls := 0
	spec := WorkerSpec{Spec: json.RawMessage(`{"observation":{"work_item_ids":["wi_one"]}}`), ReadObservation: func(context.Context) (protocol.RunObservation, error) {
		calls++
		return protocol.RunObservation{Version: 1, Seq: int64(calls)}, nil
	}}
	r := Results{Spec: spec}
	unavailable := spec
	unavailable.ReadObservation = nil
	if HasObservation(unavailable) {
		t.Fatal("scope alone exposed observation")
	}
	unavailable = spec
	unavailable.Spec = json.RawMessage(`{}`)
	if HasObservation(unavailable) {
		t.Fatal("callback alone exposed observation")
	}
	for _, args := range []string{`null`, `{"work_item_id":"wi_other"}`, `[]`, `{}`} {
		got := r.Handle(t.Context(), ToolCall{Tool: ReadObservationTool, Arguments: json.RawMessage(args)})
		if got.Success != (args == "{}") {
			t.Fatalf("%s: %+v", args, got)
		}
	}
	got := r.Handle(t.Context(), ToolCall{Tool: ReadObservationTool, Arguments: json.RawMessage(`{}`)})
	if calls != 2 || !strings.Contains(got.Text, `"seq":2`) {
		t.Fatalf("not refreshed: %+v calls=%d", got, calls)
	}
	r.Spec.Spec = json.RawMessage(`{}`)
	if got := r.Handle(t.Context(), ToolCall{Tool: ReadObservationTool, Arguments: json.RawMessage(`{}`)}); got.Success || calls != 2 {
		t.Fatal("scope-less read allowed")
	}
	r.Spec = spec
	r.Spec.ReadObservation = func(context.Context) (protocol.RunObservation, error) {
		return protocol.RunObservation{}, errors.New("secret bearer credential")
	}
	if got := r.Handle(t.Context(), ToolCall{Tool: ReadObservationTool, Arguments: json.RawMessage(`{}`)}); got.Success || strings.Contains(got.Text, "secret") || strings.Contains(got.Text, "credential") {
		t.Fatalf("unsafe error: %+v", got)
	}
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
		spec.Role = role
		_, input := Brief(spec)
		if !strings.Contains(input, "wi_one") || !strings.Contains(input, "does not prove browser") || !strings.Contains(input, "independently refresh") {
			t.Fatal(input)
		}
	}
}
