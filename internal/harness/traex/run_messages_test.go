package traex

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestRunMessageSteerPreservesOriginalThreadAndTurn(t *testing.T) {
	ws, servers := fakeWorkers(t, func(f *fakeServer) {
		f.on(methodTurnSteer, func(raw json.RawMessage) any {
			var p TurnSteerParams
			_ = json.Unmarshal(raw, &p)
			return TurnSteerResponse{TurnID: p.ExpectedTurnID}
		})
	})
	w, err := ws.Start(t.Context(), harness.WorkerSpec{RunID: "run", Role: protocol.RoleImplement, Spec: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	f := <-servers
	m := w.(harness.LiveMessenger)
	thread, turn := m.MessageTarget()
	status, reason := m.SendMessage(t.Context(), "supplement")
	var p TurnSteerParams
	_ = json.Unmarshal(f.params(methodTurnSteer), &p)
	if status != protocol.RunMessageAccepted || reason != "" || p.ThreadID != thread || p.ExpectedTurnID != turn || p.Input[0].Text != "supplement" || f.params(methodTurnInterrupt) != nil || f.params(methodThreadResume) != nil {
		t.Fatal(status, reason, p)
	}
	f.send(map[string]any{"method": notifyTurnCompleted, "params": map[string]any{"threadId": thread, "turn": map[string]any{"id": turn, "status": "completed"}}})
	for range w.Events() {
	}
	if _, err := w.Wait(); err != nil {
		t.Fatal(err)
	}
	if status, reason := m.SendMessage(context.Background(), "late"); status != protocol.RunMessageFailed || reason != "worker_ended" {
		t.Fatal(status, reason)
	}
}

func TestRunMessageNativeReceiptIsNotModelConsumption(t *testing.T) {
	for _, kind := range []string{"unsupported", "mismatched"} {
		t.Run(kind, func(t *testing.T) {
			ws, servers := fakeWorkers(t, func(f *fakeServer) {
				if kind == "mismatched" {
					f.on(methodTurnSteer, func(json.RawMessage) any { return TurnSteerResponse{TurnID: "another-turn"} })
				}
			})
			w, err := ws.Start(t.Context(), harness.WorkerSpec{Role: protocol.RoleImplement, Spec: json.RawMessage(`{}`)})
			if err != nil {
				t.Fatal(err)
			}
			f := <-servers
			status, reason := w.(harness.LiveMessenger).SendMessage(t.Context(), "supplement")
			want := protocol.RunMessageUnsupported
			if kind == "mismatched" {
				want = protocol.RunMessageUnknown
			}
			if status != want || reason == "" || f.params(methodTurnInterrupt) != nil {
				t.Fatal(status, reason)
			}
		})
	}
}

func TestRunMessageUserTextIsKeptOutOfRunLogs(t *testing.T) {
	ev := workerItemEvent(harness.Event{ItemType: harness.ItemUserMessage, Raw: json.RawMessage(`{"text":"Authorization: Bearer secret"}`)})
	if strings.Contains(string(ev.Body), "secret") {
		t.Fatal(ev)
	}
}
