package runner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

func stoppedManagerRecoveryJournal(t *testing.T, releasing bool) (*Runner, protocol.Assignment, protocol.Envelope, protocol.Envelope) {
	t.Helper()
	r, as, _, _ := retainedRunnerFixture(t, protocol.RoleIssueManager)
	ctx := t.Context()
	for i := 1; i <= 2; i++ {
		env := protocol.Envelope{RequestID: "event-" + string(rune('0'+i)), Operation: protocol.OpManagerNativeEvent, Target: runTarget(as)}
		if err := r.cfg.Journal.SaveManagerEvent(ctx, as.RunID, &env, json.RawMessage(`{"disconnected":true}`)); err != nil {
			t.Fatal(err)
		}
		// The server already received both events, but their business application
		// is pending. Repeating a local event is not how that state is resolved.
		if err := r.cfg.Journal.DeleteRequest(ctx, env.RequestID); err != nil {
			t.Fatal(err)
		}
	}
	command := protocol.ManagerNativeRequest{ID: "original-close", Method: "close", Payload: json.RawMessage(`{}`)}
	if _, _, err := r.cfg.Journal.ManagerOperation(ctx, as.RunID, command); err != nil {
		t.Fatal(err)
	}
	reply := protocol.ManagerNativeReply{ID: command.ID, Result: json.RawMessage(`null`), LastSequence: 2}
	if err := r.cfg.Journal.CompleteManagerOperation(ctx, reply); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(reply)
	closeEnv := protocol.Envelope{RequestID: "original-close-envelope", Operation: protocol.OpManagerNativeReply, Target: runTarget(as), Payload: raw}
	if err := r.cfg.Journal.PutRequest(ctx, journal.Request{RunID: as.RunID, Envelope: closeEnv}); err != nil {
		t.Fatal(err)
	}
	stopped := protocol.Envelope{RequestID: "original-stop-envelope", Operation: protocol.OpManagerNativeStopped, Target: runTarget(as), Payload: json.RawMessage(`{"reason":"original native closed","last_sequence":2}`)}
	if releasing {
		if err := r.cfg.Journal.ConfirmStopped(ctx, as.RunID); err != nil {
			t.Fatal(err)
		}
		if err := r.cfg.Journal.Finish(ctx, as.RunID, journal.PhaseReleasing, journal.Request{RunID: as.RunID, Envelope: stopped}); err != nil {
			t.Fatal(err)
		}
	}
	return r, as, closeEnv, stopped
}

func TestFault_ManagerStoppedFactPrecedesBlockedCloseAfterRetain(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	r, as, closeEnv, stopEnv := stoppedManagerRecoveryJournal(t, true)
	var mu sync.Mutex
	var seen []protocol.Envelope
	retained := true
	stopped := false
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
		case protocol.OpRunnerHello:
			verdict, desired := protocol.VerdictStop, protocol.DesiredStopped
			if retained {
				verdict, desired = protocol.VerdictRetain, protocol.DesiredRun
			}
			result = protocol.HelloResult{LeaseTTLMillis: 60000, Runs: []protocol.RunVerdict{{RunID: as.RunID, Verdict: verdict, Desired: desired}}}
		case protocol.OpManagerNativeStopped:
			if !reflect.DeepEqual(env, stopEnv) {
				t.Error("original physical stop changed", env)
			}
			stopped = true
			seen = append(seen, env)
		case protocol.OpManagerNativeReply:
			if !reflect.DeepEqual(env, closeEnv) {
				t.Error("original close changed", env)
			}
			seen = append(seen, env)
			if !stopped {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(rpcResponse{Error: protocol.Errorf(protocol.CodeUnknownOutcome, "original business events await physical stop and receipt reader")})
				return
			}
		default:
			t.Error("recovery invoked new native/provider work", env.Operation)
		}
		raw, _ := json.Marshal(result)
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: raw}})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	if err := r.freshHello(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := r.cfg.Journal.Run(ctx, as.RunID)
	if err != nil || !before.Fenced || !before.StoppedConfirmed {
		t.Fatal("retention lost the original physical fact", before, err)
	}
	mu.Lock()
	retained = false
	mu.Unlock()
	if err := r.freshHello(ctx); err != nil {
		t.Fatal(err)
	}
	r.wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if !stopped || len(seen) != 2 || seen[0].Operation != protocol.OpManagerNativeStopped || seen[1].Operation != protocol.OpManagerNativeReply {
		t.Fatal("business close blocked the already proved physical stop", seen)
	}
	if _, err := r.cfg.Journal.Run(t.Context(), as.RunID); !errors.Is(err, journal.ErrNotFound) {
		t.Fatal("original close not acknowledged before journal release", err)
	}
	if last, err := r.cfg.Journal.LastManagerEvent(t.Context(), as.RunID); err != nil || last != 2 {
		t.Fatal("recovery changed frozen event cursor", last, err)
	}
}

func TestFault_ManagerCompletedCloseSurvivesStoppedAttempt(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	r, as, closeEnv, _ := stoppedManagerRecoveryJournal(t, false)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		if !reflect.DeepEqual(env, closeEnv) {
			t.Error("cancelled attempt changed original close or entered native work", env)
		}
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded}})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	attempt, stop := context.WithCancelCause(ctx)
	stop(errStopped)
	if err := r.executeManager(ctx, attempt, &active{as: as, desired: protocol.DesiredStopped}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.cfg.Journal.Run(t.Context(), as.RunID); !errors.Is(err, journal.ErrNotFound) {
		t.Fatal("stopped attempt prevented original close recovery", err)
	}
	if last, err := r.cfg.Journal.LastManagerEvent(t.Context(), as.RunID); err != nil || last != 2 {
		t.Fatal(last, err)
	}
}

func TestFault_ManagerStopACKRetainsOriginalUnknownClose(t *testing.T) {
	r, as, closeEnv, stopEnv := stoppedManagerRecoveryJournal(t, true)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var acknowledgeClose atomic.Bool
	var stopped atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		switch env.Operation {
		case protocol.OpManagerNativeStopped:
			if !reflect.DeepEqual(env, stopEnv) {
				t.Error("physical stop changed", env)
			}
			stopped.Add(1)
		case protocol.OpManagerNativeReply:
			if !reflect.DeepEqual(env, closeEnv) {
				t.Error("unknown close changed", env)
			}
			if !acknowledgeClose.Load() {
				cancel()
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(rpcResponse{Error: protocol.Errorf(protocol.CodeUnknownOutcome, "original business receipts remain pending")})
				return
			}
		default:
			t.Error("receipt recovery attempted native work", env.Operation)
		}
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded}})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	if err := r.reportManagerStopped(ctx, as); err == nil {
		t.Fatal("unacknowledged close was treated as applied")
	}
	requests, err := r.cfg.Journal.Requests(t.Context())
	if err != nil || len(requests) != 1 || !reflect.DeepEqual(requests[0].Envelope, closeEnv) || stopped.Load() != 1 {
		t.Fatal("stop ACK removed a different original request", requests, err)
	}
	if run, err := r.cfg.Journal.Run(t.Context(), as.RunID); err != nil || run.Phase != journal.PhaseReleasing || !run.StoppedConfirmed {
		t.Fatal("unknown close lost its original journal responsibility", run, err)
	}
	acknowledgeClose.Store(true)
	restarted := New(r.cfg)
	restarted.c.base, restarted.c.http = server.URL, server.Client()
	if err := restarted.reportManagerStopped(t.Context(), as); err != nil {
		t.Fatal(err)
	}
	if stopped.Load() != 1 {
		t.Fatal("restart recreated an already acknowledged stop")
	}
	if _, err := r.cfg.Journal.Run(t.Context(), as.RunID); !errors.Is(err, journal.ErrNotFound) {
		t.Fatal("acknowledged original close did not settle", err)
	}
}
