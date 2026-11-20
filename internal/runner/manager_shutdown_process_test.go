package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestWorkerGracefulShutdownRetainsObservedProcessText(t *testing.T) {
	for _, tc := range []struct {
		name             string
		accepts, lostACK bool
	}{
		{name: "current_peer", accepts: true},
		{name: "lost_ack", accepts: true, lostACK: true},
		{name: "older_peer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verifyWorkerGracefulShutdown(t, tc.accepts, tc.lostACK)
		})
	}
}

func verifyWorkerGracefulShutdown(t *testing.T, accepts, lostACK bool) {
	t.Helper()
	ctx, stop := context.WithTimeout(t.Context(), 6*time.Second)
	defer stop()
	lifetime, cancel := context.WithCancel(ctx)
	defer cancel()
	r, as, _, _ := retainedRunnerFixture(t, protocol.RoleIssueManager)
	as.Protocol, as.EventSync = protocol.CurrentRunnerProtocol, true
	r.runnerProtocol = as.Protocol
	as.Execution = &protocol.RunExecutionPin{}
	as.Manager = &protocol.IssueManagerAssignment{ConversationID: "issue:worker-shutdown", NativeOwnerTools: true}
	a := &active{as: as, desired: protocol.DesiredRun}
	r.active[as.RunID] = a
	fake := testkit.NewHarness()
	const fragment = "observed before graceful shutdown"
	const marker = "later-reasoning-item"
	fake.Turn = func(th *testkit.FakeThread, turn string, _ harness.TurnInput) string {
		th.Emit(harness.Event{Kind: harness.EventAgentMessageDelta, ItemType: harness.ItemAgentMessage, TurnID: turn, ItemID: "partial", Text: fragment})
		th.Emit(harness.Event{Kind: harness.EventItemStarted, ItemType: "reasoning", TurnID: turn, ItemID: marker})
		return ""
	}
	r.cfg.Executions = &eventFailureResolver{closeRecoveryResolver{native: fake}}
	start, err := json.Marshal(coordination.Request{Config: harness.ThreadConfig{Model: "model"}})
	if err != nil {
		t.Fatal(err)
	}
	queue := []protocol.ManagerNativeRequest{{ID: "start", Method: "start", Payload: start}}
	var mu sync.Mutex
	var markerSequence int64
	var diagnostics []protocol.Envelope
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		var result any = struct{}{}
		switch env.Operation {
		case protocol.OpManagerNativeClaim:
			var claim protocol.ManagerNativeClaimPayload
			_ = json.Unmarshal(env.Payload, &claim)
			if !claim.AcknowledgeOnly {
				answer := protocol.ManagerNativeClaimResult{AcceptsDisconnectProcessText: true, AcceptsShutdownProcessText: accepts}
				if len(queue) != 0 {
					answer.ManagerNativeRequest, queue = queue[0], queue[1:]
				} else if markerSequence != 0 {
					cancel()
				}
				result = answer
			}
		case protocol.OpManagerNativeReply:
			var reply protocol.ManagerNativeReply
			if err := json.Unmarshal(env.Payload, &reply); err != nil || reply.Error != "" {
				t.Errorf("native reply failed: %+v %v", reply, err)
			}
			if reply.ID == "start" {
				var info coordination.ThreadInfo
				if err := json.Unmarshal(reply.Result, &info); err != nil || info.ID == "" {
					t.Errorf("start did not return the native thread: %+v %v", info, err)
				}
				turn, _ := json.Marshal(coordination.Request{ThreadID: info.ID, Input: harness.TurnInput{Text: "Open an item"}})
				queue = append(queue, protocol.ManagerNativeRequest{ID: "turn", Method: "turn", Payload: turn})
			}
		case protocol.OpManagerNativeEvent:
			var payload protocol.ManagerNativeEvent
			var event coordination.Event
			if json.Unmarshal(env.Payload, &payload) != nil || json.Unmarshal(payload.Body, &event) != nil {
				t.Errorf("invalid native event: %s", env.Payload)
				return
			}
			if event.Native != nil && event.Native.ItemID == marker {
				markerSequence = payload.Sequence
			}
			if event.Disconnected || event.Native != nil && event.Native.Kind == harness.EventAgentMessageDelta {
				t.Errorf("unexpected disconnect or delta event: %s", payload.Body)
			}
			if len(event.ProcessText) != 0 {
				if event.Native != nil || event.Call != nil || len(event.ProcessText) != 1 ||
					event.ProcessText[0].ItemID != "partial" || event.ProcessText[0].Text != fragment ||
					payload.Sequence != markerSequence+1 {
					t.Errorf("incorrect original diagnostic: %s", env.Payload)
				}
				diagnostics = append(diagnostics, env)
				if lostACK && len(diagnostics) == 1 {
					http.Error(w, "receipt unavailable", http.StatusServiceUnavailable)
					return
				}
			}
		default:
			t.Errorf("unexpected shutdown operation %s", env.Operation)
		}
		raw, err := json.Marshal(result)
		if err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: raw}})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	if err := r.executeManager(lifetime, lifetime, a); err != nil {
		t.Fatal(err)
	}
	run, err := r.cfg.Journal.Run(ctx, as.RunID)
	if err != nil || !run.StoppedConfirmed {
		t.Fatalf("original physical stop did not settle: %+v %v", run, err)
	}
	if lostACK {
		pending, err := r.cfg.Journal.NextManagerEvent(ctx, as.RunID)
		if err != nil || len(pending) != 1 {
			t.Fatalf("original diagnostic was not retained after its lost receipt: %+v %v", pending, err)
		}
		if err := r.flushManagerEvents(ctx, as); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	want := 0
	if accepts {
		want = 1
	}
	if lostACK {
		want = 2
	}
	if markerSequence == 0 || len(diagnostics) != want {
		t.Fatalf("shutdown outcome: original marker=%d diagnostic deliveries=%d want=%d", markerSequence, len(diagnostics), want)
	}
	if lostACK && (diagnostics[0].RequestID != diagnostics[1].RequestID || !bytes.Equal(diagnostics[0].Payload, diagnostics[1].Payload)) {
		t.Fatal("diagnostic recovery rewrote the original request or sequence")
	}
}
