package runner

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

// The server-side recovery test consumes this same durable Stopped protocol.
// This half exercises the real watchdog and manager cleanup rather than
// fabricating a stopped receipt or assuming that coordinator death is harmless.
func TestReceiptRecoveryLeaseFenceRetainsOriginalPhysicalStop(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	clock := testkit.NewClock()
	j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	as := protocol.Assignment{Protocol: protocol.CurrentRunnerProtocol, RunID: "run_original", LeaseID: "lease_original", Role: protocol.RoleIssueManager, Execution: &protocol.RunExecutionPin{}, Manager: &protocol.IssueManagerAssignment{ConversationID: "original-responsibility"}}
	if err := j.AcceptAssignment(ctx, as, ""); err != nil {
		t.Fatal(err)
	}
	claimed := make(chan struct{}, 1)
	stops := make(chan protocol.Envelope, 2)
	var replies atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(request.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		if env.Operation == protocol.OpManagerNativeClaim {
			select {
			case claimed <- struct{}{}:
			default:
			}
		}
		if env.Operation == protocol.OpManagerNativeStopped {
			saved, err := j.Run(ctx, as.RunID)
			if err != nil || !saved.StoppedConfirmed || saved.Phase != journal.PhaseReleasing {
				t.Errorf("stop lacks durable physical proof: %+v %v", saved, err)
			}
			stops <- env
			if replies.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(rpcResponse{Error: protocol.Errorf(protocol.CodeInternal, "simulated server recovery")})
				return
			}
		}
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: json.RawMessage(`{}`)}})
	}))
	defer server.Close()
	resolver := &closeRecoveryResolver{native: testkit.NewHarness()}
	r := New(Config{Journal: j, Executions: resolver, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), ServerURL: server.URL, HTTPClient: server.Client(), RequestTimeout: time.Second, RetryDelay: time.Millisecond, Log: slog.New(slog.DiscardHandler)})
	r.setTTL(100)
	attempt, fence := context.WithCancelCause(ctx)
	a := &active{ctx: attempt, cancel: fence, as: as, desired: protocol.DesiredRun, deadline: r.fenceDeadline(clock.Now())}
	r.active[as.RunID] = a
	watchDone, executeDone := make(chan struct{}), make(chan error, 1)
	go func() { defer close(watchDone); r.watchdog(ctx) }()
	go func() { executeDone <- r.executeManager(ctx, attempt, a) }()
	exited := false
	defer func() {
		cancel()
		<-watchDone
		if !exited {
			<-executeDone
		}
	}()
	select {
	case <-claimed:
	case <-ctx.Done():
		t.Fatal("manager did not attach before simulated outage")
	}
	clock.Advance(time.Second)
	select {
	case err := <-executeDone:
		exited = true
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("lease fence did not complete physical-stop reporting")
	}
	first, second := <-stops, <-stops
	if !reflect.DeepEqual(first, second) {
		t.Fatal("unknown stop changed its original request identity or payload")
	}
	var stopped protocol.ManagerStoppedPayload
	if err := json.Unmarshal(first.Payload, &stopped); err != nil || first.Target.ID != as.RunID || stopped.LastSequence != 1 || context.Cause(attempt) != errFenced {
		t.Fatal(first, stopped, err, context.Cause(attempt))
	}
	if resolver.opens.Load() != 1 || len(resolver.native.Starts()) != 0 {
		t.Fatal("physical stop restarted a model or opened another native process")
	}
}
