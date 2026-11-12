package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

type eventFailureResolver struct{ closeRecoveryResolver }

func (r *eventFailureResolver) ResolveManager(context.Context, protocol.RunExecutionPin) (ManagerExecution, error) {
	open := func(ctx context.Context, _ string) (harness.Session, error) { return r.native.Open(ctx) }
	return ManagerExecution{Open: open, OpenNative: open, Profile: protocol.ExecutionProfile{Model: "model"}}, nil
}

func TestFault_ManagerToolsAndCompletionDoNotWaitForArchive(t *testing.T) {
	testManagerEventFailure(t, false, false)
}

func TestFault_ManagerEventDeliveryFailureReachesSupervisorAndKeepsOriginal(t *testing.T) {
	testManagerEventFailure(t, true, false)
}

func TestFault_ManagerEventLostReceiptDoesNotReplayBusiness(t *testing.T) {
	testManagerEventFailure(t, false, true)
}

func testManagerEventFailure(t *testing.T, failEvent, loseReceipt bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	r, as, _, _ := archiveRunner(t)
	r.cfg.Log = slog.New(slog.DiscardHandler)
	r.c.log = r.cfg.Log
	r.cfg.RequestTimeout = time.Second
	if loseReceipt {
		r.cfg.RetryDelay = time.Millisecond
	}
	as.Protocol = 4
	as.Role = protocol.RoleIssueManager
	as.Execution = &protocol.RunExecutionPin{}
	as.Manager = &protocol.IssueManagerAssignment{ConversationID: "issue", NativeOwnerTools: true}
	r.runnerProtocol = 4
	if err := r.cfg.Journal.AcceptAssignment(ctx, as, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.registerRollout(ctx, as.RunID, protocol.RolloutPreviewSource{SessionID: "session", TurnID: "first"}); err != nil {
		t.Fatal(err)
	}
	fake := testkit.NewHarness()
	var turns, tools, completed atomic.Int64
	fake.Turn = func(th *testkit.FakeThread, turn string, _ harness.TurnInput) string {
		turns.Add(1)
		answer := th.CallTool(ctx, turn, "actual-call", "issues_context_read", map[string]any{})
		if !failEvent && (!answer.Success || answer.Text != "actual business context") {
			t.Error("business tool did not receive its real result", answer)
		}
		return "completed"
	}
	r.cfg.Executions = &eventFailureResolver{closeRecoveryResolver{native: fake}}
	start, _ := json.Marshal(coordination.Request{Config: harness.ThreadConfig{Model: "model"}})
	commands := []protocol.ManagerNativeRequest{{ID: "original-start", Method: "start", Payload: start}}
	var mu sync.Mutex
	type eventKey struct {
		run      string
		sequence int64
	}
	events := map[eventKey]json.RawMessage{}
	var lostToolEvent *protocol.Envelope
	var replayedToolEvent atomic.Int64
	blocked, release := make(chan struct{}), make(chan struct{})
	var blockedOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		if env.Operation == protocol.OpRunnerRolloutArchive {
			blockedOnce.Do(func() { close(blocked) })
			select {
			case <-release:
			case <-q.Context().Done():
			}
			http.Error(w, "archive is not available", http.StatusServiceUnavailable)
			return
		}
		if env.Operation == protocol.OpManagerNativeEvent && failEvent {
			_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpFailed, Error: protocol.Errorf(protocol.CodeForbidden, "injected event delivery failure")}})
			return
		}
		mu.Lock()
		defer mu.Unlock()
		var result any = map[string]any{}
		loseAck := false
		switch env.Operation {
		case protocol.OpManagerNativeClaim:
			var claim protocol.ManagerNativeClaimPayload
			_ = json.Unmarshal(env.Payload, &claim)
			if !claim.AcknowledgeOnly && len(commands) > 0 {
				result, commands = commands[0], commands[1:]
			}
		case protocol.OpManagerNativeReply:
			var reply protocol.ManagerNativeReply
			_ = json.Unmarshal(env.Payload, &reply)
			if reply.ID == "original-start" && reply.Error == "" {
				var info coordination.ThreadInfo
				_ = json.Unmarshal(reply.Result, &info)
				turn, _ := json.Marshal(coordination.Request{ThreadID: info.ID, Input: harness.TurnInput{Text: "Read context and finish"}})
				commands = append(commands, protocol.ManagerNativeRequest{ID: "original-turn", Method: "turn", Payload: turn})
			}
		case protocol.OpManagerNativeEvent:
			var p protocol.ManagerNativeEvent
			var event coordination.Event
			_ = json.Unmarshal(env.Payload, &p)
			_ = json.Unmarshal(p.Body, &event)
			if event.Call != nil {
				if loseReceipt {
					if lostToolEvent == nil {
						original := env
						lostToolEvent = &original
						loseAck = true
					} else if env.RequestID == lostToolEvent.RequestID {
						if env.Target != lostToolEvent.Target || !bytes.Equal(env.Payload, lostToolEvent.Payload) {
							t.Error("retry changed the original event envelope")
						}
						replayedToolEvent.Add(1)
					}
				}
			}
			// The server applies each immutable Run/sequence once even when the
			// event emitter and pending journal recovery both deliver it.
			key := eventKey{run: env.Target.ID, sequence: p.Sequence}
			if original, duplicate := events[key]; duplicate {
				if !bytes.Equal(original, p.Body) {
					t.Error("the original event sequence changed content")
					_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpFailed, Error: protocol.Errorf(protocol.CodeConflictPayload, "event content changed")}})
					return
				}
			} else {
				events[key] = bytes.Clone(p.Body)
				if event.Call != nil {
					tools.Add(1)
					tool, _ := json.Marshal(coordination.Request{ThreadID: event.Call.ThreadID, TurnID: event.Call.TurnID, CallID: event.Call.CallID, ToolResult: harness.ToolResult{Success: true, Text: "actual business context"}})
					commands = append(commands, protocol.ManagerNativeRequest{ID: "original-tool-result", Method: "tool_result", Payload: tool})
				}
				if event.Native != nil && event.Native.Kind == harness.EventTurnCompleted {
					completed.Add(1)
					commands = append(commands, protocol.ManagerNativeRequest{ID: "original-close", Method: "close", Payload: json.RawMessage(`{}`)})
				}
			}
			result = map[string]any{"ack": p.Sequence}
		}
		if loseAck {
			_, _ = w.Write([]byte("event receipt lost after applying the original"))
			return
		}
		raw, _ := json.Marshal(result)
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: raw}})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	archiveCtx, stopArchive := context.WithCancel(ctx)
	archiveDone := make(chan struct{})
	go func() { defer close(archiveDone); r.rolloutArchives(archiveCtx) }()
	defer func() { close(release); stopArchive(); <-archiveDone }()
	select {
	case <-blocked:
	case <-ctx.Done():
		t.Fatal("archive worker did not start", ctx.Err())
	}
	err := r.managerNative(ctx, ctx, as)
	if failEvent {
		if err == nil || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			t.Fatal("supervisor stayed alive after its event consumer failed", err, ctx.Err())
		}
		pending, e := r.cfg.Journal.Requests(ctx)
		found := false
		for _, p := range pending {
			if p.Envelope.Operation == protocol.OpManagerNativeEvent {
				found = true
			}
		}
		if e != nil || !found {
			t.Fatal("failed delivery discarded its original journaled event", found, e)
		}
		return
	}
	if err != nil || tools.Load() != 1 || completed.Load() != 1 || turns.Load() != 1 {
		t.Fatal("business waited for auxiliary archival or replayed the turn", err, tools.Load(), completed.Load(), turns.Load())
	}
	if loseReceipt && replayedToolEvent.Load() == 0 {
		t.Fatal("the injected lost receipt never retried its original event")
	}
	work, err := r.cfg.Journal.PendingArchives(ctx, "", 10)
	if err != nil || len(work) != 1 {
		t.Fatal("completed execution falsely claimed complete archival", work, err)
	}
}
