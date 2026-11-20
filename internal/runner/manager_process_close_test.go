package runner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

type workerCloseSession struct {
	harness.Session
	afterClose func()
}

func (s workerCloseSession) Close() error {
	err := s.Session.Close()
	if err == nil {
		s.afterClose()
	}
	return err
}

type workerCloseResolver struct {
	eventFailureResolver
	afterClose func()
	opened     int
}

func (r *workerCloseResolver) ResolveManager(ctx context.Context, pin protocol.RunExecutionPin) (ManagerExecution, error) {
	execution, err := r.eventFailureResolver.ResolveManager(ctx, pin)
	if err != nil {
		return execution, err
	}
	open := execution.OpenNative
	execution.OpenNative = func(ctx context.Context, dir string) (harness.Session, error) {
		session, err := open(ctx, dir)
		if err != nil {
			return nil, err
		}
		r.opened++
		return workerCloseSession{Session: session, afterClose: r.afterClose}, nil
	}
	return execution, nil
}

func TestWorkerExplicitCloseCarriesCapturedFragmentOnSameReceipt(t *testing.T) {
	t.Run("normal", func(t *testing.T) { testWorkerExplicitClose(t, false) })
	t.Run("cancel_after_native_close", func(t *testing.T) { testWorkerExplicitClose(t, true) })
}

func testWorkerExplicitClose(t *testing.T, cancelAtClose bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	attempt, cancelAttempt := context.WithCancel(ctx)
	defer cancelAttempt()
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
	physicallyClosed := false
	resolver := &workerCloseResolver{
		eventFailureResolver: eventFailureResolver{closeRecoveryResolver{native: fake}},
		afterClose: func() {
			physicallyClosed = true
			if cancelAtClose {
				cancelAttempt()
			}
		},
	}
	r.cfg.Executions = resolver
	start, _ := json.Marshal(coordination.Request{Config: harness.ThreadConfig{Model: "model"}})
	commands := []protocol.ManagerNativeRequest{{ID: "original-start", Method: "start", Payload: start}}
	var mu sync.Mutex
	var sawLive, sentClose, gotClose, saved bool
	var lastSequence, closeSequence int64
	var closeEnvelope protocol.Envelope
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
				closeEnvelope = env
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
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	err := r.managerNative(attempt, ctx, as)
	if cancelAtClose {
		mu.Lock()
		sent, delivered, sequence := sentClose, gotClose, lastSequence
		mu.Unlock()
		if !errors.Is(err, context.Canceled) || !sent || !physicallyClosed || delivered {
			t.Fatalf("expected cancellation after native close and before delivery: err=%v sent=%v closed=%v delivered=%v", err, sent, physicallyClosed, delivered)
		}
		run, readErr := r.cfg.Journal.Run(ctx, as.RunID)
		if readErr != nil || !run.StoppedConfirmed {
			t.Fatal("physical stop was not confirmed", run, readErr)
		}
		durable, readErr := r.cfg.Journal.CompletedManagerClose(ctx, as.RunID)
		if readErr != nil || durable == nil || durable.ID != "original-close" || durable.LastSequence != sequence ||
			len(durable.ProcessText) != 1 || durable.ProcessText[0].Text != fragment {
			t.Fatal("original close or process text was not retained", durable, readErr)
		}
		pending, readErr := r.cfg.Journal.Requests(ctx)
		if readErr != nil || len(pending) != 1 || pending[0].Envelope.Operation != protocol.OpManagerNativeReply {
			t.Fatal("original reply envelope was not retained", pending, readErr)
		}
		if err := r.managerNative(ctx, ctx, as); err != nil {
			t.Fatal("original reply did not reconcile", err)
		}
		mu.Lock()
		replayed := closeEnvelope
		mu.Unlock()
		original := pending[0].Envelope
		if resolver.opened != 1 || original.RequestID != replayed.RequestID || !reflect.DeepEqual(original.Payload, replayed.Payload) {
			t.Fatal("recovery changed the original reply or reopened a native process", resolver.opened, original.RequestID, replayed.RequestID)
		}
		t.Logf("physical stop confirmed, original close cursor %d and fragment recovered from the same envelope", sequence)
		err = nil
	}
	mu.Lock()
	defer mu.Unlock()
	if err != nil || !sawLive || !gotClose || closeSequence == 0 || closeSequence != lastSequence {
		t.Fatalf("close fixture failed: err=%v live=%v close=%v last=%d cursor=%d", err, sawLive, gotClose, lastSequence, closeSequence)
	}
	if !saved {
		t.Fatal("successful original close discarded the observed Worker fragment from collected fallback")
	}
}
