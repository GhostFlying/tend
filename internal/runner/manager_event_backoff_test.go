package runner

import (
	"context"
	"encoding/json"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagerNewEventsRespectFailedUploadBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	clock := testkit.NewClock()
	j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	as := protocol.Assignment{EventSync: true, RunID: "stream", LeaseID: "lease", Role: protocol.RoleIssueManager, Execution: &protocol.RunExecutionPin{}, Manager: &protocol.IssueManagerAssignment{ConversationID: "issue", NativeOwnerTools: true}}
	if err := j.AcceptAssignment(ctx, as, ""); err != nil {
		t.Fatal(err)
	}
	fake := testkit.NewHarness()
	streamDone := make(chan struct{})
	fake.Turn = func(th *testkit.FakeThread, turn string, _ harness.TurnInput) string {
		defer close(streamDone)
		for range 30 {
			th.Emit(harness.Event{Kind: harness.EventAgentMessageDelta, TurnID: turn, ItemID: "message", Text: "x"})
			time.Sleep(5 * time.Millisecond)
		}
		return "completed"
	}
	start, _ := json.Marshal(coordination.Request{Config: harness.ThreadConfig{Model: "model"}})
	var claims atomic.Int64
	var tries atomic.Int64
	var mu sync.Mutex
	var original int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		var result any = protocol.ManagerNativeRequest{}
		switch env.Operation {
		case protocol.OpManagerNativeClaim:
			switch claims.Add(1) {
			case 1:
				result = protocol.ManagerNativeRequest{ID: "start", Method: "start", Payload: start}
			case 2:
				raw, _ := json.Marshal(coordination.Request{ThreadID: "th_1", Input: harness.TurnInput{Text: "work"}})
				result = protocol.ManagerNativeRequest{ID: "turn", Method: "turn", Payload: raw}
			}
		case protocol.OpManagerNativeEvent:
			tries.Add(1)
			var ev protocol.ManagerNativeEvent
			_ = json.Unmarshal(env.Payload, &ev)
			mu.Lock()
			if original == 0 {
				original = ev.Sequence
			}
			if original != ev.Sequence {
				t.Error("different head")
			}
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpFailed, Error: protocol.Errorf(protocol.CodeForbidden, "unavailable event original")}})
			return
		}
		raw, _ := json.Marshal(result)
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: raw}})
	}))
	defer server.Close()
	r := New(Config{Journal: j, Executions: &eventFailureResolver{closeRecoveryResolver{native: fake}}, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), ServerURL: server.URL, HTTPClient: server.Client(), RequestTimeout: time.Second, RetryDelay: time.Hour, EventFlush: time.Hour, Log: slog.New(slog.DiscardHandler)})
	r.runnerProtocol = 2
	done := make(chan struct{})
	go func() { defer close(done); _ = r.managerNative(ctx, ctx, as) }()
	defer func() { cancel(); <-done }()
	select {
	case <-streamDone:
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not run")
	}
	t.Logf("same permanently rejected head was retried %d times during 150ms of native output with RetryDelay=1h", tries.Load())
	if tries.Load() != 1 {
		t.Error("new event wakes bypass failure backoff")
	}
}
