package runner

import (
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

func TestWorkerExplicitCloseCarriesCapturedFragmentOnSameReceipt(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	r, as, _, _ := retainedRunnerFixture(t, protocol.RoleIssueManager)
	as.Execution = &protocol.RunExecutionPin{}
	as.Manager = &protocol.IssueManagerAssignment{ConversationID: "issue:worker", NativeOwnerTools: true}
	activeRun := &active{as: as}
	r.active[as.RunID] = activeRun
	fake := testkit.NewHarness()
	const fragment = "observed partial Worker text"
	fake.Turn = func(th *testkit.FakeThread, turn string, _ harness.TurnInput) string {
		th.Emit(harness.Event{Kind: harness.EventAgentMessageDelta, ItemType: harness.ItemAgentMessage, TurnID: turn, ItemID: "partial-item", Text: fragment})
		return "" // Native process has no terminal turn frame before explicit close.
	}
	r.cfg.Executions = &eventFailureResolver{closeRecoveryResolver{native: fake}}
	start, _ := json.Marshal(coordination.Request{Config: harness.ThreadConfig{Model: "model"}})
	commands := []protocol.ManagerNativeRequest{{ID: "original-start", Method: "start", Payload: start}}
	var mu sync.Mutex
	var sawLive, sentClose, gotClose, saved bool
	var lastSequence, closeSequence int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		var result any = map[string]any{}
		switch env.Operation {
		case protocol.OpManagerNativeClaim:
			var claim protocol.ManagerNativeClaimPayload
			_ = json.Unmarshal(env.Payload, &claim)
			if claim.AcknowledgeOnly {
				break
			}
			if len(commands) > 0 {
				result, commands = commands[0], commands[1:]
			} else if live := activeRun.processText.Snapshot("", ""); !sentClose && len(live) == 1 && live[0].Text == fragment {
				sawLive, sentClose = true, true
				result = protocol.ManagerNativeRequest{ID: "original-close", Method: "close", Payload: json.RawMessage(`{}`)}
			}
		case protocol.OpManagerNativeReply:
			var reply protocol.ManagerNativeReply
			if err := json.Unmarshal(env.Payload, &reply); err != nil || reply.Error != "" {
				t.Errorf("reply for %s failed: %s %v", reply.ID, reply.Error, err)
			}
			switch reply.ID {
			case "original-start":
				var info coordination.ThreadInfo
				if err := json.Unmarshal(reply.Result, &info); err != nil {
					t.Error(err)
				}
				turn, _ := json.Marshal(coordination.Request{ThreadID: info.ID, Input: harness.TurnInput{Text: "Open a Worker item"}})
				commands = append(commands, protocol.ManagerNativeRequest{ID: "original-turn", Method: "turn", Payload: turn})
			case "original-close":
				gotClose, closeSequence = true, reply.LastSequence
				if len(reply.ProcessText) == 1 && reply.ProcessText[0].ItemID == "partial-item" {
					saved = reply.ProcessText[0].Text == fragment
				}
			}
		case protocol.OpManagerNativeEvent:
			var payload protocol.ManagerNativeEvent
			var event coordination.Event
			if err := json.Unmarshal(env.Payload, &payload); err != nil {
				t.Error(err)
			}
			if err := json.Unmarshal(payload.Body, &event); err != nil {
				t.Error(err)
			}
			lastSequence = payload.Sequence
			if len(event.ProcessText) != 0 || event.Native != nil && event.Native.Kind == harness.EventAgentMessageDelta {
				t.Error("standalone partial text entered the native event stream")
			}
		default:
			t.Errorf("unexpected operation %s", env.Operation)
		}
		raw, err := json.Marshal(result)
		if err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: raw}})
	}))
	r.c.base, r.c.http = server.URL, server.Client()
	err := r.managerNative(ctx, ctx, as)
	server.Close()
	if err != nil || !sawLive || !gotClose || closeSequence == 0 || closeSequence != lastSequence {
		t.Fatalf("close fixture failed: err=%v live=%v close=%v last=%d cursor=%d", err, sawLive, gotClose, lastSequence, closeSequence)
	}
	if !saved {
		t.Fatal("successful original close discarded the observed Worker fragment from collected fallback")
	}
}
