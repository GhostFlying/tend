package runner

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

type closeRecoveryResolver struct {
	native *testkit.Harness
	opens  atomic.Int64
}

func (*closeRecoveryResolver) Refresh(context.Context) error { return nil }
func (*closeRecoveryResolver) Observe(context.Context) ([]protocol.AgentExecutor, error) {
	return nil, nil
}
func (*closeRecoveryResolver) Resolve(context.Context, protocol.RunExecutionPin) (ResolvedExecution, error) {
	return ResolvedExecution{}, harness.ErrRejected
}
func (r *closeRecoveryResolver) ResolveManager(context.Context, protocol.RunExecutionPin) (ManagerExecution, error) {
	return ManagerExecution{Open: func(ctx context.Context, _ string) (harness.Session, error) {
		r.opens.Add(1)
		return r.native.Open(ctx)
	}}, nil
}

func TestFault_ManagerCloseDeliveryCancellationPreservesFrozenCursor(t *testing.T) {
	ctx := t.Context()
	clock := testkit.NewClock()
	j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	as := protocol.Assignment{Protocol: 2, RunID: "run_original", LeaseID: "lease_original", Role: protocol.RoleIssueManager, Execution: &protocol.RunExecutionPin{}, Manager: &protocol.IssueManagerAssignment{ConversationID: "issue"}}
	if err := j.AcceptAssignment(ctx, as, ""); err != nil {
		t.Fatal(err)
	}
	attempt, cancel := context.WithCancel(ctx)
	defer cancel()
	var lost atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		if env.Operation == protocol.OpManagerNativeReply && lost.CompareAndSwap(false, true) {
			cancel()
			<-r.Context().Done()
			return
		}
		var result any = map[string]any{}
		if env.Operation == protocol.OpManagerNativeClaim {
			result = protocol.ManagerNativeRequest{ID: "close-original", Method: "close", Payload: json.RawMessage(`{}`)}
		}
		raw, err := json.Marshal(result)
		if err != nil {
			t.Error(err)
			return
		}
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: raw}})
	}))
	defer server.Close()
	resolver := &closeRecoveryResolver{native: testkit.NewHarness()}
	r := New(Config{Journal: j, Executions: resolver, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), ServerURL: server.URL, HTTPClient: server.Client(), RequestTimeout: time.Second, RetryDelay: time.Millisecond, Log: slog.New(slog.DiscardHandler)})
	if err := r.managerNative(attempt, ctx, as); err == nil {
		t.Fatal("expected the first close reply to lose its receipt")
	}
	closed, err := j.CompletedManagerClose(ctx, as.RunID)
	if err != nil || closed == nil {
		t.Fatal(closed, err)
	}
	last, err := j.LastManagerEvent(ctx, as.RunID)
	if err != nil || closed.LastSequence != last {
		t.Fatal("cleanup invalidated the frozen close cursor", closed, last, err)
	}
	if err := r.managerNative(ctx, ctx, as); err != nil {
		t.Fatal("original close receipt could not reconcile", err)
	}
	if resolver.opens.Load() != 1 {
		t.Fatal("recovery opened another native process", resolver.opens.Load())
	}
}

func TestFault_ManagerCompletedCloseReplaysBeforeOpeningNativeProcess(t *testing.T) {
	for _, savedEnvelope := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_reply_envelope", true: "after_reply_envelope"}[savedEnvelope], func(t *testing.T) {
			ctx := t.Context()
			clock := testkit.NewClock()
			path := filepath.Join(t.TempDir(), "journal.db")
			j, err := journal.Open(ctx, path, clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			as := protocol.Assignment{Protocol: 2, RunID: "run_original", LeaseID: "lease_original", Role: protocol.RoleIssueManager}
			if err := j.AcceptAssignment(ctx, as, ""); err != nil {
				t.Fatal(err)
			}
			event := protocol.Envelope{RequestID: "event-original", Operation: protocol.OpManagerNativeEvent, Target: runTarget(as)}
			if err := j.SaveManagerEvent(ctx, as.RunID, &event, json.RawMessage(`{"disconnected":true}`)); err != nil {
				t.Fatal(err)
			}
			command := protocol.ManagerNativeRequest{ID: "native-close", Method: "close", Payload: json.RawMessage(`{}`)}
			if _, _, err := j.ManagerOperation(ctx, as.RunID, command); err != nil {
				t.Fatal(err)
			}
			reply := protocol.ManagerNativeReply{ID: command.ID, Result: json.RawMessage(`null`), LastSequence: 1}
			if err := j.CompleteManagerOperation(ctx, reply); err != nil {
				t.Fatal(err)
			}
			if savedEnvelope {
				raw, err := json.Marshal(reply)
				if err != nil {
					t.Fatal(err)
				}
				if err := j.PutRequest(ctx, journal.Request{RunID: as.RunID, Envelope: protocol.Envelope{RequestID: "reply-original", Operation: protocol.OpManagerNativeReply, Target: runTarget(as), Payload: raw}}); err != nil {
					t.Fatal(err)
				}
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			j, err = journal.Open(ctx, path, clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			seen := make(chan string, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var env protocol.Envelope
				if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
					t.Error(err)
					return
				}
				seen <- env.Operation
				if env.Operation == protocol.OpManagerNativeReply {
					var got protocol.ManagerNativeReply
					if err := json.Unmarshal(env.Payload, &got); err != nil || got.ID != reply.ID || got.LastSequence != 1 {
						t.Errorf("close proof changed: %+v, %v", got, err)
					}
					if savedEnvelope && env.RequestID != "reply-original" {
						t.Error("original reply identity changed")
					}
				}
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded}})
			}))
			defer server.Close()
			// No execution resolver: recovering a completed close must not open
			// any native session, even when its old runtime is unavailable.
			r := New(Config{Journal: j, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), ServerURL: server.URL, HTTPClient: server.Client(), Log: slog.New(slog.DiscardHandler)})
			if err := r.managerNative(ctx, ctx, as); err != nil {
				t.Fatal(err)
			}
			if len(seen) != 2 || <-seen != protocol.OpManagerNativeEvent || <-seen != protocol.OpManagerNativeReply {
				t.Fatal("recovery must flush old events then only the original close")
			}
			run, err := j.Run(ctx, as.RunID)
			if !errors.Is(err, journal.ErrNotFound) {
				t.Fatal(run, err)
			}
			last, err := j.LastManagerEvent(ctx, as.RunID)
			if err != nil || last != 1 {
				t.Fatal("recovery added a fact after the frozen close", last, err)
			}
		})
	}
}

type unconfirmedManagerSession struct {
	harness.Session
	done chan struct{}
}

func (s unconfirmedManagerSession) Done() <-chan struct{} { return s.done }
func (s unconfirmedManagerSession) Close() error          { return errors.New("exit status 1") }

type unconfirmedManagerResolver struct{ closeRecoveryResolver }

func (r *unconfirmedManagerResolver) ResolveManager(context.Context, protocol.RunExecutionPin) (ManagerExecution, error) {
	return ManagerExecution{Open: func(context.Context, string) (harness.Session, error) {
		done := make(chan struct{})
		close(done)
		return unconfirmedManagerSession{done: done}, nil
	}}, nil
}
func TestFault_ManagerUnexpectedExitWakesReconciliation(t *testing.T) {
	ctx := t.Context()
	clock := testkit.NewClock()
	j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	as := protocol.Assignment{Protocol: 2, RunID: "run_original", LeaseID: "lease_original", Role: protocol.RoleIssueManager, Execution: &protocol.RunExecutionPin{}, Manager: &protocol.IssueManagerAssignment{ConversationID: "issue"}}
	if err := j.AcceptAssignment(ctx, as, ""); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: json.RawMessage(`{}`)}})
	}))
	defer server.Close()
	r := New(Config{Journal: j, Executions: &unconfirmedManagerResolver{}, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), ServerURL: server.URL, HTTPClient: server.Client(), RequestTimeout: time.Second, Log: slog.New(slog.DiscardHandler)})
	a := &active{as: as, desired: protocol.DesiredRun}
	if err := r.executeManager(ctx, ctx, a); err != nil {
		t.Fatal(err)
	}
	saved, err := j.Run(ctx, as.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.Fenced {
		t.Fatal("test did not reach the unconfirmed-stop path")
	}
	select {
	case <-r.reconcile:
	default:
		t.Fatal("manager executor exited and fenced its journal, but nothing schedules hello/reconciliation")
	}
}
